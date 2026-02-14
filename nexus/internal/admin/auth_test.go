package admin

import (
	"encoding/base64"
	"net/http/httptest"
	"testing"
)

func TestDeriveRconTokenFromPassword(t *testing.T) {
	pw := "secret"
	got := deriveRconTokenFromPassword(pw)
	want := base64.StdEncoding.EncodeToString([]byte(pw))
	if got != want {
		t.Fatalf("deriveRconTokenFromPassword() = %q, want %q", got, want)
	}
}

func TestIsAdminBase64PasswordToken(t *testing.T) {
	auth := &Auth{rconPassword: "testpw"}
	token := deriveRconTokenFromPassword(auth.rconPassword)

	reqA := httptest.NewRequest("GET", "http://quake-a.local:1337/ws?token="+token, nil)
	reqA.Host = "quake-a.local:1337"
	if !auth.IsAdmin(reqA) {
		t.Fatalf("expected IsAdmin() true for matching base64 token")
	}

	reqB := httptest.NewRequest("GET", "http://quake-b.local:1337/ws?token="+base64.StdEncoding.EncodeToString([]byte("wrong")), nil)
	reqB.Host = "quake-b.local:1337"
	if auth.IsAdmin(reqB) {
		t.Fatalf("expected IsAdmin() false for non-matching token")
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
