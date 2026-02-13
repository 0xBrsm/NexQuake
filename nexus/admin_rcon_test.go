package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func readAdminReply(t *testing.T, r *Router) string {
	t.Helper()

	select {
	case frame := <-r.wsTx:
		if len(frame) < wsPortHeaderSize {
			t.Fatalf("expected ws frame with %d-byte header, got %d bytes", wsPortHeaderSize, len(frame))
		}
		if frame[0] != 0 || frame[1] != 0 {
			t.Fatalf("expected admin reply on control port 0, got header [%d %d]", frame[0], frame[1])
		}
		return string(frame[wsPortHeaderSize:])
	default:
		t.Fatalf("expected admin reply frame")
	}

	return ""
}

func TestSplitAdminPayload_AcceptsPortZero(t *testing.T) {
	pw, targetPort, args := splitAdminPayload([]byte("pw\x000\x00nexus status"))

	if pw != "pw" {
		t.Fatalf("expected password pw, got %q", pw)
	}
	if targetPort != 0 {
		t.Fatalf("expected target port 0, got %d", targetPort)
	}
	if args != "nexus status" {
		t.Fatalf("expected args %q, got %q", "nexus status", args)
	}
}

func TestHandleAdminFrame_UsageIncludesImplicitTargetForm(t *testing.T) {
	r := &Router{
		wsTx:    make(chan []byte, 1),
		isAdmin: true,
		ctx:     context.Background(),
	}

	r.handleAdminFrame([]byte("\x000\x00"))
	reply := readAdminReply(t, r)

	if !strings.Contains(reply, "usage: rcon <cmd> | rcon <host|port> <cmd>") {
		t.Fatalf("expected updated usage text, got %q", reply)
	}
}

func TestExecServerCommand_CapturesConsoleOutput(t *testing.T) {
	oldMgr := globalServerManager
	oldMaxWait := serverCommandCaptureMaxWait
	oldIdleWait := serverCommandCaptureIdleWait
	t.Cleanup(func() {
		globalServerManager = oldMgr
		serverCommandCaptureMaxWait = oldMaxWait
		serverCommandCaptureIdleWait = oldIdleWait
	})

	serverCommandCaptureMaxWait = 120 * time.Millisecond
	serverCommandCaptureIdleWait = 10 * time.Millisecond

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = ptyRead.Close()
		_ = ptyWrite.Close()
	})

	srv := &managedServer{
		cmd:     &exec.Cmd{Process: process},
		console: newServerConsole(ptyWrite),
	}

	go func() {
		buf := make([]byte, 128)
		_, _ = ptyRead.Read(buf)
		srv.console.publishLine("sv_maxspeed is \"320\"\n")
	}()

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	rec.running = srv
	globalServerManager = mgr

	r := &Router{}
	reply, err := r.execServerCommand(26000, "sv_maxspeed")
	if err != nil {
		t.Fatalf("execServerCommand error = %v", err)
	}
	if !strings.Contains(reply, "sv_maxspeed is \"320\"") {
		t.Fatalf("expected captured server output, got %q", reply)
	}
	if strings.Contains(reply, "Command executed successfully.") {
		t.Fatalf("expected real output, got fallback reply %q", reply)
	}
}

func TestExecServerCommand_FiltersNoisyConsoleOutput(t *testing.T) {
	oldMgr := globalServerManager
	oldMaxWait := serverCommandCaptureMaxWait
	oldIdleWait := serverCommandCaptureIdleWait
	t.Cleanup(func() {
		globalServerManager = oldMgr
		serverCommandCaptureMaxWait = oldMaxWait
		serverCommandCaptureIdleWait = oldIdleWait
	})

	serverCommandCaptureMaxWait = 120 * time.Millisecond
	serverCommandCaptureIdleWait = 10 * time.Millisecond

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = ptyRead.Close()
		_ = ptyWrite.Close()
	})

	srv := &managedServer{
		cmd:     &exec.Cmd{Process: process},
		console: newServerConsole(ptyWrite),
	}

	go func() {
		buf := make([]byte, 128)
		_, _ = ptyRead.Read(buf)
		srv.console.publishLine("FindFile: maps/e1m1.bsp\n")
		srv.console.publishLine("sv_maxspeed is \"320\"\n")
	}()

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	rec.running = srv
	globalServerManager = mgr

	r := &Router{}
	reply, err := r.execServerCommand(26000, "sv_maxspeed")
	if err != nil {
		t.Fatalf("execServerCommand error = %v", err)
	}
	if strings.Contains(reply, "FindFile: maps/e1m1.bsp") {
		t.Fatalf("expected FindFile noise filtered from reply, got %q", reply)
	}
	if !strings.Contains(reply, "sv_maxspeed is \"320\"") {
		t.Fatalf("expected retained command output, got %q", reply)
	}
}

