package orch

import (
	"fmt"
	"strings"
	"time"
)

var (
	serverCommandCaptureMaxWait  = 750 * time.Millisecond
	serverCommandCaptureIdleWait = 100 * time.Millisecond
)

func formatServerCommandReply(output string) string {
	output = strings.ReplaceAll(output, "\r", "")
	if strings.TrimSpace(output) == "" {
		return "Command executed successfully.\n"
	}
	if strings.HasSuffix(output, "\n") {
		return output
	}
	return output + "\n"
}

func serverCommandReplyFilter(cmd string) serverConsoleLineFilter {
	expectedEcho := normalizeConsoleRelayLine(formatServerConsoleCommand(cmd))
	return func(line string) (string, bool) {
		if !shouldRelayServerConsoleLine(line) {
			return "", false
		}
		if expectedEcho != "" && normalizeConsoleRelayLine(line) == expectedEcho {
			return "", false
		}
		return line, true
	}
}

func parseServerTailArgs(cmd string) (lineCount int, isTail bool, err error) {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return 0, false, nil
	}
	if !strings.EqualFold(fields[0], "tail") {
		return 0, false, nil
	}

	if len(fields) != 1 {
		return 0, true, fmt.Errorf("usage: rcon <host|port> tail")
	}
	return defaultServerTailLines, true, nil
}

const defaultServerTailLines = 10

func formatServerTailReply(lines []string) string {
	if len(lines) == 0 {
		return "No buffered console output.\n"
	}
	var out strings.Builder
	for _, line := range lines {
		line = strings.ReplaceAll(line, "\r", "")
		if line == "" {
			continue
		}
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteByte('\n')
		}
	}
	if out.Len() == 0 {
		return "No buffered console output.\n"
	}
	return out.String()
}

// ExecServerCommand runs a console command on the server identified by listen port.
func (m *ServerManager) ExecServerCommand(port int, cmd string) (string, error) {
	srv := m.serverByListenPort(port)
	if srv == nil {
		return "", fmt.Errorf("unknown server")
	}
	if tailLines, isTail, tailErr := parseServerTailArgs(cmd); isTail {
		if tailErr != nil {
			return "", tailErr
		}
		if srv.Console == nil {
			return "", fmt.Errorf("server console unavailable")
		}
		lines := srv.Console.tail(tailLines, func(line string) (string, bool) {
			return line, shouldRelayServerConsoleLine(line)
		})
		return formatServerTailReply(lines), nil
	}
	output, err := srv.writeConsoleAndCaptureFiltered(
		cmd,
		serverCommandCaptureMaxWait,
		serverCommandCaptureIdleWait,
		serverCommandReplyFilter(cmd),
	)
	if err != nil {
		return "", err
	}
	return formatServerCommandReply(output), nil
}
