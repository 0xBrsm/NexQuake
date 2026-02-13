package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReadPakContents(t *testing.T) {
	tmp := t.TempDir()
	pakPath := filepath.Join(tmp, "pak0.pak")

	writeTestPak(t, pakPath, map[string][]byte{
		"gfx/palette.lmp": {0x01, 0x02, 0x03},
		"progs.dat":       []byte("abcd"),
	})

	entries, err := readPakContents(pakPath)
	if err != nil {
		t.Fatalf("readPakContents: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	byName := map[string]PakFileEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if e, ok := byName["gfx/palette.lmp"]; !ok || e.Size != 3 {
		t.Fatalf("missing gfx/palette.lmp or wrong size: %+v", e)
	}
	if e, ok := byName["progs.dat"]; !ok || e.Size != 4 {
		t.Fatalf("missing progs.dat or wrong size: %+v", e)
	}
}

func TestPakExtractHandler_ServesEntry(t *testing.T) {
	dataDir := t.TempDir()
	mod := "id1"
	layer := "common"
	commonDir := filepath.Join(dataDir, mod, layer)
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	pakPath := filepath.Join(commonDir, "pak0.pak")
	want := []byte("hello from pak")
	writeTestPak(t, pakPath, map[string][]byte{
		"docs/readme.txt": want,
	})

	h := newPakExtractHandler(dataDir, newPakIndexCache())

	req := httptest.NewRequest(http.MethodGet, "/pak-extract/id1/common/pak0.pak/docs/readme.txt", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	res := rr.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("body mismatch: got=%q want=%q", string(got), string(want))
	}
}
