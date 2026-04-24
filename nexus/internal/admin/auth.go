// Package admin implements the Nexus admin subsystem: authentication,
// JSON-RPC envelope + dispatch, and admin command handlers.
//
// The entry points for callers are:
//   - [InitAuth] — construct an [Auth] from environment variables once at startup.
//   - [ParseRequest] / [Dispatch] — JSON-RPC envelope + method dispatch.
//   - [Env] — dependency-injection struct wiring the admin layer to the server
//     manager and session registry.
//
// Authentication supports two modes, which can be combined:
//
//	AUTH_ISSUER + AUTH_AUDIENCE   OIDC/JWT via an external identity provider.
//	AUTH_RCON_PASSWORD            Shared-secret rcon password, carried in the
//	                              standard Authorization header as
//	                              "Authorization: Rcon <password>".
//
// User identity (source IP resolution for all clients, admin or not) is a
// separate concern handled by [Identity] in id.go.
package admin

import (
	"context"
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Actor identifies the caller of an admin RPC request. Populated by
// [Auth.Authorize] from the HTTP request's credentials and the resolved source
// IP. ID is an audit-safe label (email, source IP, or "admin").
type Actor struct {
	ID       string
	SourceIP string
	IsAdmin  bool
}

// ActorID computes a stable actor label for audit logs from transport-provided
// bits. Prefers identity (unless empty or "anonymous"), then sourceIP, then
// nqip, then "admin". Pass nqip as "" when not applicable (HTTP).
func ActorID(identity, sourceIP, nqip string) string {
	trimmed := strings.TrimSpace(identity)
	if trimmed != "" && !strings.EqualFold(trimmed, "anonymous") {
		return trimmed
	}
	if ip := strings.TrimSpace(sourceIP); ip != "" {
		return ip
	}
	if ip := strings.TrimSpace(nqip); ip != "" {
		return ip
	}
	if trimmed != "" {
		return trimmed
	}
	return "admin"
}

// authorizationToken extracts the credential value for scheme from the
// standard Authorization header. Scheme match is case-insensitive (RFC 7235).
// Returns "" if the header is absent, empty, or carries a different scheme.
func authorizationToken(r *http.Request, scheme string) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	prefix := scheme + " "
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(prefix):])
}

// Authorize builds an [Actor] from the request. Tries OIDC JWT first
// (via [Auth.IdentifyRequest]); falls back to "Authorization: Rcon <password>".
// passwordMatched is true iff authorization succeeded via the Rcon scheme,
// letting callers label matching sessions for display/ban purposes.
func (a *Auth) Authorize(r *http.Request, sourceIP string) (actor Actor, passwordMatched bool) {
	isAdmin, identity := a.IdentifyRequest(r)
	actor = Actor{
		ID:       ActorID(identity, sourceIP, ""),
		SourceIP: sourceIP,
		IsAdmin:  isAdmin,
	}
	if actor.IsAdmin || a == nil || a.rconPassword == "" {
		return actor, false
	}
	supplied := authorizationToken(r, "Rcon")
	if supplied == "" {
		return actor, false
	}
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(a.rconPassword)) != 1 {
		return actor, false
	}
	actor.IsAdmin = true
	return actor, true
}

const adminMatchEmail = "email"

// Auth holds authentication state for admin access control.
// Construct with [InitAuth]; a nil *Auth disables all admin access.
type Auth struct {
	rconPassword string
	validator    *oidcValidator
	debugf       func(string, ...any)
}

type oidcValidator struct {
	verifier      *oidc.IDTokenVerifier
	headerName    string
	adminMatchers map[string][]string
	allowAnyJWT   bool
}

