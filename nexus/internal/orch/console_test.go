package orch

import (
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testFormatLogLine(line string, ts time.Time) string {
	return ts.Format("2006/01/02 15:04:05") + " " + line
}

func TestShouldSkipConsoleLine(t *testing.T) {
	tests := []struct {
		name         string
		line         string
		skipContains []string
		want         bool
	}{
		{
			name:         "matches single token",
			line:         "/srv/runtime/arena/pak0.pak",
			skipContains: []string{".pak"},
			want:         true,
		},
		{
			name:         "matches case-insensitive token",
			line:         "Server says DEBUG: noisy",
			skipContains: []string{"debug:"},
			want:         true,
		},
		{
			name:         "matches one of many",
			line:         "execing server.cfg",
			skipContains: []string{"unrelated", "server.cfg"},
			want:         true,
		},
		{
			name:         "ignores empty tokens",
			line:         "execing server.cfg",
			skipContains: []string{"", "   "},
			want:         false,
		},
		{
			name:         "no match",
			line:         "/srv/runtime/arena",
			skipContains: []string{".pak"},
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipConsoleLine(tt.line, tt.skipContains...)
			if got != tt.want {
				t.Fatalf("shouldSkipConsoleLine(%q, %q) = %v, want %v",
					tt.line, tt.skipContains, got, tt.want)
			}
		})
	}
}

func TestParseSearchPathConsoleLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantEntry  string
		wantIsPath bool
	}{
		{
			name:       "gamedir line",
			line:       "/srv/runtime/arena",
			wantEntry:  "arena",
			wantIsPath: true,
		},
		{
			name:       "pak line ignored",
			line:       "/srv/runtime/arena/pak0.pak",
			wantEntry:  "",
			wantIsPath: true,
		},
		{
			name:       "pak line with suffix ignored",
			line:       "/srv/runtime/arena/pak0.pak (523 files)",
			wantEntry:  "",
			wantIsPath: true,
		},
		{
			name:       "path header not a path line",
			line:       "Current search path:",
			wantEntry:  "",
			wantIsPath: false,
		},
		{
			name:       "non path line",
			line:       "execing server.cfg",
			wantEntry:  "",
			wantIsPath: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEntry, gotIsPath := parseSearchPathConsoleLine(tt.line)
			if gotEntry != tt.wantEntry || gotIsPath != tt.wantIsPath {
				t.Fatalf("parseSearchPathConsoleLine(%q) = (%q, %v), want (%q, %v)",
					tt.line, gotEntry, gotIsPath, tt.wantEntry, tt.wantIsPath)
			}
		})
	}
}

func TestSubscribeFiltered_AppliesFilter(t *testing.T) {
	c := newServerConsole(nil)
	lines, cancel := c.subscribeFiltered(8, func(line string) (string, bool) {
		if strings.Contains(line, "drop") {
			return "", false
		}
		return strings.ToUpper(line), true
	})
	defer cancel()

	c.publishLine("keep one\n")
	c.publishLine("drop me\n")
	c.publishLine("keep two\n")

	readLine := func() string {
		t.Helper()
		select {
		case line := <-lines:
			return line
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("timed out waiting for filtered line")
		}
		return ""
	}

	first := readLine()
	second := readLine()

	if first != "KEEP ONE\n" {
		t.Fatalf("expected first filtered line KEEP ONE, got %q", first)
	}
	if second != "KEEP TWO\n" {
		t.Fatalf("expected second filtered line KEEP TWO, got %q", second)
	}
}

func TestCaptureCommandOutputFiltered_AppliesFilter(t *testing.T) {
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = ptyRead.Close()
		_ = ptyWrite.Close()
	})

	c := newServerConsole(ptyWrite)
	go func() {
		buf := make([]byte, 128)
		_, _ = ptyRead.Read(buf)
		c.publishLine("FindFile: maps/e1m1.bsp\n")
		c.publishLine("hostname is \"fragfest\"\n")
	}()

	out, err := c.captureCommandOutputFiltered(
		"hostname",
		120*time.Millisecond,
		10*time.Millisecond,
		func(line string) (string, bool) { return line, shouldRelayServerConsoleLine(line) },
	)
	if err != nil {
		t.Fatalf("captureCommandOutputFiltered error = %v", err)
	}
	if strings.Contains(out, "FindFile: maps/e1m1.bsp") {
		t.Fatalf("expected FindFile noise to be filtered, got %q", out)
	}
	if !strings.Contains(out, "hostname is \"fragfest\"") {
		t.Fatalf("expected hostname output to be retained, got %q", out)
	}
}

