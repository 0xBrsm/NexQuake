package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

var globalServerManager *ServerManager

// serverSpec is resolved runtime metadata for a running server.
//
// It becomes authoritative only after startup probes resolve both listen port
// and search path from the server console.
type serverSpec struct {
	Slot       int
	ListenPort int
	SearchPath []string
}

type managedServer struct {
	spec    serverSpec
	cmd     *exec.Cmd
	console *serverConsole
	done    chan error
}

func (s *managedServer) WriteConsole(cmd string) error {
	if s == nil || s.console == nil {
		return fmt.Errorf("server console unavailable")
	}
	return s.console.WriteCommand(cmd)
}

func (s *managedServer) WriteConsoleAndCapture(cmd string, maxWait, idleWait time.Duration) (string, error) {
	return s.WriteConsoleAndCaptureFiltered(cmd, maxWait, idleWait, nil)
}

func (s *managedServer) WriteConsoleAndCaptureFiltered(cmd string, maxWait, idleWait time.Duration, filter serverConsoleLineFilter) (string, error) {
	if s == nil || s.console == nil {
		return "", fmt.Errorf("server console unavailable")
	}
	return s.console.CaptureCommandOutputFiltered(cmd, maxWait, idleWait, filter)
}

type serverRecord struct {
	id     int
	launch serverLaunch
	spec   *serverSpec

	resolvedPortKnown  bool
	resolvedPort       int
	resolvedSearchPath []string

	running   *managedServer
	lastError string

	// Best-effort observed info from CCREP_SERVER_INFO polling (used for slist
	// aggregation and for `rcon nexus slist` output).
	hostname   string
	mapName    string
	players    byte
	maxPlayers byte
	lastSeen   time.Time

	// Startup relay state for the currently running process.
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

type ServerManager struct {
	dataDir string
	logsDir string

	mu              sync.RWMutex
	serversByID     map[int]*serverRecord
	serverIDsByPort map[int][]int // listen port -> configured server IDs

	nextServerID   int
	runtimeBasedir string
}

func (m *ServerManager) serverConsoleLabel(rec *serverRecord) string {
	if rec == nil {
		return "server-0"
	}
	if m != nil {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	return serverConsoleLabelFromRecord(rec)
}

func serverConsoleLabelFromRecord(rec *serverRecord) string {
	if rec == nil {
		return "server-0"
	}

	hostname := "server"
	if name := strings.TrimSpace(rec.hostname); name != "" {
		hostname = name
	}
	identifier := 0
	switch {
	case rec.launch.slot >= 0:
		identifier = rec.launch.slot
	case rec.id >= 0:
		identifier = rec.id
	}
	return fmt.Sprintf("%s-%d", hostname, identifier)
}

func (m *ServerManager) serverConsoleRelayEnabled(rec *serverRecord, console *serverConsole) bool {
	if m == nil || rec == nil || console == nil {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if rec.running == nil || rec.running.console != console {
		return false
	}
	return rec.relayConsoleReady
}

func shouldRelayServerConsoleLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}

	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "packfile:") {
		return false
	}
	if strings.Contains(lower, "findfile:") && !strings.Contains(lower, "can't find") {
		return false
	}

	return true
}

func (m *ServerManager) formatServerConsoleRelayLine(rec *serverRecord, line string) (string, bool) {
	msg := strings.TrimRight(line, "\r\n")
	if !shouldRelayServerConsoleLine(msg) {
		return "", false
	}
	return fmt.Sprintf("[%s] %s", m.serverConsoleLabel(rec), msg), true
}

func (m *ServerManager) relayServerConsoleToNexus(rec *serverRecord, console *serverConsole) {
	if console == nil {
		return
	}

	lines, cancel := console.SubscribeFiltered(1024, func(line string) (string, bool) {
		if console.consumeSuppressedRelayEchoLine(line) {
			return "", false
		}
		if !m.serverConsoleRelayEnabled(rec, console) {
			return "", false
		}
		return m.formatServerConsoleRelayLine(rec, line)
	})
	defer cancel()

	for formatted := range lines {
		infofNoTail("%s", formatted)
	}
}

