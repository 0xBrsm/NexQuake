package orch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/creack/pty"
	"github.com/google/shlex"
)

type serverLaunch struct {
	Line   int
	LogDir string
	Binary string
	Args   []string
}

var unsupportedLaunchArgs = map[string]struct{}{
	"-basedir":  {},
	"-hipnotic": {},
	"-path":     {},
	"-rogue":    {},
}

const serverStartupCCREPTimeout = 10 * time.Second

func resetRecordStartupState(rec *serverRecord) {
	rec.relayConsoleReady = false
	rec.awaitingServerInfo = false
	rec.startupTimedOutOnce = false
}

// StartAll launches all servers from the servers.ini plan.
func (m *ServerManager) StartAll() error {
	if m.gameDir == "" {
		return fmt.Errorf("GAME_DIR is empty")
	}
	if st, err := os.Stat(m.gameDir); err != nil || !st.IsDir() {
		return fmt.Errorf("GAME_DIR is not a directory: %s", m.gameDir)
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

	runtimeBasedir, err := assets.PrepareRuntimeBasedir(m.gameDir, mods)
	if err != nil {
		return err
	}
	m.runtimeBasedir = runtimeBasedir

	m.mu.Lock()
	m.serversByID = make(map[int]*serverRecord, len(launches))
	m.serverIDsByPort = make(map[int][]int, len(launches))
	m.nextServerID = 0
	m.resetPoolRegistryLocked()
	m.mu.Unlock()

	for _, launch := range launches {
		rec, err := m.registerPoolLaunch(launch)
		if err != nil {
			_ = m.StopAll(context.Background(), 2*time.Second)
			return err
		}
		if err := m.startRecord(rec); err != nil {
			_ = m.StopAll(context.Background(), 2*time.Second)
			return err
		}
	}

	return nil
}

func (m *ServerManager) startRecord(rec *serverRecord) error {
	if rec == nil {
		return fmt.Errorf("server record not found")
	}

	m.mu.Lock()
	if m.runtimeBasedir == "" {
		m.mu.Unlock()
		return fmt.Errorf("runtime not initialized")
	}
	if rec.Running != nil && rec.Running.Cmd != nil && rec.Running.Cmd.Process != nil && isProcessAlive(rec.Running.Cmd.Process) {
		m.mu.Unlock()
		return errAlreadyRunning
	}
	runtimeBasedir := m.runtimeBasedir
	launch := cloneServerLaunch(rec.Launch)

	// Clear stale resolved-port state from any previous run so that
	// assignPortLocked will accept the new port the OS hands out
	// (critical for -port 0 servers that get a different ephemeral port
	// each time they start).
	m.removeServerIDFromPortLocked(rec.resolvedPort, rec.id)
	if rec.spec != nil {
		m.removeServerIDFromPortLocked(rec.spec.ListenPort, rec.id)
	}
	rec.resolvedPortKnown = false
	rec.resolvedPort = 0
	rec.resolvedSearchPath = nil
	rec.spec = nil
	m.mu.Unlock()

	srv, err := m.startServer(
		runtimeBasedir,
		launch,
		func(port int) {
			m.updatePort(rec, port)
			m.debugf("Resolved server %s port: %d", m.serverConsoleLabel(rec), port)
		},
		func(searchPath []string) {
			normalized := normalizeSearchPath(searchPath)
			m.updateSearchPathNormalized(rec, normalized)
			m.debugf("Resolved server search path (%s active=%q paths=%q)",
				m.serverConsoleLabel(rec), activeGameDir(normalized), normalized)
		},
	)
	if err != nil {
		m.mu.Lock()
		rec.Running = nil
		resetRecordStartupState(rec)
		rec.lastError = err.Error()
		m.mu.Unlock()
		return err
	}

	m.mu.Lock()
	rec.Running = srv
	rec.relayConsoleReady = false
	rec.awaitingServerInfo = true
	rec.startupTimedOutOnce = false
	rec.lastError = ""
	rec.Hostname = ""
	rec.MapName = ""
	rec.Players = 0
	rec.MaxPlayers = 0
	rec.LastSeen = time.Time{}
	m.mu.Unlock()
	m.resetPoolBackendState(rec.id)

	go m.relayServerConsoleToNexus(rec, srv.Console)
	go m.monitorServerStartupTimeout(rec, srv, serverStartupCCREPTimeout)

	return nil
}

func (m *ServerManager) monitorServerStartupTimeout(rec *serverRecord, srv *managedServer, timeout time.Duration) {
	if rec == nil || srv == nil || timeout <= 0 {
		return
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	<-timer.C

	m.mu.Lock()
	if rec.Running != srv || !rec.awaitingServerInfo || rec.startupTimedOutOnce {
		m.mu.Unlock()
		return
	}
	rec.startupTimedOutOnce = true
	label := m.serverConsoleLabelLocked(rec)
	m.mu.Unlock()

	m.warnf("server %s failed to start in %d seconds", label, int(timeout/time.Second))
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
	launchLabel := formatLaunchLabel(launch)

	ptyParent, ptyChild, err := pty.Open()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("open pty for server %s bin=%q: %w", launchLabel, launch.Binary, err)
	}
	cmd.Stdin = ptyChild
	cmd.Stdout = ptyChild
	cmd.Stderr = ptyChild

	m.debugf("Starting server %s: %s %s", launchLabel, launch.Binary, strings.Join(launch.Args, " "))

	if err := cmd.Start(); err != nil {
		_ = ptyParent.Close()
		_ = ptyChild.Close()
		_ = logFile.Close()
		return nil, fmt.Errorf("start server %s bin=%q: %w", launchLabel, launch.Binary, err)
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
		return nil, fmt.Errorf("server %s bin=%q exited immediately: %w (see %s)", launchLabel, launch.Binary, err, logPath)
	}

	return srv, nil
}

// StopAll gracefully stops all running servers.
func (m *ServerManager) StopAll(ctx context.Context, killAfter time.Duration) error {
	var errs []error
	running := m.runningServers()

	for _, entry := range running {
		s := entry.srv
		if s != nil && s.Cmd != nil && s.Cmd.Process != nil && isProcessAlive(s.Cmd.Process) {
			_ = s.Cmd.Process.Signal(syscall.SIGTERM)
		}
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

	m.closePoolRegistry()

	return errors.Join(errs...)
}

func (m *ServerManager) stopServer(ctx context.Context, rec *serverRecord, s *managedServer, killAfter time.Duration, sendSignal bool) error {
	if s == nil {
		return nil
	}
	waitAfterSignal := killAfter

	clearRunning := func(err error) error {
		m.mu.Lock()
		if rec != nil && rec.Running == s {
			rec.Running = nil
			resetRecordStartupState(rec)
			rec.lastError = ""
			m.refreshPoolForServerLocked(rec.id)
		}
		m.mu.Unlock()
		return err
	}

	if sendSignal && s.Cmd != nil && s.Cmd.Process != nil && isProcessAlive(s.Cmd.Process) {
		_ = s.writeConsole("quit")

		quitGrace := 750 * time.Millisecond
		if killAfter > 0 && killAfter < quitGrace {
			quitGrace = killAfter
		}
		waitAfterSignal = max(0, killAfter-quitGrace)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-s.done:
			return clearRunning(err)
		case <-time.After(quitGrace):
		}

		if isProcessAlive(s.Cmd.Process) {
			_ = s.Cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-s.done:
		return clearRunning(err)
	case <-time.After(waitAfterSignal):
		if s.Cmd != nil && s.Cmd.Process != nil && isProcessAlive(s.Cmd.Process) {
			_ = s.Cmd.Process.Kill()
		}
		select {
		case err := <-s.done:
			return clearRunning(err)
		case <-time.After(1 * time.Second):
			label := "server"
			gameDir := ""
			m.mu.Lock()
			if rec != nil {
				if rec.Running == s {
					rec.lastError = "did not exit after kill"
				}
				label = m.serverConsoleLabelLocked(rec)
				if rec.spec != nil {
					gameDir = activeGameDir(rec.spec.SearchPath)
				}
			}
			m.mu.Unlock()
			return fmt.Errorf("server %s mod=%q did not exit after kill", label, gameDir)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func isProcessAlive(p *os.Process) bool {
	if p == nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func (m *ServerManager) planLaunches() ([]serverLaunch, []string, error) {
	iniPath := filepath.Join(m.gameDir, "servers.ini")
	startedAt := time.Now().UTC()
	entries, ok, err := loadServersIni(iniPath, startedAt, m.warnf)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		startTag := startedAt.Format("20060102T150405Z")
		binary := "nqserver"
		entries = []serverLaunch{
			{
				Line:   0,
				LogDir: fmt.Sprintf("%d-%s-%s", 0, filepath.Base(binary), startTag),
				Binary: binary,
				Args:   []string{"-dedicated"},
			},
		}
		m.infof("Launch plan not found at %s; launching default server", iniPath)
	} else {
		m.debugf("Using launch plan: %s (%d server entries)", iniPath, len(entries))
	}

	// Build merged runtime dirs from whatever mods exist in GAME_DIR.
	mods, err := assets.ListMods(m.gameDir)
	if err != nil {
		return nil, nil, err
	}
	return entries, mods, nil
}

func loadServersIni(iniPath string, startedAt time.Time, warnf func(string, ...any)) (entries []serverLaunch, found bool, err error) {
	st, err := os.Stat(iniPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("stat %s: %w", iniPath, err)
	}
	if st.IsDir() {
		return nil, false, fmt.Errorf("servers.ini path is a directory: %s", iniPath)
	}

	f, err := os.Open(iniPath)
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", iniPath, err)
	}
	defer f.Close()

	startTag := startedAt.UTC().Format("20060102T150405Z")
	scanner := bufio.NewScanner(f)
	launchGroups := make(map[string][]string)
	launchLine := -1
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, ";") {
			continue
		}

		fields, err := shlex.Split(raw)
		if err != nil {
			return nil, true, fmt.Errorf("servers.ini line %d: %w", lineNo, err)
		}
		if len(fields) == 0 {
			continue
		}

		if strings.HasPrefix(fields[0], "@") {
			if len(fields[0]) <= 1 {
				return nil, true, fmt.Errorf("servers.ini line %d: invalid group name %q", lineNo, fields[0])
			}
			launchGroups[fields[0]] = append([]string(nil), fields[1:]...)
			continue
		}

		if len(launchGroups) != 0 {
			fields = mergeLaunchGroups(fields, launchGroups)
		}
		if len(fields) == 0 {
			continue
		}

		launchLine++

		binary := fields[0]
		args := applyLaunchArgTemplates(fields[1:])
		if unsupportedArg, ok := findUnsupportedLaunchArg(args); ok {
			warnf("Skipping servers.ini line %d: %s is not currently supported", lineNo, unsupportedArg)
			continue
		}

		logDir := fmt.Sprintf("%d-%s-%s", launchLine, filepath.Base(binary), startTag)

		entries = append(entries, serverLaunch{
			Line:   launchLine,
			LogDir: logDir,
			Binary: binary,
			Args:   args,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, true, fmt.Errorf("read %s: %w", iniPath, err)
	}
	if len(entries) == 0 {
		return nil, true, fmt.Errorf("servers.ini has no server launch lines: %s", iniPath)
	}

	return entries, true, nil
}

func findUnsupportedLaunchArg(args []string) (string, bool) {
	for _, arg := range args {
		if _, unsupported := unsupportedLaunchArgs[arg]; unsupported {
			return arg, true
		}
	}
	return "", false
}

func applyLaunchArgTemplates(args []string) []string {
	if len(args) == 0 {
		return nil
	}

	out := append([]string(nil), args...)
	seen := make(map[string]string)

	for i := 0; i < len(out); i++ {
		if !isLaunchKeyToken(out[i]) {
			continue
		}
		key := out[i][1:]
		if _, found := seen[key]; !found && i+1 < len(out) && !isLaunchKeyToken(out[i+1]) {
			seen[key] = out[i+1]
		}
		for i+1 < len(out) && !isLaunchKeyToken(out[i+1]) {
			i++
		}
	}

	for i, token := range out {
		if !strings.HasPrefix(token, "%") {
			continue
		}
		if v, found := seen[token[1:]]; found {
			out[i] = v
		}
	}

	return out
}

func mergeLaunchGroups(fields []string, launchGroups map[string][]string) []string {
	if len(fields) == 0 || len(launchGroups) == 0 {
		return fields
	}

	// The launch line's explicit args win over group-provided defaults.
	explicitKeys := make(map[string]struct{})
	for i := 1; i < len(fields); i++ {
		token := fields[i]
		if !isLaunchKeyToken(token) {
			continue
		}
		explicitKeys[token[1:]] = struct{}{}
	}

	insertedKeys := make(map[string]struct{})
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		token := fields[i]
		groupFields, ok := launchGroups[token]
		if !ok {
			out = append(out, token)
			continue
		}

		// Insert group fields at the reference point, but skip any keys already
		// provided by the launch line (or earlier groups).
		for j := 0; j < len(groupFields); j++ {
			token := groupFields[j]
			if !isLaunchKeyToken(token) {
				out = append(out, token)
				continue
			}

			key := token[1:]
			_, inExplicit := explicitKeys[key]
			_, inInserted := insertedKeys[key]
			if inExplicit || inInserted {
				for j+1 < len(groupFields) && !isLaunchKeyToken(groupFields[j+1]) {
					j++
				}
				continue
			}
			insertedKeys[key] = struct{}{}

			out = append(out, token)
			for j+1 < len(groupFields) && !isLaunchKeyToken(groupFields[j+1]) {
				j++
				out = append(out, groupFields[j])
			}
		}
	}

	return out
}

func isLaunchKeyToken(token string) bool {
	return len(token) >= 2 && (token[0] == '-' || token[0] == '+')
}
