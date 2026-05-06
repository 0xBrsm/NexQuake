// Package access owns Nexus HTTP caller identity and authorization policy.
//
// It verifies OIDC JWTs, accepts the optional RCON shared secret for admin
// RPCs, resolves request callers into client identities, and keeps the
// in-process source blocklist consulted by HTTP entry points.
package access

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// JWTVerifier verifies OIDC JWTs and extracts their claims. It owns the
// go-oidc dependency so callers can work with plain claim maps.
type JWTVerifier struct {
	verifier   *oidc.IDTokenVerifier
	headerName string
}

// InitJWT reads AUTH_ISSUER, AUTH_AUDIENCE, and AUTH_JWT_HEADER to set up
// OIDC JWT verification and returns the verifier. Returns (nil, nil) when
// OIDC is not configured (both ISSUER and AUDIENCE must be set to enable
// it). The combined "Admin access enabled/disabled" summary is logged once
// by main after both InitJWT and InitAuth complete.
func InitJWT(ctx context.Context) (*JWTVerifier, error) {
	issuer := strings.TrimSpace(os.Getenv("AUTH_ISSUER"))
	audience := strings.TrimSpace(os.Getenv("AUTH_AUDIENCE"))
	if issuer == "" || audience == "" {
		return nil, nil
	}
	headerName := strings.TrimSpace(os.Getenv("AUTH_JWT_HEADER"))
	if headerName == "" {
		headerName = "Authorization"
	}
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &JWTVerifier{
		verifier:   provider.Verifier(&oidc.Config{ClientID: audience}),
		headerName: headerName,
	}, nil
}

// AuthorizationToken extracts the credential value for scheme from the
// standard Authorization header. Scheme match is case-insensitive (RFC 7235).
// Returns "" if the header is absent, empty, or carries a different scheme.
func AuthorizationToken(r *http.Request, scheme string) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	prefix := scheme + " "
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(prefix):])
}

// Parse verifies the JWT in r and returns the decoded claims, or nil if no
// valid token is present or OIDC is not configured.
func (j *JWTVerifier) Parse(r *http.Request) map[string]any {
	if j == nil {
		return nil
	}
	raw := r.Header.Get(j.headerName)
	if strings.EqualFold(j.headerName, "Authorization") {
		if token := AuthorizationToken(r, "Bearer"); token != "" {
			raw = token
		}
	}
	if raw == "" {
		return nil
	}
	token, err := j.verifier.Verify(r.Context(), raw)
	if err != nil {
		slog.Debug(fmt.Sprintf("JWT verification failed: %v", err))
		return nil
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		slog.Debug(fmt.Sprintf("Failed to extract JWT claims: %v", err))
		return nil
	}
	return claims
}

// Authorize reports whether the caller is permitted to use admin RPCs.
// RCON wins if rconToken matches the configured password; otherwise JWT
// claims are checked against the configured admin rules.
func (a *Auth) Authorize(claims map[string]any, rconToken string) bool {
	if a == nil {
		return false
	}
	if a.rconPassword != "" && rconToken != "" &&
		subtle.ConstantTimeCompare([]byte(rconToken), []byte(a.rconPassword)) == 1 {
		return true
	}
	if claims == nil {
		return false
	}
	if a.adminRules == nil {
		return true
	}
	return matchesAdminRules(claims, a.adminRules)
}

// Auth holds admin access control state: the optional RCON shared secret and
// the claim rules that determine whether a verified JWT grants admin access.
// Construct with [InitAuth]; a nil *Auth disables all admin access.
//
// adminRules has three meaningful states:
//   - nil       - AUTH_ADMIN_ID unset; any verified JWT grants admin.
//   - empty map - AUTH_ADMIN_ID set but parsed to no usable rules; deny all JWTs.
//   - populated - JWT claims must match at least one rule.
type Auth struct {
	rconPassword string
	adminRules   map[string][]string
}

// HasRconPassword reports whether AUTH_RCON_PASSWORD is configured. Used by
// main to compose the combined admin-access summary line at boot.
func (a *Auth) HasRconPassword() bool {
	return a != nil && a.rconPassword != ""
}

// InitAuth reads AUTH_RCON_PASSWORD and AUTH_ADMIN_ID and returns a
// ready-to-use [Auth]. Logs the AUTH_ADMIN_ID interpretation at debug level;
// the user-facing "Admin access enabled/disabled" summary is logged once by
// main after both InitJWT and InitAuth complete.
func InitAuth() *Auth {
	auth := &Auth{
		rconPassword: strings.TrimSpace(os.Getenv("AUTH_RCON_PASSWORD")),
		adminRules:   parseAdminRules(os.Getenv("AUTH_ADMIN_ID")),
	}
	switch {
	case auth.adminRules == nil:
		slog.Debug("IdP admin mode: AUTH_ADMIN_ID not set; any valid JWT grants admin")
	case len(auth.adminRules) == 0:
		slog.Debug("IdP admin mode: AUTH_ADMIN_ID has no valid claim matchers; JWTs will not grant admin")
	}
	return auth
}

// parseAdminRules splits AUTH_ADMIN_ID into a map of claim key -> allowed
// values. Keys and values are lowercased so matching is case-insensitive
// throughout: emails, group names, role names, and any custom claim.
func parseAdminRules(raw string) map[string][]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	out := make(map[string][]string)
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		key, value, ok := strings.Cut(token, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.ToLower(strings.TrimSpace(value))
		if key == "" || value == "" {
			continue
		}
		out[key] = append(out[key], value)
	}
	return out
}

func matchesAdminRules(claims map[string]any, rules map[string][]string) bool {
	for key, values := range rules {
		claimVal := claims[key]
		for _, value := range values {
			if containsClaimValue(claimVal, value) {
				return true
			}
		}
	}
	return false
}

func containsClaimValue(val any, target string) bool {
	if val == nil || target == "" {
		return false
	}
	switch v := val.(type) {
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.EqualFold(s, target) {
				return true
			}
		}
	case string:
		return strings.EqualFold(v, target)
	}
	return false
}
