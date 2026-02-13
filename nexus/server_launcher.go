package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/shlex"
)

type serverLaunch struct {
	slot   int
	logDir string
	binary string
	args   []string
}

var unsupportedLaunchArgs = map[string]struct{}{
	"-basedir":  {},
	"-hipnotic": {},
	"-path":     {},
	"-rogue":    {},
}

func (m *ServerManager) planLaunches() ([]serverLaunch, []string, error) {
	iniPath := filepath.Join(m.dataDir, "servers.ini")
	startedAt := time.Now().UTC()
	entries, ok, err := loadServersIni(iniPath, startedAt)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		startTag := startedAt.Format("20060102T150405Z")
		binary := "nqserver"
		entries = []serverLaunch{
			{
				slot:   0,
				logDir: fmt.Sprintf("%d-%s-%s", 0, filepath.Base(binary), startTag),
				binary: binary,
				args:   []string{"-dedicated"},
			},
		}
		infof("servers.ini not found at %s; using default nqserver launch with runtime port discovery", iniPath)
	} else {
		debugf("Using servers.ini launch plan: %s (%d server entries)", iniPath, len(entries))
	}

	// Build merged runtime dirs from whatever mods exist in DATA_DIR.
	mods, err := listMods(m.dataDir)
	if err != nil {
		return nil, nil, err
	}
	return entries, mods, nil
}

func loadServersIni(iniPath string, startedAt time.Time) (entries []serverLaunch, found bool, err error) {
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

		fields, err := splitCommandLine(raw)
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
			expanded := make([]string, 0, len(fields))
			for _, field := range fields {
				if groupFields, ok := launchGroups[field]; ok {
					expanded = append(expanded, groupFields...)
					continue
				}
				expanded = append(expanded, field)
			}
			fields = expanded
		}
		if len(fields) == 0 {
			continue
		}

		slot++

		binary := fields[0]
		args := fields[1:]
		if unsupportedArg, ok := findUnsupportedLaunchArg(args); ok {
			warnf("Skipping servers.ini line %d: %s is not currently supported", lineNo, unsupportedArg)
			continue
		}

		logDir := fmt.Sprintf("%d-%s-%s", slot, filepath.Base(binary), startTag)

		entries = append(entries, serverLaunch{
			slot:   slot,
			logDir: logDir,
			binary: binary,
			args:   args,
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

func splitCommandLine(line string) ([]string, error) {
	return shlex.Split(line)
}

func findUnsupportedLaunchArg(args []string) (string, bool) {
	for _, arg := range args {
		if _, unsupported := unsupportedLaunchArgs[arg]; unsupported {
			return arg, true
		}
	}
	return "", false
}
