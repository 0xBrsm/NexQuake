package main

import (
	"context"
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
			fmt.Printf("webquake-gateway git_sha=%s build_time=%s go=%s %s/%s\n",
				currentVersionInfo().GitSHA,
				currentVersionInfo().BuildTime,
				currentVersionInfo().GoVersion,
				currentVersionInfo().GOOS,
				currentVersionInfo().GOARCH,
			)
			return
		}
	}

	// Get configuration from environment (matching start.sh defaults)
	httpPort := getEnv("HTTP_PORT", "7071")
	dataDir := getEnv("QUAKE_DATA_DIR", "/data")
	clientDir := getEnv("CLIENT_DIR", "/apps/nqwasm")
	corsOrigin := getEnv("CORS_ALLOWED_ORIGIN", "*")
	wsOrigin := getEnv("WS_ALLOWED_ORIGIN", "*")
	debugBrowserConsole := getEnv("DEBUG_BROWSER_CONSOLE", "") != ""

	// Initialize WebSocket upgrader with configurable origin
	initWebSocketUpgrader(wsOrigin)

	// Start the gateway-managed server info cache (used for Quake's `slist`).
	globalServerInfoCache = NewServerInfoCacheFromEnv()
	if err := globalServerInfoCache.Start(runCtx); err != nil {
		warnf("Server info cache disabled: %v", err)
		globalServerInfoCache = nil
	}

	// Create HTTP server with handlers
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		v := currentVersionInfo()
		w.Header().Set("X-WebQuake-Gateway-GitSHA", v.GitSHA)
		w.Header().Set("X-WebQuake-Gateway-BuildTime", v.BuildTime)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	// Version endpoint (for debugging “am I running the right artifact?” issues).
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeVersionJSON(w)
	})

	// WebSocket endpoint for game connections
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(w, r)
	})

	// Optional debug endpoint to mirror browser console logs into gateway logs.
	if debugBrowserConsole {
		mux.HandleFunc("/debug/console", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, 32<<10))
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			remote := r.RemoteAddr
			if bodyStr := strings.TrimSpace(string(body)); bodyStr != "" {
				log.Printf("BROWSER\t%s\t%s", remote, bodyStr)
			}

			w.WriteHeader(http.StatusNoContent)
		})
		infof("Debug browser console mirror enabled at POST /debug/console")
	}

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
	clientHandler := http.Handler(clientFS)
	if debugBrowserConsole {
		clientHandler = maybeInjectDebugConsole(clientDir, clientHandler)
	}
	mux.Handle("/", addCORSHeaders(contentTypeOverride(cacheControlClient(clientHandler)), corsOrigin))

	// Create server
	server := &http.Server{
		Addr:              ":" + httpPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		infof("Gateway listening on port %s", httpPort)
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

	// TODO: Clean up server processes
	log.Println("Gateway stopped")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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

func maybeInjectDebugConsole(clientDir string, fallback http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only inject when explicitly requested (avoid perturbing normal runs).
		if r.URL.Query().Get("debug_console") != "1" {
			fallback.ServeHTTP(w, r)
			return
		}

		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if !strings.HasSuffix(path, ".html") {
			fallback.ServeHTTP(w, r)
			return
		}

		rel := filepath.Clean(strings.TrimPrefix(path, "/"))
		// Prevent traversal outside clientDir.
		if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			fallback.ServeHTTP(w, r)
			return
		}

		full := filepath.Join(clientDir, rel)
		contents, err := os.ReadFile(full)
		if err != nil {
			fallback.ServeHTTP(w, r)
			return
		}

		injected := injectConsoleMirror(string(contents))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(injected))
	})
}

func injectConsoleMirror(html string) string {
	snippet := "<script>" + consoleMirrorScript + "</script>"

	// Insert early (head) when possible, otherwise append.
	if strings.Contains(html, "</head>") {
		return strings.Replace(html, "</head>", snippet+"</head>", 1)
	}
	if strings.Contains(html, "</body>") {
		return strings.Replace(html, "</body>", snippet+"</body>", 1)
	}
	return html + snippet
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
