package admin

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/session"
)

// Session is the admin package's alias for session.Session — used in handlers
// that act on live connections (kick, ban).
type Session = session.Session

// BanTarget is the admin package's alias for session.BanTarget.
type BanTarget = session.BanTarget

// SessionEntry is a point-in-time view of a single session for RPC responses.
// Server-side enrichment adds the resolved server hostname for the session's
// active port so clients don't need a separate server.list lookup.
type SessionEntry struct {
	NQIP        string `json:"nqip"`
	SourceIP         string `json:"source_ip,omitempty"`
	UserID           string `json:"user_id,omitempty"`
	IsAdmin          bool   `json:"is_admin"`
	ActiveServerPort int    `json:"server_port,omitempty"`
	ActiveServerHost string `json:"server_host,omitempty"`
}

// --- session.list -----------------------------------------------------------

type SessionListResult struct {
	Sessions []SessionEntry `json:"sessions"`
}

func sessionListHandler(env *Env, _ Actor, _ any) (any, error) {
	if env == nil || env.SessionSnapshots == nil {
		return SessionListResult{Sessions: []SessionEntry{}}, nil
	}
	return SessionListResult{Sessions: enrichedSessionEntries(env)}, nil
}

// --- session.info / session.ban ---------------------------------------------

// SessionLookup is the common param shape for session.info and session.ban.
type SessionLookup struct {
	NQIP string `json:"nqip"`
}

func parseSessionLookup(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[SessionLookup](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.NQIP) == "" {
		return nil, fmt.Errorf("nqip is required")
	}
	return p, nil
}

type SessionInfoResult struct {
	Session     SessionEntry `json:"session"`
	StatusSlot  int          `json:"status_slot,omitempty"`
	StatusLine  string       `json:"status_line,omitempty"`
	StatusAddr  string       `json:"status_addr,omitempty"`
	StatusNote  string       `json:"status_note,omitempty"`
}

func sessionInfoHandler(env *Env, _ Actor, params any) (any, error) {
	p := params.(*SessionLookup)
	entry, ok := lookupSessionEntry(env, p.NQIP)
	if !ok {
		return nil, &MethodError{Code: ErrCodeNotFound, Message: fmt.Sprintf("no session with nqip %q", p.NQIP)}
	}
	result := SessionInfoResult{Session: entry}
	if entry.ActiveServerPort < 1 || entry.ActiveServerPort > 65535 {
		result.StatusNote = "not connected to a server"
		return result, nil
	}
	if env == nil || env.DispatchInstanceCmd == nil {
		result.StatusNote = "server manager unavailable"
		return result, nil
	}
	reply, err := env.DispatchInstanceCmd(entry.ActiveServerPort, "status", "")
	if err != nil {
		return nil, &MethodError{Code: ErrCodeDispatch, Message: fmt.Sprintf("status lookup failed: %v", err)}
	}
	match, ok := statusPlayerForNQIP(reply, entry.NQIP)
	if !ok {
		result.StatusNote = "player not present in server status output"
		return result, nil
	}
	result.StatusSlot = match.slot
	result.StatusLine = match.summary
	result.StatusAddr = match.address
	return result, nil
}

type SessionBanResult struct {
	NQIP          string   `json:"nqip"`
	SourceIPs    []string `json:"source_ips,omitempty"`
	Disconnected int      `json:"disconnected"`
	ServerKicks  int      `json:"server_kicks"`
	Warnings     []string `json:"warnings,omitempty"`
}