func TestExecServerCommand_TailDefaultsToLastTenLines(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() {
		globalServerManager = oldMgr
	})

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	srv := &managedServer{
		cmd:     &exec.Cmd{Process: process},
		console: newServerConsole(nil),
	}
	for i := 1; i <= 12; i++ {
		srv.console.publishLine(fmt.Sprintf("line %02d\n", i))
	}

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	rec.running = srv
	globalServerManager = mgr

	r := &Router{}
	reply, err := r.execServerCommand(26000, "tail")
	if err != nil {
		t.Fatalf("execServerCommand(tail) error = %v", err)
	}
	if strings.Contains(reply, "line 01") || strings.Contains(reply, "line 02") {
		t.Fatalf("expected tail to skip first two lines, got %q", reply)
	}
	if !strings.Contains(reply, "line 03") || !strings.Contains(reply, "line 12") {
		t.Fatalf("expected tail to include lines 03..12, got %q", reply)
	}
}

func TestExecServerCommand_TailUsesFilteredOutput(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() {
		globalServerManager = oldMgr
	})

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	srv := &managedServer{
		cmd:     &exec.Cmd{Process: process},
		console: newServerConsole(nil),
	}
	srv.console.publishLine("line a\n")
	srv.console.publishLine("FindFile: maps/e1m1.bsp\n")
	srv.console.publishLine("PackFile: id1/pak0.pak : maps/e1m1.bsp\n")
	srv.console.publishLine("FindFile: can't find progs.dat\n")
	srv.console.publishLine("line b\n")

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	rec.running = srv
	globalServerManager = mgr

	r := &Router{}
	reply, err := r.execServerCommand(26000, "tail")
	if err != nil {
		t.Fatalf("execServerCommand(tail) error = %v", err)
	}
	if strings.Contains(reply, "FindFile: maps/e1m1.bsp") || strings.Contains(reply, "PackFile: id1/pak0.pak") {
		t.Fatalf("expected noisy lines filtered from tail reply, got %q", reply)
	}
	if !strings.Contains(reply, "FindFile: can't find progs.dat") || !strings.Contains(reply, "line b") {
		t.Fatalf("expected retained tail lines, got %q", reply)
	}
}

func TestExecServerCommand_TailUsageError(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() {
		globalServerManager = oldMgr
	})

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	srv := &managedServer{
		cmd:     &exec.Cmd{Process: process},
		console: newServerConsole(nil),
	}

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	rec.running = srv
	globalServerManager = mgr

	r := &Router{}
	_, err = r.execServerCommand(26000, "tail 5")
	if err == nil {
		t.Fatalf("expected tail usage error")
	}
	if !strings.Contains(err.Error(), "usage: rcon <host|port> tail") {
		t.Fatalf("expected tail usage error, got %q", err.Error())
	}
}

func TestExecServerCommand_NoOutputUsesSuccessFallback(t *testing.T) {
	oldMgr := globalServerManager
	oldMaxWait := serverCommandCaptureMaxWait
	oldIdleWait := serverCommandCaptureIdleWait
	t.Cleanup(func() {
		globalServerManager = oldMgr
		serverCommandCaptureMaxWait = oldMaxWait
		serverCommandCaptureIdleWait = oldIdleWait
	})

	serverCommandCaptureMaxWait = 20 * time.Millisecond
	serverCommandCaptureIdleWait = 5 * time.Millisecond

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = ptyRead.Close()
		_ = ptyWrite.Close()
	})

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	rec.running = &managedServer{
		cmd:     &exec.Cmd{Process: process},
		console: newServerConsole(ptyWrite),
	}
	globalServerManager = mgr

	r := &Router{}
	reply, err := r.execServerCommand(26000, "status")
	if err != nil {
		t.Fatalf("execServerCommand error = %v", err)
	}
	if reply != "Command executed successfully.\n" {
		t.Fatalf("expected success fallback reply, got %q", reply)
	}
}

