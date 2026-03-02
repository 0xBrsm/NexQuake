package orch

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	errUnknownServer = errors.New("unknown server")

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

type serverCommandTarget struct {
	label string
	srv   *managedServer
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
		return "", errUnknownServer
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

func (m *ServerManager) serverRecordByListenPortLocked(port int) *serverRecord {
	for _, serverID := range m.serverIDsByPort[port] {
		rec := m.serversByID[serverID]
		if m.serverRecordRunningLocked(rec) {
			return rec
		}
	}
	return nil
}

func normalizePoolHostname(hostname string) string {
	if hostname == "" {
		return "UNNAMED"
	}
	return truncateSlistField(hostname, hostcacheNameMax)
}

func (m *ServerManager) findPoolByHostnameLocked(token string) (*serverPool, error) {
	var matched *serverPool
	for _, pool := range m.poolsByID {
		if pool == nil || pool.aggregateInstances == 0 {
			continue
		}
		if !strings.EqualFold(token, normalizePoolHostname(pool.DisplayHostname)) {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("ambiguous host %q", token)
		}
		matched = pool
	}
	return matched, nil
}

func (m *ServerManager) serverCommandTargetLocked(rec *serverRecord) (serverCommandTarget, bool) {
	if !m.serverRecordRunningLocked(rec) {
		return serverCommandTarget{}, false
	}
	return serverCommandTarget{
		label: m.serverConsoleLabelLocked(rec),
		srv:   rec.Running,
	}, true
}

func (m *ServerManager) poolCommandTargetsLocked(pool *serverPool) ([]serverCommandTarget, error) {
	if pool == nil {
		return nil, errUnknownServer
	}
	targets := make([]serverCommandTarget, 0, len(pool.BackendServerIDs))
	for _, rec := range m.poolBackendsLocked(pool) {
		target, ok := m.serverCommandTargetLocked(rec)
		if !ok {
			continue
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return nil, errUnknownServer
	}
	return targets, nil
}

func (m *ServerManager) serverCommandTargetsLocked(target string) ([]serverCommandTarget, error) {
	if parsed, err := strconv.Atoi(target); err == nil && parsed > 0 {
		if parsed <= 65535 {
			if rec := m.serverRecordByListenPortLocked(parsed); rec != nil {
				resolved, _ := m.serverCommandTargetLocked(rec)
				return []serverCommandTarget{resolved}, nil
			}
		}
		pool, err := m.findPoolByIndexLocked(parsed)
		if err != nil {
			return nil, errUnknownServer
		}
		return m.poolCommandTargetsLocked(pool)
	}

	pool, err := m.findPoolByHostnameLocked(target)
	if err != nil {
		return nil, err
	}
	return m.poolCommandTargetsLocked(pool)
}

func execServerCommandTargets(targets []serverCommandTarget, cmd string, opts serverCommandOptions) (string, error) {
	if len(targets) == 0 {
		return "", errUnknownServer
	}
	if len(targets) == 1 {
		return execManagedServerCommand(targets[0].srv, cmd, opts)
	}

	var out strings.Builder
	var errs []error
	for _, target := range targets {
		reply, err := execManagedServerCommand(target.srv, cmd, opts)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", target.label, err))
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "[%s]\n%s", target.label, reply)
	}
	if len(errs) != 0 {
		return "", errors.Join(errs...)
	}
	return out.String(), nil
}

func (m *ServerManager) resolveServerTarget(token string) (port int, ok bool, err error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, false, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	pool, err := m.findPoolByHostnameLocked(token)
	if err != nil {
		return 0, true, err
	}
	if pool == nil {
		return 0, false, nil
	}
	port, ok = m.pickPoolBackendLocked(pool)
	if !ok {
		return 0, false, nil
	}
	return port, true, nil
}

// DispatchServerCmd runs a console command on a server target token, which may
// be a visible hostname, a concrete backend port, or a pool index.
func (m *ServerManager) DispatchServerCmd(target, cmd, actorID string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errUnknownServer
	}

	m.mu.RLock()
	targets, err := m.serverCommandTargetsLocked(target)
	m.mu.RUnlock()
	if err != nil {
		return "", err
	}
	return execServerCommandTargets(targets, cmd, serverCommandOptions{
		ActorID:       actorID,
		CaptureOutput: true,
	})
}
