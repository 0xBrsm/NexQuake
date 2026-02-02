package main

import (
	"context"
	"fmt"
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

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	if len(os.Args) > 1 {
		switch os.Args[1] {
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
			return
		}
	}

	// Get configuration from environment.
	httpPort := getEnv("HTTP_PORT", "7071")
	dataDir := getEnv("QUAKE_DATA_DIR", "/data")
	logsDir := getEnv("LOGS_DIR", "/logs")
	serverBinary := getEnv("SERVER_BINARY", "/apps/nqserver")
	clientDir := getEnv("CLIENT_DIR", "/apps/nqwasm")
	corsOrigin := getEnv("CORS_ALLOWED_ORIGIN", "*")
	debugStartup := getEnv("DEBUG_STARTUP", "") == "1"

	// Validate runtime artifacts early; avoids confusing partial startup.
	if !fileExists(filepath.Join(clientDir, "index.html")) {
		log.Fatalf("WASM client not found: %s", clientDir)
	}

	if debugStartup {
		log.Println("Artifact fingerprints:")
		if exe, err := os.Executable(); err == nil {
			if sum, err := sha256File(exe); err == nil {
				log.Printf("  nexus   %s  %s", sum, exe)
			}
		}
		if sum, err := sha256File(serverBinary); err == nil {
			log.Printf("  nqserver %s  %s", sum, serverBinary)
		}
		wasmPath := filepath.Join(clientDir, "index.wasm")
		if sum, err := sha256File(wasmPath); err == nil {
			log.Printf("  wasm    %s  %s", sum, wasmPath)
		}
		log.Println()
	}

	// Bootstrap game data only when GAMEDATA_PATH is set.
	if os.Getenv("GAMEDATA_PATH") != "" {
		if err := bootstrapGameData(runCtx, dataDir); err != nil {
			log.Fatalf("Game data bootstrap failed: %v", err)
		}
	}

	// Start dedicated servers (one per mod directory).
	serverMgr := NewServerManager(dataDir, logsDir, serverBinary)
	if err := serverMgr.StartAll(); err != nil {
		log.Fatalf("Failed to start servers: %v", err)
	}

	// Start the nexus-managed server info cache (used for Quake's `slist`).
	globalServerInfoCache = NewServerInfoCache(serverMgr.ServerCount())
	if err := globalServerInfoCache.Start(runCtx); err != nil {
		warnf("Server info cache disabled: %v", err)
		globalServerInfoCache = nil
	}

	// Create HTTP server with handlers
	mux := http.NewServeMux()

	pakCache := newPakIndexCache()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		v := currentVersionInfo()
		w.Header().Set("X-NexQuake-Nexus-GitSHA", v.GitSHA)
		w.Header().Set("X-NexQuake-Nexus-BuildTime", v.BuildTime)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	// WebSocket endpoint for game connections
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(w, r)
	})

	// Serve game data files - shared between WASM client and servers.
	// /data-manifest/<mod> returns a virtualized manifest from common+client layers (with PAK exploding).
	mux.Handle("/data-manifest/", addCORSHeaders(http.HandlerFunc(newDataManifestHandler(dataDir, pakCache)), corsOrigin))
	mux.Handle("/pak-extract/", addCORSHeaders(http.HandlerFunc(newPakExtractHandler(dataDir, pakCache)), corsOrigin))

	dataFS := http.FileServer(http.Dir(dataDir))
	mux.Handle("/data/", addCORSHeaders(contentTypeOverride(http.StripPrefix("/data/", dataFS)), corsOrigin))

	// Serve client files (WASM, HTML, JS, CSS)
	clientFS := http.FileServer(http.Dir(clientDir))
	mux.Handle("/", addCORSHeaders(contentTypeOverride(cacheControlClient(clientFS)), corsOrigin))

	// Create server
	server := &http.Server{
		Addr:              ":" + httpPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		infof("Nexus listening on port %s", httpPort)
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
