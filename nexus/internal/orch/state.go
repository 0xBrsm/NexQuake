package orch

import (
	"cmp"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// serverSpec is resolved runtime metadata for a running server.
type serverSpec struct {
	Line       int
	ListenPort int
	SearchPath []string
}

// managedServer represents a running server process.
type managedServer struct {
	Cmd     *exec.Cmd
	Console *serverConsole
	done    chan error
}

func (s *managedServer) writeConsole(cmd string) error {
	if s.Console == nil {
		return fmt.Errorf("server console unavailable")
	}
	return s.Console.writeCommand(cmd)
}

func (s *managedServer) writeConsoleAndCaptureFiltered(cmd string, maxWait, idleWait time.Duration, filter serverConsoleLineFilter) (string, error) {
	if s.Console == nil {
		return "", fmt.Errorf("server console unavailable")
	}
	return s.Console.captureCommandOutputFiltered(cmd, maxWait, idleWait, filter)
}

// instance tracks one launched server process in Nexus registry.
type instance struct {
	id     int
	Launch serverLaunch
	spec   *serverSpec

	resolvedPortKnown  bool
	resolvedPort       int
	resolvedSearchPath []string

	Running   *managedServer
	lastError string

	Hostname   string
	MapName    string
	Players    byte
	MaxPlayers byte
	LastSeen   time.Time

	relayConsoleReady   bool
	awaitingServerInfo  bool
	startupTimedOutOnce bool
}

type serverStateUpdate struct {
	rec                *instance
	hasResolvedPort    bool
	resolvedPort       int
	resolvedSearchPath []string

	observedPort       int
	observedHostname   string
	observedMapName    string
	observedPlayers    byte
	observedMaxPlayers byte
	hasObservedInfo    bool
}

// ServerSnapshot is a point-in-time view of a managed server or instance server,
// used for display and admin API responses.
type ServerSnapshot struct {
	// Line is the 0-based index of the servers.ini entry that owns this server.
	Line int
	// CandidatePort is the suggested connect port for a s snapshot.
	// Zero if no instance port is currently available.
	CandidatePort int
	// ListenPort is the UDP port the server is (or was last) listening on.
	// Set for instance snapshots; zero for s snapshots.
	ListenPort int
	// GameDir is the active game directory (first entry in the search path).
	GameDir string
	// Hostname is the server's self-reported hostname from the last CCREP.
	Hostname string
	// MapName is the current map from the last CCREP.
	MapName string
	// Players is the current player count from the last CCREP.
	Players byte
	// MaxPlayers is the server capacity from the last CCREP.
	MaxPlayers byte
	// Instances is the visible instance instance count for s snapshots.
	// Zero hides the server suffix and is always zero for instance snapshots.
	Instances uint16
	// State is one of "stopped", "starting", "running", or "crashed".
	State string
	// PID is the OS process ID of the running server.
	// Zero when the server is not running, or when the s has multiple instances.
	PID int
	// LastError holds the most recent launch or stop error, if any.
	LastError string
}

func cloneServerLaunch(launch serverLaunch) serverLaunch {
	cloned := launch
	cloned.Args = append([]string(nil), launch.Args...)
	return cloned
}

func activeGameDir(searchPath []string) string {
	if len(searchPath) == 0 {
		return ""
	}
	return searchPath[0]
}

func recordListenPort(rec *instance) int {
	if rec == nil {
		return 0
	}
	if rec.spec != nil && rec.spec.ListenPort > 0 && rec.spec.ListenPort <= 65535 {
		return rec.spec.ListenPort
	}
	if rec.resolvedPortKnown && rec.resolvedPort > 0 && rec.resolvedPort <= 65535 {
		return rec.resolvedPort
	}
	return 0
}

func normalizeSearchPath(searchPath []string) []string {
	if len(searchPath) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(searchPath))
	seen := make(map[string]struct{}, len(searchPath))
	for _, raw := range searchPath {
		gameDir := strings.TrimSpace(raw)
		if gameDir == "" {
			continue
		}
		if strings.ContainsAny(gameDir, `/\\`) {
			continue
		}
		if _, ok := seen[gameDir]; ok {
			continue
		}
		seen[gameDir] = struct{}{}
		normalized = append(normalized, gameDir)
	}
	return normalized
}

func (m *ServerManager) applyResolvedSpecLocked(rec *instance) {
	if !rec.resolvedPortKnown || len(rec.resolvedSearchPath) == 0 {
		return
	}
	rec.spec = &serverSpec{
		Line:       rec.Launch.Line,
		ListenPort: rec.resolvedPort,
		SearchPath: append([]string(nil), rec.resolvedSearchPath...),
	}
}

