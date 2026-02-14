package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/gamedata"
	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
)

func main() {
	initLogging()

	if handled, exitCode := handleCLI(os.Args[1:]); handled {
		os.Exit(exitCode)
	}

	if err := configureNexusLogFile(getEnv("LOGS_DIR", "/app/logs")); err != nil {
		fatalf("Failed to initialize Nexus log file: %v", err)
	}
	defer closeNexusLogFile()
	infof("== Welcome to NexQuake! ==")

	cfg := loadRuntimeConfig()

	// Initialize authentication (admin privilege system).
	authCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	auth, err := admin.InitAuth(authCtx, infof, debugf)
	cancel()
	if err != nil {
		fatalf("Failed to initialize auth: %v", err)
	}
	globalAuth = auth

	// Initialize networking layer.
	nqServerIP := parseNQServerIP(os.Getenv("NQSERVER_IP"))
	ipAlloc := nqnet.NewIPAllocator(nqServerIP)
	sessionReg := nqnet.NewSessionRegistry()
	globalIPAllocator = ipAlloc
	globalSessionRegistry = sessionReg

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	if err := prependPath(cfg.binDir); err != nil {
		fatalf("Failed to configure PATH: %v", err)
	}

	// Validate runtime artifacts early; avoids confusing partial startup.
	if !fileExists(filepath.Join(cfg.clientDir, "index.html")) {
		fatalf("WASM client not found: %s", cfg.clientDir)
	}

	if cfg.debugStartup {
		logArtifactFingerprints(cfg)
	}

	// Default QUICKSTART is "minimal"; missing manifest is a no-op.
	if err := gamedata.BootstrapGameData(runCtx, cfg.dataDir, infof); err != nil {
		fatalf("Game data bootstrap failed: %v", err)
	}

	// Start dedicated servers (one per mod directory).
	serverMgr := orch.NewServerManager(
		cfg.dataDir,
		cfg.logsDir,
		infof,
		infofNoTail,
		debugf,
		warnf,
		errorf,
		formatTimestampedLogText,
	)
	if err := serverMgr.StartAll(); err != nil {
		fatalf("Failed to start servers: %v", err)
	}
	globalServerManager = serverMgr
	globalAdminEnv = buildAdminEnv()

	// Start the nexus-managed server info poller (used for Quake's `slist`).
	serverInfoPoller := orch.NewServerInfoPoller(serverMgr, nqServerIP)
	if err := serverInfoPoller.Start(runCtx); err != nil {
		warnf("Server info poller disabled: %v", err)
		serverInfoPoller = nil
	}

	pakCache := gamedata.NewPakIndexCache()
	mux := newMux(cfg, pakCache, serverMgr)

	server := &http.Server{
		Addr:              ":" + cfg.httpPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		infof("Nexus listening on port %s", cfg.httpPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	infof("Shutting down gracefully...")
	runCancel()
	if serverInfoPoller != nil {
		serverInfoPoller.Stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		fatalf("Server shutdown failed: %v", err)
	}

	_ = serverMgr.StopAll(ctx, 2*time.Second)
	infof("Nexus stopped")
}

type runtimeConfig struct {
	httpPort               string
	dataDir                string
	logsDir                string
	binDir                 string
	clientDir              string
	corsOrigin             string
	debugStartup           bool
	vfsPrefetchConcurrency int
}

func loadRuntimeConfig() runtimeConfig {
	binDir := getEnv("BIN_DIR", "/app/bin")
	return runtimeConfig{
		httpPort:               getEnv("HTTP_PORT", "1337"),
		dataDir:                getEnv("DATA_DIR", "/app/data"),
		logsDir:                getEnv("LOGS_DIR", "/app/logs"),
		binDir:                 binDir,
		clientDir:              getEnv("CLIENT_DIR", filepath.Join(binDir, "nqwasm")),
		corsOrigin:             getEnv("CORS_ALLOWED_ORIGIN", ""),
		debugStartup:           getEnv("DEBUG_STARTUP", "") == "1",
		vfsPrefetchConcurrency: getEnvIntMin("CLIENT_BATCH_SIZE", 16, 1),
	}
}

func prependPath(binDir string) error {
	dir := strings.TrimSpace(binDir)
	if dir == "" {
		return nil
	}

	existing := os.Getenv("PATH")
	for _, part := range strings.Split(existing, string(os.PathListSeparator)) {
		if part == dir {
			return nil
		}
	}

	if existing == "" {
		return os.Setenv("PATH", dir)
	}
	return os.Setenv("PATH", dir+string(os.PathListSeparator)+existing)
}

func handleCLI(args []string) (handled bool, exitCode int) {
	if len(args) == 0 {
		return false, 0
	}

	switch args[0] {
	case "--version", "version":
		// Keep this simple so it’s usable inside minimal runtime images.
		v := currentVersionInfo()
		fmt.Printf("nexquake-nexus git_sha=%s build_time=%s go=%s %s/%s\n",
			v.GitSHA,
			v.BuildTime,
			v.GoVersion,
			v.GOOS,
			v.GOARCH,
		)
		return true, 0
	case "--healthcheck", "healthcheck":
		// Used by Docker/compose healthchecks. Do not require curl/wget/bash in the image.
		httpPort := getEnv("HTTP_PORT", "1337")
		if err := runHealthcheck(httpPort); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
			return true, 1
		}
		return true, 0
	default:
		return false, 0
	}
}

func runHealthcheck(httpPort string) error {
	url := fmt.Sprintf("http://127.0.0.1:%s/health", httpPort)

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Drain response body to allow connection reuse, though not critical for a single-shot command.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}

	return nil
}