func TestHandleAdminFrame_ServerCommandReturnsOutput(t *testing.T) {
	oldMgr := globalServerManager
	oldMaxWait := serverCommandCaptureMaxWait
	oldIdleWait := serverCommandCaptureIdleWait
	t.Cleanup(func() {
		globalServerManager = oldMgr
		serverCommandCaptureMaxWait = oldMaxWait
		serverCommandCaptureIdleWait = oldIdleWait
	})

	serverCommandCaptureMaxWait = 120 * time.Millisecond
	serverCommandCaptureIdleWait = 10 * time.Millisecond

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = ptyRead.Close()
		_ = ptyWrite.Close()
	})

	srv := &managedServer{
		cmd:     &exec.Cmd{Process: process},
		console: newServerConsole(ptyWrite),
	}

	go func() {
		buf := make([]byte, 128)
		_, _ = ptyRead.Read(buf)
		srv.console.publishLine("hostname is \"fragfest\"\n")
	}()

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	rec.running = srv
	globalServerManager = mgr

	r := &Router{
		wsTx:    make(chan []byte, 1),
		isAdmin: true,
		ctx:     context.Background(),
	}
	r.handleAdminFrame([]byte("\x0026000\x00hostname"))
	reply := readAdminReply(t, r)

	if !strings.Contains(reply, "hostname is \"fragfest\"") {
		t.Fatalf("expected server console output reply, got %q", reply)
	}
	if strings.TrimSpace(reply) == "ok" {
		t.Fatalf("expected output passthrough, got legacy ok reply")
	}
}

func TestExecNexusCommand_Slist(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	mgr.updateSearchPath(rec, []string{"id1"})
	rec.hostname = "fragfest"
	rec.mapName = "dm6"
	rec.players = 1
	rec.maxPlayers = 16
	globalServerManager = mgr

	r := &Router{}

	reply, err := r.execNexusCommand("slist")
	if err != nil {
		t.Fatalf("execNexusCommand(slist) error = %v", err)
	}
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

	reply, err = r.execNexusCommand("nexus slist")
	if err != nil {
		t.Fatalf("execNexusCommand(nexus slist) error = %v", err)
	}
	if !strings.Contains(reply, "== end list ==") {
		t.Fatalf("expected slist trailer, got %q", reply)
	}
}

func TestExecNexusCommand_SessionsNoClients(t *testing.T) {
	oldMgr := globalServerManager
	oldSessions := globalClientSessions
	t.Cleanup(func() {
		globalServerManager = oldMgr
		globalClientSessions = oldSessions
	})

	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())
	globalClientSessions = newClientSessionRegistry()
	r := &Router{}

	reply, err := r.execNexusCommand("sessions")
	if err != nil {
		t.Fatalf("execNexusCommand(sessions) error = %v", err)
	}
	if !strings.HasPrefix(reply, "\n") || !strings.Contains(reply, "No active sessions found.") {
		t.Fatalf("expected empty sessions response, got %q", reply)
	}
}

