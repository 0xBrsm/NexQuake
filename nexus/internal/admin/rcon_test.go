package admin

import (
	"fmt"
	"strings"
	"testing"
)

type mockSession struct {
	admin     bool
	sourceIP  string
	sourceKey string
	vip       string
	clientIP  [4]byte
	closed    bool
	replies   strings.Builder
}

func newMockSession(isAdmin bool) *mockSession {
	m := &mockSession{
		admin:     isAdmin,
		sourceIP:  "198.51.100.10",
		sourceKey: "ip:198.51.100.10",
		vip:       "127.100.10.1",
		clientIP:  [4]byte{127, 100, 10, 1},
	}
	if isAdmin {
		m.sourceIP = "198.51.100.11"
		m.sourceKey = "ip:198.51.100.11"
		m.vip = "127.100.10.2"
		m.clientIP = [4]byte{127, 100, 10, 2}
	}
	return m
}

func (m *mockSession) IsAdmin() bool { return m.admin }

func (m *mockSession) PromoteAdmin() { m.admin = true }

func (m *mockSession) SendAdminReply(msg string) { m.replies.WriteString(msg) }

func (m *mockSession) SourceIP() string { return m.sourceIP }

func (m *mockSession) VirtualClientIP() string { return m.vip }

func (m *mockSession) ClientIP() [4]byte { return m.clientIP }

func (m *mockSession) SourceKey() string { return m.sourceKey }

func (m *mockSession) Close() { m.closed = true }

func (m *mockSession) reply() string { return m.replies.String() }

func TestSplitAdminPayload_AcceptsPortZero(t *testing.T) {
	pw, targetText, args := splitAdminPayload([]byte("pw\x000\x00nexus status"))
	if pw != "pw" {
		t.Fatalf("expected password pw, got %q", pw)
	}
	if targetText != "0" {
		t.Fatalf("expected target text %q, got %q", "0", targetText)
	}
	if args != "nexus status" {
		t.Fatalf("expected args %q, got %q", "nexus status", args)
	}
}

func TestRouteAdminTarget_NexusAliases(t *testing.T) {
	for _, raw := range []string{"", "0", "nexus"} {
		target := routeAdminTarget(raw)
		if !target.nexus || target.label != "nexus" {
			t.Fatalf("routeAdminTarget(%q) = %+v, want nexus target", raw, target)
		}
	}
}

func TestRouteAdminTarget_PreservesServerToken(t *testing.T) {
	target := routeAdminTarget("26000")
	if target.nexus || target.label != "26000" {
		t.Fatalf("routeAdminTarget() = %+v, want raw server token", target)
	}
}

func TestHandleAdminFrame_UsageIncludesImplicitTargetForm(t *testing.T) {
	r := newMockSession(true)
	auth := &Auth{}
	HandleAdminFrameWithIdentityAndPromotionHook(r, []byte("\x000\x00"), auth, &Env{}, "", nil)
	reply := r.reply()
	if !strings.Contains(reply, "usage: rcon <cmd> | rcon nexus <cmd> | rcon <host|port|idx> <cmd>") {
		t.Fatalf("expected updated usage text, got %q", reply)
	}
}

func TestHandleAdminFrame_PromotesSessionAfterValidPassword(t *testing.T) {
	r := newMockSession(false)
	auth := &Auth{rconPassword: "pw"}

	if r.IsAdmin() {
		t.Fatalf("expected client session before auth")
	}

	HandleAdminFrameWithIdentityAndPromotionHook(r, []byte("pw\x000\x00help"), auth, &Env{}, "", nil)
	reply := r.reply()
	if !strings.Contains(reply, "Nexus commands:") {
		t.Fatalf("expected help reply after auth, got %q", reply)
	}
	if !r.IsAdmin() {
		t.Fatalf("expected session to be promoted to admin after valid password")
	}
}

func TestHandleAdminFrameWithPromotionHook_FiresOnPromotion(t *testing.T) {
	r := newMockSession(false)
	auth := &Auth{rconPassword: "pw"}

	hookCalls := 0
	HandleAdminFrameWithIdentityAndPromotionHook(r, []byte("pw\x000\x00help"), auth, &Env{}, "", func(Session) {
		hookCalls++
	})

	reply := r.reply()
	if !strings.Contains(reply, "Nexus commands:") {
		t.Fatalf("expected help reply after auth, got %q", reply)
	}
	if hookCalls != 1 {
		t.Fatalf("expected promotion hook to fire once, got %d", hookCalls)
	}
}

