package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

var (
	ErrAlreadyRunning = errors.New("already running")
	ErrAlreadyStopped = errors.New("already stopped")
)

const serverStartupCCREPTimeout = 10 * time.Second

func resetRecordStartupState(rec *serverRecord) {
	if rec == nil {
		return
	}
	rec.relayConsoleReady = false
	rec.awaitingServerInfo = false
	rec.startupTimedOutOnce = false
}

func (m *ServerManager) findRecordByPortOrIndexLocked(target int) (*serverRecord, error) {
	var byPort *serverRecord
	for _, rec := range m.serversByID {
		if rec == nil {
			continue
		}
		if rec.resolvedPort == target {
			if byPort != nil && byPort != rec {
				return nil, fmt.Errorf("ambiguous port %d", target)
			}
			byPort = rec
			continue
		}
		if rec.spec != nil && rec.spec.ListenPort == target {
			if byPort != nil && byPort != rec {
				return nil, fmt.Errorf("ambiguous port %d", target)
			}
			byPort = rec
		}
	}
	if byPort != nil {
		return byPort, nil
	}

	records := make([]*serverRecord, 0, len(m.serversByID))
	for _, rec := range m.serversByID {
		if rec == nil {
			continue
		}
		records = append(records, rec)
	}
	slices.SortFunc(records, func(a, b *serverRecord) int {
		return cmp.Compare(a.launch.slot, b.launch.slot)
	})
	index := target - 1
	if index >= 0 && index < len(records) {
		return records[index], nil
	}

	return nil, fmt.Errorf("unknown target %d", target)
}

func (m *ServerManager) nextLaunchSlotLocked() int {
	maxSlot := -1
	for _, rec := range m.serversByID {
		if rec == nil {
			continue
		}
		if rec.launch.slot > maxSlot {
			maxSlot = rec.launch.slot
		}
	}
	return maxSlot + 1
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

func (m *ServerManager) startRecord(rec *serverRecord) error {
	if rec == nil {
		return fmt.Errorf("server record not found")
	}

	m.mu.Lock()
	if m.runtimeBasedir == "" {
		m.mu.Unlock()
		return fmt.Errorf("runtime not initialized")
	}
	if rec.running != nil && rec.running.cmd != nil && rec.running.cmd.Process != nil && isProcessAlive(rec.running.cmd.Process) {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	runtimeBasedir := m.runtimeBasedir
	launch := cloneServerLaunch(rec.launch)

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
			debugf("Resolved server listen port (slot=%d port=%d)", rec.launch.slot, port)
		},
		func(searchPath []string) {
			searchPath = normalizeSearchPath(searchPath)
			m.updateSearchPath(rec, searchPath)
			debugf("Resolved server search path (slot=%d active=%q paths=%q)",
				rec.launch.slot, activeGameDir(searchPath), searchPath)
		},
	)
	if err != nil {
		m.mu.Lock()
		rec.running = nil
		resetRecordStartupState(rec)
		rec.lastError = err.Error()
		m.mu.Unlock()
		return err
	}

	m.mu.Lock()
	rec.running = srv
	if rec.spec != nil {
		rec.running.spec = *rec.spec
	}
	rec.relayConsoleReady = false
	rec.awaitingServerInfo = true
	rec.startupTimedOutOnce = false
	rec.lastError = ""
	rec.hostname = ""
	rec.mapName = ""
	rec.players = 0
	rec.maxPlayers = 0
	rec.lastSeen = time.Time{}
	m.mu.Unlock()

	go m.relayServerConsoleToNexus(rec, srv.console)
	go m.monitorServerStartupTimeout(rec, srv, serverStartupCCREPTimeout)

	return nil
}

func (m *ServerManager) monitorServerStartupTimeout(rec *serverRecord, srv *managedServer, timeout time.Duration) {
	if m == nil || rec == nil || srv == nil || timeout <= 0 {
		return
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	<-timer.C

	m.mu.Lock()
	if rec.running != srv || !rec.awaitingServerInfo || rec.startupTimedOutOnce {
		m.mu.Unlock()
		return
	}
	rec.startupTimedOutOnce = true
	slot := rec.launch.slot
	m.mu.Unlock()

	warnf("server %d failed to start in %d seconds", slot, int(timeout/time.Second))
}

func (m *ServerManager) LaunchServer(binary string, args []string) error {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return fmt.Errorf("missing binary")
	}
	if unsupportedArg, ok := findUnsupportedLaunchArg(args); ok {
		return fmt.Errorf("%s is not currently supported", unsupportedArg)
	}

	m.mu.RLock()
	if m.runtimeBasedir == "" {
		m.mu.RUnlock()
		return fmt.Errorf("runtime not initialized")
	}
	m.mu.RUnlock()

	startTag := time.Now().UTC().Format("20060102T150405Z")

	m.mu.Lock()
	slot := m.nextLaunchSlotLocked()
	rec := &serverRecord{
		id: m.nextServerID,
		launch: serverLaunch{
			slot:   slot,
			logDir: fmt.Sprintf("%d-%s-%s", slot, filepath.Base(binary), startTag),
			binary: binary,
			args:   append([]string(nil), args...),
		},
	}
	m.nextServerID++
	m.serversByID[rec.id] = rec
	m.mu.Unlock()

	return m.startRecord(rec)
}

