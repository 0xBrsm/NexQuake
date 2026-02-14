package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
)

func readAdminReply(t *testing.T, ch chan []byte) string {
	t.Helper()
	select {
	case frame := <-ch:
		if len(frame) < nqnet.WSPortHeaderSize {
			t.Fatalf("expected ws frame with %d-byte header, got %d bytes", nqnet.WSPortHeaderSize, len(frame))
		}
		if frame[0] != 0 || frame[1] != 0 {
			t.Fatalf("expected admin reply on control port 0, got header [%d %d]", frame[0], frame[1])
		}
		return string(frame[nqnet.WSPortHeaderSize:])
	default:
		t.Fatalf("expected admin reply frame")
	}
	return ""
}

func setupTestNqnetGlobals(t *testing.T) {
	t.Helper()
	oldMgr := globalServerManager
	oldAlloc := globalIPAllocator
	oldSessions := globalSessionRegistry
	oldAuth := globalAuth
	oldEnv := globalAdminEnv
	t.Cleanup(func() {
		globalServerManager = oldMgr
		globalIPAllocator = oldAlloc
		globalSessionRegistry = oldSessions
		globalAuth = oldAuth
		globalAdminEnv = oldEnv
	})
}

func execAdminNexusCommand(t *testing.T, cmd string) string {
	t.Helper()

	r, ch := nqnet.NewTestRouter(true)
	admin.HandleAdminFrame(r, []byte("\x000\x00"+cmd), nil, globalAdminEnv)
	return readAdminReply(t, ch)
}

// --- Integration tests: admin commands via HandleAdminFrame with real ServerManager ---

func TestHandleAdminFrame_ServerCommandReturnsOutput(t *testing.T) {
	setupTestNqnetGlobals(t)

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
	globalServerManager = mgr
	globalAdminEnv = buildAdminEnv()
	globalAuth = &admin.Auth{}

	r, ch := nqnet.NewTestRouter(true)
	admin.HandleAdminFrame(r, []byte("\x0026000\x00hostname"), globalAuth, globalAdminEnv)
	reply := readAdminReply(t, ch)
	if !strings.Contains(reply, "hostname is \"fragfest\"") {
		t.Fatalf("expected server console output reply, got %q", reply)
	}
	if strings.TrimSpace(reply) == "ok" {
		t.Fatalf("expected output passthrough, got legacy ok reply")
	}
}

func TestHandleAdminFrame_TargetPortZeroRunsNexusCommand(t *testing.T) {
	setupTestNqnetGlobals(t)
	globalServerManager = orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	globalAdminEnv = buildAdminEnv()
	globalAuth = &admin.Auth{}

	r, ch := nqnet.NewTestRouter(true)
	admin.HandleAdminFrame(r, []byte("\x000\x00slist"), globalAuth, globalAdminEnv)
	reply := readAdminReply(t, ch)
	if !strings.HasPrefix(reply, "\n") || !strings.Contains(reply, "No Quake servers found.") {
		t.Fatalf("expected nexus slist reply, got %q", reply)
	}
}

func TestExecNexusCommand_SessionsListClientSessions(t *testing.T) {
	setupTestNqnetGlobals(t)

	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(orch.NewTestServerLaunch(0))
	mgr.UpdatePort(rec, 26000)
	mgr.UpdateSearchPath(rec, []string{"id1"})
	mgr.SetServerInfoForTest(rec, "fragfest", "", 0, 0)
	globalServerManager = mgr

	serverIP := net.ParseIP(nqnet.DefaultNQServerIP).To4()
	globalIPAllocator = nqnet.NewIPAllocator(serverIP)
	globalSessionRegistry = nqnet.NewSessionRegistry()
	globalAdminEnv = buildAdminEnv()

	clientRouter, _ := nqnet.NewTestRouterWith(false, globalIPAllocator, globalSessionRegistry)
	clientRouter.NoteServerRoutePort(26000)

	nqnet.NewTestRouterWith(true, globalIPAllocator, globalSessionRegistry)

	reply := execAdminNexusCommand(t, "sessions")
	if !strings.Contains(reply, "#   NQIP") || !strings.Contains(reply, "Role") || !strings.Contains(reply, "Port") || !strings.Contains(reply, "Server") {
		t.Fatalf("expected sessions header, got %q", reply)
	}
	if !strings.Contains(reply, "admin") || !strings.Contains(reply, "client") {
		t.Fatalf("expected sessions output to include role markers, got %q", reply)
	}
}

func TestExecNexusCommand_TailReturnsLastTenLines(t *testing.T) {
	setupTestNqnetGlobals(t)
	globalServerManager = orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	globalAdminEnv = buildAdminEnv()

	resetNexusLogHistoryForTest()
	t.Cleanup(resetNexusLogHistoryForTest)
	for i := 1; i <= 12; i++ {
		recordNexusLogLine(fmt.Sprintf("nexus line %02d\n", i))
	}

	reply := execAdminNexusCommand(t, "tail")
	if strings.Contains(reply, "nexus line 01") || strings.Contains(reply, "nexus line 02") {
		t.Fatalf("expected tail to skip first two lines, got %q", reply)
	}
	if !strings.Contains(reply, "nexus line 03") || !strings.Contains(reply, "nexus line 12") {
		t.Fatalf("expected tail to include lines 03..12, got %q", reply)
	}
}

