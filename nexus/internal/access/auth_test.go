package access

import (
	"testing"
)

func TestAuthorize_DeniesWithoutCredentials(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	if auth.Authorize(nil, "") {
		t.Fatalf("expected unauthorized with no credentials")
	}
}

func TestAuthorize_NilAuthDenies(t *testing.T) {
	var auth *Auth
	if auth.Authorize(map[string]any{"email": "admin@example.com"}, "secret") {
		t.Fatalf("expected nil auth to deny access")
	}
}

func TestAuthorize_AdmitsMatchingPassword(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	if !auth.Authorize(nil, "secret") {
		t.Fatalf("expected authorization with matching password")
	}
}

func TestAuthorize_RejectsWrongPassword(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	if auth.Authorize(nil, "nope") {
		t.Fatalf("expected denial for wrong password")
	}
}

func TestAuthorize_RejectsEmptyToken(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	if auth.Authorize(nil, "") {
		t.Fatal("expected denial for empty token")
	}
}

func TestAuthorize_RconPasswordNotValidAsBearer(t *testing.T) {
	a := &Auth{rconPassword: "testpw"}

	// Rcon token extracted from Bearer header is "" — not the password string
	if a.Authorize(nil, "") {
		t.Fatalf("expected Authorize=false when no Rcon token is present")
	}
}

func TestParseAdminRules(t *testing.T) {
	got := parseAdminRules("group:admin, role:ops, email:Steve@OpenAI.com, bad, custom:x, email:steve@openai.com")

	want := map[string][]string{
		"group":  {"admin"},
		"role":   {"ops"},
		"custom": {"x"},
		"email":  {"steve@openai.com", "steve@openai.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseAdminRules() len = %d, want %d (%#v)", len(got), len(want), got)
	}
	for k, expected := range want {
		gotVals, ok := got[k]
		if !ok {
			t.Fatalf("parseAdminRules() missing key %q in %#v", k, got)
		}
		if len(gotVals) != len(expected) {
			t.Fatalf("parseAdminRules() key %q len=%d want %d (%#v)", k, len(gotVals), len(expected), gotVals)
		}
		for i := range expected {
			if gotVals[i] != expected[i] {
				t.Fatalf("parseAdminRules() key %q value[%d]=%q want %q", k, i, gotVals[i], expected[i])
			}
		}
	}
}

func TestMatchesAdminRules(t *testing.T) {
	claims := map[string]any{
		"email":  "steve@openai.com",
		"groups": []any{"players", "admin"},
		"roles":  []any{"viewer", "ops"},
	}

	if !matchesAdminRules(claims, map[string][]string{"email": {"steve@openai.com"}}) {
		t.Fatalf("expected email rule to pass")
	}
	if !matchesAdminRules(claims, map[string][]string{"groups": {"admin"}}) {
		t.Fatalf("expected group rule to pass")
	}
	if !matchesAdminRules(claims, map[string][]string{"roles": {"ops"}}) {
		t.Fatalf("expected role rule to pass")
	}
	if matchesAdminRules(claims, map[string][]string{"groups": {"nope"}}) {
		t.Fatalf("expected non-matching group to fail")
	}
}

func TestAuthorize_AdminClaimsCheck(t *testing.T) {
	claims := map[string]any{
		"email":  "steve@openai.com",
		"groups": []any{"players", "admin"},
	}

	// nil rules (AUTH_ADMIN_ID unset): any verified JWT grants admin
	anyJWT := &Auth{adminRules: nil}
	if !anyJWT.Authorize(claims, "") {
		t.Fatal("expected nil adminRules to grant admin for any verified JWT")
	}

	// rule-based: only matching claims grant admin
	ruleBased := &Auth{adminRules: map[string][]string{"groups": {"admin"}}}
	if !ruleBased.Authorize(claims, "") {
		t.Fatal("expected rule-based admin match to pass")
	}

	// empty (non-nil) rules: AUTH_ADMIN_ID set but unparseable, deny
	emptyRules := &Auth{adminRules: map[string][]string{}}
	if emptyRules.Authorize(claims, "") {
		t.Fatal("expected empty configured rule set to deny admin")
	}
}
