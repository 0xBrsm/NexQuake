package assets

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCDManifest_FiltersSortsAndFormatsURLs(t *testing.T) {
	cdDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(cdDir, "02-intro.ogg"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cdDir, "ambient"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdDir, "ambient", "#03-boss.mp3"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdDir, "readme.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	manifest, err := buildCDManifest(cdDir)
	if err != nil {
		t.Fatalf("buildCDManifest: %v", err)
	}
	if len(manifest) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(manifest))
	}

	if manifest[0].Path != "02-intro.ogg" {
		t.Fatalf("manifest[0].Path = %q, want %q", manifest[0].Path, "02-intro.ogg")
	}
	if manifest[0].URL != "/cd-stream/02-intro.ogg" {
		t.Fatalf("manifest[0].URL = %q, want %q", manifest[0].URL, "/cd-stream/02-intro.ogg")
	}
	if manifest[1].Path != "ambient/#03-boss.mp3" {
		t.Fatalf("manifest[1].Path = %q, want %q", manifest[1].Path, "ambient/#03-boss.mp3")
	}
	if manifest[1].URL != "/cd-stream/ambient/%2303-boss.mp3" {
		t.Fatalf("manifest[1].URL = %q, want %q", manifest[1].URL, "/cd-stream/ambient/%2303-boss.mp3")
	}
}

func TestNewCDManifestHandler_ReturnsManifest(t *testing.T) {
	cdDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cdDir, "04-run.ogg"), []byte("run"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := NewCDManifestHandler(cdDir)
	req := httptest.NewRequest(http.MethodGet, "/cd-manifest", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var manifest []cdManifestEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(manifest) != 1 || manifest[0].Path != "04-run.ogg" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
}

func TestNewCDManifestHandler_MethodAndMissingDir(t *testing.T) {
	handler := NewCDManifestHandler(filepath.Join(t.TempDir(), "missing"))

	postReq := httptest.NewRequest(http.MethodPost, "/cd-manifest", nil)
	postRec := httptest.NewRecorder()
	handler(postRec, postReq)
	if postRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want %d", postRec.Code, http.StatusMethodNotAllowed)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/cd-manifest", nil)
	getRec := httptest.NewRecorder()
	handler(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("GET status = %d, want %d", getRec.Code, http.StatusNotFound)
	}
}
