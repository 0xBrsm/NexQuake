package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

// rconMaxBody caps the size of a POST /rcon body. Admin JSON-RPC envelopes
// are tiny — method string + a handful of params — so 8 KiB is generous.
const rconMaxBody = 8 << 10

// handleWebSocket upgrades the HTTP connection to WebSocket and starts a relay
// session. Blocked clients are rejected before the upgrade. The relay runs to
// completion (i.e. until the client disconnects) before the handler returns.
func (app *nexusApp) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	isAdmin, userIdentity := app.auth.IdentifyRequest(r)
	sourceIP := app.id.ClientSourceIP(r)
	sourceKey := app.id.ClientSourceKey(r)
	if app.ipAlloc.IsBlocked(sourceKey) {
		warnf("Rejected blocked client source=%q remote=%s", sourceKey, r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	conn, err := trunk.Upgrader.Upgrade(w, r, nil)
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
		infof("Admin connected (ws): %s (%s)", displayAddr, userIdentity)
	} else {
		infof("Client connected (ws): %s (%s)", displayAddr, userIdentity)
	}

	session := app.sessionReg.Create(sourceIP, userIdentity, isAdmin)
	dispatch := app.buildFrameDispatch()

	tc, err := trunk.NewConn(trunk.WebSocket(conn), app.ipAlloc, sourceKey,
		trunk.WithDispatch(dispatch),
		trunk.WithLogger(warnf, debugf),
	)
	if err != nil {
		app.sessionReg.Remove(session)
		errorf("Failed to create trunk conn: %v", err)
		_ = conn.Close()
		return
	}

	app.sessionReg.AttachChannel(session, tc)
	defer app.sessionReg.Remove(session)

	tc.Run()

	if isAdmin {
		infof("Admin disconnected (ws): %s", displayAddr)
	} else {
		infof("Client disconnected (ws): %s", displayAddr)
	}
}

// handleRcon is the POST /rcon JSON-RPC handler. It reads the envelope,
// authorizes the request via the Authorization header (Bearer for OIDC JWT,
// Rcon for the shared-secret password), promotes matching WS sessions on
// successful password auth (for session-list display + ban protection), and
// dispatches to the admin command registry.
func (app *nexusApp) handleRcon(w http.ResponseWriter, r *http.Request) {
	sourceIP := app.id.ClientSourceIP(r)
	sourceKey := app.id.ClientSourceKey(r)
	if app.ipAlloc.IsBlocked(sourceKey) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, rconMaxBody))
	if err != nil {
		writeRPC(w, nil, admin.ErrCodeInvalidReq, "request body too large or unreadable")
		return
	}

	actor, passwordMatched := app.auth.Authorize(r, sourceIP)
	if passwordMatched {
		// Label matching sessions for session-list display and ban protection.
		// Does not grant privilege — every /rcon re-auths per-request.
		for _, s := range app.sessionReg.LookupBySourceIP(sourceIP) {
			if s.IsAdmin() {
				continue
			}
			s.PromoteAdmin()
			src := strings.TrimSpace(sourceIP)
			if src == "" {
				src = "unknown"
			}
			nqip := s.VirtualIP()
			if nqip == "" {
				nqip = "none"
			}
			infof("Admin promoted: source=%s key=%s nqip=%s", src, s.SourceKey(), nqip)
		}
	}

	req, errResp := admin.ParseRequest(body)
	if errResp != nil {
		writeResponse(w, errResp)
		return
	}

	if !actor.IsAdmin {
		writeRPC(w, req.ID, admin.ErrCodeUnauthorized, "unauthorized")
		return
	}

	resp := admin.Dispatch(req, app.adminEnv, actor)
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

// newMux builds the HTTP router for the nexus server.
// Routes:
//   - GET /health — liveness probe, returns version headers
//   - GET /ws     — WebSocket upgrade; game traffic flows here
//   - POST /rcon  — admin JSON-RPC endpoint
//   - /start      — client bootstrap page (asset server)
//   - /nq/        — hashed game assets (pak files, etc.)
//   - /           — WASM client static files
func (app *nexusApp) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	assetServer := assets.NewHashedAssetServer(
		app.cfg.gameDir,
		app.cfg.cdDir,
		app.pakCache,
		app.cfg.vfsPrefetchConcurrency,
		app.cfg.clientAutoSMenu,
		app.cfg.clientSendArgs,
		app.cfg.clientURLArgs,
	)
	assetServer.SetErrorf(errorf)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		v := currentVersionInfo()
		w.Header().Set("X-NexQuake-Nexus-GitSHA", v.GitSHA)
		w.Header().Set("X-NexQuake-Nexus-BuildTime", v.BuildTime)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	mux.HandleFunc("GET /ws", app.handleWebSocket)
	mux.HandleFunc("POST /rcon", app.handleRcon)

	mux.Handle("/start", addIsolationHeaders(http.HandlerFunc(assetServer.StartHandler())))
	mux.Handle("/nq/", addIsolationHeaders(http.HandlerFunc(assetServer.AssetHandler())))

	clientFS := http.FileServerFS(os.DirFS(app.cfg.clientDir))
	mux.Handle("/", addIsolationHeaders(contentTypeOverride(cacheControlClient(clientFS))))

	return mux
}
