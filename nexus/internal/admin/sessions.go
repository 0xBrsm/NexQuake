package admin

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// SessionInfo is a point-in-time view of a single client session.
type SessionInfo struct {
	VirtualIP        string // NQ virtual IP assigned to this session.
	SourceIP         string // Real client IP (possibly from a trusted proxy header).
	UserID           string // Authenticated identity string, or empty/anonymous.
	IsAdmin          bool   // Whether the session holds admin privileges.
	ActiveServerPort int    // Listen port of the server the client is connected to, or 0.
}

// BanTarget identifies a client to kick on a specific game server port.
type BanTarget struct {
	Port      int    // Listen port of the target game server.
	VirtualIP string // NQ virtual IP of the player to kick.
}

type nexusClientRow struct {
	nqip    string
	source  string
	userID  string
	isAdmin bool
	port    int
	server  string
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

func queryNexusClientRows(env *Env) []nexusClientRow {
	sessions := env.SessionSnapshots()
	if len(sessions) == 0 {
		return nil
	}

	serverByPort := hostnameByListenPort(env.ServerSnapshots())
	if env.BackendSnapshots != nil {
		if snaps, err := env.BackendSnapshots(0); err == nil && len(snaps) > 0 {
			serverByPort = hostnameByListenPort(snaps)
		}
	}
	out := make([]nexusClientRow, 0, len(sessions))
	for _, session := range sessions {
		nqip := strings.TrimSpace(session.VirtualIP)
		if nqip == "" {
			continue
		}

		port := 0
		server := "-"
		if session.ActiveServerPort >= 1 && session.ActiveServerPort <= 65535 {
			if resolvedServer, ok := serverByPort[session.ActiveServerPort]; ok {
				port = session.ActiveServerPort
				server = resolvedServer // already normalized to "UNNAMED" by hostnameByListenPort
			} else if env.IsManagedListenPort != nil && env.IsManagedListenPort(session.ActiveServerPort) {
				port = session.ActiveServerPort
			}
		}

		out = append(out, nexusClientRow{
			nqip:    nqip,
			source:  strings.TrimSpace(session.SourceIP),
			userID:  strings.TrimSpace(session.UserID),
			isAdmin: session.IsAdmin,
			port:    port,
			server:  server,
		})
	}

	slices.SortFunc(out, func(a, b nexusClientRow) int {
		if c := compareClientIPText(a.nqip, b.nqip); c != 0 {
			return c
		}
		if c := compareClientIPText(a.source, b.source); c != 0 {
			return c
		}
		if c := cmp.Compare(a.server, b.server); c != 0 {
			return c
		}
		return cmp.Compare(a.port, b.port)
	})

	return out
}

func formatSessionUser(userID, sourceIP string) string {
	userID = strings.TrimSpace(userID)
	if userID != "" && !strings.EqualFold(userID, "anonymous") {
		return userID
	}
	sourceIP = strings.TrimSpace(sourceIP)
	if sourceIP == "" {
		sourceIP = "unknown"
	}
	return "(" + sourceIP + ")"
}

func sessionRole(isAdmin bool) string {
	if isAdmin {
		return "admin"
	}
	return "client"
}

func formatSessionPort(port int) string {
	if port < 1 || port > 65535 {
		return "-"
	}
	return strconv.Itoa(port)
}

func formatNexusClientList(rows []nexusClientRow) string {
	if len(rows) == 0 {
		return "\nNo active sessions found.\n\n"
	}

	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString("#   Role   User                     Server          Port\n")
	b.WriteString("--- ------ ------------------------ --------------- -----\n")
	for i, row := range rows {
		server := strings.TrimSpace(row.server)
		if server == "" {
			server = "-"
		}
		fmt.Fprintf(&b, "%-3d %-6.6s %-24.24s %-15.15s %5s\n",
			i+1, sessionRole(row.isAdmin), formatSessionUser(row.userID, row.source), server, formatSessionPort(row.port))
	}
	b.WriteString("== end list ==\n\n")
	return b.String()
}

func applyServerKickTargets(targets []BanTarget, env *Env) (applied int, errs []error) {
	for _, target := range targets {
		if target.Port < 1 || target.Port > 65535 || strings.TrimSpace(target.VirtualIP) == "" {
			continue
		}
		if err := kickServerTargetByVirtualIP(target, env); err != nil {
			errs = append(errs, fmt.Errorf("server %d: %w", target.Port, err))
			continue
		}
		applied++
	}
	return applied, errs
}

func kickServerTargetByVirtualIP(target BanTarget, env *Env) error {
	statusReply, err := env.DispatchServerCmd(strconv.Itoa(target.Port), "status", "")
	if err != nil {
		return fmt.Errorf("status lookup failed: %w", err)
	}

	match, ok := statusPlayerForVirtualIP(statusReply, target.VirtualIP)
	if !ok {
		return fmt.Errorf("no active player with nqip %q", target.VirtualIP)
	}

	kickCmd := fmt.Sprintf("kick # %d Nexus ban", match.slot)
	if _, err := env.DispatchServerCmd(strconv.Itoa(target.Port), kickCmd, ""); err != nil {
		return fmt.Errorf("kick failed: %w", err)
	}
	return nil
}

func uniqueSortedSourceIPs(sessions []Session) []string {
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

func resolveSessionIndex(raw string, env *Env) (nexusClientRow, int, error) {
	text := strings.TrimSpace(raw)
	idx, err := strconv.Atoi(text)
	if err != nil || idx <= 0 {
		return nexusClientRow{}, 0, fmt.Errorf("invalid session index %q", text)
	}

	rows := queryNexusClientRows(env)
	if idx > len(rows) {
		return nexusClientRow{}, 0, fmt.Errorf("unknown session index %d", idx)
	}
	return rows[idx-1], idx, nil
}

func executeSessionInfoCommand(rawIndex string, env *Env) (string, error) {
	row, idx, err := resolveSessionIndex(rawIndex, env)
	if err != nil {
		return "", err
	}

	server := strings.TrimSpace(row.server)
	if server == "" {
		server = "-"
	}
	source := strings.TrimSpace(row.source)
	if source == "" {
		source = "unknown"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "session #%d\n", idx)
	fmt.Fprintf(&b, "role: %s\n", sessionRole(row.isAdmin))
	fmt.Fprintf(&b, "user: %s\n", formatSessionUser(row.userID, row.source))
	fmt.Fprintf(&b, "source ip: %s\n", source)
	fmt.Fprintf(&b, "nqip: %s\n", row.nqip)
	fmt.Fprintf(&b, "server: %s\n", server)
	fmt.Fprintf(&b, "port: %s\n", formatSessionPort(row.port))

	if row.port < 1 || row.port > 65535 {
		b.WriteString("status: not connected to a server\n")
		return b.String(), nil
	}

	statusReply, err := env.DispatchServerCmd(strconv.Itoa(row.port), "status", "")
	if err != nil {
		return "", fmt.Errorf("status lookup failed: %w", err)
	}

	match, ok := statusPlayerForVirtualIP(statusReply, row.nqip)
	if !ok {
		b.WriteString("status: player not present in server status output\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "status slot: %d\n", match.slot)
	fmt.Fprintf(&b, "status line: %s\n", match.summary)
	fmt.Fprintf(&b, "status addr: %s\n", match.address)
	return b.String(), nil
}

func executeSessionBanCommand(rawIndex string, env *Env) (string, error) {
	row, _, err := resolveSessionIndex(rawIndex, env)
	if err != nil {
		return "", err
	}
	return executeBanByVirtualIP(row.nqip, env)
}

func executeBanByVirtualIP(virtualIP string, env *Env) (string, error) {
	virtualIP = strings.TrimSpace(virtualIP)
	if virtualIP == "" {
		return "", fmt.Errorf("missing session nqip")
	}

	sessions, targets := env.SnapshotByVIP(virtualIP)
	if len(sessions) == 0 {
		return "", fmt.Errorf("unknown active client ip %q", virtualIP)
	}
	for _, s := range sessions {
		if s.IsAdmin() {
			return "", fmt.Errorf("cannot ban admin sessions")
		}
	}
	applied, applyErrs := applyServerKickTargets(targets, env)
	for _, s := range sessions {
		env.ReserveAndBlock(s.ClientIP(), s.SourceKey())
		s.Close()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "banned %s\n", virtualIP)
	if sourceIPs := uniqueSortedSourceIPs(sessions); len(sourceIPs) > 0 {
		fmt.Fprintf(&b, "source ip(s): %s\n", strings.Join(sourceIPs, ", "))
	}
	fmt.Fprintf(&b, "disconnected %d session(s)\n", len(sessions))
	fmt.Fprintf(&b, "reserved route identity for %d session(s)\n", len(sessions))
	fmt.Fprintf(&b, "issued server kick to %d target(s)\n", applied)
	if len(applyErrs) > 0 {
		msg := applyErrs[0].Error()
		if len(applyErrs) > 1 {
			msg = fmt.Sprintf("%s (+%d more)", msg, len(applyErrs)-1)
		}
		fmt.Fprintf(&b, "warning: %s\n", msg)
	}
	b.WriteString("complete\n")
	return b.String(), nil
}

func execSessionCommand(cmdArgs []string, env *Env) (string, error) {
	usage := adminUsageAlternatives(
		"session list",
		"session info <idx>",
		"session ban <idx>",
	)
	if len(cmdArgs) < 1 {
		return "", usage
	}
	subcmd := strings.ToLower(cmdArgs[0])
	subArgs := cmdArgs[1:]
	switch subcmd {
	case "list":
		subForm := "session list"
		if err := requireAdminArgs(subArgs, 0, subForm); err != nil {
			return "", err
		}
		return formatNexusClientList(queryNexusClientRows(env)), nil
	case "info":
		subForm := "session info <idx>"
		if err := requireAdminArgs(subArgs, 1, subForm); err != nil {
			return "", err
		}
		reply, err := executeSessionInfoCommand(subArgs[0], env)
		if err != nil {
			return "", withAdminUsage(err, subForm)
		}
		return reply, nil
	case "ban":
		subForm := "session ban <idx>"
		if err := requireAdminArgs(subArgs, 1, subForm); err != nil {
			return "", err
		}
		reply, err := executeSessionBanCommand(subArgs[0], env)
		if err != nil {
			return "", withAdminUsage(err, subForm)
		}
		return reply, nil
	default:
		return "", usage
	}
}

// statusPlayer holds the server-reported slot information for a single player,
// as extracted from a Quake `status` command reply.
type statusPlayer struct {
	slot    int    // Player slot number from the status line (e.g. "#1").
	summary string // Full "#N ..." status line for the player.
	address string // Address line following the slot line, e.g. "127.x.x.x:port".
}

// statusPlayerForVirtualIP scans a Quake `status` reply for the player whose
// address line starts with virtualIP. It returns the matching statusPlayer
// and true, or the zero value and false if no match is found.
func statusPlayerForVirtualIP(statusReply, virtualIP string) (statusPlayer, bool) {
	virtualIP = strings.TrimSpace(virtualIP)
	if virtualIP == "" {
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
		if strings.HasPrefix(trimmed, virtualIP) {
			if len(trimmed) == len(virtualIP) || trimmed[len(virtualIP)] == ':' {
				current.address = trimmed
				return current, true
			}
		}
		current = statusPlayer{}
	}

	return statusPlayer{}, false
}
