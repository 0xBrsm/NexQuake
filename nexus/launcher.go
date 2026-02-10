package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type serverLaunch struct {
	spec   serverSpec
	binary string
	args   []string
}

func (m *ServerManager) planLaunches() ([]serverLaunch, []string, error) {
	iniPath := filepath.Join(m.dataDir, "servers.ini")
	startedAt := time.Now().UTC()
	entries, ok, err := loadServerLaunchesFromINIAt(iniPath, startedAt)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		entries = []serverLaunch{
			{
				spec: serverSpec{
					Slot:       0,
					ModName:    "",
					ListenPort: 0,
					LogDir:     fmt.Sprintf("id1-%s-0", startedAt.Format("20060102T150405Z")),
				},
				binary: "nqserver",
				args:   []string{"-dedicated"},
			},
		}
		infof("servers.ini not found at %s; using default nqserver launch with runtime port discovery", iniPath)
	} else {
		infof("Using servers.ini launch plan: %s (%d server entries)", iniPath, len(entries))
	}

	// Build merged runtime dirs from whatever mods exist in DATA_DIR.
	mods, err := listMods(m.dataDir)
	if err != nil {
		return nil, nil, err
	}
	return entries, mods, nil
}

func loadServerLaunchesFromINIAt(iniPath string, startedAt time.Time) (entries []serverLaunch, found bool, err error) {
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
	slot := -1
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "//") {
			continue
		}

		fields, err := splitCommandLine(raw)
		if err != nil {
			return nil, true, fmt.Errorf("servers.ini line %d: %w", lineNo, err)
		}
		if len(fields) == 0 {
			continue
		}

		slot++

		binary := fields[0]
		args := fields[1:]

		mod := findArgValue(args, "-game")
		game := mod
		if game == "" {
			game = "id1"
		}
		logDir := fmt.Sprintf("%s-%s-%d", game, startTag, slot)

		entries = append(entries, serverLaunch{
			spec: serverSpec{
				Slot:       slot,
				ModName:    mod,
				ListenPort: 0,
				LogDir:     logDir,
			},
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
	var out []string
	i := 0

	skipSpace := func() {
		for i < len(line) {
			switch line[i] {
			case ' ', '\t':
				i++
			default:
				return
			}
		}
	}

	isSpace := func(b byte) bool {
		return b == ' ' || b == '\t'
	}

	skipSpace()
	for i < len(line) {
		var b strings.Builder
		b.Grow(32)

		inSingle := false
		inDouble := false

		for i < len(line) {
			c := line[i]

			if !inSingle && !inDouble {
				if isSpace(c) {
					break
				}
				switch c {
				case '\'':
					inSingle = true
					i++
					continue
				case '"':
					inDouble = true
					i++
					continue
				case '\\':
					// Backslash escapes the next byte (best-effort).
					i++
					if i >= len(line) {
						b.WriteByte('\\')
						break
					}
					b.WriteByte(line[i])
					i++
					continue
				default:
					b.WriteByte(c)
					i++
					continue
				}
			}

			if inSingle {
				if c == '\'' {
					inSingle = false
					i++
					continue
				}
				b.WriteByte(c)
				i++
				continue
			}

			// inDouble
			if c == '"' {
				inDouble = false
				i++
				continue
			}
			if c == '\\' {
				i++
				if i >= len(line) {
					b.WriteByte('\\')
					break
				}
				b.WriteByte(line[i])
				i++
				continue
			}
			b.WriteByte(c)
			i++
		}

		if inSingle || inDouble {
			return nil, fmt.Errorf("unterminated quote")
		}
		out = append(out, b.String())

		skipSpace()
	}

	return out, nil
}

func findArgValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