func TestHandleAdminFrameWithIdentityAndPromotionHook_UsesActorAwareExec(t *testing.T) {
	r := newMockSession(true)

	called := false
	var gotTarget string
	var gotArgs string
	var gotActor string
	var auditLogs []string
	env := &Env{
		DispatchServerCmd: func(target, cmd, actorID string) (string, error) {
			called = true
			gotTarget = target
			gotArgs = cmd
			gotActor = actorID
			return "ok\n", nil
		},
		Auditf: func(format string, args ...any) {
			auditLogs = append(auditLogs, fmt.Sprintf(format, args...))
		},
	}

	HandleAdminFrameWithIdentityAndPromotionHook(r, []byte("\x0026000\x00status"), &Auth{}, env, "alice@example.com", nil)
	reply := r.reply()
	if reply != "ok\n" {
		t.Fatalf("expected actor-aware command reply, got %q", reply)
	}
	if !called {
		t.Fatalf("expected DispatchServerCmd call")
	}
	if gotTarget != "26000" {
		t.Fatalf("expected target 26000, got %q", gotTarget)
	}
	if gotArgs != "status" {
		t.Fatalf("expected raw command args preserved, got %q", gotArgs)
	}
	if gotActor != "alice@example.com" {
		t.Fatalf("expected actor identity from connection, got %q", gotActor)
	}
	if len(auditLogs) != 2 {
		t.Fatalf("expected request+response audit logs, got %d (%v)", len(auditLogs), auditLogs)
	}
	if !strings.Contains(auditLogs[0], `actor="alice@example.com"`) || !strings.Contains(auditLogs[0], "target=26000") || !strings.Contains(auditLogs[0], `command="status"`) {
		t.Fatalf("unexpected request audit log: %q", auditLogs[0])
	}
	if !strings.Contains(auditLogs[1], `actor="alice@example.com"`) || !strings.Contains(auditLogs[1], "target=26000") || !strings.Contains(auditLogs[1], `reply="ok"`) {
		t.Fatalf("unexpected response audit log: %q", auditLogs[1])
	}
}

func TestHandleAdminFrameWithIdentityAndPromotionHook_AuditsNexusCommand(t *testing.T) {
	r := newMockSession(true)

	var auditLogs []string
	env := &Env{
		Auditf: func(format string, args ...any) {
			auditLogs = append(auditLogs, fmt.Sprintf(format, args...))
		},
	}

	HandleAdminFrameWithIdentityAndPromotionHook(r, []byte("\x000\x00help"), &Auth{}, env, "alice@example.com", nil)
	reply := r.reply()
	if !strings.Contains(reply, "Nexus commands:") {
		t.Fatalf("expected help reply, got %q", reply)
	}
	if len(auditLogs) != 2 {
		t.Fatalf("expected request+response audit logs, got %d (%v)", len(auditLogs), auditLogs)
	}
	if !strings.Contains(auditLogs[0], `actor="alice@example.com"`) || !strings.Contains(auditLogs[0], "target=nexus") || !strings.Contains(auditLogs[0], `command="help"`) {
		t.Fatalf("unexpected request audit log: %q", auditLogs[0])
	}
	if !strings.Contains(auditLogs[1], `actor="alice@example.com"`) || !strings.Contains(auditLogs[1], "target=nexus") || !strings.Contains(auditLogs[1], `reply="Nexus commands:`) {
		t.Fatalf("unexpected response audit log: %q", auditLogs[1])
	}
}

func TestHandleAdminFrameWithIdentityAndPromotionHook_ResolvesHostnameTarget(t *testing.T) {
	r := newMockSession(true)

	var gotTarget string
	var gotCmd string
	env := &Env{
		DispatchServerCmd: func(target, cmd, actorID string) (string, error) {
			gotTarget = target
			gotCmd = cmd
			return "ok\n", nil
		},
	}

	HandleAdminFrameWithIdentityAndPromotionHook(r, []byte("\x00fragfest\x00status"), &Auth{}, env, "alice@example.com", nil)
	if reply := r.reply(); reply != "ok\n" {
		t.Fatalf("expected resolved hostname reply, got %q", reply)
	}
	if gotTarget != "fragfest" || gotCmd != "status" {
		t.Fatalf("resolved hostname dispatch = target %q cmd %q, want target fragfest cmd status", gotTarget, gotCmd)
	}
}

