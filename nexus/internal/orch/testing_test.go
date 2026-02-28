package orch

import (
	"os"
	"os/exec"
)

// NewTestServerLaunch returns a minimal launch spec for tests.
func NewTestServerLaunch(line int) serverLaunch {
	return serverLaunch{Line: line}
}

// NewTestServer creates a managed server stub backed by the current process.
func NewTestServer(port int) *managedServer {
	process, _ := os.FindProcess(os.Getpid())
	srv := &managedServer{
		Cmd:     &exec.Cmd{Process: process},
		Console: newServerConsole(nil),
	}
	return srv
}

// NewTestServerWithPTY creates a managed server stub with a writable console.
func NewTestServerWithPTY(port int, pty *os.File) *managedServer {
	srv := NewTestServer(port)
	srv.Console = newServerConsole(pty)
	return srv
}

// PublishConsoleLineForTest injects a console line into the server buffer.
func (s *managedServer) PublishConsoleLineForTest(line string) {
	if s == nil || s.Console == nil {
		return
	}
	s.Console.publishLine(line)
}

// SetServerRunningForTest assigns the running process for a test record.
func (m *ServerManager) SetServerRunningForTest(rec *serverRecord, srv *managedServer) {
	if m == nil || rec == nil {
		return
	}
	m.mu.Lock()
	rec.Running = srv
	m.mu.Unlock()
}

// SetServerInfoForTest sets cached server-info fields on a test record.
func (m *ServerManager) SetServerInfoForTest(rec *serverRecord, hostname, mapName string, players, maxPlayers byte) {
	if m == nil || rec == nil {
		return
	}
	m.mu.Lock()
	rec.Hostname = hostname
	rec.MapName = mapName
	rec.Players = players
	rec.MaxPlayers = maxPlayers
	m.mu.Unlock()
}

func (m *ServerManager) registerServerLaunch(launch serverLaunch) *serverRecord {
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
		Line:       rec.Launch.Line,
	}
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

func (m *ServerManager) registerPoolSeed(rec *serverRecord) error {
	if rec == nil {
		return nil
	}
	port, ok := launchConfiguredPort(rec.Launch)
	if !ok || port != 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.poolByServerID[rec.id]; exists {
		return nil
	}

	pool := &serverPool{
		PoolID:          m.nextPoolID,
		Line:            rec.Launch.Line,
		TemplateLaunch:  cloneServerLaunch(rec.Launch),
		Autoscales: true,
		BackendServerIDs: []int{rec.id},
		backendState: map[int]*poolBackendState{
			rec.id: newPoolBackendState(poolBackendLifecycleWarming),
		},
	}
	if !rec.LastSeen.IsZero() {
		pool.backendState[rec.id].Lifecycle = poolBackendLifecycleActive
	}
	m.nextPoolID++
	m.poolsByID[pool.PoolID] = pool
	m.poolByServerID[rec.id] = pool
	m.refreshPoolSnapshotLocked(pool)

	return nil
}
