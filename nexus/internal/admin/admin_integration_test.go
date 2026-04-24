package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/session"
)

type mockChannel struct {
	sourceIP  string
	sourceKey string
	nqip       string
	clientIP  [4]byte
	closed    bool
	replies   strings.Builder
}

func newMockChannel(isAdmin bool) *mockChannel {
	m := &mockChannel{
		sourceIP:  "198.51.100.10",
		sourceKey: "ip:198.51.100.10",
		nqip:       "127.100.10.1",
		clientIP:  [4]byte{127, 100, 10, 1},
	}
	if isAdmin {
		m.sourceIP = "198.51.100.11"
		m.sourceKey = "ip:198.51.100.11"
		m.nqip = "127.100.10.2"
		m.clientIP = [4]byte{127, 100, 10, 2}
	}
	return m
}

func (m *mockChannel) SendAdminReply(msg string) { m.replies.WriteString(msg) }
func (m *mockChannel) ClientNQIP() string   { return m.nqip }
func (m *mockChannel) ClientIP() [4]byte         { return m.clientIP }
func (m *mockChannel) SourceKey() string         { return m.sourceKey }
func (m *mockChannel) ActiveServerPort() int     { return 0 }
func (m *mockChannel) Close()                    { m.closed = true }

func newMockSession(isAdmin bool) (*session.Session, *mockChannel) {
	reg := session.NewRegistry()
	ch := newMockChannel(isAdmin)
	s := reg.Create(ch.sourceIP, "", isAdmin)
	reg.AttachChannel(s, ch)
	return s, ch
}

func integrationEnv() *Env {
	return &Env{
		ServerSnapshots:   func() []ServerInfo { return nil },
		InstanceSnapshots:  func(int) ([]ServerInfo, error) { return nil, nil },
		StartServer:       func(int) error { return nil },
		StartServersAll:   func() error { return nil },
		StopServer:        func(context.Context, int, time.Duration) error { return nil },
		StopServersAll:    func(context.Context, time.Duration) error { return nil },
		RestartServer:     func(context.Context, int, time.Duration) error { return nil },
		RestartServersAll: func(context.Context, time.Duration) error { return nil },
		RemoveServer:      func(int) error { return nil },
		LaunchServer:      func(string, []string) error { return nil },
		DispatchInstanceCmd: func(int, string, string) (string, error) { return "", nil },
		TailNexusLog:      func(int) []string { return nil },
		Auditf:            func(string, ...any) {},
		SessionSnapshots:  func() []session.Snapshot { return nil },
		SnapshotByNQIP:     func(string) ([]*session.Session, []session.BanTarget) { return nil, nil },
		ReserveAndBlock:   func([4]byte, string) {},
	}
}

func dispatchMethod(t *testing.T, env *Env, method string, params any) *Response {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := &Request{Jsonrpc: "2.0", Method: method, Params: raw, ID: json.RawMessage(`1`)}
	return Dispatch(req, env, adminActor())
}

func TestIntegration_ServerList(t *testing.T) {
	env := integrationEnv()
	env.ServerSnapshots = func() []ServerInfo {
		return []ServerInfo{{Hostname: "fragfest", State: "running"}}
	}
	resp := dispatchMethod(t, env, "server.list", struct{}{})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	got := resp.Result.(ServerListResult)
	if len(got.Servers) != 1 || got.Servers[0].Hostname != "fragfest" {
		t.Fatalf("got %+v", got.Servers)
	}
}

func TestIntegration_ServerStartByIndex(t *testing.T) {
	env := integrationEnv()
	var started int
	env.StartServer = func(idx int) error { started = idx; return nil }

	resp := dispatchMethod(t, env, "server.start", map[string]any{"target": "1"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !resp.Result.(ServerLifecycleResult).OK {
		t.Fatal("expected OK=true")
	}
	if started != 1 {
		t.Fatalf("expected StartServer(1), got %d", started)
	}
}

func TestIntegration_ServerStartAll(t *testing.T) {
	env := integrationEnv()
	called := false
	env.StartServersAll = func() error { called = true; return nil }
	resp := dispatchMethod(t, env, "server.start", map[string]any{"target": "all"})
	if resp.Error != nil || !called {
		t.Fatalf("expected success, got error=%+v called=%v", resp.Error, called)
	}
}

func TestIntegration_ServerStartRejectsBadTarget(t *testing.T) {
	env := integrationEnv()
	resp := dispatchMethod(t, env, "server.start", map[string]any{"target": "fragfest"})
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams for hostname target, got %+v", resp.Error)
	}
}

func TestIntegration_ServerRemoveRequiresIndex(t *testing.T) {
	env := integrationEnv()
	resp := dispatchMethod(t, env, "server.remove", map[string]any{})
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams, got %+v", resp.Error)
	}
}

