package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type serverConsoleLineFilter func(line string) (filtered string, keep bool)

type serverConsoleWriteOptions struct {
	SuppressRelayEcho bool
}

func parsePortConsoleLine(line string) (int, bool) {
	s := strings.TrimSpace(line)
	const prefix = `"port" is "`
	if !strings.HasPrefix(s, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(s, prefix)
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return 0, false
	}
	port, err := strconv.Atoi(rest[:end])
	if err != nil || port < 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

func formatServerConsoleCommand(cmd string) string {
	line := strings.TrimSpace(cmd)
	if line == "" {
		return ""
	}
	// Sys_ConsoleInput strips the final byte and Cbuf_AddText does not append a
	// delimiter. Keep a trailing ';' so adjacent reads can't merge into one token
	// (for example "path" + "port" -> "pathport").
	return line + ";\n"
}

func shouldSkipConsoleLine(line string, skipContains ...string) bool {
	if line == "" || len(skipContains) == 0 {
		return false
	}

	lowerLine := strings.ToLower(line)
	for _, token := range skipContains {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if strings.Contains(lowerLine, strings.ToLower(token)) {
			return true
		}
	}
	return false
}

func parseSearchPathConsoleLine(line string) (entry string, isPathLine bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", false
	}

	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", false
	}

	pathToken := fields[0]
	if !strings.Contains(pathToken, "/") {
		return "", false
	}

	pathToken = strings.TrimSuffix(pathToken, "/")
	if pathToken == "" {
		return "", true
	}
	if shouldSkipConsoleLine(pathToken, ".pak") {
		return "", true
	}

	base := filepath.Base(pathToken)
	if base == "" || base == "." || base == "/" {
		return "", true
	}
	return base, true
}

type serverConsole struct {
	pty *os.File

	writeMu   sync.Mutex
	commandMu sync.Mutex

	suppressedRelayMu   sync.Mutex
	suppressedRelayEcho map[string]int

	subscribersMu    sync.RWMutex
	nextSubscriberID int
	subscribers      map[int]chan string

	historyMu  sync.RWMutex
	history    []string
	historyCap int
}

func newServerConsole(pty *os.File) *serverConsole {
	return &serverConsole{
		pty:                 pty,
		subscribers:         make(map[int]chan string),
		suppressedRelayEcho: make(map[string]int),
		historyCap:          2048,
	}
}

func (c *serverConsole) WriteCommand(cmd string) error {
	return c.WriteCommandWithOptions(cmd, serverConsoleWriteOptions{})
}

