package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/creack/pty"
)

type serverSpec struct {
	ID       int
	ModName  string
	BindAddr string
	Hostname string
}

type managedServer struct {
	spec    serverSpec
	cmd     *exec.Cmd
	logFile *os.File
	pty     *os.File
	done    chan error
}

type ServerManager struct {
	dataDir      string
	logsDir      string
	serverBinary string

	servers []*managedServer
}

func NewServerManager(dataDir, logsDir, serverBinary string) *ServerManager {
	return &ServerManager{
		dataDir:      dataDir,
		logsDir:      logsDir,
		serverBinary: serverBinary,
	}
}

func (m *ServerManager) ServerCount() int { return len(m.servers) }

func (m *ServerManager) StartAll() error {
	if m.serverBinary == "" {
		return fmt.Errorf("SERVER_BINARY is empty")
	}
	if _, err := os.Stat(m.serverBinary); err != nil {
		return fmt.Errorf("SERVER_BINARY not found: %s", m.serverBinary)
	}
	if m.dataDir == "" {
		return fmt.Errorf("QUAKE_DATA_DIR is empty")
	}
	if st, err := os.Stat(m.dataDir); err != nil || !st.IsDir() {
		return fmt.Errorf("QUAKE_DATA_DIR is not a directory: %s", m.dataDir)
	}
	if m.logsDir == "" {
		return fmt.Errorf("LOGS_DIR is empty")
	}
	if err := os.MkdirAll(m.logsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create LOGS_DIR: %w", err)
	}

	mods, err := listMods(m.dataDir)
	if err != nil {
		return err
	}
	if len(mods) == 0 {
		return fmt.Errorf("no valid mods found in %s", m.dataDir)
	}

	// Deterministic ordering makes debugging and server-id assignment stable.
	sort.Strings(mods)

	var servers []*managedServer
	serverID := 1
	for _, mod := range mods {
		if serverID > 254 {
			return fmt.Errorf("too many mods (%d): max is 254", len(mods))
		}

		spec := serverSpec{
			ID:       serverID,
			ModName:  mod,
			BindAddr: fmt.Sprintf("%d.%d.%d.%d", subnetServersA, subnetServersB, subnetServersC, serverID),
			Hostname: makeServerHostname(mod, serverID),
		}

		srv, err := m.startOne(spec)
		if err != nil {
			// Best-effort cleanup for already-started servers.
			m.servers = servers
			_ = m.StopAll(context.Background(), 2*time.Second)
			return err
		}

		servers = append(servers, srv)
		serverID++
	}

	m.servers = servers
	return nil
}

func (m *ServerManager) startOne(spec serverSpec) (*managedServer, error) {
	logDir := filepath.Join(m.logsDir, spec.ModName)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", logDir, err)
	}

	logPath := filepath.Join(logDir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}

	args := []string{
		"-dedicated",
		"-game", spec.ModName,
		"-ip", spec.BindAddr,
		"+hostname", spec.Hostname,
	}

	cmd := exec.Command(m.serverBinary, args...)
	cmd.Dir = m.dataDir

	// Without a tty, nqserver's stdio is fully-buffered and may never flush (especially if terminated by SIGTERM),
	// resulting in empty logs. A pty makes it line-buffered so logs are written as they happen.
	ptyMaster, ptySlave, err := pty.Open()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("open pty for server %q: %w", spec.ModName, err)
	}
	cmd.Stdout = ptySlave
	cmd.Stderr = ptySlave

	infof("Starting server id=%d mod=%q addr=%s:26000 hostname=%q", spec.ID, spec.ModName, spec.BindAddr, spec.Hostname)

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
			_, _ = io.Copy(logFile, ptyMaster)
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

	// Ask all servers to stop.
	for _, s := range m.servers {
		if s == nil || s.cmd == nil || s.cmd.Process == nil {
			continue
		}
		if !isProcessAlive(s.cmd.Process) {
			continue
		}
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}

	for _, s := range m.servers {
		if s == nil {
			continue
		}

		select {
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
			return errors.Join(errs...)
		case err := <-s.done:
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
				if err != nil {
					errs = append(errs, err)
				}
			case <-time.After(1 * time.Second):
				errs = append(errs, fmt.Errorf("server id=%d mod=%q did not exit after kill", s.spec.ID, s.spec.ModName))
			case <-ctx.Done():
				errs = append(errs, ctx.Err())
				return errors.Join(errs...)
			}
		}
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

func listMods(dataDir string) ([]string, error) {
	ents, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("read QUAKE_DATA_DIR: %w", err)
	}

	var mods []string
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		dir := filepath.Join(dataDir, name)

		// Minimal heuristic: a "mod" is any directory containing pak0.pak or progs.dat.
		if exists(filepath.Join(dir, "pak0.pak")) || exists(filepath.Join(dir, "progs.dat")) {
			mods = append(mods, name)
		}
	}
	return mods, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func makeServerHostname(modName string, serverID int) string {
	idStr := strconv.Itoa(serverID)
	maxPrefix := 15 - 1 - len(idStr)
	if maxPrefix < 1 {
		maxPrefix = 1
	}
	prefix := modName
	if len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	return prefix + "-" + idStr
}
