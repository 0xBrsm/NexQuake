package orch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	errAlreadyRunning = errors.New("already running")
	errAlreadyStopped = errors.New("already stopped")
)

func (m *ServerManager) findPoolByPortOrIndexLocked(target int) (*serverPool, error) {
	if pool := m.poolByListenPort[target]; pool != nil {
		return pool, nil
	}

	pools := make([]*serverPool, 0, len(m.poolsByID))
	for _, pool := range m.poolsByID {
		if pool == nil {
			continue
		}
		pools = append(pools, pool)
	}
	slices.SortFunc(pools, func(a, b *serverPool) int {
		return cmp.Compare(a.Line, b.Line)
	})
	index := target - 1
	if index >= 0 && index < len(pools) {
		return pools[index], nil
	}

	return nil, fmt.Errorf("unknown target %d", target)
}

func (m *ServerManager) nextPoolLineLocked() int {
	maxLine := -1
	for _, pool := range m.poolsByID {
		if pool == nil {
			continue
		}
		if pool.Line > maxLine {
			maxLine = pool.Line
		}
	}
	return maxLine + 1
}

func (m *ServerManager) poolBackendsLocked(pool *serverPool) []*serverRecord {
	if pool == nil {
		return nil
	}
	out := make([]*serverRecord, 0, len(pool.BackendServerIDs))
	for _, serverID := range pool.BackendServerIDs {
		rec := m.serversByID[serverID]
		if rec == nil {
			continue
		}
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b *serverRecord) int {
		aPort, bPort := recordListenPort(a), recordListenPort(b)
		switch {
		case aPort > 0 && bPort > 0:
			if aPort != bPort {
				return cmp.Compare(aPort, bPort)
			}
		case aPort > 0:
			return -1
		case bPort > 0:
			return 1
		}
		return cmp.Compare(a.id, b.id)
	})
	return out
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
	line := m.nextPoolLineLocked()
	launch := serverLaunch{
		Line:   line,
		LogDir: fmt.Sprintf("%d-%s-%s", line, filepath.Base(binary), startTag),
		Binary: binary,
		Args:   append([]string(nil), args...),
	}
	m.mu.Unlock()

	rec, err := m.registerPoolLaunch(launch)
	if err != nil {
		return err
	}
	return m.startRecord(rec)
}

func (m *ServerManager) StartServer(target int) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.RLock()
	pool, err := m.findPoolByPortOrIndexLocked(target)
	if err != nil {
		m.mu.RUnlock()
		return err
	}
	poolID := pool.PoolID
	records := m.poolBackendsLocked(pool)
	m.mu.RUnlock()

	if len(records) == 0 {
		m.mu.Lock()
		pool = m.poolsByID[poolID]
		if pool == nil {
			m.mu.Unlock()
			return fmt.Errorf("unknown target %d", target)
		}
		rec := m.appendPoolBackendRecordLocked(pool, pool.TemplateLaunch, poolBackendLifecycleWarming)
		m.mu.Unlock()
		records = []*serverRecord{rec}
	}

	started := false
	for _, rec := range records {
		err := m.startRecord(rec)
		if err == nil {
			started = true
			continue
		}
		if errors.Is(err, errAlreadyRunning) {
			continue
		}
		return err
	}
	if started {
		return nil
	}
	return errAlreadyRunning
}

func (m *ServerManager) runServersAll(runOne func(target int) error, ignoreErr func(error) bool) error {
	servers := m.Snapshots()
	var errs []error
	for i := range servers {
		target := i + 1
		err := runOne(target)
		if err == nil {
			continue
		}
		if ignoreErr != nil && ignoreErr(err) {
			continue
		}
		errs = append(errs, fmt.Errorf("target %d: %w", target, err))
	}
	return errors.Join(errs...)
}

func (m *ServerManager) StartServersAll() error {
	return m.runServersAll(
		m.StartServer,
		func(err error) bool { return errors.Is(err, errAlreadyRunning) },
	)
}

func (m *ServerManager) StopServer(ctx context.Context, target int, killAfter time.Duration) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.RLock()
	pool, err := m.findPoolByPortOrIndexLocked(target)
	if err != nil {
		m.mu.RUnlock()
		return err
	}
	records := m.poolBackendsLocked(pool)
	m.mu.RUnlock()

	stopped := false
	for _, rec := range records {
		s := rec.Running
		if s == nil || s.Cmd == nil || s.Cmd.Process == nil || !isProcessAlive(s.Cmd.Process) {
			m.mu.Lock()
			m.removeServerRecordLocked(rec.id)
			m.mu.Unlock()
			continue
		}
		if err := m.stopServer(ctx, rec, s, killAfter, true); err != nil {
			return err
		}
		m.mu.Lock()
		m.removeServerRecordLocked(rec.id)
		m.mu.Unlock()
		stopped = true
	}

	if !stopped {
		return errAlreadyStopped
	}

	return nil
}

func (m *ServerManager) StopServersAll(ctx context.Context, killAfter time.Duration) error {
	return m.runServersAll(
		func(target int) error { return m.StopServer(ctx, target, killAfter) },
		func(err error) bool { return errors.Is(err, errAlreadyStopped) },
	)
}

func (m *ServerManager) RestartServer(ctx context.Context, target int, killAfter time.Duration) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}
	if err := m.StopServer(ctx, target, killAfter); err != nil && !errors.Is(err, errAlreadyStopped) {
		return err
	}
	return m.StartServer(target)
}

func (m *ServerManager) RestartServersAll(ctx context.Context, killAfter time.Duration) error {
	return m.runServersAll(
		func(target int) error { return m.RestartServer(ctx, target, killAfter) },
		nil,
	)
}

func (m *ServerManager) RemoveServer(target int) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	pool, err := m.findPoolByPortOrIndexLocked(target)
	if err != nil {
		return err
	}

	backendIDs := append([]int(nil), pool.BackendServerIDs...)
	for _, serverID := range backendIDs {
		rec := m.serversByID[serverID]
		if rec == nil {
			continue
		}
		if rec.Running != nil && rec.Running.Cmd != nil && rec.Running.Cmd.Process != nil && isProcessAlive(rec.Running.Cmd.Process) {
			return fmt.Errorf("server is running; stop server first")
		}
		if rec.Running != nil {
			rec.Running = nil
			resetRecordStartupState(rec)
		}
		m.removeServerRecordLocked(rec.id)
	}

	if pool.ListenPort > 0 {
		delete(m.poolByListenPort, pool.ListenPort)
	}
	delete(m.poolsByID, pool.PoolID)
	return nil
}
