package assets

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type cdManifestEntry struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

func buildCDManifest(cdDir string) ([]cdManifestEntry, error) {
	st, err := os.Stat(cdDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !st.IsDir() {
		return nil, nil
	}

	var out []cdManifestEntry
	err = filepath.WalkDir(cdDir, func(full string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		rel, relErr := filepath.Rel(cdDir, full)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !isCDTrackFile(rel) {
			return nil
		}

		out = append(out, cdManifestEntry{
			Path: rel,
			URL:  "/cd-stream/" + escapeURLPathPreserveSlashes(rel),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(out, func(a, b cdManifestEntry) int { return cmp.Compare(a.Path, b.Path) })
	return out, nil
}

func isCDTrackFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".ogg") || strings.HasSuffix(lower, ".mp3")
}
