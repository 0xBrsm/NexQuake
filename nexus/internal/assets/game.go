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
	"github.com/google/shlex"
)

type gameDataEntry struct {
	Game   string   `json:"game,omitempty"`
	Base   string   `json:"base,omitempty"`
	Server []string `json:"server,omitempty"`
	Common []string `json:"common,omitempty"`
	Client []string `json:"client,omitempty"`
	Force  bool     `json:"force,omitempty"`
}

// QuickstartGame ensures GAME_DIR/servers.ini exists (created only if missing)
// and bootstraps missing mod data based on the -game values in servers.ini using
// CFG_DIR/game.json.
//
// servers.ini is treated as user-owned:
//   - If it exists, it is never modified.
//   - If it does not exist, it is created from CFG_DIR/servers.ini and populated
//     with one nqserver line for each valid QUICKSTART game entry
//     (QUICKSTART defaults to "ffa" when unset):
//     nqserver @def -game <game>
func QuickstartGame(ctx context.Context, gameDir, cfgDir string, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	gameDir = strings.TrimSpace(gameDir)
	cfgDir = strings.TrimSpace(cfgDir)
	if gameDir == "" {
		return fmt.Errorf("GAME_DIR is empty")
	}
	if cfgDir == "" {
		return fmt.Errorf("CFG_DIR is empty")
	}

	catalog, err := loadGameCatalog(cfgDir)
	if err != nil {
		return err
	}
	baseGames := baseGamesInCatalogOrder(catalog)

	serversPath := filepath.Join(gameDir, "servers.ini")
	st, err := os.Stat(serversPath)
	switch {
	case err == nil:
		if st.IsDir() {
			return fmt.Errorf("servers.ini path is a directory: %s", serversPath)
		}
	case errors.Is(err, os.ErrNotExist):
		selected := selectGamesFromQuickstart(catalog, logf)
		if err := createDefaultServersIni(gameDir, cfgDir, serversPath, selected); err != nil {
			return err
		}
	default:
		return fmt.Errorf("stat %s: %w", serversPath, err)
	}

	games, err := listGamesInServersIni(serversPath)
	if err != nil {
		return err
	}
	for _, game := range baseGames {
		games[game] = struct{}{}
	}

	byName := make(map[string]gameDataEntry, len(catalog))
	catalogOrder := make([]string, 0, len(catalog))
	for _, ent := range catalog {
		name := catalogEntryName(ent)
		if name == "" {
			continue
		}
		if _, ok := byName[name]; ok {
			continue
		}
		byName[name] = ent
		catalogOrder = append(catalogOrder, name)
	}

	installGames := make([]string, 0, len(catalogOrder))
	pendingDownloads := make(map[string]struct{}, len(catalogOrder))
	for _, game := range catalogOrder {
		if _, ok := games[game]; !ok {
			continue
		}
		installGames = append(installGames, game)
		ent := byName[game]
		if gameNeedsQuickstartDownload(gameDir, game, ent) {
			pendingDownloads[game] = struct{}{}
		}
	}
	pendingCount := len(pendingDownloads)
	if pendingCount > 0 {
		logf("Quickstart: downloading %d game packs...", pendingCount)
	}

	for _, game := range installGames {
		ent := byName[game]

		if err := os.MkdirAll(filepath.Join(gameDir, game), 0o755); err != nil {
			return fmt.Errorf("mkdir mod dir %q: %w", game, err)
		}

		if err := installLayer(ctx, gameDir, game, "common", ent.Common, ent.Force); err != nil {
			return fmt.Errorf("quickstart: %w", err)
		}
		if err := installLayer(ctx, gameDir, game, "server", ent.Server, ent.Force); err != nil {
			return fmt.Errorf("quickstart: %w", err)
		}
		if err := installLayer(ctx, gameDir, game, "client", ent.Client, ent.Force); err != nil {
			return fmt.Errorf("quickstart: %w", err)
		}
		if _, ok := pendingDownloads[game]; ok {
			logf("  %s complete!", game)
		}
	}

	return nil
}

func createDefaultServersIni(gameDir, cfgDir, serversPath string, selected []string) error {
	if err := os.MkdirAll(gameDir, 0o755); err != nil {
		return fmt.Errorf("mkdir GAME_DIR: %w", err)
	}

	basePath := filepath.Join(cfgDir, "servers.ini")
	base, err := os.ReadFile(basePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", basePath, err)
	}

	var b strings.Builder
	b.Write(base)
	if len(base) != 0 && base[len(base)-1] != '\n' {
		b.WriteByte('\n')
	}
	for _, game := range selected {
		b.WriteString("nqserver @def -game ")
		b.WriteString(game)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(serversPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", serversPath, err)
	}
	return nil
}

func listGamesInServersIni(path string) (map[string]struct{}, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	out := make(map[string]struct{})
	lines := strings.Split(string(b), "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") || strings.HasPrefix(line, ";") {
			continue
		}
		fields, err := shlex.Split(line)
		if err != nil || len(fields) == 0 {
			continue
		}
		if strings.HasPrefix(fields[0], "@") {
			// Macro definition lines are launch templates, not server launches.
			continue
		}
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "-game" {
				game := strings.TrimSpace(fields[i+1])
				if game != "" {
					out[game] = struct{}{}
				}
			}
		}
	}
	return out, nil
}

