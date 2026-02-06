package main

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

type VFSManifestEntry struct {
	Path string `json:"path"`
	URL  string `json:"url"`
	Size int64  `json:"size,omitempty"`
}

const headerVFSPrefetchConcurrency = "X-NQ-VFS-Prefetch-Concurrency"

func newDataManifestHandler(dataDir string, pakCache *pakIndexCache, prefetchConcurrency int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		mod := strings.TrimPrefix(r.URL.Path, "/data-manifest/")
		mod = strings.Trim(mod, "/")
		if mod == "" {
			http.Error(w, "missing mod", http.StatusBadRequest)
			return
		}
		if strings.Contains(mod, "..") || strings.ContainsAny(mod, `/\`) {
			http.Error(w, "invalid mod", http.StatusBadRequest)
			return
		}

		manifest, err := buildVFSManifest(dataDir, mod, pakCache)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(manifest) == 0 {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(headerVFSPrefetchConcurrency, strconv.Itoa(prefetchConcurrency))
		_ = json.NewEncoder(w).Encode(manifest)
	}
}

func buildVFSManifest(dataDir, mod string, pakCache *pakIndexCache) ([]VFSManifestEntry, error) {
	layers := []string{"common", "client"}

	// Later layers overwrite earlier ones.
	byKey := make(map[string]VFSManifestEntry)

	for _, layer := range layers {
		if err := overlayLayerIntoManifest(byKey, dataDir, mod, layer, pakCache); err != nil {
			return nil, err
		}
	}

	out := make([]VFSManifestEntry, 0, len(byKey))
	for _, v := range byKey {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b VFSManifestEntry) int { return cmp.Compare(a.Path, b.Path) })
	return out, nil
}

var pakNumberRE = regexp.MustCompile(`(?i)^pak(\d+)\.pak$`)

func overlayLayerIntoManifest(byKey map[string]VFSManifestEntry, dataDir, mod, layer string, pakCache *pakIndexCache) error {
	root := filepath.Join(dataDir, mod, layer)
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
		if err := explodePakIntoManifest(byKey, dataDir, mod, layer, full, pakRel, pakCache); err != nil {
			return err
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
		if rel == "." || rel == "" {
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

		byKey[key] = VFSManifestEntry{
			Path: key,
			URL:  "/data/" + escapeURLPathPreserveSlashes(filepath.ToSlash(filepath.Join(mod, layer, rel))),
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
		ak := pakSortKey(a)
		bk := pakSortKey(b)
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

func explodePakIntoManifest(byKey map[string]VFSManifestEntry, dataDir, mod, layer, pakPath, pakRel string, pakCache *pakIndexCache) error {
	if pakCache == nil {
		pakCache = newPakIndexCache()
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
		byKey[key] = VFSManifestEntry{
			Path: key,
			URL:  "/pak-extract/" + escapeURLPathPreserveSlashes(mod) + "/" + escapeURLPathPreserveSlashes(layer) + "/" + escapeURLPathPreserveSlashes(pakRel) + "/" + escapeURLPathPreserveSlashes(entry.Name),
			Size: entry.Size,
		}
	}
	return nil
}

func normalizeVFSKey(p string) string {
	clean, err := cleanVFSPath(p)
	if err != nil {
		return ""
	}
	return strings.ToLower(clean)
}

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
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("absolute path")
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

func listMods(dataDir string) ([]string, error) {
	ents, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("read DATA_DIR: %w", err)
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
		// Only treat directories as "mods" if they have at least one layer directory.
		// This avoids starting bogus servers for junk directories (or for a user who
		// accidentally bind-mounted a single mod dir as DATA_DIR).
		if !dirHasAnyLayer(filepath.Join(dataDir, name)) {
			continue
		}
		mods = append(mods, name)
	}
	return mods, nil
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

func prepareRuntimeBasedir(sourceDataDir string, mods []string) (string, error) {
	// The data dir is typically bind-mounted read-only. nqserver expects a writable basedir
	// containing per-mod directories; create an ephemeral overlay basedir and
	// populate it with symlinks into DATA_DIR (and allow the server to write
	// transient files alongside).
	runtimeRoot, err := os.MkdirTemp("", "nexquake-nexus-basedir-")
	if err != nil {
		return "", fmt.Errorf("create runtime basedir: %w", err)
	}

	// Materialize each detected mod as a merged directory:
	//   <mod>/common < <mod>/server
	for _, mod := range mods {
		if err := materializeMergedModDir(runtimeRoot, sourceDataDir, mod); err != nil {
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
	return filepath.WalkDir(srcRoot, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(srcRoot, full)
		if err != nil {
			return nil
		}
		if rel == "." || rel == "" {
			return nil
		}

		dst := filepath.Join(dstRoot, rel)

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
