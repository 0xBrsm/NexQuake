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

// serverRecord tracks one launched server process in Nexus registry.
type serverRecord struct {
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
	rec                *serverRecord
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

// ServerSnapshot is a point-in-time view of a managed pool or backend server,
// used for display and admin API responses.
type ServerSnapshot struct {
	// Line is the 0-based index of the servers.ini entry that owns this pool.
	Line int
	// CandidatePort is the suggested connect port for a pool snapshot.
	// Zero if no backend port is currently available.
	CandidatePort int
	// ListenPort is the UDP port the server is (or was last) listening on.
	// Set for backend snapshots; zero for pool snapshots.
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
	// Instances is the visible backend instance count for pool snapshots.
	// Zero hides the pool suffix and is always zero for backend snapshots.
	Instances uint16
	// State is one of "stopped", "starting", "running", or "crashed".
	State string
	// PID is the OS process ID of the running server.
	// Zero when the server is not running, or when the pool has multiple backends.
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

func recordListenPort(rec *serverRecord) int {
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

func (m *ServerManager) applyResolvedSpecLocked(rec *serverRecord) {
	if !rec.resolvedPortKnown || len(rec.resolvedSearchPath) == 0 {
		return
	}
	rec.spec = &serverSpec{
		Line:       rec.Launch.Line,
		ListenPort: rec.resolvedPort,
		SearchPath: append([]string(nil), rec.resolvedSearchPath...),
	}
}

func (m *ServerManager) assignPortLocked(rec *serverRecord, port int) {
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

	if ids := m.serverIDsByPort[port]; !slices.Contains(ids, rec.id) {
		m.serverIDsByPort[port] = append(ids, rec.id)
	}
}

func (m *ServerManager) removeServerIDFromPortLocked(port int, serverID int) {
	if port <= 0 {
		return
	}
	ids := m.serverIDsByPort[port]
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
		delete(m.serverIDsByPort, port)
		return
	}
	m.serverIDsByPort[port] = out
}

func (m *ServerManager) updateServerState(update serverStateUpdate) {
	type startupOnlineCommand struct {
		label   string
		console *serverConsole
	}
	var onlineCommands []startupOnlineCommand
	var observedPoolIDs []int

	m.mu.Lock()
	if rec := update.rec; rec != nil {
		if update.hasResolvedPort && update.resolvedPort >= 0 && update.resolvedPort <= 65535 {
			m.assignPortLocked(rec, update.resolvedPort)
		}
		if len(update.resolvedSearchPath) > 0 {
			rec.resolvedSearchPath = append([]string(nil), update.resolvedSearchPath...)
		}
		m.applyResolvedSpecLocked(rec)
		m.updatePoolCandidatePortForRecordLocked(rec)
	}

	if !update.hasObservedInfo || update.observedPort < 1 || update.observedPort > 65535 {
		m.mu.Unlock()
		return
	}

	now := time.Now()
	for _, serverID := range m.serverIDsByPort[update.observedPort] {
		rec := m.serversByID[serverID]
		if rec == nil {
			continue
		}
		rec.Hostname = update.observedHostname
		rec.MapName = update.observedMapName
		rec.Players = update.observedPlayers
		rec.MaxPlayers = update.observedMaxPlayers
		rec.LastSeen = now
		if pool := m.poolByServerID[rec.id]; pool != nil {
			observedPoolIDs = append(observedPoolIDs, pool.PoolID)
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

	m.reconcilePools(observedPoolIDs)
}

// updatePort updates the resolved listen port for a server record.
func (m *ServerManager) updatePort(rec *serverRecord, port int) {
	m.updateServerState(serverStateUpdate{
		hasResolvedPort: true,
		rec:             rec,
		resolvedPort:    port,
	})
}

// updateSearchPath updates the resolved search path for a server record.
func (m *ServerManager) updateSearchPath(rec *serverRecord, searchPath []string) {
	m.updateSearchPathNormalized(rec, normalizeSearchPath(searchPath))
}

func (m *ServerManager) updateSearchPathNormalized(rec *serverRecord, searchPath []string) {
	m.updateServerState(serverStateUpdate{
		rec:                rec,
		resolvedSearchPath: searchPath,
	})
}

// IsManagedListenPort reports whether a listen port belongs to any managed
// backend, or to the current candidate port for a fixed-port pool.
func (m *ServerManager) IsManagedListenPort(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.poolByCandidatePort[port] != nil {
		return true
	}
	for _, serverID := range m.serverIDsByPort[port] {
		if m.serversByID[serverID] != nil {
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
	rec *serverRecord
	srv *managedServer
}

func (m *ServerManager) runningServers() []runningServerEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []runningServerEntry
	for _, rec := range m.serversByID {
		if rec == nil || rec.Running == nil {
			continue
		}
		out = append(out, runningServerEntry{rec: rec, srv: rec.Running})
	}
	return out
}

func (m *ServerManager) buildPoolSnapshotLocked(pool *serverPool) ServerSnapshot {
	if pool == nil {
		return ServerSnapshot{State: "stopped"}
	}

	snap := ServerSnapshot{
		Line:          pool.Line,
		CandidatePort: pool.CandidatePort,
		GameDir:       pool.DisplayGameDir,
		Hostname:      pool.DisplayHostname,
		MapName:       pool.DisplayMap,
		Players:       byte(min(int(pool.aggregateUsers), 0xff)),
		MaxPlayers:    byte(min(int(pool.aggregateMaxUsers), 0xff)),
		Instances:     0,
		State:         "stopped",
	}
	if pool.Autoscales {
		snap.Instances = pool.joinableInstances
	}

	runningCount := 0
	awaitingInfo := false
	lastError := ""

	for _, serverID := range pool.BackendServerIDs {
		rec := m.serversByID[serverID]
		if rec == nil {
			continue
		}
		if lastError == "" && rec.lastError != "" {
			lastError = rec.lastError
		}
		if !m.serverRecordRunningLocked(rec) {
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

func (m *ServerManager) buildBackendSnapshotLocked(pool *serverPool, rec *serverRecord) ServerSnapshot {
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
	if pool != nil {
		snap.Line = pool.Line
		if snap.Hostname == "" {
			snap.Hostname = pool.DisplayHostname
		}
		if snap.MapName == "" {
			snap.MapName = pool.DisplayMap
		}
		if snap.GameDir == "" {
			snap.GameDir = pool.DisplayGameDir
		}
	}
	if !m.serverRecordRunningLocked(rec) {
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

	out := make([]ServerSnapshot, 0, len(m.poolsByID))
	for _, pool := range m.poolsByID {
		if pool == nil {
			continue
		}
		out = append(out, m.buildPoolSnapshotLocked(pool))
	}
	slices.SortFunc(out, func(a, b ServerSnapshot) int { return cmp.Compare(a.Line, b.Line) })
	return out
}

// BackendSnapshots returns a point-in-time view of backend servers.
// target=0 includes every pool; positive targets resolve as a pool index.
func (m *ServerManager) BackendSnapshots(target int) ([]ServerSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var pools []*serverPool
	if target > 0 {
		pool, err := m.findPoolByIndexLocked(target)
		if err != nil {
			return nil, err
		}
		pools = []*serverPool{pool}
	} else {
		pools = make([]*serverPool, 0, len(m.poolsByID))
		for _, pool := range m.poolsByID {
			if pool == nil {
				continue
			}
			pools = append(pools, pool)
		}
		slices.SortFunc(pools, func(a, b *serverPool) int { return cmp.Compare(a.Line, b.Line) })
	}

	out := make([]ServerSnapshot, 0, len(m.serversByID))
	for _, pool := range pools {
		for _, rec := range m.poolBackendsLocked(pool) {
			out = append(out, m.buildBackendSnapshotLocked(pool, rec))
		}
	}
	return out, nil
}
