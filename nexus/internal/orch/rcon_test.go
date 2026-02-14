package orch

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestExecServerCommand_CapturesConsoleOutput(t *testing.T) {
	oldMaxWait := serverCommandCaptureMaxWait
	oldIdleWait := serverCommandCaptureIdleWait
	t.Cleanup(func() {
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
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(ptyWrite)}
	go func() {
		buf := make([]byte, 128)
		_, _ = ptyRead.Read(buf)
		srv.Console.publishLine("sv_maxspeed is \"320\"\n")
	}()

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec, 26000)
	rec.Running = srv
	globalMgr := mgr

	reply, err := globalMgr.ExecServerCommand(26000, "sv_maxspeed")
	if err != nil {
		t.Fatalf("ExecServerCommand error = %v", err)
	}
	if !strings.Contains(reply, "sv_maxspeed is \"320\"") {
		t.Fatalf("expected captured server output, got %q", reply)
	}
	if strings.Contains(reply, "Command executed successfully.") {
		t.Fatalf("expected real output, got fallback reply %q", reply)
	}
}

func TestExecServerCommand_FiltersNoisyConsoleOutput(t *testing.T) {
	oldMaxWait := serverCommandCaptureMaxWait
	oldIdleWait := serverCommandCaptureIdleWait
	t.Cleanup(func() {
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
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(ptyWrite)}
	go func() {
		buf := make([]byte, 128)
		_, _ = ptyRead.Read(buf)
		srv.Console.publishLine("FindFile: maps/e1m1.bsp\n")
		srv.Console.publishLine("sv_maxspeed is \"320\"\n")
	}()

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.ExecServerCommand(26000, "sv_maxspeed")
	if err != nil {
		t.Fatalf("ExecServerCommand error = %v", err)
	}
	if strings.Contains(reply, "FindFile: maps/e1m1.bsp") {
		t.Fatalf("expected FindFile noise filtered from reply, got %q", reply)
	}
	if !strings.Contains(reply, "sv_maxspeed is \"320\"") {
		t.Fatalf("expected retained command output, got %q", reply)
	}
}

func TestExecServerCommand_SuppressesEchoAndCapturesDelayedOutput(t *testing.T) {
	oldMaxWait := serverCommandCaptureMaxWait
	oldIdleWait := serverCommandCaptureIdleWait
	t.Cleanup(func() {
		serverCommandCaptureMaxWait = oldMaxWait
		serverCommandCaptureIdleWait = oldIdleWait
	})
	serverCommandCaptureMaxWait = 300 * time.Millisecond
	serverCommandCaptureIdleWait = 50 * time.Millisecond

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(ptyWrite)}
	go func() {
		buf := make([]byte, 128)
		n, _ := ptyRead.Read(buf)
		srv.Console.publishLine(string(buf[:n]))
		time.Sleep(120 * time.Millisecond)
		srv.Console.publishLine("host: delayed-response\n")
	}()

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.ExecServerCommand(26000, "status")
	if err != nil {
		t.Fatalf("ExecServerCommand error = %v", err)
	}
	if strings.Contains(reply, "status;") {
		t.Fatalf("expected echoed command to be suppressed, got %q", reply)
	}
	if !strings.Contains(reply, "host: delayed-response") {
		t.Fatalf("expected delayed command output to be captured, got %q", reply)
	}
}

func TestExecServerCommand_TailDefaultsToLastTenLines(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(nil)}
	for i := 1; i <= 12; i++ {
		srv.Console.publishLine(fmt.Sprintf("line %02d\n", i))
	}

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.ExecServerCommand(26000, "tail")
	if err != nil {
		t.Fatalf("ExecServerCommand(tail) error = %v", err)
	}
	if strings.Contains(reply, "line 01") || strings.Contains(reply, "line 02") {
		t.Fatalf("expected tail to skip first two lines, got %q", reply)
	}
	if !strings.Contains(reply, "line 03") || !strings.Contains(reply, "line 12") {
		t.Fatalf("expected tail to include lines 03..12, got %q", reply)
	}
}

func TestExecServerCommand_TailUsesFilteredOutput(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(nil)}
	srv.Console.publishLine("line a\n")
	srv.Console.publishLine("FindFile: maps/e1m1.bsp\n")
	srv.Console.publishLine("PackFile: id1/pak0.pak : maps/e1m1.bsp\n")
	srv.Console.publishLine("FindFile: can't find progs.dat\n")
	srv.Console.publishLine("line b\n")

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.ExecServerCommand(26000, "tail")
	if err != nil {
		t.Fatalf("ExecServerCommand(tail) error = %v", err)
	}
	if strings.Contains(reply, "FindFile: maps/e1m1.bsp") || strings.Contains(reply, "PackFile: id1/pak0.pak") {
		t.Fatalf("expected noisy lines filtered from tail reply, got %q", reply)
	}
	if !strings.Contains(reply, "FindFile: can't find progs.dat") || !strings.Contains(reply, "line b") {
		t.Fatalf("expected retained tail lines, got %q", reply)
	}
}

func TestExecServerCommand_TailUsageError(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(nil)}
	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec, 26000)
	rec.Running = srv

	_, err = mgr.ExecServerCommand(26000, "tail 5")
	if err == nil {
		t.Fatalf("expected tail usage error")
	}
	if !strings.Contains(err.Error(), "usage: rcon <host|port> tail") {
		t.Fatalf("expected tail usage error, got %q", err.Error())
	}
}

func TestExecServerCommand_NoOutputUsesSuccessFallback(t *testing.T) {
	oldMaxWait := serverCommandCaptureMaxWait
	oldIdleWait := serverCommandCaptureIdleWait
	t.Cleanup(func() {
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
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec, 26000)
	rec.Running = &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(ptyWrite)}

	reply, err := mgr.ExecServerCommand(26000, "status")
	if err != nil {
		t.Fatalf("ExecServerCommand error = %v", err)
	}
	if reply != "Command executed successfully.\n" {
		t.Fatalf("expected success fallback reply, got %q", reply)
	}
}
