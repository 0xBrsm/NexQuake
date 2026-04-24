package admin

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- ActorID ----------------------------------------------------------------

func TestActorID_PrefersExplicitIdentity(t *testing.T) {
	if got := ActorID("alice@example.com", "1.2.3.4", "127.0.0.1"); got != "alice@example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestActorID_FallsBackToSourceIP(t *testing.T) {
	if got := ActorID("anonymous", "198.51.100.11", "127.100.10.2"); got != "198.51.100.11" {
		t.Fatalf("got %q", got)
	}
}

func TestActorID_FallsBackToVirtualIP(t *testing.T) {
	if got := ActorID("", "", "127.100.10.2"); got != "127.100.10.2" {
		t.Fatalf("got %q", got)
	}
}

func TestActorID_DefaultsToAdmin(t *testing.T) {
	if got := ActorID("", "", ""); got != "admin" {
		t.Fatalf("got %q", got)
	}
}

// --- Authorize --------------------------------------------------------------

func TestAuthorize_DeniesWithoutCredentials(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	r := httptest.NewRequest("POST", "/rcon", nil)
	actor, promoted := auth.Authorize(r, "198.51.100.1")
	if actor.IsAdmin {
		t.Fatalf("expected IsAdmin=false with no credentials")
	}
	if promoted {
		t.Fatalf("expected passwordMatched=false with no credentials")
	}
}

func TestAuthorize_AdmitsMatchingPasswordScheme(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	r := httptest.NewRequest("POST", "/rcon", nil)
	r.Header.Set("Authorization", "Rcon secret")
	actor, promoted := auth.Authorize(r, "198.51.100.1")
	if !actor.IsAdmin {
		t.Fatalf("expected IsAdmin=true with matching password")
	}
	if !promoted {
		t.Fatalf("expected passwordMatched=true on Rcon-scheme auth")
	}
	if actor.SourceIP != "198.51.100.1" {
		t.Fatalf("got SourceIP=%q", actor.SourceIP)
	}
}

func TestAuthorize_RejectsWrongPassword(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	r := httptest.NewRequest("POST", "/rcon", nil)
	r.Header.Set("Authorization", "Rcon nope")
	actor, promoted := auth.Authorize(r, "198.51.100.1")
	if actor.IsAdmin {
		t.Fatalf("expected denial for wrong password")
	}
	if promoted {
		t.Fatalf("unexpected passwordMatched")
	}
}

func TestAuthorize_IgnoresWrongScheme(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	r := httptest.NewRequest("POST", "/rcon", nil)
	r.Header.Set("Authorization", "Bearer secret") // wrong scheme for rcon
	actor, promoted := auth.Authorize(r, "198.51.100.1")
	if actor.IsAdmin || promoted {
		t.Fatal("expected denial when Authorization uses wrong scheme for rcon")
	}
}

func TestAuthorize_NilAuthDenies(t *testing.T) {
	var auth *Auth
	r := httptest.NewRequest("POST", "/rcon", nil)
	r.Header.Set("Authorization", "Rcon anything")
	actor, _ := auth.Authorize(r, "198.51.100.1")
	if actor.IsAdmin {
		t.Fatalf("expected denial for nil auth")
	}
}

// --- ParseRequest -----------------------------------------------------------

func TestParseRequest_Empty(t *testing.T) {
	_, resp := ParseRequest(nil)
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeInvalidReq {
		t.Fatalf("expected invalid-request error, got %+v", resp)
	}
}

func TestParseRequest_BadJSON(t *testing.T) {
	_, resp := ParseRequest([]byte("{not json"))
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeParseError {
		t.Fatalf("expected parse-error, got %+v", resp)
	}
}

func TestParseRequest_WrongVersion(t *testing.T) {
	_, resp := ParseRequest([]byte(`{"jsonrpc":"1.0","method":"x","id":1}`))
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeInvalidReq {
		t.Fatalf("expected invalid-request on wrong version, got %+v", resp)
	}
}

func TestParseRequest_MissingMethod(t *testing.T) {
	_, resp := ParseRequest([]byte(`{"jsonrpc":"2.0","id":1}`))
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeInvalidReq {
		t.Fatalf("expected invalid-request on missing method, got %+v", resp)
	}
}

func TestParseRequest_OK(t *testing.T) {
	req, resp := ParseRequest([]byte(`{"jsonrpc":"2.0","method":"server.list","id":7}`))
	if resp != nil {
		t.Fatalf("unexpected error: %+v", resp)
	}
	if req.Method != "server.list" {
		t.Fatalf("got method %q", req.Method)
	}
}

// --- Dispatch ---------------------------------------------------------------

func adminActor() Actor {
	return Actor{ID: "admin", SourceIP: "198.51.100.1", IsAdmin: true}
}

