package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
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
	nqServerIP := net.ParseIP(nqnet.DefaultNQServerIP).To4()
	ipAlloc := nqnet.NewIPAllocator(nqServerIP)
	sessionReg := nqnet.NewSessionRegistry()
	globalIPAllocator = ipAlloc
	globalSessionRegistry = sessionReg

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	if err := prependPath(cfg.binDir); err != nil {
		fatalf("Failed to configure PATH: %v", err)
	}
	if err := prependPath(cfg.serverBinDir); err != nil {
		fatalf("Failed to configure PATH: %v", err)
	}

	// Validate runtime artifacts early; avoids confusing partial startup.
	if !fileExists(filepath.Join(cfg.clientDir, "index.html")) {
		fatalf("WASM client not found: %s", cfg.clientDir)
	}

	// Bootstraps servers.ini (only if missing) and seeds missing mod data based on
	// servers.ini -game entries and CFG_DIR/game.json. `base` catalog entries are
	// always included in the install set, and QUICKSTART defaults to `ffa`.
	if err := assets.QuickstartGame(runCtx, cfg.gameDir, cfg.cfgDir, infof); err != nil {
		fatalf("Quickstart failed: %v", err)
	}

	// Start dedicated servers (one per mod directory).
	serverMgr := orch.NewServerManager(
		cfg.gameDir,
		cfg.logsDir,
		infof,
		infofNoTail,
		debugf,
		warnf,
		errorf,
		formatTimestampedLogText,
	)
	serverMgr.SetPoolMaxSize(cfg.poolSize)
	if err := serverMgr.StartAll(); err != nil {
		fatalf("Failed to start servers: %v", err)
	}
	globalServerManager = serverMgr
	globalAdminEnv = buildAdminEnv()

	// Start Nexus-managed server info poller (used for Quake's `slist`).
	serverInfoPoller := orch.NewServerInfoPoller(serverMgr, nqServerIP)
	if err := serverInfoPoller.Start(runCtx); err != nil {
		warnf("Server info poller disabled: %v", err)
		serverInfoPoller = nil
	}

	pakCache := assets.NewPakIndexCache()
	mux := newMux(cfg, pakCache)

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
	gameDir                string
	cfgDir                 string
	cdDir                  string
	logsDir                string
	binDir                 string
	serverBinDir           string
	clientDir              string
	vfsPrefetchConcurrency int
	clientAutoSMenu        bool
	clientSendArgs         []string
	clientURLArgs          bool
	poolSize               int
}

func loadRuntimeConfig() runtimeConfig {
	binDir := getEnv("BIN_DIR", "/app/bin")
	serverBinDir := getEnv("SERVER_DIR", "/app/server")
	gameDir := getEnv("GAME_DIR", "/app/game")
	return runtimeConfig{
		httpPort:               getEnv("HTTP_PORT", "1337"),
		gameDir:                gameDir,
		cfgDir:                 getEnv("CFG_DIR", "/app/etc"),
		cdDir:                  getEnv("CD_DIR", "/app/cd"),
		logsDir:                getEnv("LOGS_DIR", "/app/logs"),
		binDir:                 binDir,
		serverBinDir:           serverBinDir,
		clientDir:              getEnv("CLIENT_DIR", "/app/bin/nqwasm"),
		vfsPrefetchConcurrency: getEnvIntMin("CL_CONCURRENCY", 16, 0),
		clientAutoSMenu:        getEnvBool01("CL_SMENU", false),
		clientSendArgs:         getEnvArgs("CL_ARGS", nil),
		clientURLArgs:          getEnvBool01("CL_URL_ARGS", false),
		poolSize:               getEnvIntMin("POOL_SIZE", 1, 1),
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

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}

	return nil
}

