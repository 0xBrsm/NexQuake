package access

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
)

// Request is the access-layer view of one HTTP caller.
type Request struct {
	Client Client
	Claims map[string]any
}

type requestContextKey struct{}

// Gate resolves request callers and answers common access-policy questions
// for HTTP endpoints.
type Gate struct {
	jwt              *JWTVerifier
	identity         *Identity
	auth             *Auth
	blocklist        *Blocklist
	unauthorizedHint string
}

// NewGate wires the dependencies used by Nexus HTTP access checks.
func NewGate(jwt *JWTVerifier, identity *Identity, auth *Auth, blocklist *Blocklist) *Gate {
	return &Gate{
		jwt:              jwt,
		identity:         identity,
		auth:             auth,
		blocklist:        blocklist,
		unauthorizedHint: buildUnauthorizedHint(auth, jwt),
	}
}

// Resolve identifies the caller and verified claims, if any, for r.
func (g *Gate) Resolve(r *http.Request) Request {
	var claims map[string]any
	if g != nil {
		claims = g.jwt.Parse(r)
	}
	var identity *Identity
	if g != nil {
		identity = g.identity
	}
	return Request{
		Client: identity.ResolveClient(r, claims),
		Claims: claims,
	}
}

// Request returns the access request cached on r by [Gate.HTTPGate], falling
// back to resolving directly for tests and call paths that bypass middleware.
func (g *Gate) Request(r *http.Request) Request {
	if req, ok := r.Context().Value(requestContextKey{}).(Request); ok {
		return req
	}
	return g.Resolve(r)
}

// HTTPGate resolves access once for every HTTP request and drops blocklisted
// sources before route handlers run.
func (g *Gate) HTTPGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := g.Resolve(r)
		if g.IsBlocked(req) {
			slog.Warn(fmt.Sprintf("Rejected blocked client source=%q remote=%s", req.Client.SourceIP, r.RemoteAddr))
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), requestContextKey{}, req)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Blocklist tracks source IPs barred from future HTTP entry points.
// It is in-memory and intentionally process-local: bans last until restart.
type Blocklist struct {
	mu      sync.RWMutex
	blocked map[string]struct{}
}

// NewBlocklist constructs an empty source-IP blocklist.
func NewBlocklist() *Blocklist {
	return &Blocklist{blocked: make(map[string]struct{})}
}

// Block adds sourceIP to the in-process blocklist. Empty source keys are
// ignored because they cannot be matched reliably on future requests.
func (b *Blocklist) Block(sourceIP string) {
	if sourceIP == "" {
		return
	}
	b.mu.Lock()
	b.blocked[sourceIP] = struct{}{}
	b.mu.Unlock()
}

// IsBlocked reports whether sourceIP is currently blocklisted.
func (b *Blocklist) IsBlocked(sourceIP string) bool {
	if b == nil || sourceIP == "" {
		return false
	}
	b.mu.RLock()
	_, blocked := b.blocked[sourceIP]
	b.mu.RUnlock()
	return blocked
}

// IsBlocked reports whether req's source is currently barred.
func (g *Gate) IsBlocked(req Request) bool {
	return g != nil && g.blocklist.IsBlocked(req.Client.SourceIP)
}

// AuthorizeAdmin reports whether r carries credentials that can use admin RPCs.
func (g *Gate) AuthorizeAdmin(req Request, r *http.Request) bool {
	return g != nil && g.auth.Authorize(req.Claims, AuthorizationToken(r, "Rcon"))
}

// UnauthorizedHint returns a one-line operator-facing instruction describing
// how to authenticate against this Nexus, derived from what's actually
// configured. Empty string if Gate is nil. Suitable for surfacing alongside a
// 401 unauthorized response so the caller knows what to do next.
//
// Snapshotted at NewGate time since auth config doesn't change at runtime;
// keeps Auth itself free of methods that exist purely for external readers.
func (g *Gate) UnauthorizedHint() string {
	if g == nil {
		return ""
	}
	return g.unauthorizedHint
}

func buildUnauthorizedHint(auth *Auth, jwt *JWTVerifier) string {
	password := auth != nil && auth.rconPassword != ""
	jwtConfigured := jwt != nil
	switch {
	case password && jwtConfigured:
		return "set rcon_password <secret> or authenticate via OIDC."
	case password:
		return "set rcon_password <secret>."
	case jwtConfigured:
		return "authenticate via OIDC."
	default:
		return "admin auth not configured (set AUTH_RCON_PASSWORD or AUTH_ISSUER/AUTH_AUDIENCE)."
	}
}

// Block adds sourceIP to the shared source blocklist.
func (g *Gate) Block(sourceIP string) {
	if g != nil && g.blocklist != nil {
		g.blocklist.Block(sourceIP)
	}
}