func logArtifactFingerprints(cfg runtimeConfig) {
	infof("Artifact fingerprints:")
	if exe, err := os.Executable(); err == nil {
		if sum, err := sha256File(exe); err == nil {
			infof("  nexus   %s  %s", sum, exe)
		}
	}
	serverPath := filepath.Join(cfg.binDir, "nqserver")
	if sum, err := sha256File(serverPath); err == nil {
		infof("  nqserver %s  %s", sum, serverPath)
	}
	wasmPath := filepath.Join(cfg.clientDir, "index.wasm")
	if sum, err := sha256File(wasmPath); err == nil {
		infof("  wasm    %s  %s", sum, wasmPath)
	}
	infof("")
}

// Global singletons, initialized in main() before any goroutines.
var (
	globalIPAllocator     *nqnet.IPAllocator
	globalSessionRegistry *nqnet.SessionRegistry
	globalAuth            *admin.Auth
	globalAdminEnv        *admin.Env
	globalServerManager   *orch.ServerManager
)

func parseNQServerIP(raw string) net.IP {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = nqnet.DefaultNQServerIP
	}

	ip := net.ParseIP(raw).To4()
	if ip != nil {
		return ip
	}

	warnf("invalid NQSERVER_IP=%q; using %s", raw, nqnet.DefaultNQServerIP)
	return net.ParseIP(nqnet.DefaultNQServerIP).To4()
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	isAdmin := globalAuth.IsAdmin(r)
	sourceKey := nqnet.ResolveClientSourceKey(r)
	if globalIPAllocator.IsBlocked(sourceKey) {
		warnf("Rejected blocked client source=%q remote=%s", sourceKey, r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	conn, err := nqnet.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		warnf("WebSocket upgrade failed: %v", err)
		return
	}

	if isAdmin {
		infof("Admin connected: %s (subprotocol=%q source=%q)", conn.RemoteAddr(), conn.Subprotocol(), sourceKey)
	} else {
		debugf("Client connected: %s (subprotocol=%q source=%q)", conn.RemoteAddr(), conn.Subprotocol(), sourceKey)
	}

	dispatch := nqnet.FrameDispatch{
		HandleSlistRequest: func(payload []byte) []byte {
			entries := orch.SnapshotForSlist(globalServerManager)
			listPayload, _ := nqnet.BuildCCREPServerList(entries)
			return listPayload
		},
		HandleAdminFrame: func(router *nqnet.Router, payload []byte) {
			admin.HandleAdminFrame(router, payload, globalAuth, globalAdminEnv)
		},
	}

	router, err := nqnet.NewRouter(conn, sourceKey, isAdmin, globalIPAllocator, globalSessionRegistry, dispatch, warnf, debugf)
	if err != nil {
		errorf("Failed to create router: %v", err)
		_ = conn.Close()
		return
	}

	router.Run()

	if isAdmin {
		infof("Admin disconnected: %s", conn.RemoteAddr())
	} else {
		debugf("Client disconnected: %s", conn.RemoteAddr())
	}
}