// InitAuth reads auth configuration from environment variables and returns a
// ready-to-use [Auth]. It contacts the OIDC provider (if configured) during
// construction so that JWT verification is fast at request time.
//
// Relevant environment variables:
//
//	AUTH_RCON_PASSWORD   shared rcon secret; empty disables rcon auth
//	AUTH_ISSUER          OIDC issuer URL; both ISSUER and AUDIENCE must be set
//	AUTH_AUDIENCE        OIDC client ID / audience claim
//	AUTH_JWT_HEADER      HTTP header carrying the token (default: Authorization)
//	AUTH_ADMIN_ID        comma-separated claim matchers, e.g. "email:x@example.com,group:admin"
//	                     empty means any valid JWT grants admin
//
// infof and debugf receive human-readable startup messages; nil is accepted for either.
func InitAuth(ctx context.Context, infof, debugf func(string, ...any)) (*Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if infof == nil {
		infof = func(string, ...any) {}
	}
	if debugf == nil {
		debugf = func(string, ...any) {}
	}

	rconPassword := strings.TrimSpace(os.Getenv("AUTH_RCON_PASSWORD"))

	issuer := strings.TrimSpace(os.Getenv("AUTH_ISSUER"))
	audience := strings.TrimSpace(os.Getenv("AUTH_AUDIENCE"))
	headerName := strings.TrimSpace(os.Getenv("AUTH_JWT_HEADER"))
	rawAdminMatcherConfig := os.Getenv("AUTH_ADMIN_ID")
	adminMatchers := parseAdminMatchers(rawAdminMatcherConfig)
	allowAnyJWT := strings.TrimSpace(rawAdminMatcherConfig) == ""

	auth := &Auth{
		rconPassword: rconPassword,
		debugf:       debugf,
	}

	if issuer != "" && audience != "" {
		if headerName == "" {
			headerName = "Authorization"
		}
		provider, err := oidc.NewProvider(ctx, issuer)
		if err != nil {
			return nil, err
		}
		auth.validator = &oidcValidator{
			verifier:      provider.Verifier(&oidc.Config{ClientID: audience}),
			headerName:    headerName,
			adminMatchers: adminMatchers,
			allowAnyJWT:   allowAnyJWT,
		}
		if allowAnyJWT {
			debugf("IdP admin mode: AUTH_ADMIN_ID not set; any valid JWT grants admin")
		} else if len(adminMatchers) == 0 {
			debugf("IdP admin mode: AUTH_ADMIN_ID has no valid claim matchers; JWTs will not grant admin")
		}
	}

	var methods []string
	if auth.validator != nil {
		methods = append(methods, "IdP")
	}
	if rconPassword != "" {
		methods = append(methods, "rcon_password")
	}
	if len(methods) == 0 {
		infof("Admin access disabled")
	} else {
		infof("Admin access enabled: %s", strings.Join(methods, ", "))
	}

	return auth, nil
}

// IdentifyRequest inspects the request for credentials and returns
// (isAdmin, identity) where identity is an email, username, or "anonymous".
// Only OIDC JWTs are checked here; rcon_password authentication happens
// per-request in [Authorize].
func (a *Auth) IdentifyRequest(r *http.Request) (bool, string) {
	if a == nil {
		return false, "anonymous"
	}

	// OIDC JWT.
	if a.validator != nil {
		isAdmin, claims := a.validator.validate(r, a.debugf)
		if claims != nil {
			// Identifier = email ?? preferred_username ?? name ?? sub
			for _, key := range []string{"email", "preferred_username", "name", "sub"} {
				if val, ok := claims[key].(string); ok && val != "" {
					return isAdmin, val
				}
			}
			return isAdmin, "oidc-user"
		}
	}

	return false, "anonymous"
}

// isAdmin checks whether the HTTP request carries valid admin credentials.
func (a *Auth) isAdmin(r *http.Request) bool {
	ok, _ := a.IdentifyRequest(r)
	return ok
}

func (v *oidcValidator) validate(r *http.Request, debugf func(string, ...any)) (bool, map[string]any) {
	raw := r.Header.Get(v.headerName)
	if strings.EqualFold(v.headerName, "Authorization") {
		if token := authorizationToken(r, "Bearer"); token != "" {
			raw = token
		}
	}
	if raw == "" {
		return false, nil
	}

	token, err := v.verifier.Verify(r.Context(), raw)
	if err != nil {
		debugf("JWT verification failed: %v", err)
		return false, nil
	}

	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		debugf("Failed to extract JWT claims: %v", err)
		return false, nil
	}

	return v.isAdminClaims(claims), claims
}

func (v *oidcValidator) isAdminClaims(claims map[string]any) bool {
	if v.allowAnyJWT {
		return true
	}
	return matchesAdminMatchers(claims, v.adminMatchers)
}

func parseAdminMatchers(raw string) map[string][]string {
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
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}

		if strings.EqualFold(key, adminMatchEmail) {
			key = adminMatchEmail
			value = strings.ToLower(value)
		}
		out[key] = append(out[key], value)
	}
	return out
}

func matchesAdminMatchers(claims map[string]any, matchers map[string][]string) bool {
	for key, values := range matchers {
		if strings.EqualFold(key, adminMatchEmail) {
			if email, ok := claims["email"].(string); ok {
				email = strings.ToLower(strings.TrimSpace(email))
				for _, value := range values {
					if email == value {
						return true
					}
				}
			}
			continue
		}

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
			if s, ok := item.(string); ok && s == target {
				return true
			}
		}
	case string:
		return v == target
	}
	return false
}