// Global singletons, initialized in main() before any goroutines.
var (
	globalIPAllocator     *nqnet.IPAllocator
	globalSessionRegistry *nqnet.SessionRegistry
	globalAuth            *admin.Auth
	globalAdminEnv        *admin.Env
	globalServerManager   *orch.ServerManager
)

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	isAdmin, userIdentity := globalAuth.IdentifyRequest(r)
	sourceIP := nqnet.ResolveClientSourceIP(r)
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

	dispatch := nqnet.FrameDispatch{
		HandleSlistRequest: func(payload []byte) []byte {
			entries := orch.SnapshotForSlist(globalServerManager)
			listPayload, _ := nqnet.BuildCCREPServerList(entries)
			return listPayload
		},
		HandleAdminFrame: func(router *nqnet.Router, payload []byte) {
			admin.HandleAdminFrameWithIdentityAndPromotionHook(router, payload, globalAuth, globalAdminEnv, userIdentity, func(r *nqnet.Router) {
				source := strings.TrimSpace(r.SourceIP())
				if source == "" {
					source = "unknown"
				}
				infof("Admin promoted: source=%s key=%s nqip=%s", source, r.SourceKey(), r.VirtualClientIP())
			})
		},
	}

	router, err := nqnet.NewRouter(conn, sourceKey, sourceIP, userIdentity, isAdmin, globalIPAllocator, globalSessionRegistry, dispatch, warnf, debugf)
	if err != nil {
		errorf("Failed to create router: %v", err)
		_ = conn.Close()
		return
	}

	router.Run()

	if isAdmin {
		infof("Admin disconnected: %s", displayAddr)
	} else {
		infof("Client disconnected: %s", displayAddr)
	}

}

func newMux(cfg runtimeConfig, pakCache *assets.PakIndexCache) *http.ServeMux {
	mux := http.NewServeMux()
	assetGateway := assets.NewHashedAssetGateway(
		cfg.gameDir,
		cfg.cdDir,
		pakCache,
		cfg.vfsPrefetchConcurrency,
		cfg.clientAutoSMenu,
		cfg.clientSendArgs,
		cfg.clientURLArgs,
	)
	assetGateway.SetErrorf(errorf)

	// Health check endpoint (Go 1.22+ method-based routing)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		v := currentVersionInfo()
		w.Header().Set("X-NexQuake-Nexus-GitSHA", v.GitSHA)
		w.Header().Set("X-NexQuake-Nexus-BuildTime", v.BuildTime)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	mux.HandleFunc("GET /ws", handleWebSocket)

	// Bootstrap + hash-addressed asset delivery for browser runtime.
	mux.Handle("/start", addIsolationHeaders(http.HandlerFunc(assetGateway.StartHandler())))
	mux.Handle("/nq/", addIsolationHeaders(http.HandlerFunc(assetGateway.AssetHandler())))

	// Serve client files (WASM, HTML, JS, CSS)
	clientFS := http.FileServerFS(os.DirFS(cfg.clientDir))
	mux.Handle("/", addIsolationHeaders(contentTypeOverride(cacheControlClient(clientFS))))

	return mux
}

func buildAdminEnv() *admin.Env {
	return &admin.Env{
		ServerSnapshots:     globalServerManager.Snapshots,
		StartServer:         globalServerManager.StartServer,
		StartServersAll:     globalServerManager.StartServersAll,
		StopServer:          globalServerManager.StopServer,
		StopServersAll:      globalServerManager.StopServersAll,
		RestartServer:       globalServerManager.RestartServer,
		RestartServersAll:   globalServerManager.RestartServersAll,
		RemoveServer:        globalServerManager.RemoveServer,
		LaunchServer:        globalServerManager.LaunchServer,
		ExecServerCmd:       globalServerManager.ExecServerCmd,
		IsManagedListenPort: globalServerManager.IsManagedListenPort,
		TailNexusLog:        tailNexusLogLines,
		Auditf:              auditf,
		SessionSnapshots:    globalSessionRegistry.SnapshotAll,
		SnapshotByVIP:       globalSessionRegistry.SnapshotByVirtualIP,
		ReserveAndBlock:     globalIPAllocator.ReserveAndBlock,
	}
}
