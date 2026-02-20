package assets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const headerNexQuakeRef = "X-NexQuake-Ref"
const defaultGatewaySessionTTL = 37 * time.Minute

type startManifestEntry struct {
	Path string `json:"path"`
}

type startClientConfig struct {
	PrefetchConcurrency int      `json:"prefetchConcurrency"`
	SMenuOnFirstLoad    bool     `json:"smenuOnFirstLoad"`
	SendArgs            []string `json:"sendArgs,omitempty"`
	URLArgs             bool     `json:"urlArgs"`
}

type startManifestBundle struct {
	Client startClientConfig               `json:"client"`
	Game   map[string][]startManifestEntry `json:"game"`
	CD     []startManifestEntry            `json:"cd,omitempty"`
}

type readSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

type sectionReadSeekCloser struct {
	io.ReadSeeker
	closeFn func() error
}

func (s *sectionReadSeekCloser) Close() error {
	return s.closeFn()
}

type hashedAsset struct {
	name        string
	modTime     time.Time
	contentType string
	open        func() (readSeekCloser, error)
}

type hashedSnapshot struct {
	bundle startManifestBundle
	assets map[string]hashedAsset
}

type gatewaySession struct {
	createdAt time.Time
	assets    map[string]hashedAsset
}

// HashedAssetGateway serves a bootstrap manifest and hash-addressed asset
// requests for both VFS game data and CD tracks.
type HashedAssetGateway struct {
	gameDir             string
	cdDir               string
	pakCache            *PakIndexCache
	prefetchConcurrency int
	smenuOnFirstLoad    bool
	sendArgs            []string
	urlArgs             bool
	sessionTTL          time.Duration

	mu           sync.RWMutex
	sessions     map[string]*gatewaySession
	assetsByHash map[string]hashedAsset
}

func NewHashedAssetGateway(gameDir, cdDir string, pakCache *PakIndexCache, prefetchConcurrency int, smenuOnFirstLoad bool, sendArgs []string, urlArgs bool) *HashedAssetGateway {
	if pakCache == nil {
		pakCache = NewPakIndexCache()
	}
	return &HashedAssetGateway{
		gameDir:             gameDir,
		cdDir:               cdDir,
		pakCache:            pakCache,
		prefetchConcurrency: prefetchConcurrency,
		smenuOnFirstLoad:    smenuOnFirstLoad,
		sendArgs:            append([]string(nil), sendArgs...),
		urlArgs:             urlArgs,
		sessionTTL:          defaultGatewaySessionTTL,
		sessions:            make(map[string]*gatewaySession),
		assetsByHash:        make(map[string]hashedAsset),
	}
}

func (g *HashedAssetGateway) StartHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		ref := newGatewayRef()
		snap, err := g.buildSnapshot(ref)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(snap.bundle.Game) == 0 {
			http.NotFound(w, r)
			return
		}

		now := time.Now()
		g.mu.Lock()
		g.pruneExpiredLocked(now)
		g.sessions[ref] = &gatewaySession{
			createdAt: now,
			assets:    snap.assets,
		}
		for hash, asset := range snap.assets {
			g.assetsByHash[hash] = asset
		}
		g.mu.Unlock()

		manifestBytes, err := json.Marshal(snap.bundle)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		encodedManifest := base64.StdEncoding.EncodeToString(manifestBytes)

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set(headerNexQuakeRef, ref)
		_, _ = io.WriteString(w, encodedManifest)
	}
}

func (g *HashedAssetGateway) AssetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		hash := strings.TrimPrefix(r.URL.Path, "/nq/")
		hash = strings.TrimSpace(hash)
		if hash == "" || strings.Contains(hash, "/") {
			http.NotFound(w, r)
			return
		}

		asset, ok := g.lookupAsset(hash)
		if !ok {
			http.NotFound(w, r)
			return
		}

		rc, err := asset.open()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer rc.Close()

		if asset.contentType != "" {
			w.Header().Set("Content-Type", asset.contentType)
		}
		w.Header().Set("Cache-Control", "private, no-store")
		http.ServeContent(w, r, asset.name, asset.modTime, rc)
	}
}

func (g *HashedAssetGateway) lookupAsset(hash string) (hashedAsset, bool) {
	g.mu.RLock()
	if asset, ok := g.assetsByHash[hash]; ok {
		g.mu.RUnlock()
		return asset, true
	}
	g.mu.RUnlock()

	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneExpiredLocked(time.Now())
	asset, ok := g.assetsByHash[hash]
	return asset, ok
}

func (g *HashedAssetGateway) pruneExpiredLocked(now time.Time) {
	for ref, session := range g.sessions {
		if now.Sub(session.createdAt) <= g.sessionTTL {
			continue
		}
		for hash := range session.assets {
			delete(g.assetsByHash, hash)
		}
		delete(g.sessions, ref)
	}
}

