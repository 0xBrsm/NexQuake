package orch

import (
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultServerMaxInstances = 1
	scaleUpCooldown           = 30 * time.Second
	despawnZeroPolls          = 6

	demandWindow       = 30 * time.Second
	demandMinFreeSlots = 4
	demandSpawnReady   = 12 * time.Second
	demandSafetyFactor = 1.5
)

// instanceLifecycle is the autoscaling state of a single instance within a server.
type instanceLifecycle uint8

const (
	// instanceLifecycleWarming: instance is running but has not yet received a CCREP.
	instanceLifecycleWarming instanceLifecycle = iota
	// instanceLifecycleActive: instance accepts new player sessions.
	instanceLifecycleActive
	// instanceLifecycleDraining: instance is not accepting new sessions; waiting to become idle.
	instanceLifecycleDraining
	// instanceLifecycleTerminating: instance is scheduled for shutdown.
	instanceLifecycleTerminating
)

// instanceState tracks autoscaling state for a single instance within a server.
type instanceState struct {
	// Lifecycle is the current autoscaling state of the instance.
	Lifecycle instanceLifecycle
	// ZeroPollStreak counts consecutive CCREP polls with zero players while draining.
	ZeroPollStreak int
}

// server groups one or more instance server processes behind a single
// configured server line. Fixed-port servers have exactly one instance and a
// stable listen port; "-port 0" servers may have multiple dynamically spawned
// replicas.
type server struct {
	// ServerID is the unique identifier assigned at registration.
	ServerID int
	// Line is the 0-based servers.ini line index; -1 for synthetic servers.
	Line int
	// TemplateLaunch is the launch spec used to spawn new replicas.
	TemplateLaunch serverLaunch
	// Autoscales reports whether the s may spawn and despawn replicas.
	Autoscales bool
	// InstanceIDs lists the IDs of all instance records in this server.
	InstanceIDs []int

	// RoundRobinCursor advances each time a instance is selected for routing.
	RoundRobinCursor int
	// LastScaleUpAt records when the most recent replica was launched.
	LastScaleUpAt time.Time
	// ScaleUpInFlight is true while a replica launch is in progress.
	ScaleUpInFlight bool

	// aggregateUsers is the total player count across all running instances.
	aggregateUsers uint16
	// aggregateMaxUsers is the total capacity across all running instances.
	aggregateMaxUsers uint16
	// joinableInstances is the number of running instances currently accepting joins.
	joinableInstances uint16
	// aggregateInstances is the number of currently running instances.
	aggregateInstances uint16

	// DisplayHostname, DisplayMap, DisplayGameDir are cached from the most
	// recently observed CCREP and shown in slist responses and snapshots.
	DisplayHostname string
	DisplayMap      string
	DisplayGameDir  string

	instanceStates map[int]*instanceState
	joinDemandAt   []time.Time
}

func (m *ServerManager) appendServerInstanceLocked(s *server, launch serverLaunch, lifecycle instanceLifecycle) *instance {
	if s == nil {
		return nil
	}
	rec := &instance{
		id:     m.nextInstanceID,
		Launch: cloneServerLaunch(launch),
	}
	m.nextInstanceID++
	m.instancesByID[rec.id] = rec
	m.serverByInstanceID[rec.id] = s
	s.InstanceIDs = append(s.InstanceIDs, rec.id)
	if s.instanceStates == nil {
		s.instanceStates = make(map[int]*instanceState)
	}
	s.instanceStates[rec.id] = &instanceState{Lifecycle: lifecycle}
	m.refreshServerSnapshotLocked(s)
	return rec
}

func (m *ServerManager) ensureServerInstanceStateLocked(s *server, serverID int) *instanceState {
	if s == nil {
		return nil
	}
	if s.instanceStates == nil {
		s.instanceStates = make(map[int]*instanceState)
	}
	state := s.instanceStates[serverID]
	if state == nil {
		state = &instanceState{Lifecycle: instanceLifecycleWarming}
		s.instanceStates[serverID] = state
	}
	return state
}