func TestExecNexusCommand_TailUsageError(t *testing.T) {
	setupTestNqnetGlobals(t)
	globalServerManager = orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	globalAdminEnv = buildAdminEnv()

	reply := execAdminNexusCommand(t, "tail 5")
	if !strings.Contains(reply, "error:") || !strings.Contains(reply, "usage: rcon tail") {
		t.Fatalf("expected tail usage error, got %q", reply)
	}
}

func TestExecNexusCommand_TailExcludesNoTailRelayLogs(t *testing.T) {
	setupTestNqnetGlobals(t)
	globalServerManager = orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	globalAdminEnv = buildAdminEnv()

	resetNexusLogHistoryForTest()
	t.Cleanup(resetNexusLogHistoryForTest)

	infofNoTail("[quake-a-1] player joined")
	infof("nexus ready")

	reply := execAdminNexusCommand(t, "tail")
	if strings.Contains(reply, "[quake-a-1] player joined") {
		t.Fatalf("expected no-tail relay line to be excluded, got %q", reply)
	}
	if !strings.Contains(reply, "nexus ready") {
		t.Fatalf("expected normal nexus log line in tail, got %q", reply)
	}
}

func TestExecNexusCommand_TailRemainsCanonicalWhenConsoleTimestampsDisabled(t *testing.T) {
	setupTestNqnetGlobals(t)
	globalServerManager = orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	globalAdminEnv = buildAdminEnv()

	oldSetting := operatorConsoleTimestampsEnabled()
	t.Cleanup(func() { setOperatorConsoleTimestamps(oldSetting) })
	setOperatorConsoleTimestamps(true)

	resetNexusLogHistoryForTest()
	t.Cleanup(resetNexusLogHistoryForTest)
	recordNexusLogLine("2026/02/12 10:11:12 nexus ready\n")

	replyOn := execAdminNexusCommand(t, "tail")
	if !strings.Contains(replyOn, "2026/02/12 10:11:12 nexus ready") {
		t.Fatalf("expected canonical timestamped tail output, got %q", replyOn)
	}

	setOperatorConsoleTimestamps(false)

	replyOff := execAdminNexusCommand(t, "tail")
	if !strings.Contains(replyOff, "2026/02/12 10:11:12 nexus ready") {
		t.Fatalf("expected canonical timestamp retained when console timestamps disabled, got %q", replyOff)
	}
}

func TestExecNexusCommand_Slist(t *testing.T) {
	setupTestNqnetGlobals(t)

	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(orch.NewTestServerLaunch(0))
	mgr.UpdatePort(rec, 26000)
	mgr.UpdateSearchPath(rec, []string{"id1"})
	mgr.SetServerInfoForTest(rec, "fragfest", "dm6", 1, 16)
	globalServerManager = mgr
	globalAdminEnv = buildAdminEnv()

	reply := execAdminNexusCommand(t, "slist")
	if !strings.HasPrefix(reply, "\n") {
		t.Fatalf("expected leading blank line, got %q", reply)
	}
	if !strings.Contains(reply, "#   Port  Server          Game            Users State") {
		t.Fatalf("expected slist-style header, got %q", reply)
	}
	if !strings.Contains(reply, "fragfest") || !strings.Contains(reply, "id1") || !strings.Contains(reply, "1/16") {
		t.Fatalf("expected hostname/game/users in slist reply, got %q", reply)
	}
	if !strings.Contains(reply, "1   26000") || !strings.Contains(reply, "stopped") {
		t.Fatalf("expected idx/port/state in slist reply, got %q", reply)
	}

	reply = execAdminNexusCommand(t, "nexus slist")
	if !strings.Contains(reply, "== end list ==") {
		t.Fatalf("expected slist trailer, got %q", reply)
	}
}

func TestExecNexusCommand_RestartDispatches(t *testing.T) {
	setupTestNqnetGlobals(t)

	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(orch.NewTestServerLaunch(0))
	mgr.UpdatePort(rec, 26000)
	mgr.UpdateSearchPath(rec, []string{"id1"})
	globalServerManager = mgr
	globalAdminEnv = buildAdminEnv()

	reply := execAdminNexusCommand(t, "restart 1")
	if !strings.Contains(reply, "runtime not initialized") {
		t.Fatalf("expected restart to dispatch to manager start path, got %q", reply)
	}
	if !strings.Contains(reply, "\nusage: rcon restart <idx|port|all>") {
		t.Fatalf("expected restart helper text on error, got %q", reply)
	}
}

