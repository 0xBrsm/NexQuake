package assets

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/quake106"
)

type gameDataEntry struct {
	Game   string   `json:"game"`
	Server []string `json:"server,omitempty"`
	Common []string `json:"common,omitempty"`
	Client []string `json:"client,omitempty"`
	Force  bool     `json:"force,omitempty"`
}

// BootstrapGameData installs game data from quickstart manifests.
// logf is used for informational log messages (may be nil).
func BootstrapGameData(ctx context.Context, gameDir string, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	entries, src, err := loadGameDataEntries(gameDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	if !dirWritable(gameDir) {
		if strings.TrimSpace(os.Getenv("QUICKSTART")) != "" {
			logf("Game data bootstrap skipped (not writable): %s", gameDir)
		}
		return nil
	}

	for i, ent := range entries {
		game := strings.TrimSpace(ent.Game)
		if game == "" {
			return fmt.Errorf("quickstart[%d]: missing game", i)
		}

		if len(ent.Server) == 0 && len(ent.Common) == 0 && len(ent.Client) == 0 {
			return fmt.Errorf("quickstart[%d]: no layers for %s (config=%s)", i, game, src)
		}

		if err := installLayer(ctx, gameDir, game, "common", ent.Common, ent.Force); err != nil {
			return fmt.Errorf("quickstart[%d]: %w (config=%s)", i, err, src)
		}
		if err := installLayer(ctx, gameDir, game, "server", ent.Server, ent.Force); err != nil {
			return fmt.Errorf("quickstart[%d]: %w (config=%s)", i, err, src)
		}
		if err := installLayer(ctx, gameDir, game, "client", ent.Client, ent.Force); err != nil {
			return fmt.Errorf("quickstart[%d]: %w (config=%s)", i, err, src)
		}
	}
	return nil
}

func installLayer(ctx context.Context, gameDir, game, layer string, sources []string, force bool) error {
	if len(sources) == 0 {
		return nil
	}
	destRoot := filepath.Join(gameDir, game, layer)
	if dirHasEntries(destRoot) && !force {
		return nil
	}
	for j, urlStr := range sources {
		urlStr = strings.TrimSpace(urlStr)
		if urlStr == "" {
			return fmt.Errorf("%s[%d]: empty url for %s/%s", layer, j, game, layer)
		}
		if err := installFromSource(ctx, urlStr, destRoot); err != nil {
			return fmt.Errorf("failed to install %s/%s from %s: %w", game, layer, urlStr, err)
		}
	}
	return nil
}

func installFromSource(ctx context.Context, urlStr, destRoot string) error {
	tmpPath, cleanup, err := downloadToTemp(ctx, urlStr)
	if err != nil {
		return err
	}
	defer cleanup()

	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}

	sum, err := sha256FileHex(tmpPath)
	if err != nil {
		return err
	}
	if sum == quake106.ZipSHA256 {
		return quake106.ExtractPak0(&zr.Reader, destRoot)
	}

	return extractZipGeneric(&zr.Reader, destRoot)
}

func extractZipGeneric(zr *zip.Reader, destRoot string) error {
	stripPrefix := zipSingleRootPrefix(zr)

	for _, zf := range zr.File {
		name := strings.ReplaceAll(zf.Name, `\`, `/`)
		name = strings.TrimLeft(name, "/")

		if name == "" || strings.HasSuffix(name, "/") || zf.FileInfo().IsDir() {
			continue
		}
		if stripPrefix != "" && strings.HasPrefix(name, stripPrefix) {
			name = strings.TrimPrefix(name, stripPrefix)
		}
		if name == "" {
			continue
		}

		destPath, err := safeJoin(destRoot, name)
		if err != nil {
			return err
		}

		rc, openErr := zf.Open()
		if openErr != nil {
			return openErr
		}
		copyErr := copyToFile(destPath, rc)
		closeErr := rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func zipSingleRootPrefix(zr *zip.Reader) string {
	root := ""
	sawFile := false

	for _, zf := range zr.File {
		name := strings.ReplaceAll(zf.Name, `\`, `/`)
		name = strings.TrimLeft(name, "/")
		if name == "" || strings.HasSuffix(name, "/") || zf.FileInfo().IsDir() {
			continue
		}

		sawFile = true
		slash := strings.IndexByte(name, '/')
		if slash <= 0 {
			// At least one file already at archive root: keep paths as-is.
			return ""
		}

		part := name[:slash]
		if root == "" {
			root = part
			continue
		}
		if part != root {
			// Multiple different top-level roots: keep paths as-is.
			return ""
		}
	}

	if !sawFile || root == "" {
		return ""
	}
	return root + "/"
}

// --- Helpers ---

func copyToFile(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func loadGameDataEntries(gameDir string) ([]gameDataEntry, string, error) {
	raw := strings.TrimSpace(os.Getenv("QUICKSTART"))
	if raw == "" {
		raw = "minimal"
	}

	names := splitCSV(raw)

	var allEntries []gameDataEntry
	var sources []string

	for _, name := range names {
		if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
			return nil, "env:QUICKSTART", fmt.Errorf("invalid QUICKSTART name: %q", name)
		}

		path := filepath.Join(gameDir, name+".json")
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// For single-value QUICKSTART, missing manifest is a silent no-op
				// (preserves backward compatibility). For multi-value, skip silently
				// so partial manifests still work.
				if len(names) == 1 {
					return nil, path, nil
				}
				continue
			}
			return nil, path, fmt.Errorf("read config %q: %w", name, err)
		}
		var entries []gameDataEntry
		if err := json.Unmarshal(b, &entries); err != nil {
			return nil, path, fmt.Errorf("parse config %q: %w", name, err)
		}
		allEntries = append(allEntries, entries...)
		sources = append(sources, path)
	}

	src := strings.Join(sources, ", ")
	return allEntries, src, nil
}

// splitCSV splits a comma-separated string into trimmed, non-empty parts.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func downloadToTemp(ctx context.Context, urlStr string) (string, func(), error) {
	tmp, err := os.CreateTemp("", "nexquake-*.tmp")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }

	if strings.HasPrefix(urlStr, "file://") {
		src, err := os.Open(strings.TrimPrefix(urlStr, "file://"))
		if err != nil {
			cleanup()
			return "", nil, err
		}
		defer src.Close()
		if _, err := io.Copy(tmp, src); err != nil {
			cleanup()
			return "", nil, err
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		req.Header.Set("User-Agent", "nexquake-nexus")

		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			cleanup()
			return "", nil, fmt.Errorf("status %s", resp.Status)
		}
		if _, err := io.Copy(tmp, resp.Body); err != nil {
			cleanup()
			return "", nil, err
		}
	}

	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp.Name(), cleanup, nil
}

func dirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".nq-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

func dirHasEntries(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	return err == nil
}

func safeJoin(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("invalid path: %q", rel)
	}
	rootClean := filepath.Clean(root)
	path := filepath.Join(rootClean, clean)
	rootPrefix := rootClean + string(filepath.Separator)
	if path != rootClean && !strings.HasPrefix(path+string(filepath.Separator), rootPrefix) {
		return "", fmt.Errorf("path escape: %q", rel)
	}
	return path, nil
}

func sha256FileHex(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
