package orch

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPoolSize  = 1
	scaleUpCooldown  = 30 * time.Second
	despawnZeroPolls = 6

	demandWindow       = 30 * time.Second
	demandMinFreeSlots = 4
	demandSpawnReady   = 12 * time.Second
	demandSafetyFactor = 1.5
)

// poolBackendLifecycle is the autoscaling state of a single backend within a pool.
type poolBackendLifecycle uint8

const (
	// poolBackendLifecycleWarming: backend is running but has not yet received a CCREP.
	poolBackendLifecycleWarming poolBackendLifecycle = iota
	// poolBackendLifecycleActive: backend accepts new player sessions.
	poolBackendLifecycleActive
	// poolBackendLifecycleDraining: backend is not accepting new sessions; waiting to become idle.
	poolBackendLifecycleDraining
	// poolBackendLifecycleTerminating: backend is scheduled for shutdown.
	poolBackendLifecycleTerminating
)

// poolBackendState tracks autoscaling state for a single backend within a pool.
type poolBackendState struct {
	// Lifecycle is the current autoscaling state of the backend.
	Lifecycle poolBackendLifecycle
	// ZeroPollStreak counts consecutive CCREP polls with zero players while draining.
	ZeroPollStreak int
}

// serverPool groups one or more backend server processes behind a single
// configured server line. Fixed-port pools have exactly one backend and a
// stable candidate port; "-port 0" pools may have multiple dynamically spawned
// replicas.
type serverPool struct {
	// PoolID is the unique identifier assigned at registration.
	PoolID int
	// Line is the 0-based servers.ini line index; -1 for synthetic pools.
	Line int
	// TemplateLaunch is the launch spec used to spawn new replicas.
	TemplateLaunch serverLaunch
	// Autoscales reports whether the pool may spawn and despawn replicas.
	Autoscales bool
	// BackendServerIDs lists the IDs of all backend records in this pool.
	BackendServerIDs []int

	// CandidatePort is the stable UDP port for fixed-port pools; 0 for autoscaling pools.
	CandidatePort int

	// RoundRobinCursor advances each time a backend is selected for routing.
	RoundRobinCursor int
	// LastScaleUpAt records when the most recent replica was launched.
	LastScaleUpAt time.Time
	// ScaleUpInFlight is true while a replica launch is in progress.
	ScaleUpInFlight bool

	// aggregateUsers is the total player count across all running backends.
	aggregateUsers uint16
	// aggregateMaxUsers is the total capacity across all running backends.
	aggregateMaxUsers uint16
	// joinableInstances is the number of running backends currently accepting joins.
	joinableInstances uint16
	// aggregateInstances is the number of currently running backends.
	aggregateInstances uint16

	// DisplayHostname, DisplayMap, DisplayGameDir are cached from the most
	// recently observed CCREP and shown in slist responses and snapshots.
	DisplayHostname string
	DisplayMap      string
	DisplayGameDir  string

	backendState map[int]*poolBackendState
	joinDemandAt []time.Time
}

func (m *ServerManager) appendPoolBackendRecordLocked(pool *serverPool, launch serverLaunch, lifecycle poolBackendLifecycle) *serverRecord {
	if pool == nil {
		return nil
	}
	rec := &serverRecord{
		id:     m.nextServerID,
		Launch: cloneServerLaunch(launch),
	}
	m.nextServerID++
	m.serversByID[rec.id] = rec
	m.poolByServerID[rec.id] = pool
	pool.BackendServerIDs = append(pool.BackendServerIDs, rec.id)
	if pool.backendState == nil {
		pool.backendState = make(map[int]*poolBackendState)
	}
	pool.backendState[rec.id] = newPoolBackendState(lifecycle)
	m.refreshPoolSnapshotLocked(pool)
	return rec
}

func newPoolBackendState(lifecycle poolBackendLifecycle) *poolBackendState {
	return &poolBackendState{
		Lifecycle: lifecycle,
	}
}