func TestExecNexusCommand_RemoveDispatchesForStoppedServer(t *testing.T) {
	setupTestNqnetGlobals(t)

	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(orch.NewTestServerLaunch(0))
	mgr.UpdatePort(rec, 26000)
	mgr.UpdateSearchPath(rec, []string{"id1"})
	globalServerManager = mgr
	globalAdminEnv = buildAdminEnv()

	reply := execAdminNexusCommand(t, "remove 1")
	if reply != "ok\n" {
		t.Fatalf("expected ok reply, got %q", reply)
	}
	if snaps := mgr.Snapshots(); len(snaps) != 0 {
		t.Fatalf("expected removed server to be gone from registry, still have %d snapshots", len(snaps))
	}
}

func TestExecNexusCommand_RemoveRunningShowsUsage(t *testing.T) {
	setupTestNqnetGlobals(t)

	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(orch.NewTestServerLaunch(0))
	mgr.SetServerRunningForTest(rec, orch.NewTestServer(26000))
	globalServerManager = mgr
	globalAdminEnv = buildAdminEnv()

	reply := execAdminNexusCommand(t, "remove 1")
	if !strings.Contains(reply, "server is running; stop server first") {
		t.Fatalf("expected running-server guard, got %q", reply)
	}
	if !strings.Contains(reply, "\nusage: rcon remove <idx|port>") {
		t.Fatalf("expected remove helper text on error, got %q", reply)
	}
}

func TestExecNexusCommand_LaunchDispatchesAndShowsUsageOnError(t *testing.T) {
	setupTestNqnetGlobals(t)
	globalServerManager = orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	globalAdminEnv = buildAdminEnv()

	reply := execAdminNexusCommand(t, "launch nqserver -dedicated")
	if !strings.Contains(reply, "runtime not initialized") {
		t.Fatalf("expected launch runtime error, got %q", reply)
	}
	if !strings.Contains(reply, "\nusage: rcon launch <binary> [args...]") {
		t.Fatalf("expected launch helper text on error, got %q", reply)
	}
}

func TestExecNexusCommand_UnknownTargetShowsCommandUsage(t *testing.T) {
	setupTestNqnetGlobals(t)

	mgr := orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(orch.NewTestServerLaunch(0))
	mgr.UpdatePort(rec, 26000)
	mgr.UpdateSearchPath(rec, []string{"id1"})
	globalServerManager = mgr
	globalAdminEnv = buildAdminEnv()

	reply := execAdminNexusCommand(t, "start 2")
	if !strings.Contains(reply, "unknown target 2") {
		t.Fatalf("expected unknown target error, got %q", reply)
	}
	if !strings.Contains(reply, "\nusage: rcon start <idx|port|all>") {
		t.Fatalf("expected start helper text on unknown target, got %q", reply)
	}
}

func TestExecNexusCommand_BanDisconnectsAndBlocksIdentity(t *testing.T) {
	setupTestNqnetGlobals(t)

	serverIP := net.ParseIP(nqnet.DefaultNQServerIP).To4()
	globalIPAllocator = nqnet.NewIPAllocator(serverIP)
	globalServerManager = orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	globalSessionRegistry = nqnet.NewSessionRegistry()
	globalAdminEnv = buildAdminEnv()

	clientRouter, _ := nqnet.NewTestRouterWith(false, globalIPAllocator, globalSessionRegistry)
	vip := clientRouter.VirtualClientIP()

	reply := execAdminNexusCommand(t, "ban "+vip)
	if !strings.Contains(reply, "banned "+vip) {
		t.Fatalf("expected ban confirmation, got %q", reply)
	}
	if routers, _ := globalSessionRegistry.SnapshotByVirtualIP(vip); len(routers) != 0 {
		t.Fatalf("expected session to be disconnected after ban")
	}
}

func TestExecNexusCommand_BanAdminByIPRejected(t *testing.T) {
	setupTestNqnetGlobals(t)
	globalServerManager = orch.NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	globalSessionRegistry = nqnet.NewSessionRegistry()
	globalIPAllocator = nqnet.NewIPAllocator(net.ParseIP(nqnet.DefaultNQServerIP).To4())
	globalAdminEnv = buildAdminEnv()

	adminRouter, _ := nqnet.NewTestRouterWith(true, globalIPAllocator, globalSessionRegistry)
	vip := adminRouter.VirtualClientIP()

	reply := execAdminNexusCommand(t, "ban "+vip)
	if !strings.Contains(reply, "cannot ban admin sessions") {
		t.Fatalf("expected admin-ban rejection detail, got %q", reply)
	}
	if !strings.Contains(reply, "\nusage: rcon ban <idx|NQIP>") {
		t.Fatalf("expected ban usage helper text, got %q", reply)
	}
	if routers, _ := globalSessionRegistry.SnapshotByVirtualIP(vip); len(routers) != 1 {
		t.Fatalf("expected admin session to remain connected after rejected ban")
	}
}
