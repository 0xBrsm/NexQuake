package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/google/shlex"
)

// Env provides all external dependencies for admin command execution.
type Env struct {
	ServerSnapshots func() []orch.ServerSnapshot
	StartServer     func(target int) error
	StopServer      func(ctx context.Context, target int, killAfter time.Duration) error
	RestartServer   func(ctx context.Context, target int, killAfter time.Duration) error
	RemoveServer    func(target int) error
	LaunchServer    func(binary string, args []string) error
	ExecServerCmd   func(port int, cmd string) (string, error)
	TailNexusLog    func(n int) []string

	SessionSnapshots func() []nqnet.SessionSnapshot
	SnapshotByVIP    func(vip string) ([]*nqnet.Router, []nqnet.BanTarget)
	ReserveAndBlock  func(ip [4]byte, sourceKey string)
}

type adminCommandSpec struct {
	Form        string
	Description string
}

var adminCommandSpecs = []adminCommandSpec{
	{Form: "help", Description: "show Nexus rcon commands"},
	{Form: "tail", Description: "show last 10 Nexus log lines"},
	{Form: "slist", Description: "list managed servers"},
	{Form: "sessions", Description: "list connected client sessions"},
	{Form: "start <idx|port|all>", Description: "start one or all servers"},
	{Form: "stop <idx|port|all>", Description: "stop one or all servers"},
	{Form: "restart <idx|port|all>", Description: "restart one or all servers"},
	{Form: "remove <idx|port>", Description: "remove a stopped server from registry"},
	{Form: "launch <binary> [args...]", Description: "launch and register a new server"},
	{Form: "ban <idx|NQIP>", Description: "ban a session/NQIP from all servers until Nexus restart"},
}

func formatNexusServerList(servers []orch.ServerSnapshot) string {
	if len(servers) == 0 {
		return "\nNo Quake servers found.\n\n"
	}

	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString("#   Port  Server          Game            Users State\n")
	b.WriteString("--- ----- --------------- --------------- ----- --------\n")
	for i, s := range servers {
		server := s.Hostname
		if server == "" {
			server = "UNNAMED"
		}
		gameDir := s.GameDir
		if gameDir == "" {
			gameDir = "?"
		}
		users := "--/--"
		if s.MaxPlayers > 0 {
			users = fmt.Sprintf("%2d/%2d", s.Players, s.MaxPlayers)
		}
		fmt.Fprintf(&b, "%-3d %5d %-15.15s %-15.15s %5s %-8.8s\n",
			i+1, s.ListenPort, server, gameDir, users, s.State)
	}
	b.WriteString("== end list ==\n\n")
	return b.String()
}

type nexusClientRow struct {
	NQIP    string
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
	return strings.Compare(a, b)
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
			IsAdmin: session.IsAdmin,
			Port:    port,
			Server:  server,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if cmp := compareClientIPText(out[i].NQIP, out[j].NQIP); cmp != 0 {
			return cmp < 0
		}
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return false
	})

	return out
}

func formatNexusClientList(rows []nexusClientRow) string {
	if len(rows) == 0 {
		return "\nNo active sessions found.\n\n"
	}

	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString("#   NQIP             Role   Port  Server\n")
	b.WriteString("--- --------------- ------ ----- ---------------\n")
	for i, row := range rows {
		nqip := strings.TrimSpace(row.NQIP)
		if nqip == "" {
			nqip = "?"
		}
		role := "client"
		if row.IsAdmin {
			role = "admin"
		}
		port := "-"
		if row.Port >= 1 && row.Port <= 65535 {
			port = fmt.Sprintf("%d", row.Port)
		}
		server := strings.TrimSpace(row.Server)
		if server == "" {
			server = "-"
		}
		fmt.Fprintf(&b, "%-3d %-15.15s %-6.6s %5s %-15.15s\n", i+1, nqip, role, port, server)
	}
	b.WriteString("== end list ==\n\n")
	return b.String()
}

func formatNexusTailReply(lines []string) string {
	if len(lines) == 0 {
		return "No buffered Nexus logs.\n"
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
		return "No buffered Nexus logs.\n"
	}
	return out.String()
}

func parseNexusTarget(text string) (int, error) {
	target, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || target <= 0 {
		return 0, fmt.Errorf("invalid target %q", text)
	}
	return target, nil
}

func adminUsage(form string) error {
	return fmt.Errorf("usage: rcon %s", form)
}

func formatAdminCommandHelp() string {
	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString("Nexus commands:\n")
	for _, spec := range adminCommandSpecs {
		fmt.Fprintf(&b, "  %-28s %s\n", spec.Form, spec.Description)
	}
	b.WriteString("\n\n")
	return b.String()
}

func applyServerBanTargets(targets []nqnet.BanTarget, env *Env) (applied int, errs []error) {
	for _, target := range targets {
		if target.Port < 1 || target.Port > 65535 || strings.TrimSpace(target.VirtualIP) == "" {
			continue
		}
		if _, err := env.ExecServerCmd(target.Port, fmt.Sprintf("ban %s", target.VirtualIP)); err != nil {
			errs = append(errs, fmt.Errorf("server %d: %w", target.Port, err))
			continue
		}
		applied++
	}
	return applied, errs
}

