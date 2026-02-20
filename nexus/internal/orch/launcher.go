package orch

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/assets"

	"github.com/google/shlex"
)

type serverLaunch struct {
	Slot   int
	LogDir string
	Binary string
	Args   []string
}

var unsupportedLaunchArgs = map[string]struct{}{
	"-basedir":  {},
	"-hipnotic": {},
	"-path":     {},
	"-rogue":    {},
}

func (m *ServerManager) planLaunches() ([]serverLaunch, []string, error) {
	iniPath := filepath.Join(m.gameDir, "servers.ini")
	startedAt := time.Now().UTC()
	entries, ok, err := loadServersIni(iniPath, startedAt, m.warnf)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		startTag := startedAt.Format("20060102T150405Z")
		binary := "nqserver"
		entries = []serverLaunch{
			{
				Slot:   0,
				LogDir: fmt.Sprintf("%d-%s-%s", 0, filepath.Base(binary), startTag),
				Binary: binary,
				Args:   []string{"-dedicated"},
			},
		}
		m.infof("Launch plan not found at %s; launching default server", iniPath)
	} else {
		m.debugf("Using launch plan: %s (%d server entries)", iniPath, len(entries))
	}

	// Build merged runtime dirs from whatever mods exist in GAME_DIR.
	mods, err := assets.ListMods(m.gameDir)
	if err != nil {
		return nil, nil, err
	}
	return entries, mods, nil
}

func loadServersIni(iniPath string, startedAt time.Time, warnf func(string, ...any)) (entries []serverLaunch, found bool, err error) {
	st, statErr := os.Stat(iniPath)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("stat %s: %w", iniPath, statErr)
	}
	if st.IsDir() {
		return nil, false, fmt.Errorf("servers.ini path is a directory: %s", iniPath)
	}

	f, err := os.Open(iniPath)
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", iniPath, err)
	}
	defer f.Close()

	startTag := startedAt.UTC().Format("20060102T150405Z")
	scanner := bufio.NewScanner(f)
	launchGroups := make(map[string][]string)
	slot := -1
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, ";") {
			continue
		}

		fields, err := shlex.Split(raw)
		if err != nil {
			return nil, true, fmt.Errorf("servers.ini line %d: %w", lineNo, err)
		}
		if len(fields) == 0 {
			continue
		}

		if strings.HasPrefix(fields[0], "@") {
			if len(fields[0]) <= 1 {
				return nil, true, fmt.Errorf("servers.ini line %d: invalid group name %q", lineNo, fields[0])
			}
			launchGroups[fields[0]] = append([]string(nil), fields[1:]...)
			continue
		}

		if len(launchGroups) != 0 {
			fields = mergeLaunchGroups(fields, launchGroups)
		}
		if len(fields) == 0 {
			continue
		}

		slot++

		binary := fields[0]
		args := applyLaunchArgTemplates(fields[1:])
		if unsupportedArg, ok := findUnsupportedLaunchArg(args); ok {
			warnf("Skipping servers.ini line %d: %s is not currently supported", lineNo, unsupportedArg)
			continue
		}

		logDir := fmt.Sprintf("%d-%s-%s", slot, filepath.Base(binary), startTag)

		entries = append(entries, serverLaunch{
			Slot:   slot,
			LogDir: logDir,
			Binary: binary,
			Args:   args,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, true, fmt.Errorf("read %s: %w", iniPath, err)
	}
	if len(entries) == 0 {
		return nil, true, fmt.Errorf("servers.ini has no server launch lines: %s", iniPath)
	}

	return entries, true, nil
}

func findUnsupportedLaunchArg(args []string) (string, bool) {
	for _, arg := range args {
		if _, unsupported := unsupportedLaunchArgs[arg]; unsupported {
			return arg, true
		}
	}
	return "", false
}

func applyLaunchArgTemplates(args []string) []string {
	if len(args) == 0 {
		return nil
	}

	out := append([]string(nil), args...)
	seen := make(map[string]string)

	for i := 0; i < len(out); i++ {
		token := out[i]
		if !isLaunchKeyToken(token) {
			continue
		}
		key := token[1:]
		if _, found := seen[key]; found {
			continue
		}
		if i+1 < len(out) && !isLaunchKeyToken(out[i+1]) {
			seen[key] = out[i+1]
		}
		for i+1 < len(out) && !isLaunchKeyToken(out[i+1]) {
			i++
		}
	}

	for i, token := range out {
		if !strings.HasPrefix(token, "%") {
			continue
		}
		if v, found := seen[token[1:]]; found {
			out[i] = v
		}
	}

	return out
}

func mergeLaunchGroups(fields []string, launchGroups map[string][]string) []string {
	if len(fields) == 0 || len(launchGroups) == 0 {
		return fields
	}

	// The launch line's explicit args win over group-provided defaults.
	explicitKeys := make(map[string]struct{})
	for i := 1; i < len(fields); i++ {
		token := fields[i]
		if !isLaunchKeyToken(token) {
			continue
		}
		explicitKeys[token[1:]] = struct{}{}
	}

	var insertedKeys map[string]struct{}
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		token := fields[i]
		groupFields, ok := launchGroups[token]
		if !ok {
			out = append(out, token)
			continue
		}

		if insertedKeys == nil {
			insertedKeys = make(map[string]struct{})
		}

		// Insert group fields at the reference point, but skip any keys already
		// provided by the launch line (or earlier groups).
		for j := 0; j < len(groupFields); j++ {
			token := groupFields[j]
			if !isLaunchKeyToken(token) {
				out = append(out, token)
				continue
			}

			key := token[1:]
			if _, found := explicitKeys[key]; found {
				for j+1 < len(groupFields) && !isLaunchKeyToken(groupFields[j+1]) {
					j++
				}
				continue
			}
			if _, found := insertedKeys[key]; found {
				for j+1 < len(groupFields) && !isLaunchKeyToken(groupFields[j+1]) {
					j++
				}
				continue
			}
			insertedKeys[key] = struct{}{}

			out = append(out, token)
			for j+1 < len(groupFields) && !isLaunchKeyToken(groupFields[j+1]) {
				j++
				out = append(out, groupFields[j])
			}
		}
	}

	return out
}

func isLaunchKeyToken(token string) bool {
	return len(token) >= 2 && (token[0] == '-' || token[0] == '+')
}
