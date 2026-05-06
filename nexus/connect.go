package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

// headerNexQuakeRef is the response header carrying the manifest session ref
// returned by /start. The WASM client echoes it back when fetching assets so
// expired sessions can be detected.
const headerNexQuakeRef = "X-NexQuake-Ref"

// Register MIME types for static-asset extensions http.FileServer's default
// table doesn't cover (or that older Go versions sniff incorrectly). Registering
// here makes mime.TypeByExtension authoritative for the file server, dropping
// the need for a per-request Content-Type middleware.
func init() {
	_ = mime.AddExtensionType(".wasm", "application/wasm")
	_ = mime.AddExtensionType(".data", "application/octet-stream")
	_ = mime.AddExtensionType(".pak", "application/octet-stream")
}

// rconLoginLandingHTML is served at GET /rcon. The in-game rcon shell opens
// it as a popup when fetch can't survive a CF-Access-style cross-origin
// redirect; reaching this handler proves the edge gate let the request
// through. The page just closes itself — the shell discovers success by
// polling POST /rcon, not by listening for a signal from the popup. That
// avoids fighting COOP-same-origin (set on the WASM page for
// SharedArrayBuffer), which severs cross-window messaging the moment the
// popup navigates to the IdP.
const rconLoginLandingHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>rcon</title></head>
<body><script>window.close()</script>
<p>Authenticated. You can close this window.</p>
</body></html>
`

// rconMaxBody caps the size of a POST /rcon body. Admin JSON-RPC envelopes
// are tiny — method string + a handful of params — so 8 KiB is generous.
const rconMaxBody = 8 << 10

// startManifestBundle is the JSON envelope served at /start. The asset server
// owns the game/cd entries; client config and any transport-routing fields
// are assembled here from runtimeConfig and registered transport providers.
type startManifestBundle struct {
	Client map[string]any                         `json:"client"`
	Game   map[string][]assets.StartManifestEntry `json:"game"`
	CD     []assets.StartManifestEntry            `json:"cd,omitempty"`
}

// AddBootstrapClientFields registers a callback that contributes fields to the
// "client" object in /start responses. Called per request so transport-specific
// live values propagate within one fetch. Transports use this to add their
// routing info without the asset server knowing about HTTP layering.
func (app *nexusApp) AddBootstrapClientFields(fn func() map[string]any) {
	app.bootstrapClientFields = append(app.bootstrapClientFields, fn)
}

// handleStart issues a fresh per-session asset manifest, merges static and
// transport-contributed client config, and returns the base64-encoded JSON
// envelope. The X-NexQuake-Ref header carries the session ref clients echo
// back when fetching assets.
func (app *nexusApp) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	game, cd, ref, err := app.assetServer.IssueManifest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(game) == 0 {
		http.NotFound(w, r)
		return
	}

	client := map[string]any{
		"prefetchConcurrency": app.cfg.vfsPrefetchConcurrency,
		"smenuOnFirstLoad":    app.cfg.clientAutoSMenu,
		"urlArgs":             app.cfg.clientURLArgs,
	}
	if len(app.cfg.clientSendArgs) > 0 {
		client["sendArgs"] = app.cfg.clientSendArgs
	}
	for _, p := range app.bootstrapClientFields {
		for k, v := range p() {
			client[k] = v
		}
	}

	bundle, err := json.Marshal(startManifestBundle{Client: client, Game: game, CD: cd})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set(headerNexQuakeRef, ref)
	_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(bundle))
}

// trunkSession runs the per-request bookkeeping that's identical across
// tunnel transports: identity resolution, connect/disconnect logging, and
// bridging the transport-specific upgrade into trunk.NewConn via the supplied
// closure. trunk's Registry tracks the connection's session metadata for /rcon
// session views; admin promotion is purely a per-/rcon-request concept and is
// not stored on the session.
func (app *nexusApp) trunkSession(
	w http.ResponseWriter, r *http.Request,
	transportName string,
	upgrade func() (trunk.Transport, error),
) {
	req := app.access.Request(r)
	client := req.Client

	transport, err := upgrade()
	if err != nil {
		slog.Warn(fmt.Sprintf("WebSocket upgrade failed: %v", err))
		return
	}

	displayAddr := client.SourceIP
	if displayAddr == "" {
		displayAddr = r.RemoteAddr
	} else if _, port, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		displayAddr = net.JoinHostPort(displayAddr, port)
	}

	slog.Info(fmt.Sprintf("Client connected (ws): %s (%s)", displayAddr, client.ID))

	tc, err := app.trunk.NewSession(transport, client.SourceIP)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to create trunk session: %v", err))
		_ = transport.Close()
		return
	}

	// NQIP identity announcement: the WASM client (net_nqchan.c) reads
	// "NQIP" + 4-byte VirtualIP off the control channel on connect to
	// learn the address Quake's protocol embeds in CCREQ_CONNECT.
	vip := tc.VirtualIP()
	_ = tc.SendControl(append([]byte("NQIP"), vip[:]...))

	detachClient := app.clients.Attach(tc, client)
	defer detachClient()

	tc.Run()

	slog.Info(fmt.Sprintf("Client disconnected (ws): %s", displayAddr))
}

// handleControlFrame wires the trunk's port-0 control channel: slist
// requests only. Other port-0 payloads are silently dropped; admin rcon
// is served separately by POST /rcon.
func (app *nexusApp) handleControlFrame(s *trunk.Session, payload []byte) {
	if orch.IsSlistRequest(payload) {
		_ = s.SendControl(app.serverMgr.BuildSlistResponse())
	}
}

// notifyRconLoginComplete pushes a console echo down the trunk control
// channel to every active session sharing r's source IP, so admins see
// "rcon: authenticated" in their game console the moment GET /rcon fires
// (i.e. the moment an edge OIDC gate lets them through).
//
// Gated behind AuthorizeAdmin so an unauthenticated GET /rcon hit can't be
// used as an amplification primitive to spam echoes at a chosen IP — only
// requests Nexus would actually authorize as admin trigger the push. In
// the typical CF Access deployment this means "Nexus validated the JWT
// the edge forwarded"; absent any admin auth, the popup signal is silent.
//
// Source-IP keying is intentionally loose: multiple tabs from one IP all
// see the echo (harmless), and behind a NAT it could reach a sibling
// session (also harmless — the echo is a UI signal only).
func (app *nexusApp) notifyRconLoginComplete(r *http.Request) {
	if app.clients == nil {
		return
	}
	req := app.access.Request(r)
	if !app.access.AuthorizeAdmin(req, r) {
		return
	}
	sourceIP := req.Client.SourceIP
	if sourceIP == "" {
		return
	}
	payload := admin.ClientCommandPayload(`echo "rcon: authenticated."`)
	for _, conn := range app.clients.List() {
		if conn.SourceIP == sourceIP {
			conn.PushControl(payload)
		}
	}
}

// handleRcon is the POST /rcon JSON-RPC handler. It reads the envelope,
// authorizes the request via the Authorization header (Bearer for OIDC JWT,
// Rcon for the shared-secret password) and dispatches to the admin command
// registry. Authorization is per-request — there is no persistent admin flag
// on the connection.
func (app *nexusApp) handleRcon(w http.ResponseWriter, r *http.Request) {
	reqCtx := app.access.Request(r)
	client := reqCtx.Client

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, rconMaxBody))
	if err != nil {
		writeRPC(w, nil, admin.ErrCodeInvalidReq, "request body too large or unreadable")
		return
	}

	req, errResp := admin.ParseRequest(body)
	if errResp != nil {
		writeResponse(w, errResp)
		return
	}

	if !app.access.AuthorizeAdmin(reqCtx, r) {
		app.admin.AuditUnauthorized(client, req.Method)
		writeResponse(w, &admin.Response{
			Jsonrpc: "2.0",
			Error: &admin.RPCError{
				Code:    admin.ErrCodeUnauthorized,
				Message: "unauthorized",
				Data:    map[string]any{"hint": app.access.UnauthorizedHint()},
			},
			ID: req.ID,
		})
		return
	}

	resp := app.admin.Dispatch(req, client)
	writeResponse(w, resp)
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	writeResponse(w, &admin.Response{
		Jsonrpc: "2.0",
		Error:   &admin.RPCError{Code: code, Message: message},
		ID:      id,
	})
}

func writeResponse(w http.ResponseWriter, resp *admin.Response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// newMux builds the transport-agnostic HTTP router. Transport setup functions
// mount their implementation on the shared /connect route. The caller wraps with
// access.HTTPGate at server-construction time so blocklisted sources are
// rejected before any route-specific handler runs.
//
// Routes mounted here:
//   - GET /health           — liveness probe, returns version headers
//   - GET /rcon             — auto-closing landing page for OIDC popup login
//   - POST /rcon            — admin JSON-RPC endpoint
//   - /start                — client bootstrap manifest
//   - /nq/                  — hashed game assets (pak files, etc.)
//   - /                     — WASM client static files
func (app *nexusApp) newMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		v := currentVersionInfo()
		w.Header().Set("X-NexQuake-Nexus-GitSHA", v.GitSHA)
		w.Header().Set("X-NexQuake-Nexus-BuildTime", v.BuildTime)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// GET /rcon exists so the in-game rcon shell can drive an OIDC login flow
	// via a top-level popup when fetch hits a CF Access (or similar) cross-
	// origin redirect. Reaching this handler proves auth succeeded. The body
	// auto-closes the popup, and the server pushes a console echo to any
	// trunk session at the request's source IP so the admin sees
	// "rcon: authenticated" appear in their game console without needing
	// the popup-tracking machinery COOP-same-origin breaks.
	mux.HandleFunc("GET /rcon", func(w http.ResponseWriter, r *http.Request) {
		app.notifyRconLoginComplete(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, rconLoginLandingHTML)
	})

	mux.HandleFunc("POST /rcon", app.handleRcon)

	mux.Handle("/start", addIsolationHeaders(http.HandlerFunc(app.handleStart)))
	mux.Handle("/nq/", addIsolationHeaders(app.assetServer.AssetHandler()))

	clientFS := http.FileServerFS(os.DirFS(app.cfg.clientDir))
	mux.Handle("/", addIsolationHeaders(cacheControlClient(clientFS)))

	return mux
}

// cacheControlClient sets Cache-Control headers for the WASM client static
// files. HTML, JS, WASM, and CSS are served with no-store to prevent stale
// client/server version mismatches. Skips setting headers if a reverse proxy
// has already set Cache-Control.
func cacheControlClient(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if w.Header().Get("Cache-Control") == "" {
			path := r.URL.Path
			isHTML := path == "/" || strings.HasSuffix(path, ".html")
			isAsset := strings.HasSuffix(path, ".js") ||
				strings.HasSuffix(path, ".wasm") ||
				strings.HasSuffix(path, ".css")
			if isHTML || isAsset {
				w.Header().Set("Cache-Control", "no-store")
			}
			if isAsset {
				// no-store (not no-cache) to avoid intermittent JS/WASM
				// mismatches observed across browsers and proxies.
				w.Header().Set("Pragma", "no-cache")
				w.Header().Set("Expires", "0")
			}
		}
		h.ServeHTTP(w, r)
	})
}

// addIsolationHeaders sets the COOP/COEP/CORP headers required for
// SharedArrayBuffer (used by WASM threading) on all responses from h.
func addIsolationHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}