func TestResolveAdminActorID_FallsBackToSourceIP(t *testing.T) {
	r := newMockSession(true)
	got := resolveAdminActorID("anonymous", r)
	if got != "198.51.100.11" {
		t.Fatalf("expected source IP fallback, got %q", got)
	}
}

func TestExecNexusCommand_Help(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []ServerInfo { return nil },
		SessionSnapshots: func() []SessionInfo { return nil },
	}
	reply, err := execNexusCommand("help", env)
	if err != nil {
		t.Fatalf("execNexusCommand(help) error = %v", err)
	}
	if !strings.Contains(reply, "Nexus commands:") {
		t.Fatalf("expected help header, got %q", reply)
	}
	if !strings.Contains(reply, "rcon nexus slist [<all|idx|host>]") || !strings.Contains(reply, "rcon nexus session list") || !strings.Contains(reply, "rcon nexus session info <idx>") || !strings.Contains(reply, "rcon nexus session ban <idx>") || !strings.Contains(reply, "rcon nexus tail") {
		t.Fatalf("expected help to list new commands, got %q", reply)
	}
}

func TestExecNexusCommand_EmptyCommandReturnsHelp(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []ServerInfo { return nil },
		SessionSnapshots: func() []SessionInfo { return nil },
	}
	reply, err := execNexusCommand("", env)
	if err != nil {
		t.Fatalf("expected help reply for empty command, got error %v", err)
	}
	if !strings.Contains(reply, "Nexus commands:") || !strings.Contains(reply, "rcon nexus slist [<all|idx|host>]") {
		t.Fatalf("expected help output for empty command, got %q", reply)
	}
}

func TestExecNexusCommand_UnknownCommandReturnsHelpUsageError(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []ServerInfo { return nil },
		SessionSnapshots: func() []SessionInfo { return nil },
	}
	reply, err := execNexusCommand("wat", env)
	if err == nil {
		t.Fatalf("expected error for unknown command")
	}
	if reply != "" {
		t.Fatalf("expected empty reply for unknown command, got %q", reply)
	}
	if !strings.Contains(err.Error(), "unknown Nexus command \"wat\"") {
		t.Fatalf("expected unknown command detail, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon nexus help") {
		t.Fatalf("expected help usage hint, got %q", err.Error())
	}
}

func TestExecNexusCommand_InvalidCommandLineReturnsHelpUsageError(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []ServerInfo { return nil },
		SessionSnapshots: func() []SessionInfo { return nil },
	}
	reply, err := execNexusCommand("\"", env)
	if err == nil {
		t.Fatalf("expected error for invalid command line")
	}
	if reply != "" {
		t.Fatalf("expected empty reply for invalid command line, got %q", reply)
	}
	if !strings.Contains(err.Error(), "invalid Nexus command line:") {
		t.Fatalf("expected parse detail, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon nexus help") {
		t.Fatalf("expected help usage hint, got %q", err.Error())
	}
}

func TestExecNexusCommand_SlistNoArgsShowsPoolList(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []ServerInfo {
			return []ServerInfo{
				{CandidatePort: 26000, GameDir: "id1", Hostname: "fragfest", Players: 1, MaxPlayers: 16, Instances: 3, State: "stopped"},
			}
		},
		SessionSnapshots: func() []SessionInfo { return nil },
	}

	reply, err := execNexusCommand("slist", env)
	if err != nil {
		t.Fatalf("execNexusCommand(slist) error = %v", err)
	}
	if !strings.HasPrefix(reply, "\n") {
		t.Fatalf("expected leading blank line, got %q", reply)
	}
	if !strings.Contains(reply, "#   Pool            Candidate Game            Users        State") {
		t.Fatalf("expected pool-list header, got %q", reply)
	}
	if !strings.Contains(reply, "fragfest") || !strings.Contains(reply, "id1") || !strings.Contains(reply, "1/16 (3)") {
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

func TestExecNexusCommand_PlistUnknown(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []ServerInfo { return nil },
		SessionSnapshots: func() []SessionInfo { return nil },
	}
	reply, err := execNexusCommand("plist", env)
	if err == nil {
		t.Fatalf("expected error for removed plist command")
	}
	if reply != "" {
		t.Fatalf("expected empty reply for removed plist command, got %q", reply)
	}
	if !strings.Contains(err.Error(), "unknown Nexus command \"plist\"") {
		t.Fatalf("expected unknown command detail, got %q", err.Error())
	}
}