func sessionBanHandler(env *Env, _ Actor, params any) (any, error) {
	p := params.(*SessionLookup)
	nqip := strings.TrimSpace(p.NQIP)
	if env == nil || env.SnapshotByNQIP == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "session manager unavailable"}
	}
	sessions, targets := env.SnapshotByNQIP(nqip)
	if len(sessions) == 0 {
		return nil, &MethodError{Code: ErrCodeNotFound, Message: fmt.Sprintf("no active session with nqip %q", nqip)}
	}
	for _, s := range sessions {
		if s.IsAdmin() {
			return nil, &MethodError{Code: ErrCodeConflict, Message: "cannot ban admin sessions"}
		}
	}

	applied, applyErrs := applyServerKickTargets(targets, env)

	if env.ReserveAndBlock != nil {
		for _, s := range sessions {
			env.ReserveAndBlock(s.ClientIP(), s.SourceKey())
		}
	}
	for _, s := range sessions {
		s.Close()
	}

	result := SessionBanResult{
		NQIP:          nqip,
		SourceIPs:    uniqueSortedSourceIPs(sessions),
		Disconnected: len(sessions),
		ServerKicks:  applied,
	}
	for _, e := range applyErrs {
		result.Warnings = append(result.Warnings, e.Error())
	}
	return result, nil
}

// enrichedSessionEntries builds the session.list view: each session snapshot
// augmented with the resolved hostname of its active server (if any).
func enrichedSessionEntries(env *Env) []SessionEntry {
	snaps := env.SessionSnapshots()
	if len(snaps) == 0 {
		return []SessionEntry{}
	}
	serverByPort := serversByListenPort(env)
	out := make([]SessionEntry, 0, len(snaps))
	for _, s := range snaps {
		entry := SessionEntry{
			NQIP:        strings.TrimSpace(s.NQIP),
			SourceIP:         strings.TrimSpace(s.SourceIP),
			UserID:           strings.TrimSpace(s.UserID),
			IsAdmin:          s.IsAdmin,
			ActiveServerPort: s.ActiveServerPort,
		}
		if entry.NQIP == "" {
			continue
		}
		applyActiveServer(&entry, env, serverByPort)
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b SessionEntry) int {
		if c := compareClientIPText(a.NQIP, b.NQIP); c != 0 {
			return c
		}
		return compareClientIPText(a.SourceIP, b.SourceIP)
	})
	return out
}

// lookupSessionEntry resolves one session by NQIP via the registry's direct
// index (env.SnapshotByNQIP) instead of enriching the full list. Fast path for
// session.info / any future single-session handler.
func lookupSessionEntry(env *Env, nqip string) (SessionEntry, bool) {
	nqip = strings.TrimSpace(nqip)
	if nqip == "" || env == nil || env.SnapshotByNQIP == nil {
		return SessionEntry{}, false
	}
	sessions, _ := env.SnapshotByNQIP(nqip)
	if len(sessions) == 0 {
		return SessionEntry{}, false
	}
	s := sessions[0]
	entry := SessionEntry{
		NQIP:        nqip,
		SourceIP:         strings.TrimSpace(s.SourceIP()),
		UserID:           strings.TrimSpace(s.Identity()),
		IsAdmin:          s.IsAdmin(),
		ActiveServerPort: s.ActiveServerPort(),
	}
	applyActiveServer(&entry, env, serversByListenPort(env))
	return entry, true
}

// applyActiveServer resolves entry.ActiveServerHost from the port map, and
// clears the port when it refers to an unmanaged listener.
func applyActiveServer(entry *SessionEntry, env *Env, serverByPort map[int]string) {
	port := entry.ActiveServerPort
	if port < 1 || port > 65535 {
		entry.ActiveServerPort = 0
		return
	}
	if host, ok := serverByPort[port]; ok {
		entry.ActiveServerHost = host
		return
	}
	if env.IsManagedListenPort != nil && !env.IsManagedListenPort(port) {
		entry.ActiveServerPort = 0
	}
}