func TestExecNexusCommand_SessionsListClientSessions(t *testing.T) {
	oldMgr := globalServerManager
	oldSessions := globalClientSessions
	t.Cleanup(func() {
		globalServerManager = oldMgr
		globalClientSessions = oldSessions
	})

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	mgr.updateSearchPath(rec, []string{"id1"})
	rec.hostname = "fragfest"
	globalServerManager = mgr
	globalClientSessions = newClientSessionRegistry()

	clientRouter := &Router{
		clientIP:  [4]byte{127, 10, 20, 30},
		sourceKey: "ip:203.0.113.7",
		cancel:    func() {},
	}
	clientRouter.noteServerRoutePort(26000)
	globalClientSessions.Track(clientRouter)
	adminRouter := &Router{
		clientIP:  [4]byte{127, 10, 20, 40},
		sourceKey: "ip:203.0.113.8",
		isAdmin:   true,
		cancel:    func() {},
	}
	globalClientSessions.Track(adminRouter)

	r := &Router{}
	reply, err := r.execNexusCommand("sessions")
	if err != nil {
		t.Fatalf("execNexusCommand(sessions) error = %v", err)
	}
	if !strings.Contains(reply, "#   NQIP") || !strings.Contains(reply, "Role") || !strings.Contains(reply, "Port") || !strings.Contains(reply, "Server") {
		t.Fatalf("expected sessions header, got %q", reply)
	}
	if !strings.Contains(reply, "127.10.20.30") || !strings.Contains(reply, "26000") || !strings.Contains(reply, "fragfest") {
		t.Fatalf("expected sessions output to include NQIP/port/server, got %q", reply)
	}
	if !strings.Contains(reply, "127.10.20.40") || !strings.Contains(reply, "admin") || !strings.Contains(reply, "client") {
		t.Fatalf("expected sessions output to include role markers, got %q", reply)
	}
}

func TestExecNexusCommand_Help(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })

	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())
	r := &Router{}

	reply, err := r.execNexusCommand("help")
	if err != nil {
		t.Fatalf("execNexusCommand(help) error = %v", err)
	}
	if !strings.Contains(reply, "Nexus commands:") {
		t.Fatalf("expected help header, got %q", reply)
	}
	if !strings.Contains(reply, "ban <idx|NQIP>") || !strings.Contains(reply, "sessions") || !strings.Contains(reply, "tail") {
		t.Fatalf("expected help to list new commands, got %q", reply)
	}
}

func TestExecNexusCommand_TailReturnsLastTenLines(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	resetNexusLogHistoryForTest()
	t.Cleanup(resetNexusLogHistoryForTest)
	for i := 1; i <= 12; i++ {
		recordNexusLogLine(fmt.Sprintf("nexus line %02d\n", i))
	}

	r := &Router{}
	reply, err := r.execNexusCommand("tail")
	if err != nil {
		t.Fatalf("execNexusCommand(tail) error = %v", err)
	}
	if strings.Contains(reply, "nexus line 01") || strings.Contains(reply, "nexus line 02") {
		t.Fatalf("expected tail to skip first two lines, got %q", reply)
	}
	if !strings.Contains(reply, "nexus line 03") || !strings.Contains(reply, "nexus line 12") {
		t.Fatalf("expected tail to include lines 03..12, got %q", reply)
	}
}

func TestExecNexusCommand_TailUsageError(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	r := &Router{}
	_, err := r.execNexusCommand("tail 5")
	if err == nil {
		t.Fatalf("expected tail usage error")
	}
	if !strings.Contains(err.Error(), "usage: rcon tail") {
		t.Fatalf("expected tail usage text, got %q", err.Error())
	}
}

func TestExecNexusCommand_TailExcludesNoTailRelayLogs(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	resetNexusLogHistoryForTest()
	t.Cleanup(resetNexusLogHistoryForTest)

	infofNoTail("[quake-a-1] player joined")
	infof("nexus ready")

	r := &Router{}
	reply, err := r.execNexusCommand("tail")
	if err != nil {
		t.Fatalf("execNexusCommand(tail) error = %v", err)
	}
	if strings.Contains(reply, "[quake-a-1] player joined") {
		t.Fatalf("expected no-tail relay line to be excluded, got %q", reply)
	}
	if !strings.Contains(reply, "nexus ready") {
		t.Fatalf("expected normal nexus log line in tail, got %q", reply)
	}
}