func TestIntegration_ServerRemoveByIndex(t *testing.T) {
	env := integrationEnv()
	var removed int
	env.RemoveServer = func(idx int) error { removed = idx; return nil }
	resp := dispatchMethod(t, env, "server.remove", map[string]any{"index": 2})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !resp.Result.(ServerRemoveResult).Removed || removed != 2 {
		t.Fatalf("got removed=%d result=%+v", removed, resp.Result)
	}
}

func TestIntegration_ServerCommand(t *testing.T) {
	env := integrationEnv()
	env.DispatchInstanceCmd = func(port int, cmd, actorID string) (string, error) {
		if port != 26000 || cmd != "status" {
			t.Fatalf("unexpected dispatch: port=%d cmd=%q", port, cmd)
		}
		return "host: fragfest\n", nil
	}
	resp := dispatchMethod(t, env, "server.instance.command", map[string]any{"port": 26000, "cmd": "status"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if reply := resp.Result.(InstanceCommandResult).Reply; !strings.Contains(reply, "fragfest") {
		t.Fatalf("got reply %q", reply)
	}
}

func TestIntegration_SessionList(t *testing.T) {
	env := integrationEnv()
	env.SessionSnapshots = func() []session.Snapshot {
		return []session.Snapshot{
			{NQIP: "127.100.10.1", SourceIP: "198.51.100.10", IsAdmin: false, ActiveServerPort: 26000},
			{NQIP: "127.100.10.2", SourceIP: "198.51.100.11", IsAdmin: true},
		}
	}
	env.ServerSnapshots = func() []ServerInfo {
		return []ServerInfo{{ListenPort: 26000, Hostname: "fragfest"}}
	}
	env.InstanceSnapshots = func(int) ([]ServerInfo, error) { return nil, nil }
	resp := dispatchMethod(t, env, "session.list", struct{}{})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	got := resp.Result.(SessionListResult).Sessions
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got))
	}
	// First (by NQIP sort) should be 127.100.10.1 attached to fragfest
	if got[0].NQIP != "127.100.10.1" || got[0].ActiveServerHost != "fragfest" {
		t.Fatalf("got first entry %+v", got[0])
	}
}

func TestIntegration_SessionBanClosesAndReserves(t *testing.T) {
	clientSession, clientMock := newMockSession(false)
	nqip := clientMock.nqip

	env := integrationEnv()
	env.SnapshotByNQIP = func(lookup string) ([]*session.Session, []session.BanTarget) {
		if lookup == nqip {
			return []*session.Session{clientSession}, nil
		}
		return nil, nil
	}
	reserved := false
	env.ReserveAndBlock = func([4]byte, string) { reserved = true }

	resp := dispatchMethod(t, env, "session.ban", map[string]any{"nqip": nqip})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result := resp.Result.(SessionBanResult)
	if result.NQIP != nqip || result.Disconnected != 1 {
		t.Fatalf("got %+v", result)
	}
	if !clientMock.closed {
		t.Fatal("expected channel closed")
	}
	if !reserved {
		t.Fatal("expected reservation")
	}
}

func TestIntegration_SessionBanAdminRejected(t *testing.T) {
	adminSession, adminMock := newMockSession(true)
	nqip := adminMock.nqip

	env := integrationEnv()
	env.SnapshotByNQIP = func(lookup string) ([]*session.Session, []session.BanTarget) {
		if lookup == nqip {
			return []*session.Session{adminSession}, nil
		}
		return nil, nil
	}

	resp := dispatchMethod(t, env, "session.ban", map[string]any{"nqip": nqip})
	if resp.Error == nil || resp.Error.Code != ErrCodeConflict {
		t.Fatalf("expected Conflict, got %+v", resp.Error)
	}
	if adminMock.closed {
		t.Fatal("admin session should not have been closed")
	}
}

func TestIntegration_SessionBanUnknownVIP(t *testing.T) {
	env := integrationEnv()
	resp := dispatchMethod(t, env, "session.ban", map[string]any{"nqip": "127.0.0.99"})
	if resp.Error == nil || resp.Error.Code != ErrCodeNotFound {
		t.Fatalf("expected NotFound, got %+v", resp.Error)
	}
}
