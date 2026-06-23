package assets

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestManifestGenerationDetectsClientLayerChanges(t *testing.T) {
	gameDir := t.TempDir()
	writeFile(t, filepath.Join(gameDir, "id1", "common", "pak0.pak"), "abc")
	g := NewHashedAssetServer(gameDir, "", nil)

	gen0 := g.computeManifestGeneration()
	if gen0 == "" {
		t.Fatal("generation is empty")
	}
	// Stable when nothing changes.
	if g.computeManifestGeneration() != gen0 {
		t.Fatal("generation changed with no filesystem change")
	}

	// A new file in the client layer must bump the generation.
	writeFile(t, filepath.Join(gameDir, "id1", "client", "progs.dat"), "x")
	gen1 := g.computeManifestGeneration()
	if gen1 == gen0 {
		t.Fatal("generation did not change after a client-layer file was added")
	}

	// A change outside the client layers (server/) must NOT bump it — otherwise
	// server-only asset churn would needlessly force every client to refetch.
	writeFile(t, filepath.Join(gameDir, "id1", "server", "maps.txt"), "y")
	if g.computeManifestGeneration() != gen1 {
		t.Fatal("server-layer change leaked into the client manifest generation")
	}
}
