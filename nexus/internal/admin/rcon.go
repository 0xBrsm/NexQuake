package admin

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"
)

// Session is the relay connection interface used by the admin subsystem.
// nqrelay.Relay satisfies this interface.
type Session interface {
	// IsAdmin reports whether this session has admin privileges.
	IsAdmin() bool
	// PromoteAdmin grants admin privileges to this session.
	PromoteAdmin()
	// SendAdminReply delivers a text reply to the client's admin frame.
	SendAdminReply(msg string)
	// SourceIP returns the real client IP (possibly from a trusted header).
	SourceIP() string
	// VirtualClientIP returns the NQ virtual IP assigned to this session.
	VirtualClientIP() string
	// ClientIP returns the virtual IP as a raw 4-byte value, used for blocking.
	ClientIP() [4]byte
	// SourceKey is a stable identity key derived from SourceIP, used for ban tracking.
	SourceKey() string
	// Close terminates the WebSocket connection.
	Close()
}

const defaultServerTailLines = 10

// HandleAdminFrame processes an incoming admin (port 0) frame from a WebSocket client.
// It is a convenience wrapper around [HandleAdminFrameWithIdentityAndPromotionHook]
// with no identity label and no promotion callback.
func HandleAdminFrame(r Session, payload []byte, auth *Auth, env *Env) {
	HandleAdminFrameWithIdentityAndPromotionHook(r, payload, auth, env, "", nil)
}

// HandleAdminFrameWithPromotionHook is [HandleAdminFrame] plus an optional callback
// fired when a non-admin session is first promoted via a valid rcon_password.
func HandleAdminFrameWithPromotionHook(
	r Session,
	payload []byte,
	auth *Auth,
	env *Env,
	onPromoted func(Session),
) {
	HandleAdminFrameWithIdentityAndPromotionHook(r, payload, auth, env, "", onPromoted)
}

// HandleAdminFrameWithIdentityAndPromotionHook is the primary admin-frame handler.
// identity is an optional actor label (e.g. email from OIDC) used in audit logs;
// onPromoted is called once when a session is promoted to admin via rcon_password.
func HandleAdminFrameWithIdentityAndPromotionHook(
	r Session,
	payload []byte,
	auth *Auth,
	env *Env,
	identity string,
	onPromoted func(Session),
) {
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
		r.PromoteAdmin()
		if onPromoted != nil {
			onPromoted(r)
		}
	}

	args = strings.TrimSpace(args)
	if args == "" {
		r.SendAdminReply("usage: rcon <cmd> | rcon <host|port> <cmd>\n")
		return
	}
	actorID := resolveAdminActorID(identity, r)
	targetLabel := adminTargetLabel(targetPort)
	adminAuditf(env, "admin-rcon request actor=%q target=%s command=%q", actorID, targetLabel, sanitizeAdminAuditText(args))

	var reply string
	var err error
	if targetPort == 0 {
		reply, err = execNexusCommand(args, env)
	} else {
		reply, err = env.ExecServerCmd(targetPort, args, actorID)
	}
	if err != nil {
		adminAuditf(env, "admin-rcon response actor=%q target=%s error=%q", actorID, targetLabel, sanitizeAdminAuditText(err.Error()))
		r.SendAdminReply(fmt.Sprintf("error: %v\n", err))
		return
	}
	adminAuditf(env, "admin-rcon response actor=%q target=%s reply=%q", actorID, targetLabel, sanitizeAdminAuditText(reply))
	r.SendAdminReply(reply)
}

func resolveAdminActorID(identity string, r Session) string {
	identity = strings.TrimSpace(identity)
	if identity != "" && !strings.EqualFold(identity, "anonymous") {
		return identity
	}
	if r != nil {
		source := strings.TrimSpace(r.SourceIP())
		if source != "" {
			return source
		}
		virtualIP := strings.TrimSpace(r.VirtualClientIP())
		if virtualIP != "" {
			return virtualIP
		}
	}
	if identity != "" {
		return identity
	}
	return "admin"
}

const adminAuditTextMax = 512

func sanitizeAdminAuditText(text string) string {
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return "<empty>"
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > adminAuditTextMax {
		text = text[:adminAuditTextMax-3] + "..."
	}
	return text
}

func adminTargetLabel(targetPort int) string {
	if targetPort == 0 {
		return "nexus"
	}
	return strconv.Itoa(targetPort)
}

func adminAuditf(env *Env, format string, args ...any) {
	if env == nil || env.Auditf == nil {
		return
	}
	env.Auditf(format, args...)
}

// splitAdminPayload parses a binary admin frame into its three NUL-delimited
// fields: password, target port (0 = Nexus, 1-65535 = a game server port), and
// the command argument string.
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
