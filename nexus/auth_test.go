package main

import (
	"net/http/httptest"
	"testing"
)

func TestCanonicalRequestHost(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/ws", nil)
	req.Host = "Example.COM:1337"

	got := canonicalRequestHost(req)
	if got != "example.com:1337" {
		t.Fatalf("canonicalRequestHost() = %q, want %q", got, "example.com:1337")
	}
}

func TestDeriveRconTokenForHostDiffersByHost(t *testing.T) {
	pw := "secret"
	a := deriveRconTokenForHost(pw, "quake-a.local:1337")
	b := deriveRconTokenForHost(pw, "quake-b.local:1337")

	if a == b {
		t.Fatalf("expected host-bound token mismatch, got same token %q", a)
	}
}

func TestIsAdminHostBoundPasswordToken(t *testing.T) {
	oldPw := rconPassword
	oldValidator := globalValidator
	defer func() {
		rconPassword = oldPw
		globalValidator = oldValidator
	}()

	rconPassword = "testpw"
	globalValidator = nil

	hostA := "quake-a.local:1337"
	tokenA := deriveRconTokenForHost(rconPassword, hostA)

	reqA := httptest.NewRequest("GET", "http://"+hostA+"/ws?token="+tokenA, nil)
	reqA.Host = hostA
	if !IsAdmin(reqA) {
		t.Fatalf("expected IsAdmin() true for matching host-bound token")
	}

	reqB := httptest.NewRequest("GET", "http://quake-b.local:1337/ws?token="+tokenA, nil)
	reqB.Host = "quake-b.local:1337"
	if IsAdmin(reqB) {
		t.Fatalf("expected IsAdmin() false for host mismatch")
	}
}

func TestParseAdminMatchers(t *testing.T) {
	got := parseAdminMatchers("group:admin, role:ops, email:Steve@OpenAI.com, bad, custom:x, email:steve@openai.com")

	want := map[string][]string{
		"group":         {"admin"},
		"role":          {"ops"},
		"custom":        {"x"},
		adminMatchEmail: {"steve@openai.com", "steve@openai.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseAdminMatchers() len = %d, want %d (%#v)", len(got), len(want), got)
	}
	for k, expected := range want {
		gotVals, ok := got[k]
		if !ok {
			t.Fatalf("parseAdminMatchers() missing key %q in %#v", k, got)
		}
		if len(gotVals) != len(expected) {
			t.Fatalf("parseAdminMatchers() key %q len=%d want %d (%#v)", k, len(gotVals), len(expected), gotVals)
		}
		for i := range expected {
			if gotVals[i] != expected[i] {
				t.Fatalf("parseAdminMatchers() key %q value[%d]=%q want %q", k, i, gotVals[i], expected[i])
			}
		}
	}
}

func TestMatchesAdminMatchers(t *testing.T) {
	claims := map[string]any{
		"email":  "steve@openai.com",
		"groups": []any{"players", "admin"},
		"roles":  []string{"viewer", "ops"},
	}

	if !matchesAdminMatchers(claims, map[string][]string{adminMatchEmail: {"steve@openai.com"}}) {
		t.Fatalf("expected email matcher to pass")
	}
	if !matchesAdminMatchers(claims, map[string][]string{"groups": {"admin"}}) {
		t.Fatalf("expected group matcher to pass")
	}
	if !matchesAdminMatchers(claims, map[string][]string{"roles": {"ops"}}) {
		t.Fatalf("expected role matcher to pass")
	}
	if matchesAdminMatchers(claims, map[string][]string{"groups": {"nope"}}) {
		t.Fatalf("expected non-matching group to fail")
	}
}