func TestExecNexusCommand_TailRemainsCanonicalWhenConsoleTimestampsDisabled(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	oldSetting := operatorConsoleTimestampsEnabled()
	t.Cleanup(func() { setOperatorConsoleTimestamps(oldSetting) })
	setOperatorConsoleTimestamps(true)

	resetNexusLogHistoryForTest()
	t.Cleanup(resetNexusLogHistoryForTest)
	recordNexusLogLine("2026/02/12 10:11:12 nexus ready\n")

	r := &Router{}
	replyOn, err := r.execNexusCommand("tail")
	if err != nil {
		t.Fatalf("execNexusCommand(tail) error = %v", err)
	}
	if !strings.Contains(replyOn, "2026/02/12 10:11:12 nexus ready") {
		t.Fatalf("expected canonical timestamped tail output, got %q", replyOn)
	}

	setOperatorConsoleTimestamps(false)

	replyOff, err := r.execNexusCommand("tail")
	if err != nil {
		t.Fatalf("execNexusCommand(tail) error = %v", err)
	}
	if !strings.Contains(replyOff, "2026/02/12 10:11:12 nexus ready") {
		t.Fatalf("expected canonical timestamp retained when console timestamps disabled, got %q", replyOff)
	}
}

func TestHandleAdminFrame_TargetPortZeroRunsNexusCommand(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	r := &Router{
		wsTx:    make(chan []byte, 1),
		isAdmin: true,
		ctx:     context.Background(),
	}

	r.handleAdminFrame([]byte("\x000\x00slist"))
	reply := readAdminReply(t, r)

	if !strings.HasPrefix(reply, "\n") || !strings.Contains(reply, "No Quake servers found.") {
		t.Fatalf("expected nexus slist reply, got %q", reply)
	}
}

func TestExecNexusCommand_EmptyCommandReturnsHelp(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	r := &Router{}
	reply, err := r.execNexusCommand("")
	if err != nil {
		t.Fatalf("expected help reply for empty command, got error %v", err)
	}
	if !strings.Contains(reply, "Nexus commands:") || !strings.Contains(reply, "slist") {
		t.Fatalf("expected help output for empty command, got %q", reply)
	}
}

