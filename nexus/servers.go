package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

var globalServerManager *ServerManager

// serverSpec is nexus's minimal "known about this server" metadata.
//
// Slot is the zero-based entry index in servers.ini (or 0 for the default
// fallback). ListenPort is the resolved listen port used for routing; it may
// start as 0 (unknown) and be resolved from the server console at runtime.
type serverSpec struct {
	Slot       int
	ModName    string
	ListenPort int
	LogDir     string
}

type managedServer struct {
	spec    serverSpec
	cmd     *exec.Cmd
	logFile *os.File
	pty     *os.File
	done    chan error
}

func (s *managedServer) WriteConsole(cmd string) error {
	if s == nil || s.pty == nil {
		return fmt.Errorf("server console unavailable")
	}
	line := strings.TrimSpace(cmd)
	if line == "" {
		return nil
	}
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	_, err := io.WriteString(s.pty, line)
	return err
}

type serverRecord struct {
	launch serverLaunch

	running   *managedServer
	lastError string

	// Best-effort observed info from CCREP_SERVER_INFO polling (used for slist
	// aggregation and for future `rcon nexus list` output).
	hostname   string
	mapName    string
	players    byte
	maxPlayers byte
	lastSeen   time.Time
}

type ServerManager struct {
	dataDir string
	logsDir string

	mu            sync.RWMutex
	serversByPort map[int][]*serverRecord // listen port -> configured server(s)

	runtimeBasedir string
}

func NewServerManager(dataDir, logsDir string) *ServerManager {
	return &ServerManager{
		dataDir:       dataDir,
		logsDir:       logsDir,
		serversByPort: make(map[int][]*serverRecord),
	}
}

func cloneServerLaunch(launch serverLaunch) serverLaunch {
	cloned := launch
	cloned.args = append([]string(nil), launch.args...)
	return cloned
}

// RegisterServerLaunch adds a new server to nexus runtime state.
//
// The server is tracked immediately (seen by nexus), but not started. This is
// the primitive future `rcon nexus add`/`start` commands will build on.
func (m *ServerManager) RegisterServerLaunch(launch serverLaunch) *serverRecord {
	rec := &serverRecord{launch: cloneServerLaunch(launch)}

	m.mu.Lock()
	m.serversByPort[rec.launch.spec.ListenPort] = append(m.serversByPort[rec.launch.spec.ListenPort], rec)
	m.mu.Unlock()

	return rec
}

func removeRecordFromSlice(recs []*serverRecord, target *serverRecord) []*serverRecord {
	for i := range recs {
		if recs[i] == target {
			return append(recs[:i], recs[i+1:]...)
		}
	}
	return recs
}

func hasRecord(recs []*serverRecord, target *serverRecord) bool {
	for _, rec := range recs {
		if rec == target {
			return true
		}
	}
	return false
}

func (m *ServerManager) updateServerListenPort(rec *serverRecord, port int) {
	if rec == nil || port < 1 || port > 65535 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	oldPort := rec.launch.spec.ListenPort
	if oldPort == port {
		return
	}

	if bucket, ok := m.serversByPort[oldPort]; ok {
		bucket = removeRecordFromSlice(bucket, rec)
		if len(bucket) == 0 {
			delete(m.serversByPort, oldPort)
		} else {
			m.serversByPort[oldPort] = bucket
		}
	}

	rec.launch.spec.ListenPort = port
	if rec.running != nil {
		rec.running.spec.ListenPort = port
	}

	bucket := m.serversByPort[port]
	if !hasRecord(bucket, rec) {
		m.serversByPort[port] = append(bucket, rec)
	}
}

