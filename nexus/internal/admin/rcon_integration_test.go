package admin

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
)

func integrationEnv(mgr *orch.ServerManager, alloc *nqnet.IPAllocator, sessions *nqnet.SessionRegistry) *Env {
	env := &Env{
		ServerSnapshots:   mgr.Snapshots,
		StartServer:       mgr.StartServer,
		StartServersAll:   mgr.StartServersAll,
		StopServer:        mgr.StopServer,
		StopServersAll:    mgr.StopServersAll,
		RestartServer:     mgr.RestartServer,
		RestartServersAll: mgr.RestartServersAll,
		RemoveServer:      mgr.RemoveServer,
		LaunchServer:      mgr.LaunchServer,
		ExecServerCmd:     mgr.ExecServerCmd,
		TailNexusLog:      func(int) []string { return nil },
	}
	if sessions != nil {
		env.SessionSnapshots = sessions.SnapshotAll
		env.SnapshotByVIP = sessions.SnapshotByVirtualIP
	}
	if alloc != nil {
		env.ReserveAndBlock = alloc.ReserveAndBlock
	}
	if env.SessionSnapshots == nil {
		env.SessionSnapshots = func() []nqnet.SessionSnapshot { return nil }
	}
	if env.SnapshotByVIP == nil {
		env.SnapshotByVIP = func(string) ([]*nqnet.Router, []nqnet.BanTarget) { return nil, nil }
	}
	if env.ReserveAndBlock == nil {
		env.ReserveAndBlock = func([4]byte, string) {}
	}
	return env
}

func execNexusCommandThroughFrame(t *testing.T, env *Env, cmd string) string {
	t.Helper()

	r, ch := nqnet.NewTestRouter(true)
	HandleAdminFrame(r, []byte("\x000\x00"+cmd), nil, env)
	return readAdminReply(t, ch)
}

func TestAdminIntegration_HandleAdminFrame_ServerCommandReturnsOutput(t *testing.T) {
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	srv := orch.NewTestServerWithPTY(26000, ptyWrite)
	go func() {
		buf := make([]byte, 128)
		_, _ = ptyRead.Read(buf)
		srv.PublishConsoleLineForTest("hostname is \"fragfest\"\n")
	}()

	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(orch.NewTestServerLaunch(0))
	mgr.UpdatePort(rec, 26000)
	mgr.SetServerRunningForTest(rec, srv)
	env := integrationEnv(mgr, nil, nil)

	r, ch := nqnet.NewTestRouter(true)
	HandleAdminFrame(r, []byte("\x0026000\x00hostname"), &Auth{}, env)
	reply := readAdminReply(t, ch)
	if !strings.Contains(reply, "hostname is \"fragfest\"") {
		t.Fatalf("expected server console output reply, got %q", reply)
	}
	if strings.TrimSpace(reply) == "ok" {
		t.Fatalf("expected output passthrough, got legacy ok reply")
	}
}

func TestAdminIntegration_HandleAdminFrame_TargetPortZeroRunsNexusCommand(t *testing.T) {
	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	env := integrationEnv(mgr, nil, nil)

	r, ch := nqnet.NewTestRouter(true)
	HandleAdminFrame(r, []byte("\x000\x00slist"), &Auth{}, env)
	reply := readAdminReply(t, ch)
	if !strings.HasPrefix(reply, "\n") || !strings.Contains(reply, "No Quake servers found.") {
		t.Fatalf("expected nexus slist reply, got %q", reply)
	}
}

func TestAdminIntegration_HandleAdminFrame_SessionListClientSessions(t *testing.T) {
	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(orch.NewTestServerLaunch(0))
	mgr.UpdatePort(rec, 26000)
	mgr.UpdateSearchPath(rec, []string{"id1"})
	mgr.SetServerInfoForTest(rec, "fragfest", "", 0, 0)

	serverIP := net.ParseIP(nqnet.DefaultNQServerIP).To4()
	alloc := nqnet.NewIPAllocator(serverIP)
	sessions := nqnet.NewSessionRegistry()
	env := integrationEnv(mgr, alloc, sessions)

	clientRouter, _ := nqnet.NewTestRouterWith(false, alloc, sessions)
	clientRouter.NoteServerRoutePort(26000)
	_, _ = nqnet.NewTestRouterWith(true, alloc, sessions)

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
	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(orch.NewTestServerLaunch(0))
	mgr.UpdatePort(rec, 26000)
	mgr.UpdateSearchPath(rec, []string{"id1"})
	env := integrationEnv(mgr, nil, nil)

	reply := execNexusCommandThroughFrame(t, env, "remove 1")
	if reply != "server removed\n" {
		t.Fatalf("expected server removed reply, got %q", reply)
	}
	if snaps := mgr.Snapshots(); len(snaps) != 0 {
		t.Fatalf("expected removed server to be gone from registry, still have %d snapshots", len(snaps))
	}
}

func TestAdminIntegration_HandleAdminFrame_SessionBanDisconnectsAndBlocksIdentity(t *testing.T) {
	serverIP := net.ParseIP(nqnet.DefaultNQServerIP).To4()
	alloc := nqnet.NewIPAllocator(serverIP)
	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	sessions := nqnet.NewSessionRegistry()
	env := integrationEnv(mgr, alloc, sessions)

	clientRouter, _ := nqnet.NewTestRouterWith(false, alloc, sessions)
	vip := clientRouter.VirtualClientIP()

	reply := execNexusCommandThroughFrame(t, env, "session ban 1")
	if !strings.Contains(reply, "banned "+vip) {
		t.Fatalf("expected ban confirmation, got %q", reply)
	}
	if !strings.Contains(reply, "source ip(s): 198.51.100.10") {
		t.Fatalf("expected ban output to include source ip, got %q", reply)
	}
	if routers, _ := sessions.SnapshotByVirtualIP(vip); len(routers) != 0 {
		t.Fatalf("expected session to be disconnected after ban")
	}
}

func TestAdminIntegration_HandleAdminFrame_SessionBanAdminRejected(t *testing.T) {
	alloc := nqnet.NewIPAllocator(net.ParseIP(nqnet.DefaultNQServerIP).To4())
	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	sessions := nqnet.NewSessionRegistry()
	env := integrationEnv(mgr, alloc, sessions)

	adminRouter, _ := nqnet.NewTestRouterWith(true, alloc, sessions)
	vip := adminRouter.VirtualClientIP()

	reply := execNexusCommandThroughFrame(t, env, "session ban 1")
	if !strings.Contains(reply, "cannot ban admin sessions") {
		t.Fatalf("expected admin-ban rejection detail, got %q", reply)
	}
	if !strings.Contains(reply, "\nusage: rcon session ban <idx>") {
		t.Fatalf("expected ban usage helper text, got %q", reply)
	}
	if routers, _ := sessions.SnapshotByVirtualIP(vip); len(routers) != 1 {
		t.Fatalf("expected admin session to remain connected after rejected ban")
	}
}