func TestExecNexusCommand_InvalidTargetShowsCommandUsage(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	r := &Router{}
	_, err := r.execNexusCommand("start not-a-number")
	if err == nil {
		t.Fatalf("expected usage error for invalid target")
	}
	if !strings.Contains(err.Error(), "invalid target \"not-a-number\"") {
		t.Fatalf("expected invalid target error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon start <idx|port|all>") {
		t.Fatalf("expected command usage on next line for invalid target, got %q", err.Error())
	}
}

func TestExecNexusCommand_RestartDispatches(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	mgr.updateSearchPath(rec, []string{"id1"})
	globalServerManager = mgr

	r := &Router{}
	_, err := r.execNexusCommand("restart 1")
	if err == nil {
		t.Fatalf("expected restart to fail without initialized runtime")
	}
	if !strings.Contains(err.Error(), "runtime not initialized") {
		t.Fatalf("expected restart to dispatch to manager start path, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon restart <idx|port|all>") {
		t.Fatalf("expected restart helper text on error, got %q", err.Error())
	}
}

func TestExecNexusCommand_AllTargetAccepted(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	r := &Router{}
	reply, err := r.execNexusCommand("start all")
	if err != nil {
		t.Fatalf("expected start all to be accepted with empty registry, got %v", err)
	}
	if reply != "ok\n" {
		t.Fatalf("expected ok reply, got %q", reply)
	}
}

func TestExecNexusCommand_RemoveDispatchesForStoppedServer(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	mgr.updateSearchPath(rec, []string{"id1"})
	globalServerManager = mgr

	r := &Router{}
	reply, err := r.execNexusCommand("remove 1")
	if err != nil {
		t.Fatalf("expected remove to succeed for stopped server, got %v", err)
	}
	if reply != "ok\n" {
		t.Fatalf("expected ok reply, got %q", reply)
	}
	if snaps := mgr.Snapshots(); len(snaps) != 0 {
		t.Fatalf("expected removed server to be gone from registry, still have %d snapshots", len(snaps))
	}
}

func TestExecNexusCommand_RemoveRunningShowsUsage(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	rec.running = &managedServer{cmd: &exec.Cmd{Process: process}}
	globalServerManager = mgr

	r := &Router{}
	_, err = r.execNexusCommand("remove 1")
	if err == nil {
		t.Fatalf("expected remove to fail while running")
	}
	if !strings.Contains(err.Error(), "server is running; stop server first") {
		t.Fatalf("expected running-server guard, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon remove <idx|port>") {
		t.Fatalf("expected remove helper text on error, got %q", err.Error())
	}
}

func TestExecNexusCommand_LaunchMissingBinaryShowsUsage(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	r := &Router{}
	_, err := r.execNexusCommand("launch")
	if err == nil {
		t.Fatalf("expected launch usage error")
	}
	if !strings.Contains(err.Error(), "usage: rcon launch <binary> [args...]") {
		t.Fatalf("expected launch helper text, got %q", err.Error())
	}
}

func TestExecNexusCommand_LaunchDispatchesAndShowsUsageOnError(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	r := &Router{}
	_, err := r.execNexusCommand("launch nqserver -dedicated")
	if err == nil {
		t.Fatalf("expected launch to fail without initialized runtime")
	}
	if !strings.Contains(err.Error(), "runtime not initialized") {
		t.Fatalf("expected launch runtime error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon launch <binary> [args...]") {
		t.Fatalf("expected launch helper text on error, got %q", err.Error())
	}
}

func TestExecNexusCommand_BanInvalidIPShowsUsage(t *testing.T) {
	oldMgr := globalServerManager
	oldSessions := globalClientSessions
	t.Cleanup(func() {
		globalServerManager = oldMgr
		globalClientSessions = oldSessions
	})

	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())
	globalClientSessions = newClientSessionRegistry()

	r := &Router{}
	_, err := r.execNexusCommand("ban not-an-ip")
	if err == nil {
		t.Fatalf("expected invalid ban ip error")
	}
	if !strings.Contains(err.Error(), "invalid client ip") {
		t.Fatalf("expected invalid client ip detail, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon ban <idx|NQIP>") {
		t.Fatalf("expected ban usage helper text, got %q", err.Error())
	}
}

func TestExecNexusCommand_BanDisconnectsAndBlocksIdentity(t *testing.T) {
	oldMgr := globalServerManager
	oldAlloc := globalClientIPv4Allocator
	oldSessions := globalClientSessions
	t.Cleanup(func() {
		globalServerManager = oldMgr
		globalClientIPv4Allocator = oldAlloc
		globalClientSessions = oldSessions
	})

	globalClientIPv4Allocator = newHashedClientIPAllocator()
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())
	globalClientSessions = newClientSessionRegistry()

	clientRouter := &Router{
		clientIP:  [4]byte{127, 10, 20, 30},
		sourceKey: "ip:203.0.113.7",
		cancel:    func() {},
	}
	globalClientSessions.Track(clientRouter)

	r := &Router{}
	reply, err := r.execNexusCommand("ban 127.10.20.30")
	if err != nil {
		t.Fatalf("ban command error = %v", err)
	}
	if !strings.Contains(reply, "banned 127.10.20.30") {
		t.Fatalf("expected ban confirmation, got %q", reply)
	}
	if routers, _ := globalClientSessions.SnapshotByVirtualIP("127.10.20.30"); len(routers) != 0 {
		t.Fatalf("expected session to be disconnected after ban")
	}
	if _, err := allocateRelaySourceIPv4("ip:203.0.113.7"); err == nil {
		t.Fatalf("expected banned source key allocation to fail")
	}
}

func TestExecNexusCommand_BanBySessionIndexDisconnectsAndBlocksIdentity(t *testing.T) {
	oldMgr := globalServerManager
	oldAlloc := globalClientIPv4Allocator
	oldSessions := globalClientSessions
	t.Cleanup(func() {
		globalServerManager = oldMgr
		globalClientIPv4Allocator = oldAlloc
		globalClientSessions = oldSessions
	})

	globalClientIPv4Allocator = newHashedClientIPAllocator()
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())
	globalClientSessions = newClientSessionRegistry()

	clientRouter := &Router{
		clientIP:  [4]byte{127, 10, 20, 30},
		sourceKey: "ip:203.0.113.7",
		cancel:    func() {},
	}
	globalClientSessions.Track(clientRouter)

	r := &Router{}
	reply, err := r.execNexusCommand("ban 1")
	if err != nil {
		t.Fatalf("ban command error = %v", err)
	}
	if !strings.Contains(reply, "banned 127.10.20.30") {
		t.Fatalf("expected ban confirmation by session index, got %q", reply)
	}
	if routers, _ := globalClientSessions.SnapshotByVirtualIP("127.10.20.30"); len(routers) != 0 {
		t.Fatalf("expected session to be disconnected after ban by index")
	}
	if _, err := allocateRelaySourceIPv4("ip:203.0.113.7"); err == nil {
		t.Fatalf("expected banned source key allocation to fail")
	}
}

func TestExecNexusCommand_BanAdminByIPRejected(t *testing.T) {
	oldMgr := globalServerManager
	oldSessions := globalClientSessions
	t.Cleanup(func() {
		globalServerManager = oldMgr
		globalClientSessions = oldSessions
	})

	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())
	globalClientSessions = newClientSessionRegistry()

	adminRouter := &Router{
		clientIP:  [4]byte{127, 10, 20, 40},
		sourceKey: "ip:203.0.113.8",
		isAdmin:   true,
		cancel:    func() {},
	}
	globalClientSessions.Track(adminRouter)

	r := &Router{}
	_, err := r.execNexusCommand("ban 127.10.20.40")
	if err == nil {
		t.Fatalf("expected admin ban attempt to fail")
	}
	if !strings.Contains(err.Error(), "cannot ban admin sessions") {
		t.Fatalf("expected admin-ban rejection detail, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon ban <idx|NQIP>") {
		t.Fatalf("expected ban usage helper text, got %q", err.Error())
	}
	if routers, _ := globalClientSessions.SnapshotByVirtualIP("127.10.20.40"); len(routers) != 1 {
		t.Fatalf("expected admin session to remain connected after rejected ban")
	}
}

func TestExecNexusCommand_BanAdminByIndexRejected(t *testing.T) {
	oldMgr := globalServerManager
	oldSessions := globalClientSessions
	t.Cleanup(func() {
		globalServerManager = oldMgr
		globalClientSessions = oldSessions
	})

	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())
	globalClientSessions = newClientSessionRegistry()

	adminRouter := &Router{
		clientIP:  [4]byte{127, 10, 20, 40},
		sourceKey: "ip:203.0.113.8",
		isAdmin:   true,
		cancel:    func() {},
	}
	globalClientSessions.Track(adminRouter)

	r := &Router{}
	_, err := r.execNexusCommand("ban 1")
	if err == nil {
		t.Fatalf("expected admin ban-by-index attempt to fail")
	}
	if !strings.Contains(err.Error(), "cannot ban admin sessions") {
		t.Fatalf("expected admin-ban rejection detail, got %q", err.Error())
	}
	if routers, _ := globalClientSessions.SnapshotByVirtualIP("127.10.20.40"); len(routers) != 1 {
		t.Fatalf("expected admin session to remain connected after rejected ban by index")
	}
}

func TestExecNexusCommand_UnknownTargetShowsCommandUsage(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })

	mgr := NewServerManager(t.TempDir(), t.TempDir())
	rec := mgr.RegisterServerLaunch(serverLaunch{slot: 0})
	mgr.updatePort(rec, 26000)
	mgr.updateSearchPath(rec, []string{"id1"})
	globalServerManager = mgr

	r := &Router{}
	_, err := r.execNexusCommand("start 2")
	if err == nil {
		t.Fatalf("expected start to fail for unknown idx")
	}
	if !strings.Contains(err.Error(), "unknown target 2") {
		t.Fatalf("expected unknown target error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon start <idx|port|all>") {
		t.Fatalf("expected start helper text on unknown target, got %q", err.Error())
	}
}

func TestExecNexusCommand_UnknownCommandReturnsHelp(t *testing.T) {
	oldMgr := globalServerManager
	t.Cleanup(func() { globalServerManager = oldMgr })
	globalServerManager = NewServerManager(t.TempDir(), t.TempDir())

	r := &Router{}
	reply, err := r.execNexusCommand("wat")
	if err != nil {
		t.Fatalf("expected help reply for unknown command, got error %v", err)
	}
	if !strings.Contains(reply, "Nexus commands:") || !strings.Contains(reply, "help") {
		t.Fatalf("expected help output for unknown command, got %q", reply)
	}
}
