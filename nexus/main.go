package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
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
			fmt.Printf("webquake-nexus git_sha=%s build_time=%s go=%s %s/%s\n",
				currentVersionInfo().GitSHA,
				currentVersionInfo().BuildTime,
				currentVersionInfo().GoVersion,
				currentVersionInfo().GOOS,
				currentVersionInfo().GOARCH,
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

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		v := currentVersionInfo()
		w.Header().Set("X-WebQuake-Nexus-GitSHA", v.GitSHA)
		w.Header().Set("X-WebQuake-Nexus-BuildTime", v.BuildTime)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	// WebSocket endpoint for game connections
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(w, r)
	})

	// Serve game data files (PAK files, mods) - shared between WASM client and servers
	// JSON manifest for /data/<mod> (used by the WASM loader to mirror /data/<mod> into the VFS).
	mux.Handle("/data-manifest/", addCORSHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		mod := strings.TrimPrefix(r.URL.Path, "/data-manifest/")
		mod = strings.Trim(mod, "/")
		if mod == "" {
			http.Error(w, "missing mod", http.StatusBadRequest)
			return
		}
		// Prevent path traversal and nested paths; manifest is scoped to a single mod dir.
		if strings.Contains(mod, "..") || strings.ContainsAny(mod, `/\`) {
			http.Error(w, "invalid mod", http.StatusBadRequest)
			return
		}

		dir := filepath.Join(dataDir, mod)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			http.NotFound(w, r)
			return
		}

		var files []string
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}

			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if rel == "." || rel == "" {
				return nil
			}

			files = append(files, rel)
			return nil
		})
		if err != nil {
			http.Error(w, "failed to scan data dir", http.StatusInternalServerError)
			return
		}

		sort.Strings(files)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(files)
	}), corsOrigin))

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

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func contentTypeOverride(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".wasm"):
			w.Header().Set("Content-Type", "application/wasm")
		case strings.HasSuffix(r.URL.Path, ".data"),
			strings.HasSuffix(strings.ToLower(r.URL.Path), ".pak"):
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		h.ServeHTTP(w, r)
	})
}

func cacheControlClient(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If another layer already set Cache-Control (e.g. reverse proxy), keep it.
		if w.Header().Get("Cache-Control") == "" {
			// Avoid stale JS/WASM mismatches during rapid artifact swaps.
			switch {
			case r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html"):
				w.Header().Set("Cache-Control", "no-store")
			case strings.HasSuffix(r.URL.Path, ".js"),
				strings.HasSuffix(r.URL.Path, ".wasm"),
				strings.HasSuffix(r.URL.Path, ".css"):
				// Use no-store (not just no-cache) to avoid intermittent JS/WASM
				// mismatches observed across browsers and proxies.
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Pragma", "no-cache")
				w.Header().Set("Expires", "0")
			}
		}

		h.ServeHTTP(w, r)
	})
}

// addCORSHeaders wraps a handler to add CORS headers for SharedArrayBuffer support
func addCORSHeaders(h http.Handler, allowedOrigin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Required headers for SharedArrayBuffer (WASM threading)
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")

		// Standard CORS headers (configurable via environment)
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		// Handle preflight
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		h.ServeHTTP(w, r)
	})
}