func TestExecNexusCommand_SlistAll(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []ServerInfo {
			return []ServerInfo{
				{Line: 0, Hostname: "fragfest", CandidatePort: 26000, GameDir: "id1", Players: 1, MaxPlayers: 32, Instances: 2, State: "running"},
				{Line: 1, Hostname: "ctf", CandidatePort: 26010, GameDir: "ctf", Players: 0, MaxPlayers: 16, Instances: 1, State: "starting"},
			}
		},
		BackendSnapshots: func(target int) ([]ServerInfo, error) {
			if target != 0 {
				t.Fatalf("expected all target, got %d", target)
			}
			return []ServerInfo{
				{Line: 0, ListenPort: 26000, MapName: "dm3", Players: 1, MaxPlayers: 16, State: "running"},
				{Line: 0, ListenPort: 26001, MapName: "dm3", Players: 0, MaxPlayers: 16, State: "running"},
				{Line: 1, ListenPort: 26010, MapName: "ctf2m3", Players: 0, MaxPlayers: 16, State: "starting"},
			}, nil
		},
		SessionSnapshots: func() []SessionInfo { return nil },
	}

	reply, err := execNexusCommand("slist all", env)
	if err != nil {
		t.Fatalf("execNexusCommand(slist all) error = %v", err)
	}
	if !strings.Contains(reply, "[1] fragfest  game=id1  users=1/32 (2)  candidate=26000  state=running") {
		t.Fatalf("expected grouped pool header, got %q", reply)
	}
	if !strings.Contains(reply, "    #  Port  Map             Users   State") || !strings.Contains(reply, "    2  26001 dm3") {
		t.Fatalf("expected grouped backend rows, got %q", reply)
	}
	if !strings.Contains(reply, "[2] ctf  game=ctf  users=0/16 (1)  candidate=26010  state=starting") {
		t.Fatalf("expected second grouped pool header, got %q", reply)
	}
}

func TestExecNexusCommand_SlistPool(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []ServerInfo {
			return []ServerInfo{
				{Line: 0, Hostname: "fragfest", CandidatePort: 26000, GameDir: "id1", Players: 1, MaxPlayers: 32, Instances: 2, State: "running"},
				{Line: 1, Hostname: "ctf", CandidatePort: 26017, GameDir: "ctf", Players: 2, MaxPlayers: 16, Instances: 1, State: "running"},
			}
		},
		BackendSnapshots: func(target int) ([]ServerInfo, error) {
			if target != 2 {
				t.Fatalf("expected pool target 2, got %d", target)
			}
			return []ServerInfo{
				{Line: 1, ListenPort: 26017, MapName: "ctf2m3", Players: 2, MaxPlayers: 16, State: "running"},
			}, nil
		},
	}

	reply, err := execNexusCommand("slist 2", env)
	if err != nil {
		t.Fatalf("execNexusCommand(slist 2) error = %v", err)
	}
	if !strings.Contains(reply, "[2] ctf  game=ctf  users=2/16 (1)  candidate=26017  state=running") {
		t.Fatalf("expected selected pool header, got %q", reply)
	}
	if !strings.Contains(reply, "    #  Port  Map             Users   State") || !strings.Contains(reply, "    1  26017 ctf2m3") {
		t.Fatalf("expected selected backend row, got %q", reply)
	}
}

