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

// ServerSnapshot is a point-in-time view of a server, used for display.
type ServerSnapshot struct {
	Line       int
	ListenPort int
	GameDir    string
	Hostname   string
	MapName    string
	Players    byte
	MaxPlayers byte
	State      string
	PID        int
	LastError  string
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
		m.updatePoolListenPortForRecordLocked(rec)
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

// ServerByListenPort returns the running server on a given port.
func (m *ServerManager) serverByListenPort(port int) *managedServer {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, serverID := range m.serverIDsByPort[port] {
		rec := m.serversByID[serverID]
		if rec == nil {
			continue
		}
		s := rec.Running
		if s == nil {
			continue
		}
		if s.Cmd == nil || s.Cmd.Process == nil || !isProcessAlive(s.Cmd.Process) {
			continue
		}
		return s
	}
	return nil
}

// IsManagedListenPort reports whether a listen port belongs to any managed pool
// entry or backend at this instant.
func (m *ServerManager) IsManagedListenPort(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.poolByListenPort[port] != nil {
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
		Line:       pool.Line,
		ListenPort: pool.ListenPort,
		GameDir:    pool.DisplayGameDir,
		Hostname:   pool.DisplayHostname,
		MapName:    pool.DisplayMap,
		Players:    byte(min(int(pool.AggregateUsers), 0xff)),
		MaxPlayers: byte(min(int(pool.AggregateMaxUsers), 0xff)),
		State:      "stopped",
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
		if snap.ListenPort == 0 {
			snap.ListenPort = recordListenPort(rec)
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