func (c *serverConsole) WriteCommandWithOptions(cmd string, opts serverConsoleWriteOptions) error {
	if c == nil || c.pty == nil {
		return io.ErrClosedPipe
	}
	line := formatServerConsoleCommand(cmd)
	if line == "" {
		return nil
	}
	if opts.SuppressRelayEcho {
		c.queueSuppressedRelayEchoLine(line)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	_, err := io.WriteString(c.pty, line)
	if err != nil && opts.SuppressRelayEcho {
		c.unqueueSuppressedRelayEchoLine(line)
	}
	return err
}

func normalizeConsoleRelayLine(line string) string {
	return strings.TrimSpace(strings.ReplaceAll(line, "\r", ""))
}

func (c *serverConsole) queueSuppressedRelayEchoLine(line string) {
	if c == nil {
		return
	}
	key := normalizeConsoleRelayLine(line)
	if key == "" {
		return
	}
	c.suppressedRelayMu.Lock()
	if c.suppressedRelayEcho == nil {
		c.suppressedRelayEcho = make(map[string]int)
	}
	c.suppressedRelayEcho[key]++
	c.suppressedRelayMu.Unlock()
}

func (c *serverConsole) unqueueSuppressedRelayEchoLine(line string) {
	if c == nil {
		return
	}
	key := normalizeConsoleRelayLine(line)
	if key == "" {
		return
	}
	c.suppressedRelayMu.Lock()
	defer c.suppressedRelayMu.Unlock()
	count := c.suppressedRelayEcho[key]
	if count <= 1 {
		delete(c.suppressedRelayEcho, key)
		return
	}
	c.suppressedRelayEcho[key] = count - 1
}

func (c *serverConsole) consumeSuppressedRelayEchoLine(line string) bool {
	if c == nil {
		return false
	}
	key := normalizeConsoleRelayLine(line)
	if key == "" {
		return false
	}
	c.suppressedRelayMu.Lock()
	defer c.suppressedRelayMu.Unlock()
	count := c.suppressedRelayEcho[key]
	if count <= 0 {
		return false
	}
	if count == 1 {
		delete(c.suppressedRelayEcho, key)
		return true
	}
	c.suppressedRelayEcho[key] = count - 1
	return true
}

func (c *serverConsole) Subscribe(buffer int) (<-chan string, func()) {
	if buffer <= 0 {
		buffer = 128
	}
	ch := make(chan string, buffer)

	c.subscribersMu.Lock()
	subscriberID := c.nextSubscriberID
	c.nextSubscriberID++
	if c.subscribers == nil {
		c.subscribers = make(map[int]chan string)
	}
	c.subscribers[subscriberID] = ch
	c.subscribersMu.Unlock()

	cancel := func() {
		c.subscribersMu.Lock()
		sub := c.subscribers[subscriberID]
		delete(c.subscribers, subscriberID)
		c.subscribersMu.Unlock()
		if sub != nil {
			close(sub)
		}
	}
	return ch, cancel
}

func (c *serverConsole) SubscribeFiltered(buffer int, filter serverConsoleLineFilter) (<-chan string, func()) {
	lines, cancel := c.Subscribe(buffer)
	if filter == nil {
		filter = func(line string) (string, bool) {
			return line, true
		}
	}

	out := make(chan string, buffer)
	done := make(chan struct{})
	var cancelOnce sync.Once

	go func() {
		defer close(out)
		for {
			select {
			case <-done:
				return
			case line, ok := <-lines:
				if !ok {
					return
				}
				filtered, keep := filter(line)
				if !keep {
					continue
				}
				select {
				case <-done:
					return
				case out <- filtered:
				}
			}
		}
	}()

	cancelFiltered := func() {
		cancelOnce.Do(func() {
			close(done)
			cancel()
		})
	}
	return out, cancelFiltered
}

func (c *serverConsole) publishLine(line string) {
	if c == nil || line == "" {
		return
	}
	c.appendHistoryLine(line)

	c.subscribersMu.RLock()
	defer c.subscribersMu.RUnlock()
	for _, sub := range c.subscribers {
		select {
		case sub <- line:
		default:
		}
	}
}

func (c *serverConsole) appendHistoryLine(line string) {
	if c == nil || line == "" {
		return
	}

	c.historyMu.Lock()
	defer c.historyMu.Unlock()

	if c.historyCap <= 0 {
		c.historyCap = 2048
	}

	if len(c.history) < c.historyCap {
		c.history = append(c.history, line)
		return
	}

	copy(c.history, c.history[1:])
	c.history[len(c.history)-1] = line
}

func (c *serverConsole) Tail(n int, filter serverConsoleLineFilter) []string {
	if c == nil || n <= 0 {
		return nil
	}
	if filter == nil {
		filter = func(line string) (string, bool) {
			return line, true
		}
	}

	c.historyMu.RLock()
	snapshot := append([]string(nil), c.history...)
	c.historyMu.RUnlock()
	if len(snapshot) == 0 {
		return nil
	}

	out := make([]string, 0, n)
	for i := len(snapshot) - 1; i >= 0 && len(out) < n; i-- {
		filtered, keep := filter(snapshot[i])
		if !keep {
			continue
		}
		out = append(out, filtered)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (c *serverConsole) closeSubscribers() {
	c.subscribersMu.Lock()
	defer c.subscribersMu.Unlock()
	for id, sub := range c.subscribers {
		close(sub)
		delete(c.subscribers, id)
	}
}

func (c *serverConsole) Run(logFile *os.File) {
	if c == nil || c.pty == nil {
		return
	}
	defer c.closeSubscribers()

	reader := bufio.NewReader(c.pty)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			_, _ = io.WriteString(logFile, formatTimestampedLogText(line, time.Now()))
			c.publishLine(line)
		}
		if err != nil {
			return
		}
	}
}

func (c *serverConsole) CaptureCommandOutput(cmd string, maxWait, idleWait time.Duration) (string, error) {
	return c.CaptureCommandOutputFiltered(cmd, maxWait, idleWait, nil)
}

func (c *serverConsole) CaptureCommandOutputFiltered(cmd string, maxWait, idleWait time.Duration, filter serverConsoleLineFilter) (string, error) {
	if c == nil {
		return "", io.ErrClosedPipe
	}
	if maxWait <= 0 {
		maxWait = 750 * time.Millisecond
	}
	if idleWait <= 0 {
		idleWait = 100 * time.Millisecond
	}

	c.commandMu.Lock()
	defer c.commandMu.Unlock()

	lines, cancel := c.SubscribeFiltered(256, filter)
	defer cancel()

	if err := c.WriteCommand(cmd); err != nil {
		return "", err
	}
	return collectConsoleOutput(lines, maxWait, idleWait), nil
}

func collectConsoleOutput(lines <-chan string, maxWait, idleWait time.Duration) string {
	var out strings.Builder

	maxTimer := time.NewTimer(maxWait)
	defer maxTimer.Stop()

	var idleTimer *time.Timer
	var idleC <-chan time.Time
	resetIdle := func() {
		if idleTimer == nil {
			idleTimer = time.NewTimer(idleWait)
			idleC = idleTimer.C
			return
		}
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(idleWait)
	}
	defer func() {
		if idleTimer != nil && !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
	}()

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return out.String()
			}
			out.WriteString(line)
			resetIdle()
		case <-idleC:
			return out.String()
		case <-maxTimer.C:
			return out.String()
		}
	}
}