func (m *ServerManager) ServerByListenPort(port int) *managedServer {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, rec := range m.serversByPort[port] {
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

func (m *ServerManager) UpdateObservedServerInfo(port int, hostname, mapName string, players, maxPlayers byte) {
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, rec := range m.serversByPort[port] {
		rec.hostname = hostname
		rec.mapName = mapName
		rec.players = players
		rec.maxPlayers = maxPlayers
		rec.lastSeen = now
	}
}

type runningServerEntry struct {
	rec *serverRecord
	srv *managedServer
}

func (m *ServerManager) runningServers() []runningServerEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []runningServerEntry
	for _, recs := range m.serversByPort {
		for _, rec := range recs {
			if rec == nil || rec.running == nil {
				continue
			}
			out = append(out, runningServerEntry{rec: rec, srv: rec.running})
		}
	}
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

	runtimeBasedir, err := prepareRuntimeBasedir(m.dataDir, mods)
	if err != nil {
		return err
	}
	m.runtimeBasedir = runtimeBasedir

	m.mu.Lock()
	m.serversByPort = make(map[int][]*serverRecord, len(launches))
	m.mu.Unlock()

	for _, launch := range launches {
		rec := m.RegisterServerLaunch(launch)
		srv, err := m.startOne(runtimeBasedir, rec.launch.spec, rec.launch.binary, rec.launch.args, func(port int) {
			m.updateServerListenPort(rec, port)
			infof("Resolved server listen port (slot=%d mod=%q port=%d)", rec.launch.spec.Slot, rec.launch.spec.ModName, port)
		})
		if err != nil {
			m.mu.Lock()
			rec.running = nil
			rec.lastError = err.Error()
			m.mu.Unlock()
			_ = m.StopAll(context.Background(), 2*time.Second)
			return err
		}

		m.mu.Lock()
		rec.running = srv
		rec.lastError = ""
		m.mu.Unlock()
	}

	return nil
}

func parsePortConsoleLine(line string) (int, bool) {
	const (
		prefix = "\"port\" is \""
		suffix = "\""
	)
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(s, prefix), suffix)
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

func monitorServerConsole(ptyMaster *os.File, logFile *os.File, shouldProbePort bool, onPort func(int)) {
	reader := bufio.NewReader(ptyMaster)
	requestedPort := false
	resolvedPort := false

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			_, _ = io.WriteString(logFile, line)

			trimmed := strings.TrimSpace(line)
			if shouldProbePort && !requestedPort && strings.Contains(trimmed, "========Quake Initialized=========") {
				_, _ = io.WriteString(ptyMaster, "port\n")
				requestedPort = true
			}
			if shouldProbePort && requestedPort && !resolvedPort {
				if port, ok := parsePortConsoleLine(trimmed); ok {
					resolvedPort = true
					if onPort != nil {
						onPort(port)
					}
				}
			}
		}

		if err != nil {
			return
		}
	}
}

func (m *ServerManager) startOne(runtimeBasedir string, spec serverSpec, binary string, args []string, onPort func(int)) (*managedServer, error) {
	logDirName := spec.LogDir
	if logDirName == "" {
		logDirName = spec.ModName
	}
	logDir := filepath.Join(m.logsDir, logDirName)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", logDir, err)
	}

	logPath := filepath.Join(logDir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}

	cmd := exec.Command(binary, args...)
	cmd.Dir = runtimeBasedir

	// Without a tty, nqserver's stdio is fully-buffered and may never flush (especially if terminated by SIGTERM),
	// resulting in empty logs. A pty makes it line-buffered so logs are written as they happen.
	ptyMaster, ptySlave, err := pty.Open()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("open pty for server %q: %w", spec.ModName, err)
	}
	cmd.Stdin = ptySlave
	cmd.Stdout = ptySlave
	cmd.Stderr = ptySlave

	infof("Starting server slot=%d mod=%q bin=%q port=%d args=%q",
		spec.Slot, spec.ModName, binary, spec.ListenPort, args)

	if err := cmd.Start(); err != nil {
		_ = ptyMaster.Close()
		_ = ptySlave.Close()
		_ = logFile.Close()
		return nil, fmt.Errorf("start server %q: %w", spec.ModName, err)
	}

	// Parent must close its copy of the slave FD; the child owns it now.
	_ = ptySlave.Close()

	srv := &managedServer{
		spec:    spec,
		cmd:     cmd,
		logFile: logFile,
		pty:     ptyMaster,
		done:    make(chan error, 1),
	}

	go func() {
		copyDone := make(chan struct{})
		go func() {
			monitorServerConsole(ptyMaster, logFile, spec.ListenPort == 0, onPort)
			close(copyDone)
		}()

		err := cmd.Wait()
		_ = ptyMaster.Close()
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
		return nil, fmt.Errorf("server %q exited immediately: %w (see %s)", spec.ModName, err, logPath)
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

		select {
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
			return errors.Join(errs...)
		case err := <-s.done:
			m.mu.Lock()
			if entry.rec != nil && entry.rec.running == s {
				entry.rec.running = nil
				entry.rec.lastError = ""
			}
			m.mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
		case <-time.After(killAfter):
			// If it's still alive after SIGTERM, kill it.
			if s.cmd != nil && s.cmd.Process != nil && isProcessAlive(s.cmd.Process) {
				_ = s.cmd.Process.Kill()
			}
			// Wait for it to exit to close its log file and avoid zombies.
			select {
			case err := <-s.done:
				m.mu.Lock()
				if entry.rec != nil && entry.rec.running == s {
					entry.rec.running = nil
					entry.rec.lastError = ""
				}
				m.mu.Unlock()
				if err != nil {
					errs = append(errs, err)
				}
			case <-time.After(1 * time.Second):
				m.mu.Lock()
				if entry.rec != nil && entry.rec.running == s {
					entry.rec.lastError = "did not exit after kill"
				}
				m.mu.Unlock()
				errs = append(errs, fmt.Errorf("server slot=%d mod=%q did not exit after kill", s.spec.Slot, s.spec.ModName))
			case <-ctx.Done():
				errs = append(errs, ctx.Err())
				return errors.Join(errs...)
			}
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
