package orch

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
)

const (
	defaultPoolSize  = 10
	scaleUpCooldown  = 30 * time.Second
	despawnZeroPolls = 6

	demandWindow       = 30 * time.Second
	demandMinFreeSlots = 4
	demandSpawnReady   = 12 * time.Second
	demandSafetyFactor = 1.5
)

var proxyAffinityTTL = 300 * time.Second

type poolBackendLifecycle uint8

const (
	poolBackendLifecycleWarming poolBackendLifecycle = iota
	poolBackendLifecycleActive
	poolBackendLifecycleDraining
	poolBackendLifecycleTerminating
)

type poolBackendState struct {
	Lifecycle      poolBackendLifecycle
	ZeroPollStreak int
}

type serverPool struct {
	PoolID           int
	Line             int
	TemplateLaunch   serverLaunch
	UsesDynamicPort  bool
	BackendServerIDs []int

	ListenPort        int
	ListenReserveConn *net.UDPConn

	RoundRobinCursor int
	LastScaleUpAt    time.Time
	ScaleUpInFlight  bool

	AggregateUsers     uint16
	AggregateMaxUsers  uint16
	AggregateInstances uint16

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
	for _, pool := range m.poolsByID {
		if pool == nil || pool.ListenReserveConn == nil {
			continue
		}
		_ = pool.ListenReserveConn.Close()
	}
	m.poolsByID = make(map[int]*serverPool)
	m.poolByListenPort = make(map[int]*serverPool)
	m.poolByServerID = make(map[int]*serverPool)
	m.affinityBySession = make(map[uint64]*poolSessionAffinity)
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

func reservePoolProxyPort() (*net.UDPConn, int, error) {
	addr := &net.UDPAddr{IP: net.ParseIP(nqnet.DefaultNQServerIP).To4(), Port: 0}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, 0, err
	}
	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || udpAddr == nil || udpAddr.Port < 1 || udpAddr.Port > 65535 {
		_ = conn.Close()
		return nil, 0, fmt.Errorf("unexpected proxy addr: %v", conn.LocalAddr())
	}
	return conn, udpAddr.Port, nil
}

func (m *ServerManager) registerPoolLaunch(launch serverLaunch) (*serverRecord, error) {
	configuredPort, hasConfiguredPort := launchConfiguredPort(launch)
	usesDynamic := hasConfiguredPort && configuredPort == 0
	listenPort := 0
	var reserveConn *net.UDPConn
	if usesDynamic {
		conn, port, err := reservePoolProxyPort()
		if err != nil {
			return nil, fmt.Errorf("pool proxy reserve for line=%d: %w", launch.Line+1, err)
		}
		reserveConn = conn
		listenPort = port
	} else if hasConfiguredPort && configuredPort > 0 {
		listenPort = configuredPort
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	pool := &serverPool{
		PoolID:            m.nextPoolID,
		Line:              launch.Line,
		TemplateLaunch:    cloneServerLaunch(launch),
		UsesDynamicPort:   usesDynamic,
		ListenPort:        listenPort,
		ListenReserveConn: reserveConn,
		backendState:      make(map[int]*poolBackendState),
	}
	m.nextPoolID++
	m.poolsByID[pool.PoolID] = pool
	if pool.ListenPort > 0 {
		m.poolByListenPort[pool.ListenPort] = pool
	}

	rec := m.appendPoolBackendRecordLocked(pool, launch, poolBackendLifecycleWarming)

	if pool.UsesDynamicPort {
		m.infof("Pool %d enabled for line %d via proxy port %d", pool.PoolID, pool.Line+1, pool.ListenPort)
	}
	return rec, nil
}

func (m *ServerManager) updatePoolListenPortLocked(pool *serverPool, listenPort int) {
	if pool == nil || pool.UsesDynamicPort || listenPort < 1 || listenPort > 65535 {
		return
	}
	if pool.ListenPort == listenPort {
		return
	}
	if pool.ListenPort > 0 {
		delete(m.poolByListenPort, pool.ListenPort)
	}
	pool.ListenPort = listenPort
	m.poolByListenPort[listenPort] = pool
}

func (m *ServerManager) updatePoolListenPortForRecordLocked(rec *serverRecord) {
	if rec == nil {
		return
	}
	pool := m.poolByServerID[rec.id]
	if pool == nil {
		return
	}
	m.updatePoolListenPortLocked(pool, recordListenPort(rec))
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

	pool.AggregateUsers = clampUint16(users)
	pool.AggregateMaxUsers = clampUint16(maxUsers)
	pool.AggregateInstances = clampUint16(instances)
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