func TestDispatch_UnknownMethod(t *testing.T) {
	req := &Request{Jsonrpc: "2.0", Method: "bogus.method", ID: json.RawMessage(`1`)}
	resp := Dispatch(req, &Env{}, adminActor())
	if resp.Error == nil || resp.Error.Code != ErrCodeMethodNotFound {
		t.Fatalf("expected method-not-found, got %+v", resp.Error)
	}
}

func TestDispatch_InvalidParams(t *testing.T) {
	// server.launch requires non-empty binary
	req := &Request{Jsonrpc: "2.0", Method: "server.launch", Params: json.RawMessage(`{}`), ID: json.RawMessage(`2`)}
	resp := Dispatch(req, &Env{}, adminActor())
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("expected invalid-params, got %+v", resp.Error)
	}
}

func TestDispatch_ServerListOK(t *testing.T) {
	env := &Env{ServerSnapshots: func() []ServerInfo {
		return []ServerInfo{{Line: 0, Hostname: "fragfest", State: "running"}}
	}}
	req := &Request{Jsonrpc: "2.0", Method: "server.list", ID: json.RawMessage(`3`)}
	resp := Dispatch(req, env, adminActor())
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result := resp.Result.(ServerListResult)
	if len(result.Servers) != 1 || result.Servers[0].Hostname != "fragfest" {
		t.Fatalf("got %+v", result.Servers)
	}
}

func TestDispatch_ServerListUnavailable(t *testing.T) {
	req := &Request{Jsonrpc: "2.0", Method: "server.list", ID: json.RawMessage(`4`)}
	resp := Dispatch(req, &Env{}, adminActor())
	if resp.Error == nil || resp.Error.Code != ErrCodeUnavailable {
		t.Fatalf("expected unavailable error, got %+v", resp.Error)
	}
}

func TestDispatch_LogsTailTrimsBlank(t *testing.T) {
	env := &Env{TailNexusLog: func(n int) []string {
		return []string{"hello\n", "", "world\r\n"}
	}}
	req := &Request{Jsonrpc: "2.0", Method: "logs.tail", ID: json.RawMessage(`5`)}
	resp := Dispatch(req, env, adminActor())
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	got := resp.Result.(LogsTailResult).Lines
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Fatalf("got lines %q", got)
	}
}

// --- server target parsing ---------------------------------------------------

func TestParseServerTargetToken_Index(t *testing.T) {
	idx, all, err := parseServerTargetToken("3")
	if err != nil || all || idx != 3 {
		t.Fatalf("got idx=%d all=%v err=%v", idx, all, err)
	}
}

func TestParseServerTargetToken_All(t *testing.T) {
	idx, all, err := parseServerTargetToken("all")
	if err != nil || !all || idx != 0 {
		t.Fatalf("got idx=%d all=%v err=%v", idx, all, err)
	}
}

func TestParseServerTargetToken_AllIgnoresCase(t *testing.T) {
	_, all, err := parseServerTargetToken("ALL")
	if err != nil || !all {
		t.Fatalf("expected ALL to be accepted, got all=%v err=%v", all, err)
	}
}

func TestParseServerTargetToken_RejectsEmpty(t *testing.T) {
	_, _, err := parseServerTargetToken("")
	if _, ok := err.(*MethodError); !ok || err.(*MethodError).Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams, got %v", err)
	}
}

func TestParseServerTargetToken_RejectsHostname(t *testing.T) {
	_, _, err := parseServerTargetToken("fragfest")
	if _, ok := err.(*MethodError); !ok || err.(*MethodError).Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams for hostname, got %v", err)
	}
}

func TestParseServerTargetToken_RejectsNonPositive(t *testing.T) {
	_, _, err := parseServerTargetToken("0")
	if _, ok := err.(*MethodError); !ok || err.(*MethodError).Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams for 0, got %v", err)
	}
}

// --- audit logging --------------------------------------------------------

func TestDispatch_EmitsAuditLogs(t *testing.T) {
	var entries []string
	env := &Env{
		ServerSnapshots: func() []ServerInfo { return nil },
		Auditf: func(format string, args ...any) {
			entries = append(entries, fmt.Sprintf(format, args...))
		},
	}
	req := &Request{Jsonrpc: "2.0", Method: "server.list", ID: json.RawMessage(`9`)}
	_ = Dispatch(req, env, Actor{ID: "alice@example.com", IsAdmin: true})
	if len(entries) != 2 {
		t.Fatalf("expected request+response audit logs, got %d: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], `actor="alice@example.com"`) || !strings.Contains(entries[0], "method=server.list") {
		t.Fatalf("request audit log: %q", entries[0])
	}
	if !strings.Contains(entries[1], `method=server.list`) {
		t.Fatalf("response audit log: %q", entries[1])
	}
}
