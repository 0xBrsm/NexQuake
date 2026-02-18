package admin

import (
	"strings"
	"testing"

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
	r, ch := nqnet.NewTestRouter(true)
	auth := &Auth{}
	HandleAdminFrame(r, []byte("\x000\x00"), auth, &Env{})
	reply := readAdminReply(t, ch)
	if !strings.Contains(reply, "usage: rcon <cmd> | rcon <host|port> <cmd>") {
		t.Fatalf("expected updated usage text, got %q", reply)
	}
}

func TestHandleAdminFrame_PromotesSessionAfterValidPassword(t *testing.T) {
	r, ch := nqnet.NewTestRouter(false)
	auth := &Auth{rconPassword: "pw"}

	if r.IsAdmin() {
		t.Fatalf("expected client session before auth")
	}

	HandleAdminFrame(r, []byte("pw\x000\x00help"), auth, &Env{})
	reply := readAdminReply(t, ch)
	if !strings.Contains(reply, "Nexus commands:") {
		t.Fatalf("expected help reply after auth, got %q", reply)
	}
	if !r.IsAdmin() {
		t.Fatalf("expected session to be promoted to admin after valid password")
	}
}

func TestExecNexusCommand_Help(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []orch.ServerSnapshot { return nil },
		SessionSnapshots: func() []nqnet.SessionSnapshot { return nil },
	}
	reply, err := execNexusCommand("help", env)
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

func TestExecNexusCommand_EmptyCommandReturnsHelp(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []orch.ServerSnapshot { return nil },
		SessionSnapshots: func() []nqnet.SessionSnapshot { return nil },
	}
	reply, err := execNexusCommand("", env)
	if err != nil {
		t.Fatalf("expected help reply for empty command, got error %v", err)
	}
	if !strings.Contains(reply, "Nexus commands:") || !strings.Contains(reply, "slist") {
		t.Fatalf("expected help output for empty command, got %q", reply)
	}
}

func TestExecNexusCommand_UnknownCommandReturnsHelp(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []orch.ServerSnapshot { return nil },
		SessionSnapshots: func() []nqnet.SessionSnapshot { return nil },
	}
	reply, err := execNexusCommand("wat", env)
	if err != nil {
		t.Fatalf("expected help reply for unknown command, got error %v", err)
	}
	if !strings.Contains(reply, "Nexus commands:") || !strings.Contains(reply, "help") {
		t.Fatalf("expected help output for unknown command, got %q", reply)
	}
}

func TestExecNexusCommand_Slist(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []orch.ServerSnapshot {
			return []orch.ServerSnapshot{
				{Slot: 0, ListenPort: 26000, GameDir: "id1", Hostname: "fragfest", MapName: "dm6", Players: 1, MaxPlayers: 16, State: "stopped"},
			}
		},
		SessionSnapshots: func() []nqnet.SessionSnapshot { return nil },
	}

	reply, err := execNexusCommand("slist", env)
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

	reply, err = execNexusCommand("nexus slist", env)
	if err != nil {
		t.Fatalf("execNexusCommand(nexus slist) error = %v", err)
	}
	if !strings.Contains(reply, "== end list ==") {
		t.Fatalf("expected slist trailer, got %q", reply)
	}
}

func TestExecNexusCommand_SessionsNoClients(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []orch.ServerSnapshot { return nil },
		SessionSnapshots: func() []nqnet.SessionSnapshot { return nil },
	}
	reply, err := execNexusCommand("sessions", env)
	if err != nil {
		t.Fatalf("execNexusCommand(sessions) error = %v", err)
	}
	if !strings.HasPrefix(reply, "\n") || !strings.Contains(reply, "No active sessions found.") {
		t.Fatalf("expected empty sessions response, got %q", reply)
	}
}

func TestExecNexusCommand_InvalidTargetShowsCommandUsage(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []orch.ServerSnapshot { return nil },
		StartServer:     func(target int) error { return nil },
	}
	_, err := execNexusCommand("start not-a-number", env)
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

func TestExecNexusCommand_AllTargetAccepted(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []orch.ServerSnapshot { return nil },
		SessionSnapshots: func() []nqnet.SessionSnapshot { return nil },
		StartServer:      func(target int) error { return nil },
	}
	reply, err := execNexusCommand("start all", env)
	if err != nil {
		t.Fatalf("expected start all to be accepted with empty registry, got %v", err)
	}
	if reply != "complete\n" {
		t.Fatalf("expected complete reply, got %q", reply)
	}
}

func TestExecNexusCommand_LaunchMissingBinaryShowsUsage(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []orch.ServerSnapshot { return nil },
	}
	_, err := execNexusCommand("launch", env)
	if err == nil {
		t.Fatalf("expected launch usage error")
	}
	if !strings.Contains(err.Error(), "usage: rcon launch <binary> [args...]") {
		t.Fatalf("expected launch helper text, got %q", err.Error())
	}
}

func TestExecNexusCommand_BanInvalidIPShowsUsage(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []orch.ServerSnapshot { return nil },
		SessionSnapshots: func() []nqnet.SessionSnapshot { return nil },
		SnapshotByVIP:    func(vip string) ([]*nqnet.Router, []nqnet.BanTarget) { return nil, nil },
	}
	_, err := execNexusCommand("ban not-an-ip", env)
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
