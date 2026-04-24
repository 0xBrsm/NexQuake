package orch

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	errUnknownServerInstance = errors.New("unknown server instance")

	serverCommandCaptureMaxWait  = 750 * time.Millisecond
	serverCommandCaptureIdleWait = 100 * time.Millisecond
	// Audited commands prepend an `echo` marker and `wait` frames before the
	// actual command to keep per-server admin provenance in logs.
	// Allow a longer idle window so replies like cvar reads are not cut off.
	serverCommandCaptureAuditIdleWait = 300 * time.Millisecond
)

type serverCommandOptions struct {
	ActorID       string
	CaptureOutput bool
}

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
	return fmt.Sprintf(`echo "%s: %s"; wait; wait; wait; %s`, actor, auditCmd, execCmd)
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
		return 0, true, fmt.Errorf("usage: rcon <host|port|idx> tail")
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

func execManagedServerCommand(srv *managedServer, cmd string, opts serverCommandOptions) (string, error) {
	if srv == nil {
		return "", errUnknownServerInstance
	}
	if !opts.CaptureOutput {
		return "", srv.writeConsole(cmd)
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
	captureIdleWait := serverCommandCaptureIdleWait
	if strings.TrimSpace(opts.ActorID) != "" {
		execCmd = appendServerCommandAuditEcho(cmd, opts.ActorID)
		if captureIdleWait < serverCommandCaptureAuditIdleWait {
			captureIdleWait = serverCommandCaptureAuditIdleWait
		}
	}
	output, err := srv.writeConsoleAndCaptureFiltered(
		execCmd,
		serverCommandCaptureMaxWait,
		captureIdleWait,
		serverCommandReplyFilter(execCmd),
	)
	if err != nil {
		return "", err
	}
	return formatServerCommandReply(output), nil
}

func (m *ServerManager) instanceByListenPortLocked(port int) *instance {
	for _, serverID := range m.instanceIDsByPort[port] {
		rec := m.instancesByID[serverID]
		if m.instanceRunningLocked(rec) {
			return rec
		}
	}
	return nil
}

// DispatchInstanceCmd runs a console command against the running instance on
// the given listen port. Port is the only valid addressing scheme — hostnames
// and registry indices are not accepted.
func (m *ServerManager) DispatchInstanceCmd(port int, cmd, actorID string) (string, error) {
	if port < 1 || port > 65535 {
		return "", errUnknownServerInstance
	}

	m.mu.RLock()
	rec := m.instanceByListenPortLocked(port)
	var srv *managedServer
	if rec != nil && m.instanceRunningLocked(rec) {
		srv = rec.Running
	}
	m.mu.RUnlock()

	if srv == nil {
		return "", errUnknownServerInstance
	}
	return execManagedServerCommand(srv, cmd, serverCommandOptions{
		ActorID:       actorID,
		CaptureOutput: true,
	})
}
