package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/0xBrsm/NexQuake/nexus/internal/clients"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

// nexusApp is the central application object. It wires together the
// networking layer (IP allocator, session registry), game server orchestration
// (server manager, info poller), and the admin subsystem into a single
// coherent unit that drives the HTTP server lifecycle. Transport modules
// attach themselves via their respective setup functions.
type nexusApp struct {
	cfg         runtimeConfig
	access      *access.Gate
	clients     *clients.Registry
	admin       *admin.Admin
	trunk       *trunk.Trunk
	serverMgr   *orch.ServerManager
	assetServer *assets.HashedAssetServer

	bootstrapClientFields []func() map[string]any
}

func main() {
	initLogging()
	auditLogger := initSlog()

	if handled, exitCode := handleCLI(os.Args[1:]); handled {
		os.Exit(exitCode)
	}

	if err := configureNexusLogFile(getEnv("LOGS_DIR", "/app/logs")); err != nil {
		fatalf("Failed to initialize Nexus log file: %v", err)
	}
	defer closeNexusLogFile()
	slog.Info("== Welcome to NexQuake! ==")

	cfg := loadRuntimeConfig()

	// Initialize access management. HTTP endpoints resolve callers once per
	// request; /rcon is just one route that asks for admin capability.
	authCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	jwt, err := access.InitJWT(authCtx)
	cancel()
	if err != nil {
		fatalf("Failed to initialize OIDC: %v", err)
	}
	auth := access.InitAuth()
	blocklist := access.NewBlocklist()

	var methods []string
	if auth.HasRconPassword() {
		methods = append(methods, "rcon")
	}
	if jwt != nil {
		methods = append(methods, "JWT")
	}
	if len(methods) == 0 {
		slog.Info("Admin access disabled")
	} else {
		slog.Info(fmt.Sprintf("Admin access enabled: %s", strings.Join(methods, ", ")))
	}

	nqServerIP := net.ParseIP("127.0.0.1").To4()
	id := access.NewIdentity()
	accessGate := access.NewGate(jwt, id, auth, blocklist)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	for _, dir := range []string{cfg.binDir, cfg.serverBinDir} {
		if err := prependPath(dir); err != nil {
			fatalf("Failed to configure PATH: %v", err)
		}
	}

	// Validate runtime artifacts early; avoids confusing partial startup.
	indexPath := filepath.Join(cfg.clientDir, "index.html")
	if st, err := os.Stat(indexPath); err != nil || st.IsDir() {
		fatalf("WASM client not found: %s", cfg.clientDir)
	}

	// Bootstraps servers.ini (only if missing) and seeds missing mod data based on
	// servers.ini -game entries and CFG_DIR/game.json. `base` catalog entries are
	// always included in the install set, and QUICKSTART defaults to `ffa`.
	if err := assets.QuickstartGame(runCtx, cfg.gameDir, cfg.cfgDir); err != nil {
		fatalf("Quickstart failed: %v", err)
	}

	// Start dedicated servers (one per mod directory).
	serverMgr := orch.NewServerManager(
		cfg.gameDir,
		cfg.logsDir,
		infofNoTail,
		formatTimestampedLogText,
	)
	serverMgr.SetServerMaxInstances(cfg.serverMaxInstances)
	if err := serverMgr.StartAll(); err != nil {
		fatalf("Failed to start servers: %v", err)
	}

	app := &nexusApp{
		cfg:         cfg,
		access:      accessGate,
		serverMgr:   serverMgr,
		assetServer: assets.NewHashedAssetServer(cfg.gameDir, cfg.cdDir, assets.NewPakIndexCache()),
	}
	app.trunk = trunk.New(
		trunk.WithServerIP(nqServerIP),
		trunk.WithControlHandler(app.handleControlFrame),
	)
	app.clients = clients.NewRegistry(app.trunk)
	app.admin = admin.New(app.clients, serverMgr, auditLogger, tailNexusLogLines, accessGate)

	// Start Nexus-managed server info poller (used for Quake's `slist`).
	stopInfoPoller := serverMgr.StartInfoPoller(runCtx, nqServerIP)

	// Build the shared HTTP mux and let each transport module attach.
	mux := app.newMux()
	setupWebSocket(app, mux)

	wtServer, err := setupWebTransport(app, runCtx)
	if err != nil {
		fatalf("WebTransport setup: %v", err)
	}

	server := &http.Server{
		Addr:              ":" + cfg.httpPort,
		Handler:           accessGate.HTTPGate(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	if wtServer != nil {
		slog.Info(fmt.Sprintf("Nexus listening on port %s (%s, %s)", cfg.httpPort, trunk.TransportWebSocket, trunk.TransportWebTransport))
	} else {
		slog.Info(fmt.Sprintf("Nexus listening on port %s (%s)", cfg.httpPort, trunk.TransportWebSocket))
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatalf("Server error: %v", err)
		}
	}()
	if wtServer != nil {
		go func() {
			if err := wtServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error(fmt.Sprintf("%s server error: %v", trunk.TransportWebTransport, err))
			}
		}()
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	slog.Info("Shutting down gracefully...")
	runCancel()
	stopInfoPoller()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		fatalf("Server shutdown failed: %v", err)
	}
	if wtServer != nil {
		_ = wtServer.Close()
	}

	_ = serverMgr.StopAll(ctx, 2*time.Second)
	slog.Info("Nexus stopped")
}
