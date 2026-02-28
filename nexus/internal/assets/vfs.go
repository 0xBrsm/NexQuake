package assets

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// vfsManifestEntry describes a single file in the virtual file system manifest.
type vfsManifestEntry struct {
	Path string `json:"path"`
	URL  string `json:"url"`
	Size int64  `json:"size,omitempty"`
}

type vfsManifestBundle struct {
	Mods map[string][]vfsManifestEntry `json:"mods"`
}

// headerVFSPrefetchConcurrency is the HTTP header name for VFS prefetch concurrency.
const headerVFSPrefetchConcurrency = "X-NQ-VFS-Prefetch-Concurrency"

// NewGameManifestBundleHandler returns all mod manifests in one response.
func NewGameManifestBundleHandler(gameDir string, pakCache *PakIndexCache, prefetchConcurrency int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		mods, err := ListMods(gameDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(mods) == 0 {
			http.NotFound(w, r)
			return
		}

		bundle := vfsManifestBundle{
			Mods: make(map[string][]vfsManifestEntry, len(mods)),
		}
		for _, mod := range mods {
			manifest, manifestErr := buildVFSManifest(gameDir, mod, pakCache)
			if manifestErr != nil {
				http.Error(w, manifestErr.Error(), http.StatusInternalServerError)
				return
			}
			bundle.Mods[mod] = manifest
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(headerVFSPrefetchConcurrency, strconv.Itoa(prefetchConcurrency))
		_ = json.NewEncoder(w).Encode(bundle)
	}
}

// buildVFSManifest produces a sorted manifest of all files available for one mod.
func buildVFSManifest(gameDir, mod string, pakCache *PakIndexCache) ([]vfsManifestEntry, error) {
	return buildVFSManifestWithWarnings(gameDir, mod, pakCache, nil)
}

func buildVFSManifestWithWarnings(gameDir, mod string, pakCache *PakIndexCache, warnf func(string, ...any)) ([]vfsManifestEntry, error) {
	layers := []string{"common", "client"}

	// Later layers overwrite earlier ones.
	byKey := make(map[string]vfsManifestEntry)

	for _, layer := range layers {
		if err := overlayLayerIntoManifest(byKey, gameDir, mod, layer, pakCache, warnf); err != nil {
			return nil, err
		}
	}

	out := make([]vfsManifestEntry, 0, len(byKey))
	for _, v := range byKey {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b vfsManifestEntry) int { return cmp.Compare(a.Path, b.Path) })
	return out, nil
}

var pakNumberRE = regexp.MustCompile(`(?i)^pak(\d+)\.pak$`)

func overlayLayerIntoManifest(byKey map[string]vfsManifestEntry, gameDir, mod, layer string, pakCache *PakIndexCache, warnf func(string, ...any)) error {
	root := filepath.Join(gameDir, mod, layer)
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil
	}

	// Quake precedence (within a single gamedir):
	//   loose files > pak files
	// and pak order (within a gamedir):
	//   pak0 < pak1 < ... (higher numbers override lower ones).
	//
	// Implement this deterministically by:
	//  1) exploding paks in ascending order (later paks overwrite earlier)
	//  2) overlaying loose files last (loose overwrites pak entries)
	paks, err := listLayerPakFiles(root)
	if err != nil {
		return err
	}
	for _, pakRel := range paks {
		full := filepath.Join(root, pakRel)
		if err := explodePakIntoManifest(byKey, mod, layer, full, pakRel, pakCache); err != nil {
			// Keep bootstrap resilient when optional/third-party paks are malformed.
			// Loose files (and other valid paks) still populate the manifest.
			if warnf != nil {
				warnf("skipping unreadable pak %q: %v", full, err)
			}
			continue
		}
	}

	return filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, full)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}

		relLower := strings.ToLower(rel)
		if strings.HasSuffix(relLower, ".pak") {
			// Current /pak-extract URL scheme assumes paks live at the layer root.
			if strings.Contains(rel, "/") {
				return fmt.Errorf("nested pak not supported: %s/%s/%s", mod, layer, rel)
			}
			// Root pak: already processed in the deterministic pak pass above.
			return nil
		}

		key := normalizeVFSKey(rel)
		if key == "" {
			return nil
		}

		var size int64
		if info, err := d.Info(); err == nil {
			size = info.Size()
		}

		byKey[key] = vfsManifestEntry{
			Path: key,
			URL:  "/game/" + escapeURLPathPreserveSlashes(filepath.ToSlash(filepath.Join(mod, layer, rel))),
			Size: size,
		}
		return nil
	})
}

func listLayerPakFiles(root string) ([]string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var paks []string
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".pak") {
			continue
		}
		paks = append(paks, name)
	}

	slices.SortFunc(paks, func(a, b string) int {
		ak, bk := pakSortKey(a), pakSortKey(b)
		if c := cmp.Compare(ak.group, bk.group); c != 0 {
			return c
		}
		if ak.group == 0 {
			if c := cmp.Compare(ak.num, bk.num); c != 0 {
				return c
			}
		}
		return cmp.Compare(ak.name, bk.name)
	})

	return paks, nil
}

type pakKey struct {
	group int // 0: pakN.pak numeric, 1: other *.pak
	num   int
	name  string
}

func pakSortKey(name string) pakKey {
	lower := strings.ToLower(strings.TrimSpace(name))
	m := pakNumberRE.FindStringSubmatch(lower)
	if len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return pakKey{group: 0, num: n, name: lower}
		}
	}
	return pakKey{group: 1, name: lower}
}

