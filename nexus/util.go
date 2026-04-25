package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/shlex"
)

// ---- Logging ----

type logLevel int

const (
	logError logLevel = iota
	logWarn
	logInfo
	logDebug
)

const (
	nexusLogHistoryCap = 2048
	logTimestampLayout = "2006/01/02 15:04:05"
)

var (
	currentLogLevel logLevel = logInfo

	operatorConsoleTimestamp atomic.Bool
	operatorConsoleMu        sync.Mutex

	nexusLogHistoryMu sync.RWMutex
	nexusLogHistory   []string

	nexusLogFileMu sync.Mutex
	nexusLogFile   *os.File
)

// initLogging configures the log level from LOG_LEVEL and applies the
// CONSOLE_TIMESTAMPS preference. Must be called once before any logf calls.
func initLogging() {
	operatorConsoleTimestamp.Store(true)
	applyOperatorConsoleTimestampEnv()

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
		warnf("Unknown LOG_LEVEL=%q (expected: error|warn|info|debug); defaulting to info", level)
	}
}

// applyOperatorConsoleTimestampEnv reads CONSOLE_TIMESTAMPS (0|1) and updates
// the operatorConsoleTimestamp flag. Called at startup and exposed for testing.
func applyOperatorConsoleTimestampEnv() {
	raw := strings.TrimSpace(os.Getenv("CONSOLE_TIMESTAMPS"))
	if raw == "" {
		return
	}
	switch raw {
	case "1":
		operatorConsoleTimestamp.Store(true)
	case "0":
		operatorConsoleTimestamp.Store(false)
	default:
		warnf("Unknown CONSOLE_TIMESTAMPS=%q (expected: 0|1); defaulting to 1", raw)
		operatorConsoleTimestamp.Store(true)
	}
}