func (m *ServerManager) StartServer(target int) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.RLock()
	rec, err := m.findRecordByPortOrIndexLocked(target)
	if err != nil {
		m.mu.RUnlock()
		return err
	}
	m.mu.RUnlock()
	return m.startRecord(rec)
}

func (m *ServerManager) StopServer(ctx context.Context, target int, killAfter time.Duration) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.RLock()
	rec, err := m.findRecordByPortOrIndexLocked(target)
	if err != nil {
		m.mu.RUnlock()
		return err
	}
	s := rec.running
	m.mu.RUnlock()

	if s == nil || s.cmd == nil || s.cmd.Process == nil || !isProcessAlive(s.cmd.Process) {
		m.mu.Lock()
		if rec.running == s {
			rec.running = nil
			resetRecordStartupState(rec)
		}
		m.mu.Unlock()
		return ErrAlreadyStopped
	}

	return m.stopServer(ctx, rec, s, killAfter, true)
}

func (m *ServerManager) RestartServer(ctx context.Context, target int, killAfter time.Duration) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.RLock()
	rec, err := m.findRecordByPortOrIndexLocked(target)
	if err != nil {
		m.mu.RUnlock()
		return err
	}
	s := rec.running
	m.mu.RUnlock()

	if s != nil && s.cmd != nil && s.cmd.Process != nil && isProcessAlive(s.cmd.Process) {
		if err := m.stopServer(ctx, rec, s, killAfter, true); err != nil {
			return err
		}
	} else {
		m.mu.Lock()
		if rec.running == s {
			rec.running = nil
			resetRecordStartupState(rec)
		}
		m.mu.Unlock()
	}

	return m.StartServer(target)
}

func (m *ServerManager) stopServer(ctx context.Context, rec *serverRecord, s *managedServer, killAfter time.Duration, sendSignal bool) error {
	if s == nil {
		return nil
	}
	waitAfterSignal := killAfter

	if sendSignal && s.cmd != nil && s.cmd.Process != nil && isProcessAlive(s.cmd.Process) {
		// Graceful path first: ask the server to quit via console.
		_ = s.WriteConsole("quit")

		quitGrace := 750 * time.Millisecond
		if killAfter > 0 && killAfter < quitGrace {
			quitGrace = killAfter
		}
		if waitAfterSignal > quitGrace {
			waitAfterSignal -= quitGrace
		} else {
			waitAfterSignal = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-s.done:
			m.mu.Lock()
			if rec != nil && rec.running == s {
				rec.running = nil
				resetRecordStartupState(rec)
				rec.lastError = ""
			}
			m.mu.Unlock()
			return err
		case <-time.After(quitGrace):
		}

		if isProcessAlive(s.cmd.Process) {
			_ = s.cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-s.done:
		m.mu.Lock()
		if rec != nil && rec.running == s {
			rec.running = nil
			resetRecordStartupState(rec)
			rec.lastError = ""
		}
		m.mu.Unlock()
		return err
	case <-time.After(waitAfterSignal):
		if s.cmd != nil && s.cmd.Process != nil && isProcessAlive(s.cmd.Process) {
			_ = s.cmd.Process.Kill()
		}
		select {
		case err := <-s.done:
			m.mu.Lock()
			if rec != nil && rec.running == s {
				rec.running = nil
				resetRecordStartupState(rec)
				rec.lastError = ""
			}
			m.mu.Unlock()
			return err
		case <-time.After(1 * time.Second):
			slot := -1
			gameDir := ""
			m.mu.Lock()
			if rec != nil {
				if rec.running == s {
					rec.lastError = "did not exit after kill"
				}
				slot = rec.launch.slot
				if rec.spec != nil {
					gameDir = activeGameDir(rec.spec.SearchPath)
				}
			}
			m.mu.Unlock()
			return fmt.Errorf("server slot=%d mod=%q did not exit after kill", slot, gameDir)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (m *ServerManager) RemoveServer(target int) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	rec, err := m.findRecordByPortOrIndexLocked(target)
	if err != nil {
		return err
	}
	if rec.running != nil && rec.running.cmd != nil && rec.running.cmd.Process != nil && isProcessAlive(rec.running.cmd.Process) {
		return fmt.Errorf("server is running; stop server first")
	}
	if rec.running != nil {
		rec.running = nil
		resetRecordStartupState(rec)
	}

	m.removeServerIDFromPortLocked(rec.resolvedPort, rec.id)
	if rec.spec != nil {
		m.removeServerIDFromPortLocked(rec.spec.ListenPort, rec.id)
	}
	delete(m.serversByID, rec.id)
	return nil
}
