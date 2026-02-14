package orch

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

	"github.com/0xBrsm/NexQuake/nexus/internal/gamedata"
	"github.com/creack/pty"
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

func (s *managedServer) writeConsoleAndCapture(cmd string, maxWait, idleWait time.Duration) (string, error) {
	return s.writeConsoleAndCaptureFiltered(cmd, maxWait, idleWait, nil)
}

func (s *managedServer) writeConsoleAndCaptureFiltered(cmd string, maxWait, idleWait time.Duration, filter serverConsoleLineFilter) (string, error) {
	if s.Console == nil {
		return "", fmt.Errorf("server console unavailable")
	}
	return s.Console.captureCommandOutputFiltered(cmd, maxWait, idleWait, filter)
}

// serverRecord tracks one server slot in the nexus registry.
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

// ServerManager orchestrates all managed game servers.
type ServerManager struct {
	dataDir string
	logsDir string
	infof   func(string, ...any)
	debugf  func(string, ...any)
	warnf   func(string, ...any)
	errorf  func(string, ...any)

	consoleInfof  func(string, ...any)
	formatLogLine func(string, time.Time) string

	mu              sync.RWMutex
	serversByID     map[int]*serverRecord
	serverIDsByPort map[int][]int

	nextServerID   int
	runtimeBasedir string
}

func (m *ServerManager) serverConsoleLabel(rec *serverRecord) string {
	if rec == nil {
		return "1-server"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return serverConsoleLabelFromRecord(rec)
}

func serverConsoleLabelFromRecord(rec *serverRecord) string {
	if rec == nil {
		return "1-server"
	}

	hostname := "server"
	if name := strings.TrimSpace(rec.Hostname); name != "" {
		hostname = name
	}
	identifier := 1
	switch {
	case rec.Launch.Slot >= 0:
		identifier = rec.Launch.Slot + 1
	case rec.id >= 0:
		identifier = rec.id + 1
	}
	return fmt.Sprintf("%d-%s", identifier, hostname)
}

func (m *ServerManager) serverConsoleRelayEnabled(rec *serverRecord, console *serverConsole) bool {
	if rec == nil || console == nil {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if rec.Running == nil || rec.Running.Console != console {
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
	if strings.Contains(lower, "findfile: can't find maps/") && strings.HasSuffix(lower, ".ent") {
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

	lines, cancel := console.subscribeFiltered(1024, func(line string) (string, bool) {
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
		m.consoleInfof("%s", formatted)
	}
}

func noopLogf(string, ...any) {}

func identityLogLine(line string, _ time.Time) string {
	return line
}

// NewServerManager creates a new server manager with injected logging.
func NewServerManager(
	dataDir, logsDir string,
	infof, consoleInfof, debugf, warnf, errorf func(string, ...any),
	formatLogLine func(string, time.Time) string,
) *ServerManager {
	if infof == nil {
		infof = noopLogf
	}
	if consoleInfof == nil {
		consoleInfof = infof
	}
	if debugf == nil {
		debugf = noopLogf
	}
	if warnf == nil {
		warnf = noopLogf
	}
	if errorf == nil {
		errorf = noopLogf
	}
	if formatLogLine == nil {
		formatLogLine = identityLogLine
	}
	return &ServerManager{
		dataDir:         dataDir,
		logsDir:         logsDir,
		infof:           infof,
		debugf:          debugf,
		warnf:           warnf,
		errorf:          errorf,
		consoleInfof:    consoleInfof,
		formatLogLine:   formatLogLine,
		serversByID:     make(map[int]*serverRecord),
		serverIDsByPort: make(map[int][]int),
	}
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

// StartAll launches all servers from the servers.ini plan.
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
	m.infof("Launching %d servers...", len(launches))

	runtimeBasedir, err := gamedata.PrepareRuntimeBasedir(m.dataDir, mods)
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
	logDirName := launch.LogDir
	logDir := filepath.Join(m.logsDir, logDirName)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", logDir, err)
	}

	logPath := filepath.Join(logDir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}

	cmd := exec.Command(launch.Binary, launch.Args...)
	cmd.Dir = runtimeBasedir

	ptyParent, ptyChild, err := pty.Open()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("open pty for server slot=%d bin=%q: %w", launch.Slot, launch.Binary, err)
	}
	cmd.Stdin = ptyChild
	cmd.Stdout = ptyChild
	cmd.Stderr = ptyChild

	m.debugf("Starting server %d: %s %s", launch.Slot+1, launch.Binary, strings.Join(launch.Args, " "))

	if err := cmd.Start(); err != nil {
		_ = ptyParent.Close()
		_ = ptyChild.Close()
		_ = logFile.Close()
		return nil, fmt.Errorf("start server slot=%d bin=%q: %w", launch.Slot, launch.Binary, err)
	}

	_ = ptyChild.Close()

	console := newServerConsole(ptyParent)
	srv := &managedServer{
		Cmd:     cmd,
		Console: console,
		done:    make(chan error, 1),
	}

	formatLogLine := m.formatLogLine
	go func() {
		go monitorServerStartup(console, true, onPort, onSearchPath)

		copyDone := make(chan struct{})
		go func() {
			console.run(logFile, formatLogLine)
			close(copyDone)
		}()

		err := cmd.Wait()
		_ = ptyParent.Close()
		<-copyDone
		srv.done <- err
		_ = logFile.Close()
	}()

	time.Sleep(50 * time.Millisecond)
	if !isProcessAlive(cmd.Process) {
		err := <-srv.done
		if err == nil {
			err = errors.New("process exited")
		}
		return nil, fmt.Errorf("server slot=%d bin=%q exited immediately: %w (see %s)", launch.Slot, launch.Binary, err, logPath)
	}

	return srv, nil
}

// StopAll gracefully stops all running servers.
func (m *ServerManager) StopAll(ctx context.Context, killAfter time.Duration) error {
	var errs []error
	running := m.runningServers()

	for _, entry := range running {
		s := entry.srv
		if s == nil || s.Cmd == nil || s.Cmd.Process == nil {
			continue
		}
		if !isProcessAlive(s.Cmd.Process) {
			continue
		}
		_ = s.Cmd.Process.Signal(syscall.SIGTERM)
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
	return p.Signal(syscall.Signal(0)) == nil
}

// ResolveManifestGameDirs resolves the full search path for a given game directory,
// including fallback directories from running servers.
func (m *ServerManager) ResolveManifestGameDirs(gameDir string) []string {
	gameDir = strings.TrimSpace(gameDir)
	if gameDir == "" {
		return nil
	}

	resolved := []string{gameDir}
	seen := map[string]struct{}{gameDir: {}}

	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]int, 0, len(m.serversByID))
	for id := range m.serversByID {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	for _, id := range ids {
		rec := m.serversByID[id]
		if rec.spec == nil {
			continue
		}
		idx := slices.Index(rec.spec.SearchPath, gameDir)
		if idx < 0 {
			continue
		}
		for _, fallback := range rec.spec.SearchPath[idx+1:] {
			fallback = strings.TrimSpace(fallback)
			if fallback == "" {
				continue
			}
			if _, ok := seen[fallback]; ok {
				continue
			}
			seen[fallback] = struct{}{}
			resolved = append(resolved, fallback)
		}
	}
	return resolved
}
