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
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/internal/session"
	"github.com/0xBrsm/NexQuake/nexus/nqrelay"
)

// nexusApp is the central application object. It wires together the
// networking layer (IP allocator, session registry), game server orchestration
// (server manager, info poller), and the admin subsystem into a single
// coherent unit that drives the HTTP server lifecycle.
type nexusApp struct {
	cfg        runtimeConfig
	auth       *admin.Auth
	id         *admin.Identity
	ipAlloc    *nqrelay.NQIPAllocator
	sessionReg *session.Registry
	serverMgr  *orch.ServerManager
	adminEnv   *admin.Env
	pakCache   *assets.PakIndexCache
}

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

	// Initialize networking layer.
	nqServerIP := net.ParseIP(nqrelay.DefaultNQServerIP).To4()
	ipAlloc := nqrelay.NewNQIPAllocator(nqServerIP)
	sessionReg := session.NewRegistry()

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
	serverMgr.SetServerMaxInstances(cfg.serverMaxInstances)
	if err := serverMgr.StartAll(); err != nil {
		fatalf("Failed to start servers: %v", err)
	}

	app := &nexusApp{
		cfg:        cfg,
		auth:       auth,
		id:         admin.NewIdentity(),
		ipAlloc:    ipAlloc,
		sessionReg: sessionReg,
		serverMgr:  serverMgr,
		adminEnv:   buildAdminEnv(serverMgr, sessionReg, ipAlloc),
		pakCache:   assets.NewPakIndexCache(),
	}

	// Start Nexus-managed server info poller (used for Quake's `slist`).
	stopInfoPoller := serverMgr.StartInfoPoller(runCtx, nqServerIP)

	server := &http.Server{
		Addr:              ":" + cfg.httpPort,
		Handler:           app.newMux(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start server in goroutine.
	go func() {
		infof("Nexus listening on port %s", cfg.httpPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	infof("Shutting down gracefully...")
	runCancel()
	stopInfoPoller()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		fatalf("Server shutdown failed: %v", err)
	}

	_ = serverMgr.StopAll(ctx, 2*time.Second)
	infof("Nexus stopped")
}

// runtimeConfig holds all values derived from environment variables at startup.
// Reading env vars here (once) instead of on every request avoids syscall overhead
// in hot paths such as WebSocket upgrade and IP resolution.
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
	serverMaxInstances     int
}

// loadRuntimeConfig reads all environment variables once and returns the
// resolved configuration. Callers must not call os.Getenv for these keys again.
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
		clientURLArgs:          getEnvBool01("CL_URL_ARGS", true),
		serverMaxInstances:     getEnvIntMin("SV_MAX_INSTANCES", 1, 1),
	}
}

// prependPath adds dir to the front of PATH if it is not already present.
// It is a no-op for empty strings.
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

// handleCLI processes CLI-only sub-commands (--version, --healthcheck).
// It returns (true, exitCode) when a sub-command was matched and the process
// should exit; (false, 0) means normal server startup should proceed.
func handleCLI(args []string) (handled bool, exitCode int) {
	if len(args) == 0 {
		return false, 0
	}

	switch args[0] {
	case "--version", "version":
		// Keep this simple so it's usable inside minimal runtime images.
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

// runHealthcheck performs an HTTP GET against the local /health endpoint.
// Designed for Docker/compose healthchecks — avoids needing curl or bash.
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