func applyServerDisplayFromInstance(s *server, rec *instance) {
	if s == nil || rec == nil {
		return
	}
	if hostname := strings.TrimSpace(rec.Hostname); hostname != "" {
		s.DisplayHostname = hostname
	}
	if mapName := strings.TrimSpace(rec.MapName); mapName != "" {
		s.DisplayMap = mapName
	}
	if gameDir := recordGameDir(rec); gameDir != "" {
		s.DisplayGameDir = gameDir
	}
}

func (m *ServerManager) setServerInstanceLifecycleLocked(s *server, serverID int, lifecycle instanceLifecycle) {
	if s == nil {
		return
	}
	if state := s.instanceStates[serverID]; state != nil {
		state.Lifecycle = lifecycle
		state.ZeroPollStreak = 0
	}
	m.refreshServerSnapshotLocked(s)
}

func (m *ServerManager) resetServerRegistryLocked() {
	m.serversByID = make(map[int]*server)
	m.serverByInstanceID = make(map[int]*server)
	m.nextServerID = 1
}

func (m *ServerManager) closeServerRegistry() {
	m.mu.Lock()
	m.resetServerRegistryLocked()
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

func (m *ServerManager) registerServerLaunch(launch serverLaunch) (*instance, error) {
	configuredPort, hasConfiguredPort := launchConfiguredPort(launch)

	m.mu.Lock()
	defer m.mu.Unlock()

	autoscales := hasConfiguredPort && configuredPort == 0 && max(1, m.serverMaxInstances) > 1

	s := &server{
		ServerID:       m.nextServerID,
		Line:           launch.Line,
		TemplateLaunch: cloneServerLaunch(launch),
		Autoscales:     autoscales,
		instanceStates: make(map[int]*instanceState),
	}
	m.nextServerID++
	m.serversByID[s.ServerID] = s

	rec := m.appendServerInstanceLocked(s, launch, instanceLifecycleWarming)

	if s.Autoscales {
		slog.Info(fmt.Sprintf("Server %d enabled for line %d (autoscaling)", s.ServerID, s.Line+1))
	}
	return rec, nil
}

func (m *ServerManager) resetServerInstanceState(serverID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.serverByInstanceID[serverID]
	if s == nil {
		return
	}
	state := m.ensureServerInstanceStateLocked(s, serverID)
	state.Lifecycle = instanceLifecycleWarming
	state.ZeroPollStreak = 0
	m.refreshServerSnapshotLocked(s)
}

func (m *ServerManager) instanceRunningLocked(rec *instance) bool {
	if rec == nil || rec.Running == nil || rec.Running.Cmd == nil || rec.Running.Cmd.Process == nil {
		return false
	}
	return isProcessAlive(rec.Running.Cmd.Process)
}

func recordGameDir(rec *instance) string {
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

func (m *ServerManager) refreshServerSnapshotLocked(s *server) {
	if s == nil {
		return
	}

	users := 0
	maxUsers := 0
	instances := 0

	for _, serverID := range s.InstanceIDs {
		rec := m.instancesByID[serverID]
		if !m.instanceRunningLocked(rec) {
			continue
		}
		instances++
		users += int(rec.Players)
		maxUsers += int(rec.MaxPlayers)
		applyServerDisplayFromInstance(s, rec)
	}

	s.aggregateUsers = uint16(min(max(users, 0), 0xffff))
	s.aggregateMaxUsers = uint16(min(max(maxUsers, 0), 0xffff))
	s.joinableInstances = uint16(min(max(len(m.serverRoutableCandidatesLocked(s, false)), 0), 0xffff))
	s.aggregateInstances = uint16(min(max(instances, 0), 0xffff))
}

func (m *ServerManager) removeServerRecordLocked(serverID int) {
	rec := m.instancesByID[serverID]
	if rec == nil {
		return
	}
	m.removeServerIDFromPortLocked(rec.resolvedPort, rec.id)
	if rec.spec != nil {
		m.removeServerIDFromPortLocked(rec.spec.ListenPort, rec.id)
	}

	if s := m.serverByInstanceID[serverID]; s != nil {
		delete(m.serverByInstanceID, serverID)
		delete(s.instanceStates, serverID)
		s.InstanceIDs = slices.DeleteFunc(s.InstanceIDs, func(id int) bool { return id == serverID })
		m.refreshServerSnapshotLocked(s)
	}
	delete(m.instancesByID, serverID)
}