func (m *ServerManager) ensurePoolBackendStateLocked(pool *serverPool, serverID int) *poolBackendState {
	if pool == nil {
		return nil
	}
	if pool.backendState == nil {
		pool.backendState = make(map[int]*poolBackendState)
	}
	state := pool.backendState[serverID]
	if state == nil {
		state = newPoolBackendState(poolBackendLifecycleWarming)
		pool.backendState[serverID] = state
	}
	return state
}

func transitionPoolBackendLifecycle(state *poolBackendState, next poolBackendLifecycle) {
	if state == nil {
		return
	}
	if state.Lifecycle == next {
		return
	}
	state.Lifecycle = next
}

func backendAllowsPoolRouting(state *poolBackendState, allowDraining bool) bool {
	if state == nil {
		return false
	}
	if state.Lifecycle == poolBackendLifecycleActive {
		return true
	}
	return allowDraining && state.Lifecycle == poolBackendLifecycleDraining
}

func applyPoolDisplayFromRecord(pool *serverPool, rec *serverRecord) {
	if pool == nil || rec == nil {
		return
	}
	if hostname := strings.TrimSpace(rec.Hostname); hostname != "" {
		pool.DisplayHostname = hostname
	}
	if mapName := strings.TrimSpace(rec.MapName); mapName != "" {
		pool.DisplayMap = mapName
	}
	if gameDir := recordGameDir(rec); gameDir != "" {
		pool.DisplayGameDir = gameDir
	}
}

func (m *ServerManager) setPoolBackendLifecycleLocked(pool *serverPool, serverID int, lifecycle poolBackendLifecycle, ensureState bool) {
	if pool == nil {
		return
	}
	state := pool.backendState[serverID]
	if state == nil && ensureState {
		state = m.ensurePoolBackendStateLocked(pool, serverID)
	}
	if state != nil {
		transitionPoolBackendLifecycle(state, lifecycle)
		state.ZeroPollStreak = 0
	}
	m.refreshPoolSnapshotLocked(pool)
}

func (m *ServerManager) resetPoolRegistryLocked() {
	m.poolsByID = make(map[int]*serverPool)
	m.poolByCandidatePort = make(map[int]*serverPool)
	m.poolByServerID = make(map[int]*serverPool)
	m.nextPoolID = 1
}

func (m *ServerManager) closePoolRegistry() {
	m.mu.Lock()
	m.resetPoolRegistryLocked()
	m.mu.Unlock()
}

func launchConfiguredPort(launch serverLaunch) (int, bool) {
	port := -1
	for i := 0; i < len(launch.Args); i++ {
		if !strings.EqualFold(launch.Args[i], "-port") {
			continue
		}
		if i+1 >= len(launch.Args) || isLaunchKeyToken(launch.Args[i+1]) {
			continue
		}
		parsed, err := strconv.Atoi(launch.Args[i+1])
		if err != nil || parsed < 0 || parsed > 65535 {
			continue
		}
		port = parsed
		i++
	}
	return port, port >= 0
}

func forceLaunchPortZero(args []string) []string {
	out := append([]string(nil), args...)
	replaceAt := -1
	for i := 0; i < len(out)-1; i++ {
		if !strings.EqualFold(out[i], "-port") || isLaunchKeyToken(out[i+1]) {
			continue
		}
		replaceAt = i + 1
		i++
	}
	if replaceAt >= 0 {
		out[replaceAt] = "0"
		return out
	}
	return append(out, "-port", "0")
}

func (m *ServerManager) registerPoolLaunch(launch serverLaunch) (*serverRecord, error) {
	configuredPort, hasConfiguredPort := launchConfiguredPort(launch)
	candidatePort := 0

	m.mu.Lock()
	defer m.mu.Unlock()

	autoscales := hasConfiguredPort && configuredPort == 0 && max(1, m.poolMaxSize) > 1
	if !autoscales && hasConfiguredPort && configuredPort > 0 {
		candidatePort = configuredPort
	}

	pool := &serverPool{
		PoolID:         m.nextPoolID,
		Line:           launch.Line,
		TemplateLaunch: cloneServerLaunch(launch),
		Autoscales:     autoscales,
		CandidatePort:  candidatePort,
		backendState:   make(map[int]*poolBackendState),
	}
	m.nextPoolID++
	m.poolsByID[pool.PoolID] = pool
	if pool.CandidatePort > 0 {
		m.poolByCandidatePort[pool.CandidatePort] = pool
	}

	rec := m.appendPoolBackendRecordLocked(pool, launch, poolBackendLifecycleWarming)

	if pool.Autoscales {
		m.infof("Pool %d enabled for line %d (autoscaling)", pool.PoolID, pool.Line+1)
	}
	return rec, nil
}

