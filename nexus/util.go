package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// ---- Logging ----

type logLevel int

const (
	logError logLevel = iota
	logWarn
	logInfo
	logDebug
)

var currentLogLevel = logInfo

func initLogging() {
	level := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	switch level {
	case "", "info":
		currentLogLevel = logInfo
	case "debug":
		currentLogLevel = logDebug
	case "warn", "warning":
		currentLogLevel = logWarn
	case "error":
		currentLogLevel = logError
	default:
		currentLogLevel = logInfo
		log.Printf("Unknown LOG_LEVEL=%q (expected: error|warn|info|debug); defaulting to info", level)
	}
}

func logf(level logLevel, format string, args ...any) {
	if level <= currentLogLevel {
		log.Printf(format, args...)
	}
}

func errorf(format string, args ...any) { logf(logError, format, args...) }
func warnf(format string, args ...any)  { logf(logWarn, format, args...) }
func infof(format string, args ...any)  { logf(logInfo, format, args...) }
func debugf(format string, args ...any) { logf(logDebug, format, args...) }

// ---- Version ----

// These are set at build time via -ldflags.
// Example:
//
//	-X github.com/brstm/WebQuake/nexus.gitSHA=$GITHUB_SHA -X github.com/brstm/WebQuake/nexus.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)
var (
	gitSHA    = "dev"
	buildTime = ""
)

type versionInfo struct {
	GitSHA    string `json:"git_sha"`
	BuildTime string `json:"build_time,omitempty"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

func currentVersionInfo() versionInfo {
	bt := buildTime
	if bt == "" {
		bt = time.Now().UTC().Format(time.RFC3339)
	}
	return versionInfo{
		GitSHA:    gitSHA,
		BuildTime: bt,
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}
}

// ---- Misc runtime helpers ----

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