func monitorServerStartup(
	console *serverConsole,
	shouldProbePort bool,
	onPort func(int),
	onSearchPath func([]string),
) {
	if console == nil {
		return
	}
	needPort := shouldProbePort
	needPath := onSearchPath != nil
	if !needPort && !needPath {
		return
	}

	lines, cancel := console.SubscribeFiltered(256, nil)
	defer cancel()

	requestedPort := false
	resolvedPort := false
	requestedPath := false
	collectingPath := false
	resolvedPath := false
	pathEntries := make([]string, 0, 8)

	for {
		line, ok := <-lines
		if !ok {
			if collectingPath && !resolvedPath && len(pathEntries) > 0 {
				onSearchPath(append([]string(nil), pathEntries...))
			}
			return
		}

		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "========Quake Initialized=========") {
			if needPath && !requestedPath {
				if err := console.WriteCommand("path"); err == nil {
					requestedPath = true
				}
			}
			if needPort && !requestedPort {
				if err := console.WriteCommand("port"); err == nil {
					requestedPort = true
				}
			}
		}
		if needPort && requestedPort && !resolvedPort {
			port, ok := parsePortConsoleLine(trimmed)
			if ok {
				resolvedPort = true
				if onPort != nil {
					onPort(port)
				}
			}
		}
		if needPath && requestedPath && !resolvedPath {
			if collectingPath {
				if gameDir, isPathLine := parseSearchPathConsoleLine(trimmed); isPathLine {
					if gameDir != "" {
						pathEntries = append(pathEntries, gameDir)
					}
					continue
				}

				collectingPath = false
				if len(pathEntries) > 0 {
					resolvedPath = true
					onSearchPath(append([]string(nil), pathEntries...))
				}
			} else if strings.Contains(trimmed, "Current search path:") {
				collectingPath = true
				pathEntries = pathEntries[:0]
			}
		}

		if (!needPort || resolvedPort) && (!needPath || resolvedPath) {
			return
		}
	}
}