func (m *ServerManager) updatePoolCandidatePortLocked(pool *serverPool, port int) {
	if pool == nil || pool.Autoscales || port < 1 || port > 65535 {
		return
	}
	if pool.CandidatePort == port {
		return
	}
	if pool.CandidatePort > 0 {
		delete(m.poolByCandidatePort, pool.CandidatePort)
	}
	pool.CandidatePort = port
	m.poolByCandidatePort[port] = pool
}

func (m *ServerManager) updatePoolCandidatePortForRecordLocked(rec *serverRecord) {
	if rec == nil {
		return
	}
	pool := m.poolByServerID[rec.id]
	if pool == nil {
		return
	}
	m.updatePoolCandidatePortLocked(pool, recordListenPort(rec))
}

func (m *ServerManager) resetPoolBackendState(serverID int) {
	m.mu.Lock()
	pool := m.poolByServerID[serverID]
	if pool != nil {
		m.setPoolBackendLifecycleLocked(pool, serverID, poolBackendLifecycleWarming, true)
	}
	m.mu.Unlock()
}

func (m *ServerManager) refreshPoolForServerLocked(serverID int) {
	pool := m.poolByServerID[serverID]
	if pool == nil {
		return
	}
	m.refreshPoolSnapshotLocked(pool)
}

func (m *ServerManager) serverRecordRunningLocked(rec *serverRecord) bool {
	if rec == nil || rec.Running == nil || rec.Running.Cmd == nil || rec.Running.Cmd.Process == nil {
		return false
	}
	return isProcessAlive(rec.Running.Cmd.Process)
}

func recordGameDir(rec *serverRecord) string {
	if rec == nil {
		return ""
	}
	if rec.spec != nil {
		if gameDir := activeGameDir(rec.spec.SearchPath); gameDir != "" {
			return gameDir
		}
	}
	return activeGameDir(rec.resolvedSearchPath)
}

func clampUint16(value int) uint16 {
	if value < 0 {
		return 0
	}
	if value > 0xffff {
		return 0xffff
	}
	return uint16(value)
}

func (m *ServerManager) refreshPoolSnapshotLocked(pool *serverPool) {
	if pool == nil {
		return
	}

	users := 0
	maxUsers := 0
	instances := 0

	for _, serverID := range pool.BackendServerIDs {
		rec := m.serversByID[serverID]
		if !m.serverRecordRunningLocked(rec) {
			continue
		}
		instances++
		users += int(rec.Players)
		maxUsers += int(rec.MaxPlayers)
		applyPoolDisplayFromRecord(pool, rec)
	}

	pool.aggregateUsers = clampUint16(users)
	pool.aggregateMaxUsers = clampUint16(maxUsers)
	pool.joinableInstances = clampUint16(m.poolRoutableCandidateCountLocked(pool, false))
	pool.aggregateInstances = clampUint16(instances)
}

func (m *ServerManager) removeServerRecordLocked(serverID int) {
	rec := m.serversByID[serverID]
	if rec == nil {
		return
	}
	m.removeServerIDFromPortLocked(rec.resolvedPort, rec.id)
	if rec.spec != nil {
		m.removeServerIDFromPortLocked(rec.spec.ListenPort, rec.id)
	}

	if pool := m.poolByServerID[serverID]; pool != nil {
		delete(m.poolByServerID, serverID)
		delete(pool.backendState, serverID)
		pool.BackendServerIDs = slices.DeleteFunc(pool.BackendServerIDs, func(id int) bool { return id == serverID })
		m.refreshPoolSnapshotLocked(pool)
	}
	delete(m.serversByID, serverID)
}
