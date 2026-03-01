package main

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/0xBrsm/NexQuake/nexus/nqrelay"
)

// parseClientIP extracts an IP address from a raw header value or remote addr
// string, handling Forwarded/X-Forwarded-For formats and port stripping.
func parseClientIP(raw string) (netip.Addr, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return netip.Addr{}, false
	}

	if comma := strings.IndexByte(value, ','); comma >= 0 {
		value = strings.TrimSpace(value[:comma])
	}
	if k, v, ok := strings.Cut(value, "="); ok && strings.EqualFold(strings.TrimSpace(k), "for") {
		value = strings.TrimSpace(v)
	}
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	ip, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// resolveClientSourceIP returns the external client IP for a request.
// Preference order:
//  1. headerName header (if non-empty and parseable)
//  2. Remote address IP
func resolveClientSourceIPWithHeader(r *http.Request, headerName string) string {
	if r == nil {
		return ""
	}
	if headerName != "" {
		if ip, ok := parseClientIP(r.Header.Get(headerName)); ok {
			return ip.String()
		}
	}
	if ip, ok := parseClientIP(r.RemoteAddr); ok {
		return ip.String()
	}
	return ""
}

// resolveClientSourceIP reads AUTH_CLIENT_IP_HEADER from the environment on
// each call and delegates to resolveClientSourceIPWithHeader. Used by tests
// that set the env var per-subtest; production code should prefer the
// nexusApp methods that use the pre-loaded cfg.clientIPHeader.
func resolveClientSourceIP(r *http.Request) string {
	return resolveClientSourceIPWithHeader(r, strings.TrimSpace(os.Getenv("AUTH_CLIENT_IP_HEADER")))
}

// resolveClientSourceKey derives a stable identity key from an HTTP request
// using AUTH_CLIENT_IP_HEADER read from the environment.
// Production code should prefer the nexusApp method that uses cfg.clientIPHeader.
func resolveClientSourceKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	if sourceIP := resolveClientSourceIP(r); sourceIP != "" {
		return "ip:" + sourceIP
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// clientSourceIP returns the external client IP using the pre-loaded header name.
func (app *nexusApp) clientSourceIP(r *http.Request) string {
	return resolveClientSourceIPWithHeader(r, app.cfg.clientIPHeader)
}

// clientSourceKey derives a stable identity key using the pre-loaded header name.
func (app *nexusApp) clientSourceKey(r *http.Request) string {
	if sourceIP := app.clientSourceIP(r); sourceIP != "" {
		return "ip:" + sourceIP
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// handleWebSocket upgrades the HTTP connection to WebSocket and starts a relay
// session. Blocked clients are rejected before the upgrade. The relay runs to
// completion (i.e. until the client disconnects) before the handler returns.
func (app *nexusApp) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	isAdmin, userIdentity := app.auth.IdentifyRequest(r)
	sourceIP := app.clientSourceIP(r)
	sourceKey := app.clientSourceKey(r)
	if app.ipAlloc.IsBlocked(sourceKey) {
		warnf("Rejected blocked client source=%q remote=%s", sourceKey, r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	conn, err := nqrelay.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		warnf("WebSocket upgrade failed: %v", err)
		return
	}

	displayAddr := sourceIP
	if displayAddr == "" {
		displayAddr = conn.RemoteAddr().String()
	} else if _, port, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil {
		displayAddr = net.JoinHostPort(displayAddr, port)
	}

	if isAdmin {
		infof("Admin connected: %s (%s)", displayAddr, userIdentity)
	} else {
		infof("Client connected: %s (%s)", displayAddr, userIdentity)
	}

	dispatch := app.buildFrameDispatch(userIdentity)

	relay, err := nqrelay.NewRelay(conn, sourceKey, sourceIP, userIdentity, isAdmin, app.ipAlloc, app.sessionReg, dispatch, warnf, debugf)
	if err != nil {
		errorf("Failed to create relay: %v", err)
		_ = conn.Close()
		return
	}

	relay.Run()

	if isAdmin {
		infof("Admin disconnected: %s", displayAddr)
	} else {
		infof("Client disconnected: %s", displayAddr)
	}
}

// newMux builds the HTTP router for the nexus server.
// Routes:
//   - GET /health — liveness probe, returns version headers
//   - GET /ws     — WebSocket upgrade; all game traffic flows here
//   - /start      — client bootstrap page (game asset gateway)
//   - /nq/        — hashed game assets (pak files, etc.)
//   - /           — WASM client static files
func (app *nexusApp) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	assetGateway := assets.NewHashedAssetGateway(
		app.cfg.gameDir,
		app.cfg.cdDir,
		app.pakCache,
		app.cfg.vfsPrefetchConcurrency,
		app.cfg.clientAutoSMenu,
		app.cfg.clientSendArgs,
		app.cfg.clientURLArgs,
	)
	assetGateway.SetErrorf(errorf)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		v := currentVersionInfo()
		w.Header().Set("X-NexQuake-Nexus-GitSHA", v.GitSHA)
		w.Header().Set("X-NexQuake-Nexus-BuildTime", v.BuildTime)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	mux.HandleFunc("GET /ws", app.handleWebSocket)

	mux.Handle("/start", addIsolationHeaders(http.HandlerFunc(assetGateway.StartHandler())))
	mux.Handle("/nq/", addIsolationHeaders(http.HandlerFunc(assetGateway.AssetHandler())))

	clientFS := http.FileServerFS(os.DirFS(app.cfg.clientDir))
	mux.Handle("/", addIsolationHeaders(contentTypeOverride(cacheControlClient(clientFS))))

	return mux
}
