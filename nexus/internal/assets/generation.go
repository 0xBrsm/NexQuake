package assets

import (
	"encoding/binary"
	"hash"
	"hash/fnv"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// manifestGenTTL caps how often the fingerprint is recomputed. The SSE snapshot
// build calls ManifestGeneration ~2x/sec; this keeps it a cache hit between
// recomputes. Manifest changes propagate within this interval plus the SSE poll.
const manifestGenTTL = 3 * time.Second

// ManifestGeneration returns a short fingerprint of the client asset tree (each
// mod's common/ + client/ layers, plus the cd dir). It changes whenever a
// client-visible file is added, removed, renamed, or modified. It is a stat-only
// walk (path + size + mtime) — no file reads, no pak parsing — so a changed .pak
// is caught by its own size/mtime and the client re-explodes it on /gamedir. The
// value is cached for manifestGenTTL.
func (g *HashedAssetServer) ManifestGeneration() string {
	now := time.Now()
	g.genMu.Lock()
	defer g.genMu.Unlock()
	if g.genValue != "" && now.Sub(g.genAt) < manifestGenTTL {
		return g.genValue
	}
	g.genValue = g.computeManifestGeneration()
	g.genAt = now
	return g.genValue
}

func (g *HashedAssetServer) computeManifestGeneration() string {
	h := fnv.New64a()
	var buf [8]byte

	mods, _ := ListMods(g.gameDir)
	sort.Strings(mods) // stable order regardless of ListMods' guarantees
	for _, mod := range mods {
		for _, layer := range clientLayers {
			fingerprintTree(h, filepath.Join(g.gameDir, mod, layer), buf[:])
		}
	}
	if g.cdDir != "" {
		fingerprintTree(h, g.cdDir, buf[:])
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

// fingerprintTree folds each file's (relpath, size, mtime) under root into h.
// WalkDir visits lexically, so the fold is order-stable for an unchanged tree.
// A missing or unreadable root contributes nothing (skipped), which is correct:
// an absent layer is indistinguishable from an empty one.
func fingerprintTree(h hash.Hash, root string, buf []byte) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		_, _ = h.Write([]byte(rel))
		binary.LittleEndian.PutUint64(buf, uint64(info.Size()))
		_, _ = h.Write(buf)
		binary.LittleEndian.PutUint64(buf, uint64(info.ModTime().UnixNano()))
		_, _ = h.Write(buf)
		return nil
	})
}
