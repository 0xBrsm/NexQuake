// Package orch manages the lifecycle of dedicated Quake server processes.
//
// The central type is [ServerManager], which owns a set of server pools.
// Each pool tracks one or more backend processes launched from the same
// servers.ini entry.  Pools whose launch line specifies "-port 0" autoscale:
// the manager spawns additional backend replicas when join demand outpaces
// available capacity, and despawns idle ones when headroom is sufficient.
//
// Typical usage:
//
//	mgr := orch.NewServerManager(gameDir, logsDir, infof, consoleInfof,
//	    debugf, warnf, errorf, formatLogLine)
//	mgr.SetPoolMaxSize(cfg.poolSize)
//	if err := mgr.StartAll(); err != nil { ... }
//
//	stopPoller := mgr.StartInfoPoller(ctx, serverIP)
//	defer stopPoller()
//	...
//	mgr.StopAll(ctx, 2*time.Second)
package orch

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ServerManager orchestrates all managed game servers.
// Use [NewServerManager] to construct; the zero value is not usable.
type ServerManager struct {
	gameDir string
	logsDir string
	infof   func(string, ...any)
	debugf  func(string, ...any)
	warnf   func(string, ...any)
	errorf  func(string, ...any)

	consoleInfof  func(string, ...any)
	formatLogLine func(string, time.Time) string

	mu              sync.RWMutex
	serversByID     map[int]*serverRecord
	serverIDsByPort map[int][]int

	poolsByID        map[int]*serverPool
	poolByListenPort map[int]*serverPool
	poolByServerID   map[int]*serverPool
	nextPoolID       int
	poolMaxSize      int
	nextServerID   int
	runtimeBasedir string
}

func (m *ServerManager) serverConsoleLabel(rec *serverRecord) string {
	if rec == nil {
		return "server"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.serverConsoleLabelLocked(rec)
}

func (m *ServerManager) serverConsoleLabelLocked(rec *serverRecord) string {
	if rec == nil {
		return "server"
	}

	pool := m.poolByServerID[rec.id]

	hostname := "server"
	if pool != nil {
		if name := strings.TrimSpace(pool.DisplayHostname); name != "" {
			hostname = name
		}
	}
	if name := strings.TrimSpace(rec.Hostname); name != "" {
		hostname = name
	}

	port := recordListenPort(rec)
	if pool != nil && pool.Line >= 0 {
		if port > 0 {
			return fmt.Sprintf("%d-%s-%d", pool.Line+1, hostname, port)
		}
		return fmt.Sprintf("%d-%s", pool.Line+1, hostname)
	}
	if port > 0 {
		return fmt.Sprintf("%s-%d", hostname, port)
	}
	if rec.Launch.Line >= 0 {
		return fmt.Sprintf("%d-%s", rec.Launch.Line+1, hostname)
	}
	return hostname
}

func formatLaunchLabel(launch serverLaunch) string {
	if launch.Line >= 0 {
		return fmt.Sprintf("line=%d", launch.Line+1)
	}
	return "replica"
}

func (m *ServerManager) serverConsoleRelayEnabled(rec *serverRecord, console *serverConsole) bool {
	if rec == nil || console == nil {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if rec.Running == nil || rec.Running.Console != console {
		return false
	}
	return rec.relayConsoleReady
}

func shouldRelayServerConsoleLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}

	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "packfile:") {
		return false
	}
	if strings.Contains(lower, "findfile: can't find maps/") && strings.HasSuffix(lower, ".ent") {
		return false
	}
	if strings.Contains(lower, "findfile:") && !strings.Contains(lower, "can't find") {
		return false
	}

	return true
}

func (m *ServerManager) formatServerConsoleRelayLine(rec *serverRecord, line string) (string, bool) {
	msg := strings.TrimRight(line, "\r\n")
	if !shouldRelayServerConsoleLine(msg) {
		return "", false
	}
	return fmt.Sprintf("[%s] %s", m.serverConsoleLabel(rec), msg), true
}

func (m *ServerManager) relayServerConsoleToNexus(rec *serverRecord, console *serverConsole) {
	if console == nil {
		return
	}

	lines, cancel := console.subscribeFiltered(1024, func(line string) (string, bool) {
		if console.consumeSuppressedRelayEchoLine(line) {
			return "", false
		}
		if !m.serverConsoleRelayEnabled(rec, console) {
			return "", false
		}
		return m.formatServerConsoleRelayLine(rec, line)
	})
	defer cancel()

	for formatted := range lines {
		m.consoleInfof("%s", formatted)
	}
}

func noopLogf(string, ...any) {}

func identityLogLine(line string, _ time.Time) string {
	return line
}

// NewServerManager creates a [ServerManager] with injected logging callbacks.
//
//   - gameDir: path to the game data directory (GAME_DIR); must exist before
//     calling [ServerManager.StartAll].
//   - logsDir: directory where per-server log files are written; created on
//     demand by [ServerManager.StartAll].
//   - infof, consoleInfof, debugf, warnf, errorf: printf-style log callbacks;
//     any nil callback is silently replaced with a no-op.
//     consoleInfof defaults to infof when nil.
//   - formatLogLine: formats a raw console line with a timestamp for the
//     on-disk log file; defaults to the identity function when nil.
func NewServerManager(
	gameDir, logsDir string,
	infof, consoleInfof, debugf, warnf, errorf func(string, ...any),
	formatLogLine func(string, time.Time) string,
) *ServerManager {
	if infof == nil {
		infof = noopLogf
	}
	if consoleInfof == nil {
		consoleInfof = infof
	}
	if debugf == nil {
		debugf = noopLogf
	}
	if warnf == nil {
		warnf = noopLogf
	}
	if errorf == nil {
		errorf = noopLogf
	}
	if formatLogLine == nil {
		formatLogLine = identityLogLine
	}
	return &ServerManager{
		gameDir:           gameDir,
		logsDir:           logsDir,
		infof:             infof,
		debugf:            debugf,
		warnf:             warnf,
		errorf:            errorf,
		consoleInfof:      consoleInfof,
		formatLogLine:     formatLogLine,
		serversByID:       make(map[int]*serverRecord),
		serverIDsByPort:   make(map[int][]int),
		poolsByID:         make(map[int]*serverPool),
		poolByListenPort:  make(map[int]*serverPool),
		poolByServerID:    make(map[int]*serverPool),
		nextPoolID:        1,
		poolMaxSize:       defaultPoolSize,
	}
}
