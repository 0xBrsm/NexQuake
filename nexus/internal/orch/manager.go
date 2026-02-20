package orch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/creack/pty"
)

// ServerManager orchestrates all managed game servers.
type ServerManager struct {
	gameDir string
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
	gameDir, logsDir string,
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
		gameDir:         gameDir,
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
