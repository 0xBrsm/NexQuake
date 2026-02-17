package gamedata

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildVFSManifest_LayersAndPakExplode(t *testing.T) {
	dataDir := t.TempDir()
	mod := "id1"

	commonDir := filepath.Join(dataDir, mod, "common")
	clientDir := filepath.Join(dataDir, mod, "client")
	if err := os.MkdirAll(filepath.Join(commonDir, "gfx"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(clientDir, "gfx"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Loose file in common.
	if err := os.WriteFile(filepath.Join(commonDir, "foo.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Pak in common with gfx/palette.lmp.
	pakPath := filepath.Join(commonDir, "pak0.pak")
	writeTestPak(t, pakPath, map[string][]byte{
		"gfx/palette.lmp": []byte{0x10, 0x11},
	})

	// Override gfx/palette.lmp with a loose file in client.
	if err := os.WriteFile(filepath.Join(clientDir, "gfx", "palette.lmp"), []byte("override"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	manifest, err := buildVFSManifest(dataDir, mod, NewPakIndexCache())
	if err != nil {
		t.Fatalf("buildVFSManifest: %v", err)
	}

	get := func(p string) (vfsManifestEntry, bool) {
		for _, e := range manifest {
			if e.Path == p {
				return e, true
			}
		}
		return vfsManifestEntry{}, false
	}

	if e, ok := get("foo.txt"); !ok || !strings.HasPrefix(e.URL, "/data/id1/common/foo.txt") {
		t.Fatalf("expected foo.txt from common, got: ok=%v entry=%+v", ok, e)
	}

	// Should come from client (override), not /pak-extract from common.
	if e, ok := get("gfx/palette.lmp"); !ok || !strings.HasPrefix(e.URL, "/data/id1/client/gfx/palette.lmp") {
		t.Fatalf("expected gfx/palette.lmp from client override, got: ok=%v entry=%+v", ok, e)
	}
}

func TestBuildVFSManifest_LooseBeatsPakWithinLayer(t *testing.T) {
	dataDir := t.TempDir()
	mod := "id1"

	commonDir := filepath.Join(dataDir, mod, "common")
	if err := os.MkdirAll(filepath.Join(commonDir, "gfx"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Pak in common with gfx/palette.lmp.
	pakPath := filepath.Join(commonDir, "pak0.pak")
	writeTestPak(t, pakPath, map[string][]byte{
		"gfx/palette.lmp": []byte{0x10, 0x11},
	})

	// Loose file in the same gamedir should override pak.
	if err := os.WriteFile(filepath.Join(commonDir, "gfx", "palette.lmp"), []byte("override"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	manifest, err := buildVFSManifest(dataDir, mod, NewPakIndexCache())
	if err != nil {
		t.Fatalf("buildVFSManifest: %v", err)
	}

	var got vfsManifestEntry
	found := false
	for _, e := range manifest {
		if e.Path == "gfx/palette.lmp" {
			got = e
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing gfx/palette.lmp")
	}
	if !strings.HasPrefix(got.URL, "/data/id1/common/gfx/palette.lmp") {
		t.Fatalf("expected loose file to win over pak, got url=%q", got.URL)
	}
}

func TestBuildVFSManifest_PakOrderWithinLayer(t *testing.T) {
	dataDir := t.TempDir()
	mod := "id1"

	commonDir := filepath.Join(dataDir, mod, "common")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeTestPak(t, filepath.Join(commonDir, "pak0.pak"), map[string][]byte{
		"docs/readme.txt": []byte("from pak0"),
	})
	writeTestPak(t, filepath.Join(commonDir, "pak1.pak"), map[string][]byte{
		"docs/readme.txt": []byte("from pak1"),
	})

	manifest, err := buildVFSManifest(dataDir, mod, NewPakIndexCache())
	if err != nil {
		t.Fatalf("buildVFSManifest: %v", err)
	}

	var got vfsManifestEntry
	found := false
	for _, e := range manifest {
		if e.Path == "docs/readme.txt" {
			got = e
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing docs/readme.txt")
	}
	if !strings.HasPrefix(got.URL, "/pak-extract/id1/common/pak1.pak/docs/readme.txt") {
		t.Fatalf("expected pak1 to override pak0, got url=%q", got.URL)
	}
}

func TestNewDataManifestBundleHandler_ReturnsDirectModManifests(t *testing.T) {
	dataDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dataDir, "id1", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "ctf", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "cfgmod"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "id1", "common", "base.txt"), []byte("id1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "ctf", "common", "ctf.txt"), []byte("ctf"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	handler := NewDataManifestBundleHandler(dataDir, NewPakIndexCache(), 7)

	req := httptest.NewRequest(http.MethodGet, "/data-manifest", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerVFSPrefetchConcurrency); got != "7" {
		t.Fatalf("prefetch header=%q want %q", got, "7")
	}

	var bundle vfsManifestBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}

	hasPath := func(entries []vfsManifestEntry, want string) bool {
		for _, entry := range entries {
			if entry.Path == want {
				return true
			}
		}
		return false
	}

	if !hasPath(bundle.Mods["id1"], "base.txt") {
		t.Fatalf("id1 manifest missing base.txt: %+v", bundle.Mods["id1"])
	}
	if !hasPath(bundle.Mods["ctf"], "ctf.txt") {
		t.Fatalf("ctf manifest missing ctf.txt: %+v", bundle.Mods["ctf"])
	}
	if hasPath(bundle.Mods["ctf"], "base.txt") {
		t.Fatalf("ctf manifest unexpectedly duplicated id1 file: %+v", bundle.Mods["ctf"])
	}
	cfgEntries, ok := bundle.Mods["cfgmod"]
	if !ok {
		t.Fatalf("cfgmod missing from manifest bundle: %+v", bundle.Mods)
	}
	if len(cfgEntries) != 0 {
		t.Fatalf("cfgmod expected empty manifest, got %+v", cfgEntries)
	}
}

func TestNewDataManifestBundleHandler_NoModsReturnsNotFound(t *testing.T) {
	dataDir := t.TempDir()
	handler := NewDataManifestBundleHandler(dataDir, NewPakIndexCache(), 4)
	req := httptest.NewRequest(http.MethodGet, "/data-manifest", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestListMods_IncludesEmptyModDirs(t *testing.T) {
	dataDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dataDir, "id1", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "mod2", "server"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "cfgmod"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "junk"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "junk", "note.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, ".hidden", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "thismodnameistoolong", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "bad\tname", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	mods, err := ListMods(dataDir)
	if err != nil {
		t.Fatalf("ListMods: %v", err)
	}

	if !slices.Contains(mods, "id1") || !slices.Contains(mods, "mod2") || !slices.Contains(mods, "cfgmod") {
		t.Fatalf("expected id1, mod2, and cfgmod, got %v", mods)
	}
	if slices.Contains(mods, "junk") {
		t.Fatalf("did not expect junk dir, got %v", mods)
	}
	if slices.Contains(mods, ".hidden") {
		t.Fatalf("did not expect hidden dir, got %v", mods)
	}
	if slices.Contains(mods, "thismodnameistoolong") {
		t.Fatalf("did not expect >15-byte mod name, got %v", mods)
	}
	if slices.Contains(mods, "bad\tname") {
		t.Fatalf("did not expect control-char mod name, got %v", mods)
	}
}

func TestMaterializeMergedModDir_ServerOverridesCommon_AndIgnoresRoot(t *testing.T) {
	dataDir := t.TempDir()
	runtimeRoot := t.TempDir()
	mod := "id1"

	commonDir := filepath.Join(dataDir, mod, "common")
	serverDir := filepath.Join(dataDir, mod, "server")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(commonDir, "file.txt"), []byte("common"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "file.txt"), []byte("server"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, mod, "root.txt"), []byte("root"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := materializeMergedModDir(runtimeRoot, dataDir, mod); err != nil {
		t.Fatalf("materializeMergedModDir: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(runtimeRoot, mod, "file.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "server" {
		t.Fatalf("expected server override, got %q", string(got))
	}

	if _, err := os.Stat(filepath.Join(runtimeRoot, mod, "root.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected root.txt to be ignored, stat err=%v", err)
	}
}

func TestMaterializeMergedModDir_DoesNotSymlinkDirs(t *testing.T) {
	dataDir := t.TempDir()
	runtimeRoot := t.TempDir()
	mod := "ctf"

	commonDir := filepath.Join(dataDir, mod, "common", "maps")
	serverDir := filepath.Join(dataDir, mod, "server", "maps")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Common provides a baseline file, server overrides it.
	if err := os.WriteFile(filepath.Join(commonDir, "dm3.ent"), []byte("common"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "dm3.ent"), []byte("server"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := materializeMergedModDir(runtimeRoot, dataDir, mod); err != nil {
		t.Fatalf("materializeMergedModDir: %v", err)
	}

	// The maps directory must be a real directory in the runtime overlay, not a symlink
	// into the (potentially read-only) source tree.
	mapsPath := filepath.Join(runtimeRoot, mod, "maps")
	if st, err := os.Lstat(mapsPath); err != nil {
		t.Fatalf("lstat maps: %v", err)
	} else if st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected maps dir to not be a symlink")
	}

	got, err := os.ReadFile(filepath.Join(runtimeRoot, mod, "maps", "dm3.ent"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "server" {
		t.Fatalf("expected server override, got %q", string(got))
	}
}
