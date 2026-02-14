package orch

import (
	"os"
	"os/exec"
)

// NewTestServerLaunch returns a minimal launch spec for tests.
func NewTestServerLaunch(slot int) serverLaunch {
	return serverLaunch{Slot: slot}
}

// NewTestServer creates a managed server stub backed by the current process.
func NewTestServer(port int) *managedServer {
	process, _ := os.FindProcess(os.Getpid())
	srv := &managedServer{
		Cmd:     &exec.Cmd{Process: process},
		Console: newServerConsole(nil),
	}
	if port >= 0 && port <= 65535 {
		srv.spec.ListenPort = port
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