func loadGameCatalog(cfgDir string) ([]gameDataEntry, error) {
	path := filepath.Join(cfgDir, "game.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var entries []gameDataEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range entries {
		for j := range entries[i].Common {
			entries[i].Common[j] = normalizeSource(entries[i].Common[j], cfgDir)
		}
		for j := range entries[i].Server {
			entries[i].Server[j] = normalizeSource(entries[i].Server[j], cfgDir)
		}
		for j := range entries[i].Client {
			entries[i].Client[j] = normalizeSource(entries[i].Client[j], cfgDir)
		}
	}
	return entries, nil
}

func normalizeSource(source, cfgDir string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if strings.Contains(source, "://") {
		return source
	}
	return "file://" + filepath.Clean(filepath.Join(cfgDir, source))
}

func selectGamesFromQuickstart(entries []gameDataEntry, logf func(string, ...any)) []string {
	byName := make(map[string]struct{}, len(entries))
	order := make([]string, 0, len(entries))
	for _, ent := range entries {
		name := strings.TrimSpace(ent.Game)
		if name == "" {
			continue
		}
		if _, ok := byName[name]; ok {
			continue
		}
		byName[name] = struct{}{}
		order = append(order, name)
	}

	selected := make([]string, 0, len(order))
	seen := make(map[string]struct{}, len(order))

	raw := strings.TrimSpace(os.Getenv("QUICKSTART"))
	if raw == "" {
		raw = "ffa"
	}
	if strings.EqualFold(raw, "all") {
		for _, name := range order {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			selected = append(selected, name)
		}
		return selected
	}

	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if _, ok := byName[name]; !ok {
			logf("quickstart: skipped QUICKSTART entry %q", name)
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		selected = append(selected, name)
	}
	return selected
}

func catalogEntryName(ent gameDataEntry) string {
	if base := strings.TrimSpace(ent.Base); base != "" {
		return base
	}
	return strings.TrimSpace(ent.Game)
}

func baseGamesInCatalogOrder(entries []gameDataEntry) []string {
	var out []string
	seen := make(map[string]struct{}, len(entries))
	for _, ent := range entries {
		base := strings.TrimSpace(ent.Base)
		if base == "" {
			continue
		}
		if _, ok := seen[base]; ok {
			continue
		}
		seen[base] = struct{}{}
		out = append(out, base)
	}
	return out
}

func gameNeedsQuickstartDownload(gameDir, game string, ent gameDataEntry) bool {
	return layerNeedsInstall(gameDir, game, "common", ent.Common, ent.Force) ||
		layerNeedsInstall(gameDir, game, "server", ent.Server, ent.Force) ||
		layerNeedsInstall(gameDir, game, "client", ent.Client, ent.Force)
}

func layerNeedsInstall(gameDir, game, layer string, sources []string, force bool) bool {
	if len(sources) == 0 {
		return false
	}
	destRoot := filepath.Join(gameDir, game, layer)
	if !force && dirHasEntries(destRoot) {
		return false
	}
	return true
}

func installLayer(ctx context.Context, gameDir, game, layer string, sources []string, force bool) error {
	if !layerNeedsInstall(gameDir, game, layer, sources, force) {
		return nil
	}
	destRoot := filepath.Join(gameDir, game, layer)
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
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}

	tmpPath, cleanup, err := downloadToTemp(ctx, urlStr)
	if err != nil {
		return err
	}
	defer cleanup()

	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		destPath := filepath.Join(destRoot, filepath.Base(strings.TrimSpace(urlStr)))
		src, err := os.Open(tmpPath)
		if err != nil {
			return err
		}
		defer src.Close()
		return copyToFile(destPath, src)
	}
	defer zr.Close()

	sum, err := sha256FileHex(tmpPath)
	if err != nil {
		return err
	}
	if sum == quake106.ZipSHA256 {
		return quake106.ExtractPak0(&zr.Reader, destRoot)
	}
	return extractZipGeneric(&zr.Reader, destRoot)
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
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func downloadToTemp(ctx context.Context, urlStr string) (string, func(), error) {
	tmp, err := os.CreateTemp("", "nexquake-*.tmp")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	fail := func(err error) (string, func(), error) {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}

	var src io.ReadCloser
	if strings.HasPrefix(urlStr, "file://") {
		src, err = os.Open(strings.TrimPrefix(urlStr, "file://"))
		if err != nil {
			return fail(err)
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return fail(err)
		}
		req.Header.Set("User-Agent", "nexquake-nexus")

		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return fail(err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return fail(fmt.Errorf("status %s", resp.Status))
		}
		src = resp.Body
	}
	defer src.Close()

	if _, err := io.Copy(tmp, src); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	return tmp.Name(), cleanup, nil
}

func extractZipGeneric(zr *zip.Reader, destRoot string) error {
	stripPrefix := zipSingleRootPrefix(zr)

	for _, zf := range zr.File {
		name := normalizedZipEntryName(zf)
		if name == "" {
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

		if err := copyZipEntry(zf, destPath); err != nil {
			return err
		}
	}
	return nil
}

func zipSingleRootPrefix(zr *zip.Reader) string {
	root := ""
	sawFile := false

	for _, zf := range zr.File {
		name := normalizedZipEntryName(zf)
		if name == "" {
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

func normalizedZipEntryName(zf *zip.File) string {
	name := strings.TrimLeft(strings.ReplaceAll(zf.Name, `\`, `/`), "/")
	if name == "" || strings.HasSuffix(name, "/") || zf.FileInfo().IsDir() {
		return ""
	}
	return name
}

func copyZipEntry(zf *zip.File, destPath string) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return copyToFile(destPath, rc)
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