func explodePakIntoManifest(byKey map[string]vfsManifestEntry, mod, layer, pakPath, pakRel string, pakCache *PakIndexCache) error {
	if pakCache == nil {
		pakCache = NewPakIndexCache()
	}
	idx, err := pakCache.Get(pakPath)
	if err != nil {
		return err
	}

	for _, entry := range idx.entries {
		// idx.entries are keyed by normalized names, but keep the original entry for URL path.
		key := normalizeVFSKey(entry.Name)
		if key == "" {
			continue
		}
		byKey[key] = vfsManifestEntry{
			Path: key,
			URL:  "/pak-extract/" + escapeURLPathPreserveSlashes(mod) + "/" + escapeURLPathPreserveSlashes(layer) + "/" + escapeURLPathPreserveSlashes(pakRel) + "/" + escapeURLPathPreserveSlashes(entry.Name),
			Size: entry.Size,
		}
	}
	return nil
}

// normalizeVFSKey cleans and lowercases a VFS path.
func normalizeVFSKey(p string) string {
	clean, err := cleanVFSPath(p)
	if err != nil {
		return ""
	}
	return strings.ToLower(clean)
}

// cleanVFSPath validates and cleans a VFS path, rejecting traversals and absolute paths.
func cleanVFSPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimLeft(p, "/")
	p = path.Clean(p)
	if p == "." || p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "../") || p == ".." || strings.Contains(p, "/../") {
		return "", fmt.Errorf("path traversal")
	}
	return p, nil
}

func escapeURLPathPreserveSlashes(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// ---- Server runtime filesystem layout ----

// ListMods returns valid game directory names found under gameDir that either:
// - contain at least one layer directory (common, client, or server), or
// - are empty directories (used for client-only config mods with no data yet).
func ListMods(gameDir string) ([]string, error) {
	ents, err := os.ReadDir(gameDir)
	if err != nil {
		return nil, fmt.Errorf("read GAME_DIR: %w", err)
	}

	var mods []string
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !isValidQuakeGameDirName(name) {
			continue
		}
		modDir := filepath.Join(gameDir, name)
		// Treat directories as mods when they have layer dirs, or when they are
		// intentionally empty placeholders for client-side config mods.
		if !dirHasAnyLayer(modDir) && !dirIsEmpty(modDir) {
			continue
		}
		mods = append(mods, name)
	}
	return mods, nil
}

func isValidQuakeGameDirName(name string) bool {
	// Quake wire/UI gamedir fields are 15-byte C strings.
	if len(name) == 0 || len(name) > 15 {
		return false
	}
	// Keep Linux filename semantics from os.ReadDir; only reject control bytes
	// that can break protocol/logging.
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

func dirHasAnyLayer(modDir string) bool {
	for _, layer := range []string{"common", "client", "server"} {
		st, err := os.Stat(filepath.Join(modDir, layer))
		if err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func dirIsEmpty(modDir string) bool {
	ents, err := os.ReadDir(modDir)
	if err != nil {
		return false
	}
	return len(ents) == 0
}

// PrepareRuntimeBasedir creates an ephemeral overlay basedir with symlinks into
// sourceGameDir for each mod. The returned directory is writable for the server.
func PrepareRuntimeBasedir(sourceGameDir string, mods []string) (string, error) {
	runtimeRoot, err := os.MkdirTemp("", "nexquake-nexus-basedir-")
	if err != nil {
		return "", fmt.Errorf("create runtime basedir: %w", err)
	}

	// Materialize each detected mod as a merged directory:
	//   <mod>/common < <mod>/server
	for _, mod := range mods {
		if err := materializeMergedModDir(runtimeRoot, sourceGameDir, mod); err != nil {
			return "", err
		}
	}

	return runtimeRoot, nil
}

func materializeMergedModDir(runtimeRoot, sourceDataDir, mod string) error {
	dst := filepath.Join(runtimeRoot, mod)
	_ = os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create runtime mod dir %q: %w", mod, err)
	}

	layers := []string{
		filepath.Join(sourceDataDir, mod, "common"),
		filepath.Join(sourceDataDir, mod, "server"),
	}
	for _, src := range layers {
		st, err := os.Stat(src)
		if err != nil || !st.IsDir() {
			continue
		}
		if err := overlaySymlinks(dst, src); err != nil {
			return fmt.Errorf("overlay %s into %s: %w", src, dst, err)
		}
	}
	return nil
}

func overlaySymlinks(dstRoot, srcRoot string) error {
	seen := make(map[string]string)

	return filepath.WalkDir(srcRoot, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(srcRoot, full)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}

		lowerRel := strings.ToLower(filepath.ToSlash(rel))
		if prev, ok := seen[lowerRel]; ok && prev != rel {
			return fmt.Errorf("case-colliding paths in %s: %s and %s", srcRoot, prev, rel)
		}
		seen[lowerRel] = rel

		dst := filepath.Join(dstRoot, filepath.FromSlash(lowerRel))

		// Never symlink directories. If we symlink a directory from a read-only
		// source tree (e.g. /data), then later overlays that try to remove/replace
		// a file inside that directory will target the source path and fail with
		// EROFS/EPERM (and ultimately "file exists" on the new symlink).
		if d.IsDir() {
			// If a previous layer created a symlink here, replace it with a real dir.
			if st, err := os.Lstat(dst); err == nil && st.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(dst)
			}
			return os.MkdirAll(dst, 0o755)
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}

		// Remove anything at the destination (file, symlink, or directory).
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.RemoveAll(dst)
		}

		// Always link to the source path. This keeps runtimeRoot writable and
		// avoids copying large PAKs.
		return os.Symlink(full, dst)
	})
}