func TestExecNexusCommand_SlistPoolByHostname(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []ServerInfo {
			return []ServerInfo{
				{Line: 0, Hostname: "fragfest", CandidatePort: 26000, GameDir: "id1", Players: 1, MaxPlayers: 32, Instances: 2, State: "running"},
				{Line: 1, Hostname: "ctf", CandidatePort: 26017, GameDir: "ctf", Players: 2, MaxPlayers: 16, Instances: 1, State: "running"},
			}
		},
		BackendSnapshots: func(target int) ([]ServerInfo, error) {
			if target != 2 {
				t.Fatalf("expected pool target 2, got %d", target)
			}
			return []ServerInfo{
				{Line: 1, ListenPort: 26017, MapName: "ctf2m3", Players: 2, MaxPlayers: 16, State: "running"},
			}, nil
		},
	}

	reply, err := execNexusCommand("slist ctf", env)
	if err != nil {
		t.Fatalf("execNexusCommand(slist ctf) error = %v", err)
	}
	if !strings.Contains(reply, "[2] ctf  game=ctf  users=2/16 (1)  candidate=26017  state=running") {
		t.Fatalf("expected selected pool header, got %q", reply)
	}
	if !strings.Contains(reply, "    #  Port  Map             Users   State") || !strings.Contains(reply, "    1  26017 ctf2m3") {
		t.Fatalf("expected selected backend row, got %q", reply)
	}
}

func TestExecNexusCommand_SlistRejectsTooManyArgs(t *testing.T) {
	env := &Env{}
	_, err := execNexusCommand("slist all extra", env)
	if err == nil {
		t.Fatalf("expected slist usage error")
	}
	if !strings.Contains(err.Error(), "usage: rcon nexus slist [<all|idx|host>]") {
		t.Fatalf("expected slist usage helper, got %q", err.Error())
	}
}

func TestExecNexusCommand_SessionRequiresConcreteCommandUsage(t *testing.T) {
	env := &Env{}
	_, err := execNexusCommand("session", env)
	if err == nil {
		t.Fatalf("expected session usage error")
	}
	if !strings.Contains(err.Error(), "usage: rcon nexus session list | rcon nexus session info <idx> | rcon nexus session ban <idx>") {
		t.Fatalf("expected explicit session command usage, got %q", err.Error())
	}
}

func TestExecNexusCommand_SessionListNoClients(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []ServerInfo { return nil },
		SessionSnapshots: func() []SessionInfo { return nil },
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
		ServerSnapshots: func() []ServerInfo {
			return []ServerInfo{{Hostname: "fragfest"}}
		},
		StartServer: func(target int) error { return nil },
	}
	_, err := execNexusCommand("start not-a-number", env)
	if err == nil {
		t.Fatalf("expected usage error for invalid target")
	}
	if !strings.Contains(err.Error(), "unknown target \"not-a-number\"") {
		t.Fatalf("expected unknown target error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon nexus start <idx|host|all>") {
		t.Fatalf("expected command usage on next line for invalid target, got %q", err.Error())
	}
}

func TestExecNexusCommand_StartPoolByHostname(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []ServerInfo {
			return []ServerInfo{
				{Hostname: "fragfest"},
				{Hostname: "ctf"},
			}
		},
		StartServer: func(target int) error {
			if target != 2 {
				t.Fatalf("expected hostname ctf to resolve to pool 2, got %d", target)
			}
			return nil
		},
	}

	reply, err := execNexusCommand("start ctf", env)
	if err != nil {
		t.Fatalf("execNexusCommand(start ctf) error = %v", err)
	}
	if reply != "complete\n" {
		t.Fatalf("expected complete reply, got %q", reply)
	}
}

func TestExecNexusCommand_PoolPortTargetRejected(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []ServerInfo {
			return []ServerInfo{{Hostname: "fragfest", CandidatePort: 26000}}
		},
		StartServer: func(target int) error {
			t.Fatalf("unexpected start dispatch for pool port target %d", target)
			return nil
		},
	}

	_, err := execNexusCommand("start 26000", env)
	if err == nil {
		t.Fatalf("expected pool port target to be rejected")
	}
	if !strings.Contains(err.Error(), "unknown target \"26000\"") {
		t.Fatalf("expected unknown target error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon nexus start <idx|host|all>") {
		t.Fatalf("expected updated command usage, got %q", err.Error())
	}
}

