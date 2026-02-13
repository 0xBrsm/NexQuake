package main

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	serverCommandCaptureMaxWait  = 750 * time.Millisecond
	serverCommandCaptureIdleWait = 100 * time.Millisecond
)

const (
	defaultServerTailLines = 10
)

func (r *Router) sendAdminReply(msg string) {
	if msg == "" {
		return
	}

	frame := make([]byte, wsPortHeaderSize+len(msg))
	frame[0] = 0
	frame[1] = 0
	copy(frame[wsPortHeaderSize:], msg)
	r.sendWS(frame, true)
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

func filterServerCommandReplyLine(line string) (string, bool) {
	if !shouldRelayServerConsoleLine(line) {
		return "", false
	}
	return line, true
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

func (r *Router) execServerCommand(port int, cmd string) (string, error) {
	if globalServerManager == nil {
		return "", fmt.Errorf("server manager not available")
	}
	srv := globalServerManager.ServerByListenPort(port)
	if srv == nil {
		return "", fmt.Errorf("unknown server")
	}
	if tailLines, isTail, tailErr := parseServerTailArgs(cmd); isTail {
		if tailErr != nil {
			return "", tailErr
		}
		if srv.console == nil {
			return "", fmt.Errorf("server console unavailable")
		}
		lines := srv.console.Tail(tailLines, filterServerCommandReplyLine)
		return formatServerTailReply(lines), nil
	}
	output, err := srv.WriteConsoleAndCaptureFiltered(
		cmd,
		serverCommandCaptureMaxWait,
		serverCommandCaptureIdleWait,
		filterServerCommandReplyLine,
	)
	if err != nil {
		return "", err
	}
	return formatServerCommandReply(output), nil
}

func splitAdminPayload(payload []byte) (password string, targetPort int, args string) {
	var targetText string

	i := bytes.IndexByte(payload, 0)
	if i < 0 {
		return string(payload), 0, ""
	}

	password = string(payload[:i])
	rest := payload[i+1:]

	j := bytes.IndexByte(rest, 0)
	if j < 0 {
		targetText = strings.TrimSpace(string(rest))
	} else {
		targetText = strings.TrimSpace(string(rest[:j]))
		args = string(rest[j+1:])
	}

	if targetText != "" {
		if parsed, err := strconv.Atoi(targetText); err == nil && parsed >= 0 && parsed <= 65535 {
			targetPort = parsed
		}
	}

	return password, targetPort, args
}

func (r *Router) handleAdminFrame(payload []byte) {
	pw, targetPort, args := splitAdminPayload(payload)

	// Authorize either at connection time (OIDC / shared token) or per-frame
	// via rcon_password (traditional Quake rcon-style shared secret).
	if !r.isAdmin {
		if rconPassword == "" || pw == "" || subtle.ConstantTimeCompare([]byte(pw), []byte(rconPassword)) != 1 {
			r.sendAdminReply("unauthorized\n")
			return
		}
	}

	args = strings.TrimSpace(args)
	if args == "" {
		r.sendAdminReply("usage: rcon <cmd> | rcon <host|port> <cmd>\n")
		return
	}
	if targetPort == 0 {
		reply, err := r.execNexusCommand(args)
		if err != nil {
			r.sendAdminReply(fmt.Sprintf("error: %v\n", err))
			return
		}
		r.sendAdminReply(reply)
		return
	}
	if targetPort < 1 || targetPort > 65535 {
		r.sendAdminReply("error: unknown target\n")
		return
	}

	reply, err := r.execServerCommand(targetPort, args)
	if err != nil {
		r.sendAdminReply(fmt.Sprintf("error: %v\n", err))
		return
	}
	r.sendAdminReply(reply)
}
