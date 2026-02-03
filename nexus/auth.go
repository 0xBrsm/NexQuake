package main

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Global singleton to match the original usage pattern.
var globalValidator *OIDCValidator

type OIDCValidator struct {
	verifier    *oidc.IDTokenVerifier
	headerName  string
	groupsClaim string
	adminGroup  string
	adminEmails map[string]struct{}
}

// initAuth initializes the validator and is called from main.go at startup.
func initAuth() error {
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

	if issuer == "" && audience == "" {
		infof("Auth disabled (no issuer/audience configured)")
		return nil
	}

	// Default to Authorization header if not specified
	if headerName == "" {
		headerName = "Authorization"
	}

	provider, err := oidc.NewProvider(context.Background(), issuer)
	if err != nil {
		return err
	}

	infof("Auth enabled: oidc_issuer=%q jwt_header=%q admins=%d", issuer, headerName, len(emailMap))

	globalValidator = &OIDCValidator{
		verifier:    provider.Verifier(&oidc.Config{ClientID: audience}),
		headerName:  headerName,
		groupsClaim: groupsClaim,
		adminGroup:  adminGroup,
		adminEmails: emailMap,
	}

	return nil
}

// IsAdmin checks if the request is authorized.
func IsAdmin(r *http.Request) bool {
	if globalValidator == nil {
		return false
	}
	return globalValidator.check(r)
}

func (v *OIDCValidator) check(r *http.Request) bool {
	rawToken := r.Header.Get(v.headerName)

	// Handle standard Bearer casing if using standard header
	if strings.EqualFold(v.headerName, "Authorization") {
		if len(rawToken) > 7 && strings.EqualFold(rawToken[:7], "Bearer ") {
			rawToken = strings.TrimSpace(rawToken[7:])
		}
	}

	if rawToken == "" {
		return false
	}

	token, err := v.verifier.Verify(r.Context(), rawToken)
	if err != nil {
		debugf("JWT Verification Error: %v", err)
		return false
	}

	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		debugf("Failed to extract JWT claims: %v", err)
		return false
	}

	// Strategy 1: Email allowlist
	if len(v.adminEmails) > 0 {
		if email, ok := claims["email"].(string); ok {
			if _, allowed := v.adminEmails[strings.ToLower(email)]; allowed {
				return true
			}
		}
	}

	// Strategy 2: Group membership (if configured)
	if v.groupsClaim != "" && v.adminGroup != "" {
		return containsGroup(claims[v.groupsClaim], v.adminGroup)
	}

	return false
}

func containsGroup(claimValue any, targetGroup string) bool {
	if claimValue == nil || targetGroup == "" {
		return false
	}
	switch val := claimValue.(type) {
	case []interface{}:
		for _, item := range val {
			if s, ok := item.(string); ok && s == targetGroup {
				return true
			}
		}
	case []string:
		for _, s := range val {
			if s == targetGroup {
				return true
			}
		}
	case string:
		return val == targetGroup
	}
	return false
}
