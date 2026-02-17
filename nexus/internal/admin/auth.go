package admin

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

const adminMatchEmail = "email"

// Auth holds authentication state for admin access control.
type Auth struct {
	rconPassword string
	validator    *oidcValidator
	debugf       func(string, ...any)
}

type oidcValidator struct {
	verifier      *oidc.IDTokenVerifier
	headerName    string
	adminMatchers map[string][]string
}

// InitAuth initializes admin authentication from environment variables and
// returns the Auth configuration. infof and debugf are used for log output.
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
	adminMatchers := parseAdminMatchers(os.Getenv("AUTH_ADMIN_ID"))

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

func (a *Auth) rconPasswordValue() string {
	if a == nil {
		return ""
	}
	return a.rconPassword
}

// IdentifyRequest checks the request for credentials and returns (isAdmin, identityString).
func (a *Auth) IdentifyRequest(r *http.Request) (bool, string) {
	if a == nil {
		return false, "anonymous"
	}
	k := requestKey(r)

	// Static shared secret token.
	if a.rconPassword != "" && k != "" {
		expected := deriveRconTokenFromPassword(a.rconPassword)
		if subtle.ConstantTimeCompare([]byte(k), []byte(expected)) == 1 {
			return true, "rcon"
		}
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

// IsAdmin checks whether the HTTP request carries valid admin credentials.
func (a *Auth) IsAdmin(r *http.Request) bool {
	ok, _ := a.IdentifyRequest(r)
	return ok
}

// requestKey extracts a credential from ?token= query param or Authorization header.
func requestKey(r *http.Request) string {
	if k := strings.TrimSpace(r.URL.Query().Get("token")); k != "" {
		return k
	}
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(h)
}

// deriveRconTokenFromPassword derives the connection token from the rcon password.
func deriveRconTokenFromPassword(password string) string {
	return base64.StdEncoding.EncodeToString([]byte(password))
}

func (v *oidcValidator) validate(r *http.Request, debugf func(string, ...any)) (bool, map[string]any) {
	raw := r.Header.Get(v.headerName)
	if strings.EqualFold(v.headerName, "Authorization") && len(raw) > 7 && strings.EqualFold(raw[:7], "Bearer ") {
		raw = strings.TrimSpace(raw[7:])
	}
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("token"))
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

	return matchesAdminMatchers(claims, v.adminMatchers), claims
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