func newMux(cfg runtimeConfig, pakCache *gamedata.PakIndexCache, mgr *orch.ServerManager) *http.ServeMux {
	mux := http.NewServeMux()

	// Health check endpoint (Go 1.22+ method-based routing)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		v := currentVersionInfo()
		w.Header().Set("X-NexQuake-Nexus-GitSHA", v.GitSHA)
		w.Header().Set("X-NexQuake-Nexus-BuildTime", v.BuildTime)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	// WebSocket endpoint for game connections
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(w, r)
	})

	// Serve game data files - shared between WASM client and servers.
	// /data-manifest/<mod> returns a virtualized manifest from common+client layers (with PAK exploding).
	mux.Handle("/data-manifest/", addCORSHeaders(http.HandlerFunc(gamedata.NewDataManifestHandler(cfg.dataDir, func(gameDir string) []string {
		return mgr.ResolveManifestGameDirs(gameDir)
	}, pakCache, cfg.vfsPrefetchConcurrency)), cfg.corsOrigin))
	mux.Handle("/pak-extract/", addCORSHeaders(http.HandlerFunc(gamedata.NewPakExtractHandler(cfg.dataDir, pakCache)), cfg.corsOrigin))

	dataFS := http.FileServerFS(os.DirFS(cfg.dataDir))
	mux.Handle("/data/", addCORSHeaders(contentTypeOverride(http.StripPrefix("/data/", dataFS)), cfg.corsOrigin))

	// Serve client files (WASM, HTML, JS, CSS)
	clientFS := http.FileServerFS(os.DirFS(cfg.clientDir))
	mux.Handle("/", addCORSHeaders(contentTypeOverride(cacheControlClient(clientFS)), cfg.corsOrigin))

	return mux
}

func buildAdminEnv() *admin.Env {
	return &admin.Env{
		ServerSnapshots: func() []orch.ServerSnapshot {
			return globalServerManager.Snapshots()
		},
		StartServer: func(target int) error {
			return globalServerManager.StartServer(target)
		},
		StopServer: func(ctx context.Context, target int, killAfter time.Duration) error {
			return globalServerManager.StopServer(ctx, target, killAfter)
		},
		RestartServer: func(ctx context.Context, target int, killAfter time.Duration) error {
			return globalServerManager.RestartServer(ctx, target, killAfter)
		},
		RemoveServer: func(target int) error {
			return globalServerManager.RemoveServer(target)
		},
		LaunchServer: func(binary string, args []string) error {
			return globalServerManager.LaunchServer(binary, args)
		},
		ExecServerCmd: func(port int, cmd string) (string, error) {
			return globalServerManager.ExecServerCommand(port, cmd)
		},
		TailNexusLog: tailNexusLogLines,
		SessionSnapshots: func() []nqnet.SessionSnapshot {
			return globalSessionRegistry.SnapshotAll()
		},
		SnapshotByVIP: func(vip string) ([]*nqnet.Router, []nqnet.BanTarget) {
			return globalSessionRegistry.SnapshotByVirtualIP(vip)
		},
		ReserveAndBlock: func(ip [4]byte, sourceKey string) {
			globalIPAllocator.ReserveAndBlock(ip, sourceKey)
		},
	}
}
