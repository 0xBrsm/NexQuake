// Package assets manages the NexQuake game-asset pipeline: scanning and serving
// VFS (Virtual File System) manifests built from layered mod directories and .pak
// archives, streaming CD audio tracks, and bootstrapping missing game data on
// first run.
//
// # Layout
//
// Each mod lives under GAME_DIR/<mod>/ and is split into three layers:
//
//	common/  – shared data (paks, loose files)
//	server/  – server-only overrides (merged into the runtime basedir)
//	client/  – client-only assets (served to browsers; override common in VFS)
//
// # Key types
//
//	HashedAssetServer  – start-manifest + hash-addressed asset serving
//	PakIndexCache       – thread-safe cache of parsed .pak directory entries
//
// Typical usage in the nexus HTTP mux:
//
//	gw := assets.NewHashedAssetServer(gameDir, cdDir, pakCache, concurrency, smenu, args, urlArgs)
//	gw.SetErrorf(errorf)
//	mux.Handle("/start", http.HandlerFunc(gw.StartHandler()))
//	mux.Handle("/nq/",   http.HandlerFunc(gw.AssetHandler()))
package assets

import (
	"cmp"
	"fmt"
	"io/fs"
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

// buildVFSManifestWithWarnings produces a sorted manifest of all files
// available for one mod. warnf may be nil; it is used by upper layers (e.g.
// the manifest gateway) to log non-fatal scan issues without spamming logs
// when the same scan is run from a hot path.
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
	idx, err := pakCache.get(pakPath)
	if err != nil {
		return err
	}

	for _, entry := range idx.entries {
		// idx.entries are keyed by normalized names, but keep the original entry for URL path.
		key := normalizeVFSKey(entry.name)
		if key == "" {
			continue
		}
		byKey[key] = vfsManifestEntry{
			Path: key,
			URL:  "/pak-extract/" + escapeURLPathPreserveSlashes(mod) + "/" + escapeURLPathPreserveSlashes(layer) + "/" + escapeURLPathPreserveSlashes(pakRel) + "/" + escapeURLPathPreserveSlashes(entry.name),
			Size: entry.size,
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
