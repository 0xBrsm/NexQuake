package admin

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
)

type nexusClientRow struct {
	NQIP    string
	Source  string
	UserID  string
	IsAdmin bool
	Port    int
	Server  string
}

func hostnameByListenPort(snapshots []orch.ServerSnapshot) map[int]string {
	out := make(map[int]string, len(snapshots))
	for _, snap := range snapshots {
		if snap.ListenPort < 1 || snap.ListenPort > 65535 {
			continue
		}
		if _, exists := out[snap.ListenPort]; exists {
			continue
		}
		hostname := strings.TrimSpace(snap.Hostname)
		if hostname == "" {
			hostname = "UNNAMED"
		}
		out[snap.ListenPort] = hostname
	}
	return out
}

func compareClientIPText(a, b string) int {
	ipa, oka := nqnet.ParseClientIP(a)
	ipb, okb := nqnet.ParseClientIP(b)
	if oka && okb {
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
	out := make([]nexusClientRow, 0, len(sessions))
	for _, session := range sessions {
		nqip := strings.TrimSpace(session.VirtualIP)
		if nqip == "" {
			continue
		}

		port := 0
		server := "-"
		if session.ActiveServerPort >= 1 && session.ActiveServerPort <= 65535 {
			port = session.ActiveServerPort
			server = serverByPort[session.ActiveServerPort]
			if strings.TrimSpace(server) == "" {
				server = "UNNAMED"
			}
		}

		out = append(out, nexusClientRow{
			NQIP:    nqip,
			Source:  strings.TrimSpace(session.SourceIP),
			UserID:  strings.TrimSpace(session.UserID),
			IsAdmin: session.IsAdmin,
			Port:    port,
			Server:  server,
		})
	}

	slices.SortFunc(out, func(a, b nexusClientRow) int {
		if c := compareClientIPText(a.NQIP, b.NQIP); c != 0 {
			return c
		}
		if c := compareClientIPText(a.Source, b.Source); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Server, b.Server); c != 0 {
			return c
		}
		return cmp.Compare(a.Port, b.Port)
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
	b.WriteString("#   Role   User                 Server          Port\n")
	b.WriteString("--- ------ -------------------- --------------- -----\n")
	for i, row := range rows {
		server := strings.TrimSpace(row.Server)
		if server == "" {
			server = "-"
		}
		fmt.Fprintf(&b, "%-3d %-6.6s %-20.20s %-15.15s %5s\n",
			i+1, sessionRole(row.IsAdmin), formatSessionUser(row.UserID, row.Source), server, formatSessionPort(row.Port))
	}
	b.WriteString("== end list ==\n\n")
	return b.String()
}

func applyServerKickTargets(targets []nqnet.BanTarget, env *Env) (applied int, errs []error) {
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

func kickServerTargetByVirtualIP(target nqnet.BanTarget, env *Env) error {
	statusReply, err := env.ExecServerCmd(target.Port, "status", "")
	if err != nil {
		return fmt.Errorf("status lookup failed: %w", err)
	}

	match, ok := StatusPlayerForVirtualIP(statusReply, target.VirtualIP)
	if !ok {
		return fmt.Errorf("no active player with nqip %q", target.VirtualIP)
	}

	kickCmd := fmt.Sprintf("kick # %d Nexus ban", match.Slot)
	if _, err := env.ExecServerCmd(target.Port, kickCmd, ""); err != nil {
		return fmt.Errorf("kick failed: %w", err)
	}
	return nil
}

func uniqueSortedSourceIPs(routers []*nqnet.Router) []string {
	if len(routers) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(routers))
	for _, router := range routers {
		sourceIP := strings.TrimSpace(router.SourceIP())
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
	slices.SortFunc(out, func(a, b string) int {
		return compareClientIPText(a, b)
	})
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

	server := strings.TrimSpace(row.Server)
	if server == "" {
		server = "-"
	}
	source := strings.TrimSpace(row.Source)
	if source == "" {
		source = "unknown"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "session #%d\n", idx)
	fmt.Fprintf(&b, "role: %s\n", sessionRole(row.IsAdmin))
	fmt.Fprintf(&b, "user: %s\n", formatSessionUser(row.UserID, row.Source))
	fmt.Fprintf(&b, "source ip: %s\n", source)
	fmt.Fprintf(&b, "nqip: %s\n", row.NQIP)
	fmt.Fprintf(&b, "server: %s\n", server)
	fmt.Fprintf(&b, "port: %s\n", formatSessionPort(row.Port))

	if row.Port < 1 || row.Port > 65535 {
		b.WriteString("status: not connected to a server\n")
		return b.String(), nil
	}

	statusReply, err := env.ExecServerCmd(row.Port, "status", "")
	if err != nil {
		return "", fmt.Errorf("status lookup failed: %w", err)
	}

	match, ok := StatusPlayerForVirtualIP(statusReply, row.NQIP)
	if !ok {
		b.WriteString("status: player not present in server status output\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "status slot: %d\n", match.Slot)
	fmt.Fprintf(&b, "status line: %s\n", match.Summary)
	fmt.Fprintf(&b, "status addr: %s\n", match.Address)
	return b.String(), nil
}

func executeSessionBanCommand(rawIndex string, env *Env) (string, error) {
	row, _, err := resolveSessionIndex(rawIndex, env)
	if err != nil {
		return "", err
	}
	return executeBanByVirtualIP(row.NQIP, env)
}

func executeBanByVirtualIP(virtualIP string, env *Env) (string, error) {
	virtualIP = strings.TrimSpace(virtualIP)
	if virtualIP == "" {
		return "", fmt.Errorf("missing session nqip")
	}

	routers, targets := env.SnapshotByVIP(virtualIP)
	if len(routers) == 0 {
		return "", fmt.Errorf("unknown active client ip %q", virtualIP)
	}
	for _, router := range routers {
		if router.IsAdmin() {
			return "", fmt.Errorf("cannot ban admin sessions")
		}
	}
	applied, applyErrs := applyServerKickTargets(targets, env)
	for _, router := range routers {
		env.ReserveAndBlock(router.ClientIP(), router.SourceKey())
		router.Close()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "banned %s\n", virtualIP)
	if sourceIPs := uniqueSortedSourceIPs(routers); len(sourceIPs) > 0 {
		fmt.Fprintf(&b, "source ip(s): %s\n", strings.Join(sourceIPs, ", "))
	}
	fmt.Fprintf(&b, "disconnected %d session(s)\n", len(routers))
	fmt.Fprintf(&b, "reserved route identity for %d session(s)\n", len(routers))
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
	form := "session <list|info|ban>"
	if len(cmdArgs) < 1 {
		return "", adminUsage(form)
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
		return "", adminUsage(form)
	}
}

type StatusPlayer struct {
	Slot    int
	Summary string
	Address string
}

func StatusPlayerForVirtualIP(statusReply, virtualIP string) (StatusPlayer, bool) {
	virtualIP = strings.TrimSpace(virtualIP)
	if virtualIP == "" {
		return StatusPlayer{}, false
	}

	current := StatusPlayer{}
	for _, line := range strings.Split(strings.ReplaceAll(statusReply, "\r", ""), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			fields := strings.Fields(strings.TrimPrefix(trimmed, "#"))
			current = StatusPlayer{}
			if len(fields) == 0 {
				continue
			}
			slot, err := strconv.Atoi(fields[0])
			if err != nil || slot <= 0 {
				continue
			}
			current.Slot = slot
			current.Summary = trimmed
			continue
		}

		if current.Slot <= 0 {
			continue
		}
		if strings.HasPrefix(trimmed, virtualIP) {
			if len(trimmed) == len(virtualIP) || trimmed[len(virtualIP)] == ':' {
				current.Address = trimmed
				return current, true
			}
		}
		current = StatusPlayer{}
	}

	return StatusPlayer{}, false
}