func TestServerConsoleTail_ReturnsMostRecentFilteredLines(t *testing.T) {
	c := newServerConsole(nil)
	c.publishLine("line one\n")
	c.publishLine("FindFile: maps/e1m1.bsp\n")
	c.publishLine("line two\n")
	c.publishLine("line three\n")

	lines := c.tail(2, nil)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d (%q)", len(lines), strings.Join(lines, ""))
	}
	if lines[0] != "line two\n" || lines[1] != "line three\n" {
		t.Fatalf("expected last two lines, got %q", strings.Join(lines, ""))
	}

	filtered := c.tail(3, func(line string) (string, bool) { return line, shouldRelayServerConsoleLine(line) })
	joined := strings.Join(filtered, "")
	if strings.Contains(joined, "FindFile: maps/e1m1.bsp") {
		t.Fatalf("expected noisy FindFile line filtered from tail, got %q", joined)
	}
}

func TestServerConsoleRun_WritesTimestampedLogLines(t *testing.T) {
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = ptyRead.Close()
		_ = ptyWrite.Close()
	})

	logFile, err := os.CreateTemp(t.TempDir(), "server-log-*.log")
	if err != nil {
		t.Fatalf("CreateTemp(log): %v", err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	c := newServerConsole(ptyRead)
	done := make(chan struct{})
	go func() {
		c.run(logFile, testFormatLogLine)
		close(done)
	}()

	if _, err := io.WriteString(ptyWrite, "hello server\n"); err != nil {
		t.Fatalf("write pty: %v", err)
	}
	_ = ptyWrite.Close()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for console run to exit")
	}

	if err := logFile.Close(); err != nil {
		t.Fatalf("close log file: %v", err)
	}
	b, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	got := string(b)
	re := regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} hello server\n$`)
	if !re.MatchString(got) {
		t.Fatalf("expected timestamped server log line, got %q", got)
	}
}

func TestServerConsoleRun_SuppressedEchoNotRecorded(t *testing.T) {
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = ptyRead.Close()
		_ = ptyWrite.Close()
	})

	logFile, err := os.CreateTemp(t.TempDir(), "server-log-*.log")
	if err != nil {
		t.Fatalf("CreateTemp(log): %v", err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	c := newServerConsole(ptyRead)
	c.queueSuppressedRelayEchoLine("echo \"alice@example.com: status\"; wait; wait; wait; status;\n")

	done := make(chan struct{})
	go func() {
		c.run(logFile, testFormatLogLine)
		close(done)
	}()

	if _, err := io.WriteString(ptyWrite, "echo \"alice@example.com: status\"; wait; wait; wait; status;\n"); err != nil {
		t.Fatalf("write suppressed line: %v", err)
	}
	if _, err := io.WriteString(ptyWrite, "alice@example.com: status\n"); err != nil {
		t.Fatalf("write audit output line: %v", err)
	}
	_ = ptyWrite.Close()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for console run to exit")
	}

	lines := c.tail(10, nil)
	joined := strings.Join(lines, "")
	if strings.Contains(joined, "echo \"alice@example.com: status\"; wait; wait; wait; status;") {
		t.Fatalf("expected suppressed echoed command omitted from tail, got %q", joined)
	}
	if !strings.Contains(joined, "alice@example.com: status") {
		t.Fatalf("expected audit output retained in tail, got %q", joined)
	}
}

func TestWriteCommandWithOptions_SuppressRelayEchoConsumesOnce(t *testing.T) {
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = ptyRead.Close()
		_ = ptyWrite.Close()
	})

	c := newServerConsole(ptyWrite)
	if err := c.writeCommandWithOptions(
		"echo online and accepting clients",
		serverConsoleWriteOptions{SuppressRelayEcho: true},
	); err != nil {
		t.Fatalf("writeCommandWithOptions: %v", err)
	}

	buf := make([]byte, 128)
	n, err := ptyRead.Read(buf)
	if err != nil {
		t.Fatalf("pty read: %v", err)
	}
	got := string(buf[:n])
	if got != "echo online and accepting clients;\n" {
		t.Fatalf("expected command write to pty, got %q", got)
	}

	if !c.consumeSuppressedRelayEchoLine("echo online and accepting clients;\n") {
		t.Fatalf("expected first matching relay line to be suppressed")
	}
	if c.consumeSuppressedRelayEchoLine("echo online and accepting clients;\n") {
		t.Fatalf("expected suppression to be consumed once")
	}
}
