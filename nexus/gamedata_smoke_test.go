package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapGameData_Smoke(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	quake106 := filepath.Clean(filepath.Join(wd, "..", "assets", "quake106.zip"))
	lqpak1 := filepath.Clean(filepath.Join(wd, "..", "assets", "lq-pak1.zip"))

	if !fileExists(quake106) || !fileExists(lqpak1) {
		t.Skip("assets not present in this checkout")
	}

	dataDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "gamedata.json")

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

	t.Setenv("GAMEDATA_PATH", cfgPath)

	if err := bootstrapGameData(context.Background(), dataDir); err != nil {
		t.Fatalf("bootstrapGameData: %v", err)
	}

	root := filepath.Join(dataDir, "id1", "common")
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
