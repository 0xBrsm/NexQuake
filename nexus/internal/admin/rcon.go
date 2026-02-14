package admin

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
)

const defaultServerTailLines = 10

// HandleAdminFrame processes an incoming admin (port 0) frame from a WebSocket client.
func HandleAdminFrame(r *nqnet.Router, payload []byte, auth *Auth, env *Env) {
	pw, targetPort, args := splitAdminPayload(payload)

	// Authorize either at connection time (OIDC / shared token) or per-frame
	// via rcon_password (traditional Quake rcon-style shared secret).
	if !r.IsAdmin() {
		rconPw := ""
		if auth != nil {
			rconPw = auth.rconPasswordValue()
		}
		if rconPw == "" || pw == "" || subtle.ConstantTimeCompare([]byte(pw), []byte(rconPw)) != 1 {
			r.SendAdminReply("unauthorized\n")
			return
		}
	}

	args = strings.TrimSpace(args)
	if args == "" {
		r.SendAdminReply("usage: rcon <cmd> | rcon <host|port> <cmd>\n")
		return
	}
	if targetPort == 0 {
		reply, err := execNexusCommand(args, env)
		if err != nil {
			r.SendAdminReply(fmt.Sprintf("error: %v\n", err))
			return
		}
		r.SendAdminReply(reply)
		return
	}
	if targetPort < 1 || targetPort > 65535 {
		r.SendAdminReply("error: unknown target\n")
		return
	}

	reply, err := env.ExecServerCmd(targetPort, args)
	if err != nil {
		r.SendAdminReply(fmt.Sprintf("error: %v\n", err))
		return
	}
	r.SendAdminReply(reply)
}

// splitAdminPayload parses the binary admin frame into password, target port, and args.
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
