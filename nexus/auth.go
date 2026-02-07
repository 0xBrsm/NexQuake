package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	adminMatchEmail = "email"
)

var (
	globalValidator *OIDCValidator
	rconPassword    string // AUTH_RCON_PASSWORD, token is base64(password)
)

type OIDCValidator struct {
	verifier      *oidc.IDTokenVerifier
	headerName    string
	adminMatchers map[string][]string
}

func initAuth() error {
	rconPassword = strings.TrimSpace(os.Getenv("AUTH_RCON_PASSWORD"))

	issuer := strings.TrimSpace(os.Getenv("AUTH_ISSUER"))
	audience := strings.TrimSpace(os.Getenv("AUTH_AUDIENCE"))
	headerName := strings.TrimSpace(os.Getenv("AUTH_JWT_HEADER"))
	adminMatchers := parseAdminMatchers(os.Getenv("AUTH_ADMIN_ID"))

	if issuer != "" && audience != "" {
		if headerName == "" {
			headerName = "Authorization"
		}
		provider, err := oidc.NewProvider(context.Background(), issuer)
		if err != nil {
			return err
		}
		globalValidator = &OIDCValidator{
			verifier:      provider.Verifier(&oidc.Config{ClientID: audience}),
			headerName:    headerName,
			adminMatchers: adminMatchers,
		}
	}

	var methods []string
	if rconPassword != "" {
		methods = append(methods, "rcon_password")
	}
	if globalValidator != nil {
		if len(adminMatchers) > 0 {
			methods = append(methods, "IdP (admin_id)")
		} else {
			methods = append(methods, "IdP")
		}
	}
	if len(methods) == 0 {
		infof("Admin access disabled")
	} else {
		infof("Admin access enabled via: %s", strings.Join(methods, ", "))
	}

	return nil
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

func IsAdmin(r *http.Request) bool {
	k := requestKey(r)

	// Static shared secret token.
	if rconPassword != "" && k != "" {
		expected := deriveRconTokenFromPassword(rconPassword)
		if subtle.ConstantTimeCompare([]byte(k), []byte(expected)) == 1 {
			return true
		}
	}

	// OIDC JWT.
	if globalValidator != nil {
		return globalValidator.check(r)
	}
	return false
}

func deriveRconTokenFromPassword(password string) string {
	return base64.StdEncoding.EncodeToString([]byte(password))
}

func (v *OIDCValidator) check(r *http.Request) bool {
	raw := r.Header.Get(v.headerName)
	if strings.EqualFold(v.headerName, "Authorization") && len(raw) > 7 && strings.EqualFold(raw[:7], "Bearer ") {
		raw = strings.TrimSpace(raw[7:])
	}
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if raw == "" {
		return false
	}

	token, err := v.verifier.Verify(r.Context(), raw)
	if err != nil {
		debugf("JWT verification failed: %v", err)
		return false
	}

	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		debugf("Failed to extract JWT claims: %v", err)
		return false
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
			email, ok := claims["email"].(string)
			if ok {
				email = strings.ToLower(strings.TrimSpace(email))
				for _, value := range values {
					if email == strings.ToLower(strings.TrimSpace(value)) {
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
			s, ok := item.(string)
			if ok && s == target {
				return true
			}
		}
	case []string:
		for _, s := range v {
			if s == target {
				return true
			}
		}
	case string:
		return v == target
	}
	return false
}
