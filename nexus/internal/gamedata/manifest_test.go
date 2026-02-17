package gamedata

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodeStartBundle(t *testing.T, body []byte) startManifestBundle {
	t.Helper()

	encoded := strings.TrimSpace(string(body))
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode start bundle (base64): %v", err)
	}

	var bundle startManifestBundle
	if err := json.Unmarshal(decoded, &bundle); err != nil {
		t.Fatalf("decode start bundle (json): %v", err)
	}
	return bundle
}

func TestHashedAssetGateway_StartAndAssetFetch(t *testing.T) {
	dataDir := t.TempDir()
	cdDir := t.TempDir()

	commonDir := filepath.Join(dataDir, "id1", "common")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "foo.txt"), []byte("hello data"), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cdDir, "02-intro.ogg"), []byte("hello cd"), 0o644); err != nil {
		t.Fatalf("write cd file: %v", err)
	}

	gateway := NewHashedAssetGateway(dataDir, cdDir, NewPakIndexCache(), 9)

	startReq := httptest.NewRequest(http.MethodGet, "/start", nil)
	startRec := httptest.NewRecorder()
	gateway.StartHandler().ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%q", startRec.Code, startRec.Body.String())
	}
	if got := startRec.Header().Get(headerVFSPrefetchConcurrency); got != "9" {
		t.Fatalf("prefetch header=%q want=9", got)
	}
	ref := strings.TrimSpace(startRec.Header().Get(headerNexQuakeRef))
	if ref == "" {
		t.Fatalf("missing %s header", headerNexQuakeRef)
	}

	bundle := decodeStartBundle(t, startRec.Body.Bytes())
	if len(bundle.Game["id1"]) == 0 {
		t.Fatalf("id1 manifest missing entries: %+v", bundle.Game)
	}

	hasPath := func(entries []startManifestEntry, path string) bool {
		for _, entry := range entries {
			if entry.Path == path {
				return true
			}
		}
		return false
	}

	if !hasPath(bundle.Game["id1"], "foo.txt") {
		t.Fatalf("foo.txt path missing from start manifest: %+v", bundle.Game["id1"])
	}
	if !hasPath(bundle.CD, "02-intro.ogg") {
		t.Fatalf("cd track path missing from start manifest: %+v", bundle.CD)
	}

	fooHash := hashAssetKey(ref, "mod:id1:"+normalizeVFSKey("foo.txt"))
	cdHash := hashAssetKey(ref, "cd:"+normalizeVFSKey("02-intro.ogg"))

	dataReq := httptest.NewRequest(http.MethodGet, "/nq/"+fooHash, nil)
	dataRec := httptest.NewRecorder()
	gateway.AssetHandler().ServeHTTP(dataRec, dataReq)
	if dataRec.Code != http.StatusOK {
		t.Fatalf("data fetch status=%d body=%q", dataRec.Code, dataRec.Body.String())
	}
	if got := dataRec.Body.String(); got != "hello data" {
		t.Fatalf("data fetch body=%q want=%q", got, "hello data")
	}

	cdReq := httptest.NewRequest(http.MethodGet, "/nq/"+cdHash, nil)
	cdRec := httptest.NewRecorder()
	gateway.AssetHandler().ServeHTTP(cdRec, cdReq)
	if cdRec.Code != http.StatusOK {
		t.Fatalf("cd fetch status=%d body=%q", cdRec.Code, cdRec.Body.String())
	}
	if got := cdRec.Header().Get("Content-Type"); !strings.HasPrefix(got, "audio/ogg") {
		t.Fatalf("cd content-type=%q, expected audio/ogg", got)
	}
	if got := cdRec.Body.String(); got != "hello cd" {
		t.Fatalf("cd fetch body=%q want=%q", got, "hello cd")
	}
}

func TestHashedAssetGateway_RangeRequests(t *testing.T) {
	dataDir := t.TempDir()
	commonDir := filepath.Join(dataDir, "id1", "common")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("abcdef")
	if err := os.WriteFile(filepath.Join(commonDir, "foo.bin"), content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	gateway := NewHashedAssetGateway(dataDir, filepath.Join(t.TempDir(), "missing"), NewPakIndexCache(), 4)
	startReq := httptest.NewRequest(http.MethodGet, "/start", nil)
	startRec := httptest.NewRecorder()
	gateway.StartHandler().ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%q", startRec.Code, startRec.Body.String())
	}

	bundle := decodeStartBundle(t, startRec.Body.Bytes())
	if len(bundle.Game["id1"]) == 0 {
		t.Fatalf("expected id1 manifest entries")
	}
	ref := strings.TrimSpace(startRec.Header().Get(headerNexQuakeRef))
	if ref == "" {
		t.Fatalf("missing %s header", headerNexQuakeRef)
	}
	hash := hashAssetKey(ref, "mod:id1:"+normalizeVFSKey(bundle.Game["id1"][0].Path))

	rangeReq := httptest.NewRequest(http.MethodGet, "/nq/"+hash, nil)
	rangeReq.Header.Set("Range", "bytes=1-3")
	rangeRec := httptest.NewRecorder()
	gateway.AssetHandler().ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent {
		t.Fatalf("range status=%d body=%q", rangeRec.Code, rangeRec.Body.String())
	}
	got, err := io.ReadAll(rangeRec.Result().Body)
	if err != nil {
		t.Fatalf("read range body: %v", err)
	}
	if string(got) != "bcd" {
		t.Fatalf("range body=%q want=%q", string(got), "bcd")
	}
}
