package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

const rconTokenPrefix = "NexQuake:rcon:v1:"

var (
	globalValidator *OIDCValidator
	rconToken       string // derived from AUTH_RCON_PASSWORD (base64url(sha256(prefix + password)))
)

type OIDCValidator struct {
	verifier    *oidc.IDTokenVerifier
	headerName  string
	groupsClaim string
	adminGroup  string
	adminEmails map[string]struct{}
}

func initAuth() error {
	rconPassword := strings.TrimSpace(os.Getenv("AUTH_RCON_PASSWORD"))
	if rconPassword != "" {
		rconToken = deriveRconToken(rconPassword)
	}

	issuer := strings.TrimSpace(os.Getenv("AUTH_ISSUER"))
	audience := strings.TrimSpace(os.Getenv("AUTH_AUDIENCE"))
	headerName := strings.TrimSpace(os.Getenv("AUTH_JWT_HEADER"))
	groupsClaim := strings.TrimSpace(os.Getenv("AUTH_GROUPS_CLAIM"))
	adminGroup := strings.TrimSpace(os.Getenv("AUTH_ADMIN_GROUP"))
	emailMap := make(map[string]struct{})
	if raw := os.Getenv("AUTH_ADMIN_EMAIL"); raw != "" {
		for _, e := range strings.Split(raw, ",") {
			if val := strings.TrimSpace(strings.ToLower(e)); val != "" {
				emailMap[val] = struct{}{}
			}
		}
	}

	// Set up OIDC if configured.
	if issuer != "" && audience != "" {
		if headerName == "" {
			headerName = "Authorization"
		}
		provider, err := oidc.NewProvider(context.Background(), issuer)
		if err != nil {
			return err
		}
		globalValidator = &OIDCValidator{
			verifier:    provider.Verifier(&oidc.Config{ClientID: audience}),
			headerName:  headerName,
			groupsClaim: groupsClaim,
			adminGroup:  adminGroup,
			adminEmails: emailMap,
		}
	}

	// Single summary line.
	var methods []string
	if rconToken != "" {
		methods = append(methods, "rcon_password")
	}
	if globalValidator != nil {
		var idpParts []string
		if len(emailMap) > 0 {
			idpParts = append(idpParts, "email")
		}
		if groupsClaim != "" && adminGroup != "" {
			idpParts = append(idpParts, "group")
		}
		methods = append(methods, "IdP("+strings.Join(idpParts, ", ")+")")
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
	if rconToken != "" && k != "" {
		if subtle.ConstantTimeCompare([]byte(k), []byte(rconToken)) == 1 {
			return true
		}
	}

	// OIDC JWT.
	if globalValidator != nil {
		return globalValidator.check(r)
	}
	return false
}

func deriveRconToken(password string) string {
	sum := sha256.Sum256([]byte(rconTokenPrefix + password))
	return base64.RawURLEncoding.EncodeToString(sum[:])
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

	if len(v.adminEmails) > 0 {
		if email, ok := claims["email"].(string); ok {
			if _, allowed := v.adminEmails[strings.ToLower(email)]; allowed {
				return true
			}
		}
	}

	if v.groupsClaim != "" && v.adminGroup != "" {
		return containsGroup(claims[v.groupsClaim], v.adminGroup)
	}

	return false
}

func containsGroup(val any, target string) bool {
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
