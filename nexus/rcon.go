package main

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"
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

func (r *Router) execServerCommand(port int, cmd string) error {
	if globalServerManager == nil {
		return fmt.Errorf("server manager not available")
	}
	srv := globalServerManager.ServerByListenPort(port)
	if srv == nil {
		return fmt.Errorf("unknown server")
	}
	return srv.WriteConsole(cmd)
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
		if parsed, err := strconv.Atoi(targetText); err == nil && parsed >= 1 && parsed <= 65535 {
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
		r.sendAdminReply("usage: rcon <host|port> <cmd>\n")
		return
	}
	if targetPort < 1 || targetPort > 65535 {
		r.sendAdminReply("error: unknown target\n")
		return
	}

	if err := r.execServerCommand(targetPort, args); err != nil {
		r.sendAdminReply(fmt.Sprintf("error: %v\n", err))
		return
	}
	r.sendAdminReply("ok\n")
}
