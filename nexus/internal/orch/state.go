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
	Slot       int
	ListenPort int
	SearchPath []string
}

// managedServer represents a running server process.
type managedServer struct {
	spec    serverSpec
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

// serverRecord tracks one server slot in Nexus registry.
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
	Slot       int
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

// RegisterServerLaunch adds a new server to Nexus runtime state.
func (m *ServerManager) RegisterServerLaunch(launch serverLaunch) *serverRecord {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec := &serverRecord{
		id:     m.nextServerID,
		Launch: cloneServerLaunch(launch),
	}
	m.nextServerID++
	m.serversByID[rec.id] = rec

	return rec
}

func (m *ServerManager) applyResolvedSpecLocked(rec *serverRecord) {
	if !rec.resolvedPortKnown || len(rec.resolvedSearchPath) == 0 {
		return
	}
	rec.spec = &serverSpec{
		Slot:       rec.Launch.Slot,
		ListenPort: rec.resolvedPort,
		SearchPath: append([]string(nil), rec.resolvedSearchPath...),
	}
	if rec.Running != nil {
		rec.Running.spec = *rec.spec
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

func (m *ServerManager) updateServerState(update serverStateUpdate) {
	type startupOnlineCommand struct {
		label   string
		console *serverConsole
	}
	var onlineCommands []startupOnlineCommand

	m.mu.Lock()
	if rec := update.rec; rec != nil {
		if update.hasResolvedPort && update.resolvedPort >= 0 && update.resolvedPort <= 65535 {
			m.assignPortLocked(rec, update.resolvedPort)
		}
		if len(update.resolvedSearchPath) > 0 {
			rec.resolvedSearchPath = append([]string(nil), update.resolvedSearchPath...)
		}
		m.applyResolvedSpecLocked(rec)
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
		if rec.Running == nil || !rec.awaitingServerInfo {
			continue
		}
		rec.awaitingServerInfo = false
		rec.relayConsoleReady = true
		onlineCommands = append(onlineCommands, startupOnlineCommand{
			label:   serverConsoleLabelFromRecord(rec),
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
}

// UpdatePort updates the resolved listen port for a server record.
func (m *ServerManager) UpdatePort(rec *serverRecord, port int) {
	m.updateServerState(serverStateUpdate{
		hasResolvedPort: true,
		rec:             rec,
		resolvedPort:    port,
	})
}

// UpdateSearchPath updates the resolved search path for a server record.
func (m *ServerManager) UpdateSearchPath(rec *serverRecord, searchPath []string) {
	searchPath = normalizeSearchPath(searchPath)
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

func buildServerSnapshot(rec *serverRecord) ServerSnapshot {
	if rec == nil {
		return ServerSnapshot{State: "stopped"}
	}

	snap := ServerSnapshot{
		State:      "stopped",
		LastError:  rec.lastError,
		Hostname:   rec.Hostname,
		MapName:    rec.MapName,
		Players:    rec.Players,
		MaxPlayers: rec.MaxPlayers,
	}

	snap.Slot = rec.Launch.Slot
	if rec.resolvedPortKnown {
		snap.ListenPort = rec.resolvedPort
	}
	if rec.spec != nil && rec.spec.ListenPort >= 0 && rec.spec.ListenPort <= 65535 {
		snap.ListenPort = rec.spec.ListenPort
	}
	if len(rec.resolvedSearchPath) > 0 {
		snap.GameDir = activeGameDir(rec.resolvedSearchPath)
	}
	if rec.spec != nil && len(rec.spec.SearchPath) > 0 {
		snap.GameDir = activeGameDir(rec.spec.SearchPath)
	}

	s := rec.Running
	if s != nil && s.Cmd != nil && s.Cmd.Process != nil && isProcessAlive(s.Cmd.Process) {
		snap.PID = s.Cmd.Process.Pid
		if rec.awaitingServerInfo {
			snap.State = "starting"
		} else {
			snap.State = "running"
		}
		return snap
	}
	if rec.lastError != "" {
		snap.State = "crashed"
	}
	return snap
}

// Snapshots returns a point-in-time view of all managed servers.
func (m *ServerManager) Snapshots() []ServerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]ServerSnapshot, 0, len(m.serversByID))
	for _, rec := range m.serversByID {
		if rec == nil {
			continue
		}
		out = append(out, buildServerSnapshot(rec))
	}
	slices.SortFunc(out, func(a, b ServerSnapshot) int { return cmp.Compare(a.Slot, b.Slot) })
	return out
}
