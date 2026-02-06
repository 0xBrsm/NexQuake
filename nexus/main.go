package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	initLogging()

	if handled, exitCode := handleCLI(os.Args[1:]); handled {
		os.Exit(exitCode)
	}

	// Initialize authentication (admin privilege system).
	if err := initAuth(); err != nil {
		log.Fatalf("Failed to initialize auth: %v", err)
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	cfg := loadRuntimeConfig()

	// Validate runtime artifacts early; avoids confusing partial startup.
	if !fileExists(filepath.Join(cfg.clientDir, "index.html")) {
		log.Fatalf("WASM client not found: %s", cfg.clientDir)
	}

	if cfg.debugStartup {
		logArtifactFingerprints(cfg)
	}

	// Default QUICKSTART is "minimal"; missing manifest is a no-op.
	if err := bootstrapGameData(runCtx, cfg.dataDir); err != nil {
		log.Fatalf("Game data bootstrap failed: %v", err)
	}

	// Start dedicated servers (one per mod directory).
	serverMgr := NewServerManager(cfg.dataDir, cfg.logsDir, cfg.serverBinary)
	if err := serverMgr.StartAll(); err != nil {
		log.Fatalf("Failed to start servers: %v", err)
	}

	// Start the nexus-managed server info cache (used for Quake's `slist`).
	globalServerInfoCache = NewServerInfoCache(serverMgr.ServerCount())
	if err := globalServerInfoCache.Start(runCtx); err != nil {
		warnf("Server info cache disabled: %v", err)
		globalServerInfoCache = nil
	}

	pakCache := newPakIndexCache()
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
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down gracefully...")
	runCancel()
	if globalServerInfoCache != nil {
		globalServerInfoCache.Stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown failed: %v", err)
	}

	_ = serverMgr.StopAll(ctx, 2*time.Second)
	log.Println("Nexus stopped")
}

type runtimeConfig struct {
	httpPort     string
	dataDir      string
	logsDir      string
	serverBinary string
	clientDir    string
	corsOrigin   string
	debugStartup bool
}

func loadRuntimeConfig() runtimeConfig {
	binDir := getEnv("BIN_DIR", "/app/bin")
	return runtimeConfig{
		httpPort:     getEnv("HTTP_PORT", "1337"),
		dataDir:      getEnv("DATA_DIR", "/app/data"),
		logsDir:      getEnv("LOGS_DIR", "/app/logs"),
		serverBinary: getEnv("SERVER_BIN", filepath.Join(binDir, "nqserver")),
		clientDir:    getEnv("CLIENT_DIR", "/app/bin/nqwasm"),
		corsOrigin:   getEnv("CORS_ALLOWED_ORIGIN", ""),
		debugStartup: getEnv("DEBUG_STARTUP", "") == "1",
	}
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
	log.Println("Artifact fingerprints:")
	if exe, err := os.Executable(); err == nil {
		if sum, err := sha256File(exe); err == nil {
			log.Printf("  nexus   %s  %s", sum, exe)
		}
	}
	if sum, err := sha256File(cfg.serverBinary); err == nil {
		log.Printf("  nqserver %s  %s", sum, cfg.serverBinary)
	}
	wasmPath := filepath.Join(cfg.clientDir, "index.wasm")
	if sum, err := sha256File(wasmPath); err == nil {
		log.Printf("  wasm    %s  %s", sum, wasmPath)
	}
	log.Println()
}

func newMux(cfg runtimeConfig, pakCache *pakIndexCache) *http.ServeMux {
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
	mux.Handle("/data-manifest/", addCORSHeaders(http.HandlerFunc(newDataManifestHandler(cfg.dataDir, pakCache)), cfg.corsOrigin))
	mux.Handle("/pak-extract/", addCORSHeaders(http.HandlerFunc(newPakExtractHandler(cfg.dataDir, pakCache)), cfg.corsOrigin))

	dataFS := http.FileServerFS(os.DirFS(cfg.dataDir))
	mux.Handle("/data/", addCORSHeaders(contentTypeOverride(http.StripPrefix("/data/", dataFS)), cfg.corsOrigin))

	// Serve client files (WASM, HTML, JS, CSS)
	clientFS := http.FileServerFS(os.DirFS(cfg.clientDir))
	mux.Handle("/", addCORSHeaders(contentTypeOverride(cacheControlClient(clientFS)), cfg.corsOrigin))

	return mux
}
