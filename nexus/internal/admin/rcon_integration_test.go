package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/nqrelay"
)

func integrationEnv() *Env {
	return &Env{
		ServerSnapshots:   func() []orch.ServerSnapshot { return nil },
		StartServer:       func(int) error { return nil },
		StartServersAll:   func() error { return nil },
		StopServer:        func(context.Context, int, time.Duration) error { return nil },
		StopServersAll:    func(context.Context, time.Duration) error { return nil },
		RestartServer:     func(context.Context, int, time.Duration) error { return nil },
		RestartServersAll: func(context.Context, time.Duration) error { return nil },
		RemoveServer:      func(int) error { return nil },
		LaunchServer:      func(string, []string) error { return nil },
		ExecServerCmd:     func(int, string, string) (string, error) { return "", nil },
		TailNexusLog:      func(int) []string { return nil },
		Auditf:            func(string, ...any) {},
		SessionSnapshots:  func() []nqrelay.SessionSnapshot { return nil },
		SnapshotByVIP:     func(string) ([]Session, []nqrelay.BanTarget) { return nil, nil },
		ReserveAndBlock:   func([4]byte, string) {},
	}
}

func execNexusCommandThroughFrame(t *testing.T, env *Env, cmd string) string {
	t.Helper()

	r := newMockSession(true)
	HandleAdminFrame(r, []byte("\x000\x00"+cmd), nil, env)
	return r.reply()
}

func TestAdminIntegration_HandleAdminFrame_ServerCommandReturnsOutput(t *testing.T) {
	env := integrationEnv()
	env.ExecServerCmd = func(port int, cmd, actorID string) (string, error) {
		if port != 26000 || cmd != "hostname" {
			return "", nil
		}
		return "hostname is \"fragfest\"\n", nil
	}

	r := newMockSession(true)
	HandleAdminFrame(r, []byte("\x0026000\x00hostname"), &Auth{}, env)
	reply := r.reply()
	if !strings.Contains(reply, "hostname is \"fragfest\"") {
		t.Fatalf("expected server console output reply, got %q", reply)
	}
	if strings.TrimSpace(reply) == "ok" {
		t.Fatalf("expected output passthrough, got legacy ok reply")
	}
}

func TestAdminIntegration_HandleAdminFrame_TargetPortZeroRunsNexusCommand(t *testing.T) {
	env := integrationEnv()

	r := newMockSession(true)
	HandleAdminFrame(r, []byte("\x000\x00slist"), &Auth{}, env)
	reply := r.reply()
	if !strings.HasPrefix(reply, "\n") || !strings.Contains(reply, "No Quake servers found.") {
		t.Fatalf("expected nexus slist reply, got %q", reply)
	}
}

func TestAdminIntegration_HandleAdminFrame_SessionListClientSessions(t *testing.T) {
	env := integrationEnv()
	env.ServerSnapshots = func() []orch.ServerSnapshot {
		return []orch.ServerSnapshot{{ListenPort: 26000, Hostname: "fragfest"}}
	}
	env.SessionSnapshots = func() []nqrelay.SessionSnapshot {
		return []nqrelay.SessionSnapshot{
			{VirtualIP: "127.100.10.1", SourceIP: "198.51.100.10", IsAdmin: false, ActiveServerPort: 26000},
			{VirtualIP: "127.100.10.2", SourceIP: "198.51.100.11", IsAdmin: true},
		}
	}

	reply := execNexusCommandThroughFrame(t, env, "session list")
	if !strings.Contains(reply, "#   Role") || !strings.Contains(reply, "User") || !strings.Contains(reply, "Server") || !strings.Contains(reply, "Port") {
		t.Fatalf("expected sessions header, got %q", reply)
	}
	if !strings.Contains(reply, "admin") || !strings.Contains(reply, "client") {
		t.Fatalf("expected sessions output to include role markers, got %q", reply)
	}
	if !strings.Contains(reply, "198.51.100.10") || !strings.Contains(reply, "198.51.100.11") {
		t.Fatalf("expected sessions output to include source IPs, got %q", reply)
	}
}

func TestAdminIntegration_HandleAdminFrame_RemoveDispatchesForStoppedServer(t *testing.T) {
	servers := []orch.ServerSnapshot{{Line: 0, ListenPort: 26000, GameDir: "id1", State: "stopped"}}
	env := integrationEnv()
	env.ServerSnapshots = func() []orch.ServerSnapshot {
		return append([]orch.ServerSnapshot(nil), servers...)
	}
	env.RemoveServer = func(target int) error {
		if target == 1 || target == 26000 {
			servers = nil
		}
		return nil
	}

	reply := execNexusCommandThroughFrame(t, env, "remove 1")
	if reply != "server removed\n" {
		t.Fatalf("expected server removed reply, got %q", reply)
	}
	if snaps := env.ServerSnapshots(); len(snaps) != 0 {
		t.Fatalf("expected removed server to be gone from registry, still have %d snapshots", len(snaps))
	}
}

func TestAdminIntegration_HandleAdminFrame_SessionBanDisconnectsAndBlocksIdentity(t *testing.T) {
	clientMock := newMockSession(false)
	vip := clientMock.vip

	env := integrationEnv()
	env.SessionSnapshots = func() []nqrelay.SessionSnapshot {
		return []nqrelay.SessionSnapshot{
			{VirtualIP: vip, SourceIP: clientMock.sourceIP, IsAdmin: false},
		}
	}
	env.SnapshotByVIP = func(lookupVIP string) ([]Session, []nqrelay.BanTarget) {
		if lookupVIP == vip {
			return []Session{clientMock}, nil
		}
		return nil, nil
	}

	reply := execNexusCommandThroughFrame(t, env, "session ban 1")
	if !strings.Contains(reply, "banned "+vip) {
		t.Fatalf("expected ban confirmation, got %q", reply)
	}
	if !strings.Contains(reply, "source ip(s): "+clientMock.sourceIP) {
		t.Fatalf("expected ban output to include source ip, got %q", reply)
	}
	if !clientMock.closed {
		t.Fatalf("expected session to be closed after ban")
	}
}

func TestAdminIntegration_HandleAdminFrame_SessionBanAdminRejected(t *testing.T) {
	adminMock := newMockSession(true)
	vip := adminMock.vip

	env := integrationEnv()
	env.SessionSnapshots = func() []nqrelay.SessionSnapshot {
		return []nqrelay.SessionSnapshot{
			{VirtualIP: vip, SourceIP: adminMock.sourceIP, IsAdmin: true},
		}
	}
	env.SnapshotByVIP = func(lookupVIP string) ([]Session, []nqrelay.BanTarget) {
		if lookupVIP == vip {
			return []Session{adminMock}, nil
		}
		return nil, nil
	}

	reply := execNexusCommandThroughFrame(t, env, "session ban 1")
	if !strings.Contains(reply, "cannot ban admin sessions") {
		t.Fatalf("expected admin-ban rejection detail, got %q", reply)
	}
	if !strings.Contains(reply, "\nusage: rcon session ban <idx>") {
		t.Fatalf("expected ban usage helper text, got %q", reply)
	}
	if adminMock.closed {
		t.Fatalf("expected admin session to remain connected after rejected ban")
	}
}