func (g *HashedAssetGateway) buildSnapshot(ref string) (*hashedSnapshot, error) {
	mods, err := ListMods(g.gameDir)
	if err != nil {
		return nil, err
	}

	bundle := startManifestBundle{
		Client: startClientConfig{
			PrefetchConcurrency: g.prefetchConcurrency,
			SMenuOnFirstLoad:    g.smenuOnFirstLoad,
			SendArgs:            append([]string(nil), g.sendArgs...),
			URLArgs:             g.urlArgs,
		},
		Game: make(map[string][]startManifestEntry, len(mods)),
	}
	assets := make(map[string]hashedAsset)

	for _, mod := range mods {
		manifest, manifestErr := buildVFSManifest(g.gameDir, mod, g.pakCache)
		if manifestErr != nil {
			return nil, manifestErr
		}

		entries := make([]startManifestEntry, 0, len(manifest))
		for _, ent := range manifest {
			key := normalizeVFSKey(ent.Path)
			if key == "" {
				continue
			}
			hash := hashAssetKey(ref, "mod:"+mod+":"+key)
			asset, err := g.resolveVFSAsset(ent.URL, ent.Path)
			if err != nil {
				return nil, err
			}
			if err := addAsset(assets, hash, asset, ent.Path); err != nil {
				return nil, err
			}
			entries = append(entries, startManifestEntry{
				Path: ent.Path,
			})
		}
		bundle.Game[mod] = entries
	}

	cdManifest, err := buildCDManifest(g.cdDir)
	if err != nil {
		return nil, err
	}
	if len(cdManifest) > 0 {
		bundle.CD = make([]startManifestEntry, 0, len(cdManifest))
		for _, ent := range cdManifest {
			key := normalizeVFSKey(ent.Path)
			if key == "" {
				continue
			}
			hash := hashAssetKey(ref, "cd:"+key)
			asset, err := g.resolveCDAsset(ent.URL)
			if err != nil {
				return nil, err
			}
			if err := addAsset(assets, hash, asset, ent.Path); err != nil {
				return nil, err
			}
			bundle.CD = append(bundle.CD, startManifestEntry{
				Path: ent.Path,
			})
		}
	}

	return &hashedSnapshot{
		bundle: bundle,
		assets: assets,
	}, nil
}

func (g *HashedAssetGateway) resolveVFSAsset(assetURL, fallbackName string) (hashedAsset, error) {
	switch {
	case strings.HasPrefix(assetURL, "/game/"):
		return resolveEscapedFileAsset(g.gameDir, strings.TrimPrefix(assetURL, "/game/"))
	case strings.HasPrefix(assetURL, "/pak-extract/"):
		rel := strings.TrimPrefix(assetURL, "/pak-extract/")
		parts, err := decodeEscapedParts(rel)
		if err != nil {
			return hashedAsset{}, err
		}
		if len(parts) < 4 {
			return hashedAsset{}, fmt.Errorf("invalid pak extract url: %s", assetURL)
		}

		mod := parts[0]
		layer := parts[1]
		pakName := parts[2]
		internal := strings.Join(parts[3:], "/")
		key := normalizeVFSKey(internal)
		if key == "" {
			return hashedAsset{}, fmt.Errorf("invalid pak path: %q", internal)
		}

		pakPath := filepath.Join(g.gameDir, mod, layer, pakName)
		idx, err := g.pakCache.Get(pakPath)
		if err != nil {
			return hashedAsset{}, err
		}

		entry, ok := idx.entries[key]
		if !ok {
			return hashedAsset{}, fmt.Errorf("missing pak entry: %s", internal)
		}

		st, err := os.Stat(pakPath)
		if err != nil {
			return hashedAsset{}, err
		}

		entryName := path.Base(entry.Name)
		if entryName == "" {
			entryName = fallbackName
		}

		return hashedAsset{
			name:        entryName,
			modTime:     st.ModTime(),
			contentType: contentTypeForAsset(entryName),
			open: func() (readSeekCloser, error) {
				f, openErr := os.Open(pakPath)
				if openErr != nil {
					return nil, openErr
				}
				section := io.NewSectionReader(f, entry.Offset, entry.Size)
				return &sectionReadSeekCloser{
					ReadSeeker: section,
					closeFn:    f.Close,
				}, nil
			},
		}, nil
	default:
		return hashedAsset{}, fmt.Errorf("unsupported asset url: %s", assetURL)
	}
}

func (g *HashedAssetGateway) resolveCDAsset(assetURL string) (hashedAsset, error) {
	if !strings.HasPrefix(assetURL, "/cd-stream/") {
		return hashedAsset{}, fmt.Errorf("unsupported cd url: %s", assetURL)
	}
	return resolveEscapedFileAsset(g.cdDir, strings.TrimPrefix(assetURL, "/cd-stream/"))
}

func newFileAsset(fullPath, fallbackName string) (hashedAsset, error) {
	st, err := os.Stat(fullPath)
	if err != nil {
		return hashedAsset{}, err
	}
	if st.IsDir() {
		return hashedAsset{}, fmt.Errorf("expected file, got directory: %s", fullPath)
	}

	name := path.Base(filepath.ToSlash(fullPath))
	if name == "" {
		name = fallbackName
	}

	return hashedAsset{
		name:        name,
		modTime:     st.ModTime(),
		contentType: contentTypeForAsset(name),
		open: func() (readSeekCloser, error) {
			return os.Open(fullPath)
		},
	}, nil
}

func resolveEscapedFileAsset(rootDir, raw string) (hashedAsset, error) {
	parts, err := decodeEscapedParts(raw)
	if err != nil {
		return hashedAsset{}, err
	}
	rel := strings.Join(parts, "/")
	full, err := safeJoin(rootDir, filepath.FromSlash(rel))
	if err != nil {
		return hashedAsset{}, err
	}
	return newFileAsset(full, path.Base(rel))
}

func decodeEscapedParts(raw string) ([]string, error) {
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return nil, fmt.Errorf("empty path")
	}
	parts := strings.Split(raw, "/")
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return nil, err
		}
		parts[i] = decoded
	}
	return parts, nil
}

func contentTypeForAsset(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".ogg":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".pak", ".data":
		return "application/octet-stream"
	default:
		return ""
	}
}

func hashAssetKey(ref, key string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(ref + ":" + key))
	return fmt.Sprintf("%016x", h.Sum64())
}

func addAsset(assets map[string]hashedAsset, hash string, asset hashedAsset, path string) error {
	if _, exists := assets[hash]; exists {
		return fmt.Errorf("hash collision for %q", path)
	}
	assets[hash] = asset
	return nil
}

func newGatewayRef() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