func NewServerManager(dataDir, logsDir string) *ServerManager {
	return &ServerManager{
		dataDir:         dataDir,
		logsDir:         logsDir,
		serversByID:     make(map[int]*serverRecord),
		serverIDsByPort: make(map[int][]int),
	}
}

func cloneServerLaunch(launch serverLaunch) serverLaunch {
	cloned := launch
	cloned.args = append([]string(nil), launch.args...)
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
		if strings.ContainsAny(gameDir, `/\`) {
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

// RegisterServerLaunch adds a new server to nexus runtime state.
//
// The server is tracked immediately (seen by nexus), but not started. This is
// the primitive future `rcon nexus add`/`start` commands will build on.
func (m *ServerManager) RegisterServerLaunch(launch serverLaunch) *serverRecord {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec := &serverRecord{
		id:     m.nextServerID,
		launch: cloneServerLaunch(launch),
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
		Slot:       rec.launch.slot,
		ListenPort: rec.resolvedPort,
		SearchPath: append([]string(nil), rec.resolvedSearchPath...),
	}
	if rec.running != nil {
		rec.running.spec = *rec.spec
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
		rec.hostname = update.observedHostname
		rec.mapName = update.observedMapName
		rec.players = update.observedPlayers
		rec.maxPlayers = update.observedMaxPlayers
		rec.lastSeen = now
		if rec.running == nil || !rec.awaitingServerInfo {
			continue
		}
		rec.awaitingServerInfo = false
		rec.relayConsoleReady = true
		onlineCommands = append(onlineCommands, startupOnlineCommand{
			label:   serverConsoleLabelFromRecord(rec),
			console: rec.running.console,
		})
	}
	m.mu.Unlock()

	for _, cmd := range onlineCommands {
		if cmd.console == nil {
			errorf("server %s online marker failed: server console unavailable", cmd.label)
			continue
		}
		if err := cmd.console.WriteCommandWithOptions(
			"echo online and accepting clients",
			serverConsoleWriteOptions{SuppressRelayEcho: true},
		); err != nil {
			errorf("server %s online marker failed: %v", cmd.label, err)
		}
	}
}

func (m *ServerManager) updatePort(rec *serverRecord, port int) {
	m.updateServerState(serverStateUpdate{
		hasResolvedPort: true,
		rec:             rec,
		resolvedPort:    port,
	})
}

func (m *ServerManager) updateSearchPath(rec *serverRecord, searchPath []string) {
	searchPath = normalizeSearchPath(searchPath)
	m.updateServerState(serverStateUpdate{
		rec:                rec,
		resolvedSearchPath: searchPath,
	})
}

func (m *ServerManager) ServerByListenPort(port int) *managedServer {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, serverID := range m.serverIDsByPort[port] {
		rec := m.serversByID[serverID]
		if rec == nil {
			continue
		}
		s := rec.running
		if s == nil {
			continue
		}
		if s.cmd == nil || s.cmd.Process == nil || !isProcessAlive(s.cmd.Process) {
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
		if rec == nil || rec.running == nil {
			continue
		}
		out = append(out, runningServerEntry{rec: rec, srv: rec.running})
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
		Hostname:   rec.hostname,
		MapName:    rec.mapName,
		Players:    rec.players,
		MaxPlayers: rec.maxPlayers,
	}

	snap.Slot = rec.launch.slot
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

	s := rec.running
	if s != nil && s.cmd != nil && s.cmd.Process != nil && isProcessAlive(s.cmd.Process) {
		snap.PID = s.cmd.Process.Pid
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

func (m *ServerManager) StartAll() error {
	if m.dataDir == "" {
		return fmt.Errorf("DATA_DIR is empty")
	}
	if st, err := os.Stat(m.dataDir); err != nil || !st.IsDir() {
		return fmt.Errorf("DATA_DIR is not a directory: %s", m.dataDir)
	}
	if m.logsDir == "" {
		return fmt.Errorf("LOGS_DIR is empty")
	}
	if err := os.MkdirAll(m.logsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create LOGS_DIR: %w", err)
	}

	launches, mods, err := m.planLaunches()
	if err != nil {
		return err
	}
	infof("Launching %d servers...", len(launches))

	runtimeBasedir, err := prepareRuntimeBasedir(m.dataDir, mods)
	if err != nil {
		return err
	}
	m.runtimeBasedir = runtimeBasedir

	m.mu.Lock()
	m.serversByID = make(map[int]*serverRecord, len(launches))
	m.serverIDsByPort = make(map[int][]int, len(launches))
	m.nextServerID = 0
	m.mu.Unlock()

	for _, launch := range launches {
		rec := m.RegisterServerLaunch(launch)
		if err := m.startRecord(rec); err != nil {
			_ = m.StopAll(context.Background(), 2*time.Second)
			return err
		}
	}

	return nil
}

func (m *ServerManager) startServer(runtimeBasedir string, launch serverLaunch, onPort func(int), onSearchPath func([]string)) (*managedServer, error) {
	logDirName := launch.logDir
	logDir := filepath.Join(m.logsDir, logDirName)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", logDir, err)
	}

	logPath := filepath.Join(logDir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}

	cmd := exec.Command(launch.binary, launch.args...)
	cmd.Dir = runtimeBasedir

	// Without a tty, nqserver's stdio is fully-buffered and may never flush (especially if terminated by SIGTERM),
	// resulting in empty logs. A pty makes it line-buffered so logs are written as they happen.
	ptyParent, ptyChild, err := pty.Open()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("open pty for server slot=%d bin=%q: %w", launch.slot, launch.binary, err)
	}
	cmd.Stdin = ptyChild
	cmd.Stdout = ptyChild
	cmd.Stderr = ptyChild

	debugf("Starting server slot=%d bin=%q args=%q", launch.slot, launch.binary, launch.args)

	if err := cmd.Start(); err != nil {
		_ = ptyParent.Close()
		_ = ptyChild.Close()
		_ = logFile.Close()
		return nil, fmt.Errorf("start server slot=%d bin=%q: %w", launch.slot, launch.binary, err)
	}

	// Parent closes its copy of the child-side FD after spawn.
	_ = ptyChild.Close()

	console := newServerConsole(ptyParent)
	srv := &managedServer{
		cmd:     cmd,
		console: console,
		done:    make(chan error, 1),
	}

	go func() {
		go monitorServerStartup(console, true, onPort, onSearchPath)

		copyDone := make(chan struct{})
		go func() {
			console.Run(logFile)
			close(copyDone)
		}()

		err := cmd.Wait()
		_ = ptyParent.Close()
		<-copyDone
		srv.done <- err
		_ = logFile.Close()
	}()

	// If the process dies immediately, surface that now to avoid confusing startup failures.
	time.Sleep(50 * time.Millisecond)
	if !isProcessAlive(cmd.Process) {
		err := <-srv.done
		if err == nil {
			err = errors.New("process exited")
		}
		return nil, fmt.Errorf("server slot=%d bin=%q exited immediately: %w (see %s)", launch.slot, launch.binary, err, logPath)
	}

	return srv, nil
}

func (m *ServerManager) StopAll(ctx context.Context, killAfter time.Duration) error {
	var errs []error
	running := m.runningServers()

	// Ask all servers to stop.
	for _, entry := range running {
		s := entry.srv
		if s == nil || s.cmd == nil || s.cmd.Process == nil {
			continue
		}
		if !isProcessAlive(s.cmd.Process) {
			continue
		}
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}

	for _, entry := range running {
		s := entry.srv
		if s == nil {
			continue
		}
		if err := m.stopServer(ctx, entry.rec, s, killAfter, false); err != nil {
			errs = append(errs, err)
		}
	}

	// Cleanup the ephemeral runtime basedir after all servers have exited (or after
	// we have attempted to stop them).
	if m.runtimeBasedir != "" {
		_ = os.RemoveAll(m.runtimeBasedir)
		m.runtimeBasedir = ""
	}

	return errors.Join(errs...)
}

func isProcessAlive(p *os.Process) bool {
	if p == nil {
		return false
	}
	// Signal 0 checks for existence without sending a real signal.
	return p.Signal(syscall.Signal(0)) == nil
}