// configureNexusLogFile opens (or creates) nexus.log inside logsDir and sets
// it as the active log destination. It is safe to call more than once; the
// previous file is closed atomically.
func configureNexusLogFile(logsDir string) error {
	logsDir = strings.TrimSpace(logsDir)
	if logsDir == "" {
		return fmt.Errorf("LOGS_DIR is empty")
	}
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}

	path := filepath.Join(logsDir, "nexus.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	nexusLogFileMu.Lock()
	old := nexusLogFile
	nexusLogFile = f
	nexusLogFileMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// closeNexusLogFile closes the active nexus log file. Called via defer at shutdown.
func closeNexusLogFile() {
	nexusLogFileMu.Lock()
	f := nexusLogFile
	nexusLogFile = nil
	nexusLogFileMu.Unlock()
	if f != nil {
		_ = f.Close()
	}
}

func operatorConsoleTimestampsEnabled() bool {
	return operatorConsoleTimestamp.Load()
}

// formatTimestampedLogText prefixes every line of msg with a timestamp.
// The result is always newline-terminated; empty input returns "".
func formatTimestampedLogText(msg string, now time.Time) string {
	lines := splitLogLines(msg)
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	prefix := now.Format(logTimestampLayout) + " "
	for _, line := range lines {
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// formatPlainLogText normalises msg to a newline-terminated string without
// timestamps. Used for the no-timestamp operator console variant.
func formatPlainLogText(msg string) string {
	lines := splitLogLines(msg)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// splitLogLines normalises line endings and splits msg into individual lines,
// stripping trailing newlines. Returns nil for blank input.
func splitLogLines(msg string) []string {
	msg = strings.ReplaceAll(msg, "\r\n", "\n")
	msg = strings.ReplaceAll(msg, "\r", "\n")
	msg = strings.TrimRight(msg, "\n")
	if msg == "" {
		return nil
	}
	return strings.Split(msg, "\n")
}

// writeOperatorConsoleText writes to stderr. If timestamp mode is enabled,
// the timestamped form is used; otherwise the plain form. Both arguments
// must refer to the same logical message.
func writeOperatorConsoleText(timestamped, plain string) {
	operatorConsoleMu.Lock()
	defer operatorConsoleMu.Unlock()

	text := plain
	if operatorConsoleTimestampsEnabled() {
		text = timestamped
	}
	if text == "" {
		return
	}
	_, _ = io.WriteString(os.Stderr, text)
}

// writeNexusLogFile appends text to the open nexus log file, if any.
func writeNexusLogFile(text string) {
	if text == "" {
		return
	}
	nexusLogFileMu.Lock()
	defer nexusLogFileMu.Unlock()
	if nexusLogFile == nil {
		return
	}
	_, _ = io.WriteString(nexusLogFile, text)
}

// logf formats and emits a log message at the given level. Messages are written
// to the in-memory tail buffer, the log file, and the operator console.
// Suppressed when level exceeds currentLogLevel.
func logf(level logLevel, format string, args ...any) {
	if level > currentLogLevel {
		return
	}
	msg := fmt.Sprintf(format, args...)
	now := time.Now()
	timestamped := formatTimestampedLogText(msg, now)
	plain := formatPlainLogText(msg)
	recordNexusLogLine(timestamped)
	writeNexusLogFile(timestamped)
	writeOperatorConsoleText(timestamped, plain)
}

// logfNoTail is like logf but skips the tail buffer and file — used for
// server console relay lines that should appear on-screen but not pollute the
// admin tail history.
func logfNoTail(level logLevel, format string, args ...any) {
	if level > currentLogLevel {
		return
	}
	msg := fmt.Sprintf(format, args...)
	now := time.Now()
	writeOperatorConsoleText(formatTimestampedLogText(msg, now), formatPlainLogText(msg))
}

func errorf(format string, args ...any) { logf(logError, format, args...) }
func warnf(format string, args ...any)  { logf(logWarn, format, args...) }
func infof(format string, args ...any)  { logf(logInfo, format, args...) }
func debugf(format string, args ...any) { logf(logDebug, format, args...) }

// auditf always records the message to the tail buffer and log file regardless
// of the current log level. It also writes to the operator console when debug
// level is active. Use for security-sensitive events (bans, promotions, etc.).
func auditf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	now := time.Now()
	timestamped := formatTimestampedLogText(msg, now)
	plain := formatPlainLogText(msg)
	recordNexusLogLine(timestamped)
	writeNexusLogFile(timestamped)
	if currentLogLevel >= logDebug {
		writeOperatorConsoleText(timestamped, plain)
	}
}
func infofNoTail(format string, args ...any) {
	logfNoTail(logInfo, format, args...)
}
func fatalf(format string, args ...any) {
	errorf(format, args...)
	os.Exit(1)
}

// recordNexusLogLine appends each line of text to the in-memory ring buffer.
// When the buffer is full (nexusLogHistoryCap) the oldest line is evicted.
func recordNexusLogLine(text string) {
	if text == "" {
		return
	}
	nexusLogHistoryMu.Lock()
	defer nexusLogHistoryMu.Unlock()
	for _, line := range splitLogLines(text) {
		if line == "" {
			continue
		}
		if len(nexusLogHistory) < nexusLogHistoryCap {
			nexusLogHistory = append(nexusLogHistory, line)
		} else {
			copy(nexusLogHistory, nexusLogHistory[1:])
			nexusLogHistory[len(nexusLogHistory)-1] = line
		}
	}
}

// tailNexusLogLines returns the last n lines from the in-memory log buffer.
// Returns nil when n <= 0 or the buffer is empty.
func tailNexusLogLines(n int) []string {
	if n <= 0 {
		return nil
	}

	nexusLogHistoryMu.RLock()
	snapshot := append([]string(nil), nexusLogHistory...)
	nexusLogHistoryMu.RUnlock()
	if len(snapshot) == 0 {
		return nil
	}

	n = min(n, len(snapshot))
	return append([]string(nil), snapshot[len(snapshot)-n:]...)
}

// ---- Version ----

// These are set at build time via -ldflags.
// Example:
//
//	-X github.com/0xBrsm/NexQuake/nexus.gitSHA=$GITHUB_SHA -X github.com/0xBrsm/NexQuake/nexus.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)
var (
	gitSHA    = "dev"
	buildTime = ""
)

// versionInfo carries build-time metadata returned by the /health endpoint
// and the --version CLI command.
type versionInfo struct {
	GitSHA    string `json:"git_sha"`
	BuildTime string `json:"build_time,omitempty"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

// currentVersionInfo returns build metadata. When buildTime is not set via
// -ldflags it defaults to the current UTC time (dev builds only).
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

// getEnv returns the value of key, or defaultValue when the variable is unset or empty.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvIntMin parses key as an integer. Returns defaultValue when the variable
// is unset, non-integer, or below minValue, and warns in those last two cases.
func getEnvIntMin(key string, defaultValue, minValue int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue {
		warnf("Invalid %s=%q (expected integer >= %d); using %d", key, raw, minValue, defaultValue)
		return defaultValue
	}
	return value
}

// getEnvBool01 parses key as "0" or "1". Returns defaultValue when the variable
// is unset or not a recognised value, and warns in the latter case.
func getEnvBool01(key string, defaultValue bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	switch raw {
	case "1":
		return true
	case "0":
		return false
	default:
		warnf("Invalid %s=%q (expected 0|1); using default (%t)", key, raw, defaultValue)
		return defaultValue
	}
}

// getEnvArgs parses key as shell-style arguments using shlex. Returns a copy of
// defaultValue when the variable is unset, empty, or contains a parse error.
func getEnvArgs(key string, defaultValue []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return append([]string(nil), defaultValue...)
	}

	parsed, err := shlex.Split(raw)
	if err != nil {
		warnf("Invalid %s=%q (expected shell-style args); using default", key, raw)
		return append([]string(nil), defaultValue...)
	}

	out := make([]string, 0, len(parsed))
	for _, value := range parsed {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

// fileExists reports whether path exists and is a regular file (not a directory).
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// contentTypeOverride forces correct Content-Type for .wasm, .data, and .pak
// files, which net/http's built-in sniffing may misclassify.
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

// cacheControlClient sets Cache-Control headers for the WASM client static
// files. HTML, JS, WASM, and CSS are served with no-store to prevent stale
// client/server version mismatches. Skips setting headers if a reverse proxy
// has already set Cache-Control.
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

// addIsolationHeaders sets the COOP/COEP/CORP headers required for
// SharedArrayBuffer (used by WASM threading) on all responses from h.
func addIsolationHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}