func (m *ServerManager) assignPortLocked(rec *instance, port int) {
	if rec == nil || port < 0 || port > 65535 {
		return
	}

	if rec.resolvedPortKnown {
		if rec.resolvedPort > 0 {
			return
		}
		if port == 0 {
			return
		}
	}

	rec.resolvedPortKnown = true
	rec.resolvedPort = port

	if port < 1 {
		return
	}

	if ids := m.instanceIDsByPort[port]; !slices.Contains(ids, rec.id) {
		m.instanceIDsByPort[port] = append(ids, rec.id)
	}
}

func (m *ServerManager) removeServerIDFromPortLocked(port int, serverID int) {
	if port <= 0 {
		return
	}
	ids := m.instanceIDsByPort[port]
	if len(ids) == 0 {
		return
	}
	out := ids[:0]
	for _, id := range ids {
		if id != serverID {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		delete(m.instanceIDsByPort, port)
		return
	}
	m.instanceIDsByPort[port] = out
}

func (m *ServerManager) updateServerState(update serverStateUpdate) {
	type startupOnlineCommand struct {
		label   string
		console *serverConsole
	}
	var onlineCommands []startupOnlineCommand
	var observedServerIDs []int

	m.mu.Lock()
	if rec := update.rec; rec != nil {
		if update.hasResolvedPort && update.resolvedPort >= 0 && update.resolvedPort <= 65535 {
			m.assignPortLocked(rec, update.resolvedPort)
		}
		if len(update.resolvedSearchPath) > 0 {
			rec.resolvedSearchPath = append([]string(nil), update.resolvedSearchPath...)
		}
		m.applyResolvedSpecLocked(rec)
		m.updateServerCandidatePortForInstanceLocked(rec)
	}

	if !update.hasObservedInfo || update.observedPort < 1 || update.observedPort > 65535 {
		m.mu.Unlock()
		return
	}

	now := time.Now()
	for _, serverID := range m.instanceIDsByPort[update.observedPort] {
		rec := m.instancesByID[serverID]
		if rec == nil {
			continue
		}
		rec.Hostname = update.observedHostname
		rec.MapName = update.observedMapName
		rec.Players = update.observedPlayers
		rec.MaxPlayers = update.observedMaxPlayers
		rec.LastSeen = now
		if s := m.serverByInstanceID[rec.id]; s != nil {
			observedServerIDs = append(observedServerIDs, s.ServerID)
		}
		if rec.Running == nil || !rec.awaitingServerInfo {
			continue
		}
		rec.awaitingServerInfo = false
		rec.relayConsoleReady = true
		onlineCommands = append(onlineCommands, startupOnlineCommand{
			label:   m.serverConsoleLabelLocked(rec),
			console: rec.Running.Console,
		})
	}
	m.mu.Unlock()

	for _, cmd := range onlineCommands {
		if cmd.console == nil {
			m.errorf("server %s online marker failed: server console unavailable", cmd.label)
			continue
		}
		if err := cmd.console.writeCommandWithOptions(
			"echo online and accepting clients",
			serverConsoleWriteOptions{SuppressRelayEcho: true},
		); err != nil {
			m.errorf("server %s online marker failed: %v", cmd.label, err)
		}
	}

	m.reconcileServers(observedServerIDs)
}

// updatePort updates the resolved listen port for a server record.
func (m *ServerManager) updatePort(rec *instance, port int) {
	m.updateServerState(serverStateUpdate{
		hasResolvedPort: true,
		rec:             rec,
		resolvedPort:    port,
	})
}

// updateSearchPathNormalized updates the resolved search path for an instance
// record. searchPath must already be normalized — see [normalizeSearchPath].
func (m *ServerManager) updateSearchPathNormalized(rec *instance, searchPath []string) {
	m.updateServerState(serverStateUpdate{
		rec:                rec,
		resolvedSearchPath: searchPath,
	})
}

// IsManagedListenPort reports whether a listen port belongs to any managed
// instance, or to the current candidate port for a fixed-port s.
func (m *ServerManager) IsManagedListenPort(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.serverByCandidatePort[port] != nil {
		return true
	}
	for _, serverID := range m.instanceIDsByPort[port] {
		if m.instancesByID[serverID] != nil {
			return true
		}
	}
	return false
}

func (m *ServerManager) updateGameState(port int, hostname, mapName string, players, maxPlayers byte) {
	m.updateServerState(serverStateUpdate{
		observedPort:       port,
		observedHostname:   hostname,
		observedMapName:    mapName,
		observedPlayers:    players,
		observedMaxPlayers: maxPlayers,
		hasObservedInfo:    true,
	})
}

type runningServerEntry struct {
	rec *instance
	srv *managedServer
}

func (m *ServerManager) runningServers() []runningServerEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []runningServerEntry
	for _, rec := range m.instancesByID {
		if rec == nil || rec.Running == nil {
			continue
		}
		out = append(out, runningServerEntry{rec: rec, srv: rec.Running})
	}
	return out
}