// serversByListenPort returns a map of listen port → display hostname for
// the current managed servers. Prefers InstanceSnapshots (covers running
// instances); falls back to ServerSnapshots when instances aren't available.
func serversByListenPort(env *Env) map[int]string {
	if env == nil {
		return nil
	}
	if env.InstanceSnapshots != nil {
		if backs, err := env.InstanceSnapshots(0); err == nil && len(backs) > 0 {
			return hostnameByListenPort(backs)
		}
	}
	if env.ServerSnapshots != nil {
		return hostnameByListenPort(env.ServerSnapshots())
	}
	return nil
}

func hostnameByListenPort(snapshots []ServerInfo) map[int]string {
	out := make(map[int]string, len(snapshots))
	for _, snap := range snapshots {
		if snap.ListenPort < 1 || snap.ListenPort > 65535 {
			continue
		}
		if _, exists := out[snap.ListenPort]; exists {
			continue
		}
		out[snap.ListenPort] = displayHostname(strings.TrimSpace(snap.Hostname))
	}
	return out
}

func compareClientIPText(a, b string) int {
	ipa, oka := netip.ParseAddr(a)
	ipb, okb := netip.ParseAddr(b)
	if oka == nil && okb == nil {
		return ipa.Compare(ipb)
	}
	return cmp.Compare(a, b)
}

func applyServerKickTargets(targets []session.BanTarget, env *Env) (applied int, errs []error) {
	if env == nil || env.DispatchInstanceCmd == nil {
		return 0, nil
	}
	for _, target := range targets {
		if target.Port < 1 || target.Port > 65535 || strings.TrimSpace(target.NQIP) == "" {
			continue
		}
		if err := kickServerTargetByNQIP(target, env); err != nil {
			errs = append(errs, fmt.Errorf("server %d: %w", target.Port, err))
			continue
		}
		applied++
	}
	return applied, errs
}

func kickServerTargetByNQIP(target session.BanTarget, env *Env) error {
	statusReply, err := env.DispatchInstanceCmd(target.Port, "status", "")
	if err != nil {
		return fmt.Errorf("status lookup failed: %w", err)
	}
	match, ok := statusPlayerForNQIP(statusReply, target.NQIP)
	if !ok {
		return fmt.Errorf("no active player with nqip %q", target.NQIP)
	}
	kickCmd := fmt.Sprintf("kick # %d Nexus ban", match.slot)
	if _, err := env.DispatchInstanceCmd(target.Port, kickCmd, ""); err != nil {
		return fmt.Errorf("kick failed: %w", err)
	}
	return nil
}

func uniqueSortedSourceIPs(sessions []*session.Session) []string {
	if len(sessions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		sourceIP := strings.TrimSpace(s.SourceIP())
		if sourceIP == "" {
			continue
		}
		seen[sourceIP] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for sourceIP := range seen {
		out = append(out, sourceIP)
	}
	slices.SortFunc(out, compareClientIPText)
	return out
}

// statusPlayer holds the server-reported slot info for a single player.
type statusPlayer struct {
	slot    int
	summary string
	address string
}

// statusPlayerForNQIP scans a Quake `status` reply for the player whose
// address line starts with nqip.
func statusPlayerForNQIP(statusReply, nqip string) (statusPlayer, bool) {
	nqip = strings.TrimSpace(nqip)
	if nqip == "" {
		return statusPlayer{}, false
	}
	current := statusPlayer{}
	for _, line := range strings.Split(strings.ReplaceAll(statusReply, "\r", ""), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			fields := strings.Fields(strings.TrimPrefix(trimmed, "#"))
			current = statusPlayer{}
			if len(fields) == 0 {
				continue
			}
			slot, err := strconv.Atoi(fields[0])
			if err != nil || slot <= 0 {
				continue
			}
			current.slot = slot
			current.summary = trimmed
			continue
		}
		if current.slot <= 0 {
			continue
		}
		if strings.HasPrefix(trimmed, nqip) {
			if len(trimmed) == len(nqip) || trimmed[len(nqip)] == ':' {
				current.address = trimmed
				return current, true
			}
		}
		current = statusPlayer{}
	}
	return statusPlayer{}, false
}
