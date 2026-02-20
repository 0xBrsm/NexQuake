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

func TestHandleAdminFrameWithPromotionHook_FiresOnPromotion(t *testing.T) {
	r, ch := nqnet.NewTestRouter(false)
	auth := &Auth{rconPassword: "pw"}

	hookCalls := 0
	HandleAdminFrameWithPromotionHook(r, []byte("pw\x000\x00help"), auth, &Env{}, func(*nqnet.Router) {
		hookCalls++
	})

	reply := readAdminReply(t, ch)
	if !strings.Contains(reply, "Nexus commands:") {
		t.Fatalf("expected help reply after auth, got %q", reply)
	}
	if hookCalls != 1 {
		t.Fatalf("expected promotion hook to fire once, got %d", hookCalls)
	}
}

func TestHandleAdminFrameWithIdentityAndPromotionHook_UsesActorAwareExec(t *testing.T) {
	r, ch := nqnet.NewTestRouter(true)

	called := false
	var gotPort int
	var gotArgs string
	var gotActor string
	env := &Env{
		ExecServerCmd: func(port int, cmd, actorID string) (string, error) {
			called = true
			gotPort = port
			gotArgs = cmd
			gotActor = actorID
			return "ok\n", nil
		},
	}

	HandleAdminFrameWithIdentityAndPromotionHook(r, []byte("\x0026000\x00status"), &Auth{}, env, "alice@example.com", nil)
	reply := readAdminReply(t, ch)
	if reply != "ok\n" {
		t.Fatalf("expected actor-aware command reply, got %q", reply)
	}
	if !called {
		t.Fatalf("expected ExecServerCmd call")
	}
	if gotPort != 26000 {
		t.Fatalf("expected port 26000, got %d", gotPort)
	}
	if gotArgs != "status" {
		t.Fatalf("expected raw command args preserved, got %q", gotArgs)
	}
	if gotActor != "alice@example.com" {
		t.Fatalf("expected actor identity from connection, got %q", gotActor)
	}
}

func TestResolveAdminActorID_FallsBackToSourceIP(t *testing.T) {
	r, _ := nqnet.NewTestRouter(true)
	got := resolveAdminActorID("anonymous", r)
	if got != "198.51.100.11" {
		t.Fatalf("expected source IP fallback, got %q", got)
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
	if !strings.Contains(reply, "session list") || !strings.Contains(reply, "session info <idx>") || !strings.Contains(reply, "session ban <idx>") || !strings.Contains(reply, "tail") {
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
	if !strings.Contains(reply, "#   Server          Port  Game            Users State") {
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

func TestExecNexusCommand_SessionListNoClients(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []orch.ServerSnapshot { return nil },
		SessionSnapshots: func() []nqnet.SessionSnapshot { return nil },
	}
	reply, err := execNexusCommand("session list", env)
	if err != nil {
		t.Fatalf("execNexusCommand(session list) error = %v", err)
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
		StartServersAll:  func() error { return nil },
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

func TestExecNexusCommand_SessionBanInvalidIndexShowsUsage(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []orch.ServerSnapshot { return nil },
		SessionSnapshots: func() []nqnet.SessionSnapshot { return nil },
	}
	_, err := execNexusCommand("session ban not-a-number", env)
	if err == nil {
		t.Fatalf("expected invalid session index error")
	}
	if !strings.Contains(err.Error(), "invalid session index") {
		t.Fatalf("expected invalid session index detail, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon session ban <idx>") {
		t.Fatalf("expected session ban usage helper text, got %q", err.Error())
	}
}

func TestExecNexusCommand_SessionInfoByIndex(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []orch.ServerSnapshot {
			return []orch.ServerSnapshot{
				{ListenPort: 26000, Hostname: "fragfest"},
			}
		},
		SessionSnapshots: func() []nqnet.SessionSnapshot {
			return []nqnet.SessionSnapshot{
				{
					VirtualIP:        "127.100.10.1",
					SourceIP:         "198.51.100.10",
					UserID:           "alice@example.com",
					IsAdmin:          false,
					ActiveServerPort: 26000,
				},
			}
		},
		ExecServerCmd: func(port int, cmd, actorID string) (string, error) {
			if port != 26000 || cmd != "status" || actorID != "" {
				t.Fatalf("unexpected status lookup call: port=%d cmd=%q actor=%q", port, cmd, actorID)
			}
			return `
#1  player-one          0  0:00:22
   127.100.10.1:51234
`, nil
		},
	}

	reply, err := execNexusCommand("session info 1", env)
	if err != nil {
		t.Fatalf("execNexusCommand(session info 1) error = %v", err)
	}
	if !strings.Contains(reply, "session #1") {
		t.Fatalf("expected session index header, got %q", reply)
	}
	if !strings.Contains(reply, "user: alice@example.com") || !strings.Contains(reply, "server: fragfest") || !strings.Contains(reply, "port: 26000") {
		t.Fatalf("expected session identity and route info, got %q", reply)
	}
	if !strings.Contains(reply, "status slot: 1") || !strings.Contains(reply, "status addr: 127.100.10.1:51234") {
		t.Fatalf("expected status-derived player detail, got %q", reply)
	}
}

func TestStatusPlayerForVirtualIP_FindsMatchingPlayer(t *testing.T) {
	statusReply := `
host:    fragfest
players: 2 active (16 max)

#1  player-one          0  0:00:22
   127.100.10.1:51234
#2  player-two          5  0:01:11
   127.100.10.2:51235
`
	match, ok := StatusPlayerForVirtualIP(statusReply, "127.100.10.2")
	if !ok {
		t.Fatalf("expected to find matching slot in status output")
	}
	if match.Slot != 2 {
		t.Fatalf("expected slot 2, got %d", match.Slot)
	}
}

func TestApplyServerKickTargets_UsesKickByResolvedStatusSlot(t *testing.T) {
	type execCall struct {
		port    int
		cmd     string
		actorID string
	}

	calls := make([]execCall, 0, 2)
	env := &Env{
		ExecServerCmd: func(port int, cmd, actorID string) (string, error) {
			calls = append(calls, execCall{port: port, cmd: cmd, actorID: actorID})
			if cmd == "status" {
				return `
#1  player-one          0  0:00:22
   127.100.10.1:51234
`, nil
			}
			if cmd == "kick # 1 Nexus ban" {
				return "ok\n", nil
			}
			t.Fatalf("unexpected command: %q", cmd)
			return "", nil
		},
	}

	applied, errs := applyServerKickTargets([]nqnet.BanTarget{
		{Port: 26000, VirtualIP: "127.100.10.1"},
	}, env)

	if applied != 1 {
		t.Fatalf("expected one applied server kick, got %d", applied)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if len(calls) != 2 {
		t.Fatalf("expected status+kick command pair, got %d calls", len(calls))
	}
	if calls[0].port != 26000 || calls[0].cmd != "status" || calls[0].actorID != "" {
		t.Fatalf("unexpected first call: %+v", calls[0])
	}
	if calls[1].port != 26000 || calls[1].cmd != "kick # 1 Nexus ban" || calls[1].actorID != "" {
		t.Fatalf("unexpected second call: %+v", calls[1])
	}
}
