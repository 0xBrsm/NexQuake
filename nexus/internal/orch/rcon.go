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

func trimTrailingCommandSemicolons(cmd string) string {
	out := strings.TrimSpace(cmd)
	for strings.HasSuffix(out, ";") {
		out = strings.TrimSpace(strings.TrimSuffix(out, ";"))
	}
	return out
}

func sanitizeServerAuditText(text string) string {
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\"", "'")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return strings.Join(strings.Fields(text), " ")
}

func appendServerCommandAuditEcho(cmd, actorID string) string {
	execCmd := trimTrailingCommandSemicolons(cmd)
	if execCmd == "" {
		return ""
	}
	actor := sanitizeServerAuditText(actorID)
	if actor == "" {
		actor = "admin"
	}
	auditCmd := sanitizeServerAuditText(execCmd)
	return fmt.Sprintf(`%s; echo "%s: %s"`, execCmd, actor, auditCmd)
}

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

// ExecServerCmd runs a console command on the server identified by listen port.
// When actorID is non-empty, an audit echo marker is appended to the command.
func (m *ServerManager) ExecServerCmd(port int, cmd, actorID string) (string, error) {
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
	execCmd := cmd
	if strings.TrimSpace(actorID) != "" {
		execCmd = appendServerCommandAuditEcho(cmd, actorID)
	}
	output, err := srv.writeConsoleAndCaptureFiltered(
		execCmd,
		serverCommandCaptureMaxWait,
		serverCommandCaptureIdleWait,
		serverCommandReplyFilter(execCmd),
	)
	if err != nil {
		return "", err
	}
	return formatServerCommandReply(output), nil
}