func executeBanCommand(rawTarget string, env *Env) (string, error) {
	virtualIP, err := resolveBanVirtualIP(rawTarget, env)
	if err != nil {
		return "", err
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
	applied, applyErrs := applyServerBanTargets(targets, env)
	for _, router := range routers {
		env.ReserveAndBlock(router.ClientIP(), router.SourceKey())
		router.Close()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "banned %s\n", virtualIP)
	fmt.Fprintf(&b, "disconnected %d session(s)\n", len(routers))
	fmt.Fprintf(&b, "reserved route identity for %d session(s)\n", len(routers))
	fmt.Fprintf(&b, "issued server ban to %d target(s)\n", applied)
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

func normalizeVirtualClientIP(raw string) (string, error) {
	ip, ok := nqnet.ParseClientIP(raw)
	if !ok {
		return "", fmt.Errorf("invalid client ip %q", strings.TrimSpace(raw))
	}
	if !ip.Is4() {
		return "", fmt.Errorf("invalid client ip %q", strings.TrimSpace(raw))
	}
	oct := ip.As4()
	if oct[0] != 127 {
		return "", fmt.Errorf("client ip must be a virtual relay NQIP (127.x.x.x)")
	}
	return ip.String(), nil
}

func resolveBanVirtualIP(raw string, env *Env) (string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", fmt.Errorf("missing ban target")
	}
	if idx, err := strconv.Atoi(target); err == nil {
		if idx <= 0 {
			return "", fmt.Errorf("invalid session index %q", target)
		}
		rows := queryNexusClientRows(env)
		if idx > len(rows) {
			return "", fmt.Errorf("unknown session index %d", idx)
		}
		target = rows[idx-1].NQIP
	}
	return normalizeVirtualClientIP(target)
}

// execNexusCommand dispatches a nexus admin command and returns its reply.
func execNexusCommand(args string, env *Env) (string, error) {
	if env == nil {
		return "", fmt.Errorf("server manager not available")
	}

	usage := adminUsage
	withUsage := func(err error, form string) error {
		if err == nil {
			return nil
		}
		return fmt.Errorf("%v\n%v", err, usage(form))
	}
	requireArgs := func(got []string, want int, form string) error {
		if len(got) != want {
			return usage(form)
		}
		return nil
	}

	parts, splitErr := shlex.Split(strings.TrimSpace(args))
	if splitErr != nil {
		return formatAdminCommandHelp(), nil
	}
	if len(parts) > 0 && strings.EqualFold(parts[0], "nexus") {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return formatAdminCommandHelp(), nil
	}

	cmd := strings.ToLower(parts[0])
	cmdArgs := parts[1:]

	switch cmd {
	case "help":
		if err := requireArgs(cmdArgs, 0, "help"); err != nil {
			return "", err
		}
		return formatAdminCommandHelp(), nil

	case "tail":
		form := "tail"
		if err := requireArgs(cmdArgs, 0, form); err != nil {
			return "", err
		}
		return formatNexusTailReply(env.TailNexusLog(defaultServerTailLines)), nil

	case "slist":
		if err := requireArgs(cmdArgs, 0, "slist"); err != nil {
			return "", err
		}
		return formatNexusServerList(env.ServerSnapshots()), nil

	case "sessions":
		form := "sessions"
		if err := requireArgs(cmdArgs, 0, form); err != nil {
			return "", err
		}
		return formatNexusClientList(queryNexusClientRows(env)), nil

	case "launch":
		form := "launch <binary> [args...]"
		if len(cmdArgs) < 1 {
			return "", usage(form)
		}
		binary := cmdArgs[0]
		launchArgs := append([]string(nil), cmdArgs[1:]...)
		if err := env.LaunchServer(binary, launchArgs); err != nil {
			return "", withUsage(err, form)
		}
		return "server launched\n", nil

	case "remove":
		form := "remove <idx|port>"
		if err := requireArgs(cmdArgs, 1, form); err != nil {
			return "", err
		}
		target, parseErr := parseNexusTarget(strings.TrimSpace(cmdArgs[0]))
		if parseErr != nil {
			return "", withUsage(parseErr, form)
		}
		if err := env.RemoveServer(target); err != nil {
			return "", withUsage(err, form)
		}
		return "server removed\n", nil

	case "ban":
		form := "ban <idx|NQIP>"
		if err := requireArgs(cmdArgs, 1, form); err != nil {
			return "", err
		}
		reply, err := executeBanCommand(cmdArgs[0], env)
		if err != nil {
			return "", withUsage(err, form)
		}
		return reply, nil

	case "start", "stop", "restart":
		form := cmd + " <idx|port|all>"
		if err := requireArgs(cmdArgs, 1, form); err != nil {
			return "", err
		}
		runOne := func(target int) error {
			if cmd == "start" {
				return env.StartServer(target)
			}
			if cmd == "stop" {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				return env.StopServer(ctx, target, 2*time.Second)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return env.RestartServer(ctx, target, 2*time.Second)
		}
		runAll := func() error {
			servers := env.ServerSnapshots()
			var errs []error
			for i := range servers {
				target := i + 1
				err := runOne(target)
				if err == nil {
					continue
				}
				if cmd == "start" && errors.Is(err, orch.ErrAlreadyRunning) {
					continue
				}
				if cmd == "stop" && errors.Is(err, orch.ErrAlreadyStopped) {
					continue
				}
				errs = append(errs, fmt.Errorf("target %d: %w", target, err))
			}
			return errors.Join(errs...)
		}

		targetArg := strings.TrimSpace(cmdArgs[0])
		var err error
		if strings.EqualFold(targetArg, "all") {
			err = runAll()
		} else {
			target, parseErr := parseNexusTarget(targetArg)
			if parseErr != nil {
				return "", withUsage(parseErr, form)
			}
			err = runOne(target)
		}
		if err != nil {
			return "", withUsage(err, form)
		}
		return "complete\n", nil
	}

	return formatAdminCommandHelp(), nil
}
