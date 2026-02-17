package orch

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

		isMatch := (rec.resolvedPort == target) || (rec.spec != nil && rec.spec.ListenPort == target)
		if isMatch {
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
		return cmp.Compare(a.Launch.Slot, b.Launch.Slot)
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
		if rec.Launch.Slot > maxSlot {
			maxSlot = rec.Launch.Slot
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
	if rec.Running != nil && rec.Running.Cmd != nil && rec.Running.Cmd.Process != nil && isProcessAlive(rec.Running.Cmd.Process) {
		m.mu.Unlock()
		return ErrAlreadyRunning
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
			m.UpdatePort(rec, port)
			m.debugf("Resolved server %d port: %d", rec.Launch.Slot+1, port)
		},
		func(searchPath []string) {
			searchPath = normalizeSearchPath(searchPath)
			m.UpdateSearchPath(rec, searchPath)
			m.debugf("Resolved server search path (slot=%d active=%q paths=%q)",
				rec.Launch.Slot, activeGameDir(searchPath), searchPath)
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
	if rec.spec != nil {
		rec.Running.spec = *rec.spec
	}
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
	slot := rec.Launch.Slot
	m.mu.Unlock()

	m.warnf("server %d failed to start in %d seconds", slot, int(timeout/time.Second))
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
		Launch: serverLaunch{
			Slot:   slot,
			LogDir: fmt.Sprintf("%d-%s-%s", slot, filepath.Base(binary), startTag),
			Binary: binary,
			Args:   append([]string(nil), args...),
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
	s := rec.Running
	m.mu.RUnlock()

	if s == nil || s.Cmd == nil || s.Cmd.Process == nil || !isProcessAlive(s.Cmd.Process) {
		m.mu.Lock()
		if rec.Running == s {
			rec.Running = nil
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
	s := rec.Running
	m.mu.RUnlock()

	if s != nil && s.Cmd != nil && s.Cmd.Process != nil && isProcessAlive(s.Cmd.Process) {
		if err := m.stopServer(ctx, rec, s, killAfter, true); err != nil {
			return err
		}
	} else {
		m.mu.Lock()
		if rec.Running == s {
			rec.Running = nil
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

	clearRunning := func(err error) error {
		m.mu.Lock()
		if rec != nil && rec.Running == s {
			rec.Running = nil
			resetRecordStartupState(rec)
			rec.lastError = ""
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
		if waitAfterSignal > quitGrace {
			waitAfterSignal -= quitGrace
		} else {
			waitAfterSignal = 0
		}
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
			slot := -1
			gameDir := ""
			m.mu.Lock()
			if rec != nil {
				if rec.Running == s {
					rec.lastError = "did not exit after kill"
				}
				slot = rec.Launch.Slot
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
	if rec.Running != nil && rec.Running.Cmd != nil && rec.Running.Cmd.Process != nil && isProcessAlive(rec.Running.Cmd.Process) {
		return fmt.Errorf("server is running; stop server first")
	}
	if rec.Running != nil {
		rec.Running = nil
		resetRecordStartupState(rec)
	}

	m.removeServerIDFromPortLocked(rec.resolvedPort, rec.id)
	if rec.spec != nil {
		m.removeServerIDFromPortLocked(rec.spec.ListenPort, rec.id)
	}
	delete(m.serversByID, rec.id)
	return nil
}