func (m *ServerManager) buildServerSnapshotLocked(s *server) ServerSnapshot {
	if s == nil {
		return ServerSnapshot{State: "stopped"}
	}

	snap := ServerSnapshot{
		Line:          s.Line,
		CandidatePort: s.CandidatePort,
		GameDir:       s.DisplayGameDir,
		Hostname:      s.DisplayHostname,
		MapName:       s.DisplayMap,
		Players:       byte(min(int(s.aggregateUsers), 0xff)),
		MaxPlayers:    byte(min(int(s.aggregateMaxUsers), 0xff)),
		Instances:     0,
		State:         "stopped",
	}
	if s.Autoscales {
		snap.Instances = s.joinableInstances
	}

	runningCount := 0
	awaitingInfo := false
	lastError := ""

	for _, serverID := range s.InstanceIDs {
		rec := m.instancesByID[serverID]
		if rec == nil {
			continue
		}
		if lastError == "" && rec.lastError != "" {
			lastError = rec.lastError
		}
		if !m.instanceRunningLocked(rec) {
			continue
		}

		runningCount++
		if rec.awaitingServerInfo {
			awaitingInfo = true
		}
		if snap.PID == 0 && rec.Running != nil && rec.Running.Cmd != nil && rec.Running.Cmd.Process != nil {
			snap.PID = rec.Running.Cmd.Process.Pid
		}
		if snap.CandidatePort == 0 {
			snap.CandidatePort = recordListenPort(rec)
		}
		if snap.GameDir == "" {
			snap.GameDir = recordGameDir(rec)
		}
		if snap.Hostname == "" && strings.TrimSpace(rec.Hostname) != "" {
			snap.Hostname = rec.Hostname
		}
		if snap.MapName == "" && strings.TrimSpace(rec.MapName) != "" {
			snap.MapName = rec.MapName
		}
	}

	snap.LastError = lastError
	if runningCount > 0 {
		if awaitingInfo {
			snap.State = "starting"
		} else {
			snap.State = "running"
		}
		if runningCount > 1 {
			snap.PID = 0
		}
		return snap
	}
	if snap.LastError != "" {
		snap.State = "crashed"
	}
	return snap
}

func (m *ServerManager) buildInstanceSnapshotLocked(s *server, rec *instance) ServerSnapshot {
	snap := ServerSnapshot{
		Line:       rec.Launch.Line,
		ListenPort: recordListenPort(rec),
		GameDir:    recordGameDir(rec),
		Hostname:   strings.TrimSpace(rec.Hostname),
		MapName:    strings.TrimSpace(rec.MapName),
		Players:    rec.Players,
		MaxPlayers: rec.MaxPlayers,
		State:      "stopped",
		LastError:  rec.lastError,
	}
	if s != nil {
		snap.Line = s.Line
		if snap.Hostname == "" {
			snap.Hostname = s.DisplayHostname
		}
		if snap.MapName == "" {
			snap.MapName = s.DisplayMap
		}
		if snap.GameDir == "" {
			snap.GameDir = s.DisplayGameDir
		}
	}
	if !m.instanceRunningLocked(rec) {
		if snap.LastError != "" {
			snap.State = "crashed"
		}
		return snap
	}
	snap.State = "running"
	if rec.awaitingServerInfo {
		snap.State = "starting"
	}
	if running := rec.Running; running != nil && running.Cmd != nil && running.Cmd.Process != nil {
		snap.PID = running.Cmd.Process.Pid
	}
	return snap
}

// Snapshots returns a point-in-time view of all managed servers.
func (m *ServerManager) Snapshots() []ServerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]ServerSnapshot, 0, len(m.serversByID))
	for _, s := range m.serversByID {
		if s == nil {
			continue
		}
		out = append(out, m.buildServerSnapshotLocked(s))
	}
	slices.SortFunc(out, func(a, b ServerSnapshot) int { return cmp.Compare(a.Line, b.Line) })
	return out
}

// InstanceSnapshots returns a point-in-time view of instance servers.
// target=0 includes every s; positive targets resolve as a server index.
func (m *ServerManager) InstanceSnapshots(target int) ([]ServerSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var servers []*server
	if target > 0 {
		s, err := m.findServerByIndexLocked(target)
		if err != nil {
			return nil, err
		}
		servers = []*server{s}
	} else {
		servers = make([]*server, 0, len(m.serversByID))
		for _, s := range m.serversByID {
			if s == nil {
				continue
			}
			servers = append(servers, s)
		}
		slices.SortFunc(servers, func(a, b *server) int { return cmp.Compare(a.Line, b.Line) })
	}

	out := make([]ServerSnapshot, 0, len(m.instancesByID))
	for _, s := range servers {
		for _, rec := range m.serverInstancesLocked(s) {
			out = append(out, m.buildInstanceSnapshotLocked(s, rec))
		}
	}
	return out, nil
}