func TestExecNexusCommand_AllTargetAccepted(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []ServerInfo { return nil },
		SessionSnapshots: func() []SessionInfo { return nil },
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
		ServerSnapshots: func() []ServerInfo { return nil },
	}
	_, err := execNexusCommand("launch", env)
	if err == nil {
		t.Fatalf("expected launch usage error")
	}
	if !strings.Contains(err.Error(), "usage: rcon nexus launch <binary> [args...]") {
		t.Fatalf("expected launch helper text, got %q", err.Error())
	}
}

func TestExecNexusCommand_SessionBanInvalidIndexShowsUsage(t *testing.T) {
	env := &Env{
		ServerSnapshots:  func() []ServerInfo { return nil },
		SessionSnapshots: func() []SessionInfo { return nil },
	}
	_, err := execNexusCommand("session ban not-a-number", env)
	if err == nil {
		t.Fatalf("expected invalid session index error")
	}
	if !strings.Contains(err.Error(), "invalid session index") {
		t.Fatalf("expected invalid session index detail, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\nusage: rcon nexus session ban <idx>") {
		t.Fatalf("expected session ban usage helper text, got %q", err.Error())
	}
}

func TestExecNexusCommand_SessionInfoByIndex(t *testing.T) {
	env := &Env{
		ServerSnapshots: func() []ServerInfo {
			return []ServerInfo{
				{ListenPort: 26000, Hostname: "fragfest"},
			}
		},
		SessionSnapshots: func() []SessionInfo {
			return []SessionInfo{
				{
					VirtualIP:        "127.100.10.1",
					SourceIP:         "198.51.100.10",
					UserID:           "alice@example.com",
					IsAdmin:          false,
					ActiveServerPort: 26000,
				},
			}
		},
		DispatchServerCmd: func(target, cmd, actorID string) (string, error) {
			if target != "26000" || cmd != "status" || actorID != "" {
				t.Fatalf("unexpected status lookup call: target=%q cmd=%q actor=%q", target, cmd, actorID)
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

func TestExecNexusCommand_SessionInfoUnknownManagedPortTreatedDisconnected(t *testing.T) {
	statusLookups := 0
	env := &Env{
		ServerSnapshots: func() []ServerInfo {
			return []ServerInfo{
				{ListenPort: 26000, Hostname: "fragfest"},
			}
		},
		SessionSnapshots: func() []SessionInfo {
			return []SessionInfo{
				{
					VirtualIP:        "127.100.10.1",
					SourceIP:         "198.51.100.10",
					UserID:           "alice@example.com",
					IsAdmin:          false,
					ActiveServerPort: 42000,
				},
			}
		},
		DispatchServerCmd: func(target, cmd, actorID string) (string, error) {
			statusLookups++
			return "", fmt.Errorf("unknown server")
		},
	}

	reply, err := execNexusCommand("session info 1", env)
	if err != nil {
		t.Fatalf("execNexusCommand(session info 1) error = %v", err)
	}
	if statusLookups != 0 {
		t.Fatalf("expected no status lookup for unknown managed server port, got %d call(s)", statusLookups)
	}
	if !strings.Contains(reply, "port: -") || !strings.Contains(reply, "status: not connected to a server") {
		t.Fatalf("expected disconnected status for unknown managed server port, got %q", reply)
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
	match, ok := statusPlayerForVirtualIP(statusReply, "127.100.10.2")
	if !ok {
		t.Fatalf("expected to find matching slot in status output")
	}
	if match.slot != 2 {
		t.Fatalf("expected slot 2, got %d", match.slot)
	}
}

func TestApplyServerKickTargets_UsesKickByResolvedStatusSlot(t *testing.T) {
	type execCall struct {
		target  string
		cmd     string
		actorID string
	}

	calls := make([]execCall, 0, 2)
	env := &Env{
		DispatchServerCmd: func(target, cmd, actorID string) (string, error) {
			calls = append(calls, execCall{target: target, cmd: cmd, actorID: actorID})
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

	applied, errs := applyServerKickTargets([]BanTarget{
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
	if calls[0].target != "26000" || calls[0].cmd != "status" || calls[0].actorID != "" {
		t.Fatalf("unexpected first call: %+v", calls[0])
	}
	if calls[1].target != "26000" || calls[1].cmd != "kick # 1 Nexus ban" || calls[1].actorID != "" {
		t.Fatalf("unexpected second call: %+v", calls[1])
	}
}
