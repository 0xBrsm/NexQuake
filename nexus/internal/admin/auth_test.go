package admin

import (
	"net/http/httptest"
	"testing"
)

func TestIsAdminIgnoresRconPasswordQueryToken(t *testing.T) {
	auth := &Auth{rconPassword: "testpw"}

	reqA := httptest.NewRequest("GET", "http://quake-a.local:1337/ws?token=dGVzdHB3", nil)
	reqA.Host = "quake-a.local:1337"
	if auth.IsAdmin(reqA) {
		t.Fatalf("expected IsAdmin() false when only query token is provided")
	}

	reqB := httptest.NewRequest("GET", "http://quake-b.local:1337/ws", nil)
	reqB.Header.Set("Authorization", "Bearer dGVzdHB3")
	reqB.Host = "quake-b.local:1337"
	if auth.IsAdmin(reqB) {
		t.Fatalf("expected IsAdmin() false when only rcon_password-derived bearer token is provided")
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
		"roles":  []any{"viewer", "ops"},
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

func TestOIDCValidatorIsAdminClaims(t *testing.T) {
	claims := map[string]any{
		"email":  "steve@openai.com",
		"groups": []any{"players", "admin"},
	}

	allowAny := &oidcValidator{allowAnyJWT: true}
	if !allowAny.isAdminClaims(claims) {
		t.Fatalf("expected allowAnyJWT=true to grant admin")
	}

	matcherBased := &oidcValidator{
		adminMatchers: map[string][]string{"groups": {"admin"}},
	}
	if !matcherBased.isAdminClaims(claims) {
		t.Fatalf("expected matcher-based admin match to pass")
	}

	invalidConfiguredMatchers := &oidcValidator{
		allowAnyJWT:   false,
		adminMatchers: map[string][]string{},
	}
	if invalidConfiguredMatchers.isAdminClaims(claims) {
		t.Fatalf("expected empty configured matcher set to deny admin")
	}
}
