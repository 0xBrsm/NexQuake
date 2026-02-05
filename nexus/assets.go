package main

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

	"github.com/brstm/NexQuake/nexus/quake106"
)

type gameDataEntry struct {
	Game   string   `json:"game"`
	Server []string `json:"server,omitempty"`
	Common []string `json:"common,omitempty"`
	Client []string `json:"client,omitempty"`
	Force  bool     `json:"force,omitempty"`
}

func bootstrapGameData(ctx context.Context, dataDir string) error {
	entries, src, err := loadGameDataEntries(dataDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	if !dirWritable(dataDir) {
		if strings.TrimSpace(os.Getenv("QUICKSTART")) != "" {
			infof("Game data bootstrap skipped (not writable): %s", dataDir)
		}
		return nil
	}

	for i, ent := range entries {
		game := strings.TrimSpace(ent.Game)
		if game == "" {
			return fmt.Errorf("gamedata.json[%d]: missing game", i)
		}

		if len(ent.Server) == 0 && len(ent.Common) == 0 && len(ent.Client) == 0 {
			return fmt.Errorf("gamedata.json[%d]: no layers for %s (config=%s)", i, game, src)
		}

		if err := installLayer(ctx, dataDir, game, "common", ent.Common, ent.Force, i); err != nil {
			return fmt.Errorf("gamedata.json[%d]: %w (config=%s)", i, err, src)
		}
		if err := installLayer(ctx, dataDir, game, "server", ent.Server, ent.Force, i); err != nil {
			return fmt.Errorf("gamedata.json[%d]: %w (config=%s)", i, err, src)
		}
		if err := installLayer(ctx, dataDir, game, "client", ent.Client, ent.Force, i); err != nil {
			return fmt.Errorf("gamedata.json[%d]: %w (config=%s)", i, err, src)
		}
	}
	return nil
}

func installLayer(ctx context.Context, dataDir, game, layer string, sources []string, force bool, entryIndex int) error {
	if len(sources) == 0 {
		return nil
	}
	destRoot := filepath.Join(dataDir, game, layer)
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
	for _, zf := range zr.File {
		name := strings.ReplaceAll(zf.Name, `\`, `/`)
		name = strings.TrimLeft(name, "/")

		if name == "" || strings.HasSuffix(name, "/") || zf.FileInfo().IsDir() {
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

func loadGameDataEntries(dataDir string) ([]gameDataEntry, string, error) {
	quickstart := strings.TrimSpace(os.Getenv("QUICKSTART"))
	if quickstart == "" {
		quickstart = "minimal"
	}
	if strings.Contains(quickstart, "..") || strings.ContainsAny(quickstart, `/\`) {
		return nil, "env:QUICKSTART", fmt.Errorf("invalid QUICKSTART: %q", quickstart)
	}

	path := filepath.Join(dataDir, quickstart+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Default QUICKSTART is "minimal". If the user bind-mounts a data dir or
			// otherwise doesn't have the stock manifests present, treat as disabled.
			return nil, path, nil
		}
		return nil, path, fmt.Errorf("read config: %w", err)
	}
	var entries []gameDataEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, path, fmt.Errorf("parse config: %w", err)
	}
	return entries, path, nil
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
