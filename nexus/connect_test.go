package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
)

// captureLogger returns a *slog.Logger that appends "msg key=value ..."
// lines to entries. Mirrors the same helper in the admin package's
// fakes_test.go (different package, so duplicated).
func captureLogger(entries *[]string) *slog.Logger {
	return slog.New(&captureHandlerTest{entries: entries})
}

type captureHandlerTest struct{ entries *[]string }

func (h *captureHandlerTest) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandlerTest) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	*h.entries = append(*h.entries, b.String())
	return nil
}
func (h *captureHandlerTest) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandlerTest) WithGroup(string) slog.Handler      { return h }

func TestHandleRcon_AuditsUnauthorizedRequest(t *testing.T) {
	t.Setenv("AUTH_CLIENT_IP_HEADER", "")

	var entries []string
	id := access.NewIdentity()
	accessGate := access.NewGate(nil, id, &access.Auth{}, access.NewBlocklist())
	app := &nexusApp{
		access: accessGate,
		admin:  admin.New(nil, nil, captureLogger(&entries), nil, accessGate),
	}

	req := httptest.NewRequest("POST", "/rcon", strings.NewReader(`{"jsonrpc":"2.0","method":"server.list","id":1}`))
	req.RemoteAddr = "198.51.100.1:4242"
	rr := httptest.NewRecorder()

	app.handleRcon(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"code":-32000`) {
		t.Fatalf("expected unauthorized JSON-RPC response, got %s", rr.Body.String())
	}
	if len(entries) != 1 {
		t.Fatalf("expected one audit entry, got %d: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], `actor="198.51.100.1"`) ||
		!strings.Contains(entries[0], "method=server.list") ||
		!strings.Contains(entries[0], "admin-rcon error") ||
		!strings.Contains(entries[0], `error="unauthorized"`) {
		t.Fatalf("audit entry: %q", entries[0])
	}
}

func TestMux_BlocksSourceBeforeRoutes(t *testing.T) {
	blocklist := access.NewBlocklist()
	blocklist.Block("198.51.100.1")
	accessGate := access.NewGate(nil, &access.Identity{}, &access.Auth{}, blocklist)
	app := newTestApp(t, accessGate)

	req := httptest.NewRequest("GET", "/health", nil)
	req.RemoteAddr = "198.51.100.1:4242"
	rr := httptest.NewRecorder()

	accessGate.HTTPGate(app.newMux()).ServeHTTP(rr, req)

	if rr.Code != 403 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMux_BlocksAssetFetchAfterStart(t *testing.T) {
	blocklist := access.NewBlocklist()
	accessGate := access.NewGate(nil, &access.Identity{}, &access.Auth{}, blocklist)

	gameDir := t.TempDir()
	commonDir := filepath.Join(gameDir, "id1", "common")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir common: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "config.cfg"), []byte("echo ok\n"), 0o644); err != nil {
		t.Fatalf("write game file: %v", err)
	}

	app := newTestApp(t, accessGate)
	app.cfg.gameDir = gameDir
	app.assetServer = assets.NewHashedAssetServer(gameDir, app.cfg.cdDir, assets.NewPakIndexCache())
	handler := accessGate.HTTPGate(app.newMux())

	startReq := httptest.NewRequest(http.MethodGet, "/start", nil)
	startReq.RemoteAddr = "198.51.100.1:4242"
	startRec := httptest.NewRecorder()
	handler.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", startRec.Code, startRec.Body.String())
	}

	blocklist.Block("198.51.100.1")

	assetReq := httptest.NewRequest(http.MethodGet, "/nq/not-a-real-hash", nil)
	assetReq.RemoteAddr = "198.51.100.1:4242"
	assetRec := httptest.NewRecorder()
	handler.ServeHTTP(assetRec, assetReq)

	if assetRec.Code != http.StatusForbidden {
		t.Fatalf("asset status=%d body=%s", assetRec.Code, assetRec.Body.String())
	}
}

// newTestApp constructs a minimal *nexusApp suitable for mux-level tests.
// Callers may overwrite cfg fields and assetServer afterward.
func newTestApp(t *testing.T, gate *access.Gate) *nexusApp {
	t.Helper()
	cfg := runtimeConfig{
		clientDir: t.TempDir(),
		gameDir:   t.TempDir(),
		cdDir:     t.TempDir(),
	}
	return &nexusApp{
		cfg:    cfg,
		access: gate,
		assetServer: assets.NewHashedAssetServer(cfg.gameDir, cfg.cdDir, assets.NewPakIndexCache()),
	}
}
