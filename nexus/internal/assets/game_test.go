package assets

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestBootstrapGameData_Smoke(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// internal/assets is two levels deeper than src/nexus
	quake106 := filepath.Clean(filepath.Join(wd, "..", "..", "..", "assets", "quake106.zip"))
	lqpak1 := filepath.Clean(filepath.Join(wd, "..", "..", "..", "assets", "lq-pak1.zip"))

	if !fileExists(quake106) || !fileExists(lqpak1) {
		t.Skip("assets not present in this checkout")
	}

	gameDir := t.TempDir()
	cfgPath := filepath.Join(gameDir, "id1.json")

	if err := os.WriteFile(cfgPath, []byte(`[
  {
    "game": "id1",
    "common": [
      "file://`+quake106+`",
      "file://`+lqpak1+`"
    ]
  }
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("QUICKSTART", "id1")

	if err := BootstrapGameData(context.Background(), gameDir, nil); err != nil {
		t.Fatalf("BootstrapGameData: %v", err)
	}

	root := filepath.Join(gameDir, "id1", "common")
	if !fileExists(filepath.Join(root, "pak0.pak")) {
		t.Fatalf("missing pak0.pak in %s", root)
	}
	if !fileExists(filepath.Join(root, "SLICNSE.TXT")) {
		t.Fatalf("missing SLICNSE.TXT in %s", root)
	}
	if !fileExists(filepath.Join(root, "pak1.pak")) {
		t.Fatalf("missing pak1.pak in %s", root)
	}
	if !fileExistsCaseInsensitive(root, "LICENSE.md") {
		t.Fatalf("missing LICENSE.md in %s", root)
	}
}

func fileExistsCaseInsensitive(root, name string) bool {
	if name == "" {
		return false
	}
	if fileExists(filepath.Join(root, name)) {
		return true
	}
	upper := strings.ToUpper(name)
	lower := strings.ToLower(name)
	if name != upper && fileExists(filepath.Join(root, upper)) {
		return true
	}
	if name != lower && fileExists(filepath.Join(root, lower)) {
		return true
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	want := strings.ToLower(name)
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		if strings.ToLower(ent.Name()) == want {
			return true
		}
	}
	return false
}

func zipReaderFromFiles(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open in-memory zip: %v", err)
	}
	return zr
}

func TestExtractZipGeneric_StripsSingleRootDirectory(t *testing.T) {
	destRoot := t.TempDir()
	zr := zipReaderFromFiles(t, map[string]string{
		"3wave421x/progs.dat":     "progs",
		"3wave421x/maps/e1m1.ent": "ent",
	})

	if err := extractZipGeneric(zr, destRoot); err != nil {
		t.Fatalf("extractZipGeneric: %v", err)
	}

	if !fileExists(filepath.Join(destRoot, "progs.dat")) {
		t.Fatalf("missing flattened progs.dat")
	}
	if !fileExists(filepath.Join(destRoot, "maps", "e1m1.ent")) {
		t.Fatalf("missing flattened maps/e1m1.ent")
	}
	if fileExists(filepath.Join(destRoot, "3wave421x", "progs.dat")) {
		t.Fatalf("unexpected nested 3wave421x/progs.dat")
	}
}

func TestExtractZipGeneric_KeepsPathsWhenMultipleRoots(t *testing.T) {
	destRoot := t.TempDir()
	zr := zipReaderFromFiles(t, map[string]string{
		"id1/pak0.pak": "id1",
		"ctf/pak0.pak": "ctf",
	})

	if err := extractZipGeneric(zr, destRoot); err != nil {
		t.Fatalf("extractZipGeneric: %v", err)
	}

	if !fileExists(filepath.Join(destRoot, "id1", "pak0.pak")) {
		t.Fatalf("missing id1/pak0.pak")
	}
	if !fileExists(filepath.Join(destRoot, "ctf", "pak0.pak")) {
		t.Fatalf("missing ctf/pak0.pak")
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"id1", []string{"id1"}},
		{"ctf,arena,tf", []string{"ctf", "arena", "tf"}},
		{" ctf , arena , tf ", []string{"ctf", "arena", "tf"}},
		{"ctf,,arena", []string{"ctf", "arena"}},
		{"", nil},
		{",,,", nil},
		{"single", []string{"single"}},
	}
	for _, tt := range tests {
		got := splitCSV(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitCSV(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestLoadGameDataEntries_DefaultQuickstartID1(t *testing.T) {
	gameDir := t.TempDir()
	t.Setenv("QUICKSTART", "")

	m := `[{"game":"id1","common":["file:///fake/id1.zip"]}]`
	if err := os.WriteFile(filepath.Join(gameDir, "id1.json"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, src, err := loadGameDataEntries(gameDir)
	if err != nil {
		t.Fatalf("loadGameDataEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Game != "id1" {
		t.Fatalf("entries[0].Game = %q, want %q", entries[0].Game, "id1")
	}
	if !strings.Contains(src, "id1.json") {
		t.Fatalf("source string missing id1 manifest path: %q", src)
	}
}

func TestLoadGameDataEntries_CSV(t *testing.T) {
	gameDir := t.TempDir()

	// Create two manifest files.
	m1 := `[{"game":"ctf","common":["file:///fake/ctf.zip"]}]`
	m2 := `[{"game":"arena","server":["file:///fake/arena.zip"]}]`
	if err := os.WriteFile(filepath.Join(gameDir, "ctf.json"), []byte(m1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, "arena.json"), []byte(m2), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("QUICKSTART", "ctf,arena")

	entries, src, err := loadGameDataEntries(gameDir)
	if err != nil {
		t.Fatalf("loadGameDataEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Game != "ctf" {
		t.Errorf("entries[0].Game = %q, want %q", entries[0].Game, "ctf")
	}
	if entries[1].Game != "arena" {
		t.Errorf("entries[1].Game = %q, want %q", entries[1].Game, "arena")
	}
	if !strings.Contains(src, "ctf.json") || !strings.Contains(src, "arena.json") {
		t.Errorf("source string missing manifest paths: %q", src)
	}
}

func TestLoadGameDataEntries_CSV_SkipsMissing(t *testing.T) {
	gameDir := t.TempDir()

	// Only create one of the two manifests.
	m1 := `[{"game":"ctf","common":["file:///fake/ctf.zip"]}]`
	if err := os.WriteFile(filepath.Join(gameDir, "ctf.json"), []byte(m1), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("QUICKSTART", "ctf,nonexistent")

	entries, _, err := loadGameDataEntries(gameDir)
	if err != nil {
		t.Fatalf("loadGameDataEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (missing skipped), got %d", len(entries))
	}
	if entries[0].Game != "ctf" {
		t.Errorf("entries[0].Game = %q, want %q", entries[0].Game, "ctf")
	}
}

func TestLoadGameDataEntries_SingleMissingIsNoOp(t *testing.T) {
	gameDir := t.TempDir()
	t.Setenv("QUICKSTART", "nonexistent")

	entries, _, err := loadGameDataEntries(gameDir)
	if err != nil {
		t.Fatalf("loadGameDataEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("single missing manifest should return 0 entries, got %d", len(entries))
	}
}

func TestLoadGameDataEntries_InvalidName(t *testing.T) {
	gameDir := t.TempDir()
	t.Setenv("QUICKSTART", "ctf,../escape")

	_, _, err := loadGameDataEntries(gameDir)
	if err == nil {
		t.Fatal("expected error for path-traversal name, got nil")
	}
	if !strings.Contains(err.Error(), "invalid QUICKSTART name") {
		t.Errorf("unexpected error: %v", err)
	}
}
