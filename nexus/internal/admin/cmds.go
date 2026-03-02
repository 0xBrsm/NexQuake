package admin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/shlex"
)

// ServerInfo is a point-in-time view of a managed server for admin display.
type ServerInfo struct {
	Line          int    // 0-based pool line index.
	Hostname      string // Server hostname reported by the game server.
	MapName       string // Current map name.
	CandidatePort int    // Suggested connect port for a pool snapshot.
	ListenPort    int    // Backend UDP/TCP listen port.
	GameDir       string // Quake game directory (e.g. "id1", "ctf").
	Players       int    // Current player count.
	MaxPlayers    int    // Maximum allowed players; 0 means unknown.
	Instances     int    // Pool instance count; 0 hides the suffix.
	State         string // Lifecycle state string (e.g. "running", "stopped").
}

// Env provides all external dependencies for admin command execution.
// Callers construct an Env at startup and pass it to
// [HandleAdminFrameWithIdentityAndPromotionHook];
// all fields are optional unless the corresponding admin command is used.
type Env struct {
	// ServerSnapshots returns a current point-in-time list of managed pools.
	ServerSnapshots func() []ServerInfo
	// BackendSnapshots returns a current point-in-time list of backend servers.
	// target=0 selects all pools; positive targets resolve as a pool index.
	BackendSnapshots func(target int) ([]ServerInfo, error)
	// StartServer starts the server identified by pool index.
	StartServer func(target int) error
	// StartServersAll starts all managed servers; may be nil if unavailable.
	StartServersAll func() error
	// StopServer stops one server; killAfter is the grace period before SIGKILL.
	StopServer func(ctx context.Context, target int, killAfter time.Duration) error
	// StopServersAll stops all servers; may be nil if unavailable.
	StopServersAll func(ctx context.Context, killAfter time.Duration) error
	// RestartServer restarts one server.
	RestartServer func(ctx context.Context, target int, killAfter time.Duration) error
	// RestartServersAll restarts all servers; may be nil if unavailable.
	RestartServersAll func(ctx context.Context, killAfter time.Duration) error
	// RemoveServer removes a stopped server from the registry.
	RemoveServer func(target int) error
	// LaunchServer starts a new server process and registers it.
	LaunchServer func(binary string, args []string) error
	// DispatchServerCmd sends a console command to a managed server selector.
	// Targets may be a hostname cache entry, listen port, or pool index.
	DispatchServerCmd func(target, cmd, actorID string) (string, error)
	// IsManagedListenPort reports whether a port belongs to a managed server that
	// is not yet reflected in ServerSnapshots (e.g. still starting up).
	IsManagedListenPort func(port int) bool
	// TailNexusLog returns the last n buffered Nexus log lines.
	TailNexusLog func(n int) []string
	// Auditf writes a structured audit log entry; may be nil to disable auditing.
	Auditf func(format string, args ...any)

	// SessionSnapshots returns a point-in-time list of all active client sessions.
	SessionSnapshots func() []SessionInfo
	// SnapshotByVIP returns the live [Session] handles and active [BanTarget] list
	// for every connection that holds the given virtual IP.
	SnapshotByVIP func(vip string) ([]Session, []BanTarget)
	// ReserveAndBlock permanently reserves the virtual IP slot and blocks the
	// source key so the banned client cannot reconnect.
	ReserveAndBlock func(ip [4]byte, sourceKey string)
}

type adminCommandSpec struct {
	form        string
	description string
}

const (
	topLevelRconUsage = "usage: rcon <cmd> | rcon nexus <cmd> | rcon <host|port|idx> <cmd>\n"
	nexusRconPrefix   = "rcon nexus "
	poolTargetForm    = "<all|idx|host>"
)

var adminCommandSpecs = []adminCommandSpec{
	{form: "help", description: "show Nexus rcon commands"},
	{form: "tail", description: "show last 10 Nexus log lines"},
	{form: "slist [" + poolTargetForm + "]", description: "list managed pools or backend servers"},
	{form: "session list", description: "list connected client sessions"},
	{form: "session info <idx>", description: "show detailed info for one session"},
	{form: "session ban <idx>", description: "ban session until Nexus restart"},
	{form: "start <idx|host|all>", description: "start one or all pools"},
	{form: "stop <idx|host|all>", description: "stop one or all pools"},
	{form: "restart <idx|host|all>", description: "restart one or all pools"},
	{form: "remove <idx|host>", description: "remove a stopped pool from registry"},
	{form: "launch <binary> [args...]", description: "launch and register a new server"},
}

func displayHostname(hostname string) string {
	if hostname == "" {
		return "UNNAMED"
	}
	return hostname
}

func displayField(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func formatNexusPoolList(servers []ServerInfo) string {
	if len(servers) == 0 {
		return "\nNo Quake pools found.\n\n"
	}

	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString("#   Pool            Candidate Game            Users        State\n")
	b.WriteString("--- --------------- --------- --------------- ------------ --------\n")
	for i, s := range servers {
		fmt.Fprintf(&b, "%-3d %-15.15s %9d %-15.15s %-12.12s %-8.8s\n",
			i+1, displayHostname(s.Hostname), s.CandidatePort, displayField(s.GameDir), formatPoolUsers(s), s.State)
	}
	b.WriteString("== end list ==\n\n")
	return b.String()
}

func formatPoolUsers(s ServerInfo) string {
	users := "--/--"
	if s.MaxPlayers > 0 {
		users = fmt.Sprintf("%d/%d", s.Players, s.MaxPlayers)
		if s.Instances > 0 {
			users = fmt.Sprintf("%s (%d)", users, s.Instances)
		}
	}
	return users
}

func formatBackendUsers(s ServerInfo) string {
	if s.MaxPlayers <= 0 {
		return "--/--"
	}
	return fmt.Sprintf("%d/%d", s.Players, s.MaxPlayers)
}

func formatNexusBackendGroup(b *strings.Builder, poolIndex int, pool ServerInfo, servers []ServerInfo) {
	fmt.Fprintf(b, "[%d] %s  game=%s  users=%s  candidate=%d  state=%s\n",
		poolIndex, displayHostname(pool.Hostname), displayField(pool.GameDir), formatPoolUsers(pool), pool.CandidatePort, pool.State)
	b.WriteString("    #  Port  Map             Users   State\n")
	b.WriteString("    -- ----- --------------- ------- --------\n")
	for i, s := range servers {
		fmt.Fprintf(b, "    %-2d %5d %-15.15s %-7.7s %-8.8s\n",
			i+1, s.ListenPort, displayField(s.MapName), formatBackendUsers(s), s.State)
	}
}

func formatNexusBackendList(allPools, selectedPools, servers []ServerInfo) string {
	grouped := make(map[int][]ServerInfo, len(selectedPools))
	for _, s := range servers {
		grouped[s.Line] = append(grouped[s.Line], s)
	}
	indexByLine := make(map[int]int, len(allPools))
	for i, pool := range allPools {
		indexByLine[pool.Line] = i + 1
	}

	var b strings.Builder
	wrote := false
	for _, pool := range selectedPools {
		group := grouped[pool.Line]
		if len(group) == 0 {
			continue
		}
		b.WriteByte('\n')
		formatNexusBackendGroup(&b, indexByLine[pool.Line], pool, group)
		wrote = true
	}
	if !wrote {
		return "\nNo Quake backend servers found.\n\n"
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

func parseAdminIndex(text string) (int, error) {
	target, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || target <= 0 {
		return 0, fmt.Errorf("invalid target %q", text)
	}
	return target, nil
}

func normalizePoolTargetName(hostname string) string {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "UNNAMED"
	}
	return hostname
}

func resolvePoolTarget(text string, env *Env) (int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, fmt.Errorf("invalid target %q", text)
	}

	hasSnapshots := env != nil && env.ServerSnapshots != nil
	var snapshots []ServerInfo
	if hasSnapshots {
		snapshots = env.ServerSnapshots()
	}

	if idx, err := parseAdminIndex(text); err == nil {
		if !hasSnapshots || idx <= len(snapshots) {
			return idx, nil
		}
		return 0, fmt.Errorf("unknown target %q", text)
	}

	if !hasSnapshots {
		return 0, fmt.Errorf("unknown target %q", text)
	}

	match := 0
	for i, snap := range snapshots {
		if !strings.EqualFold(text, normalizePoolTargetName(snap.Hostname)) {
			continue
		}
		if match != 0 {
			return 0, fmt.Errorf("ambiguous target %q", text)
		}
		match = i + 1
	}
	if match == 0 {
		return 0, fmt.Errorf("unknown target %q", text)
	}
	return match, nil
}

func adminCommandForm(form string) string {
	return nexusRconPrefix + form
}

func adminUsageLine(form string) string {
	return "usage: " + adminCommandForm(form)
}

func adminUsage(form string) error {
	return fmt.Errorf("%s", adminUsageLine(form))
}

func adminUsageAlternatives(forms ...string) error {
	alternatives := make([]string, 0, len(forms))
	for _, form := range forms {
		alternatives = append(alternatives, adminCommandForm(form))
	}
	return fmt.Errorf("usage: %s", strings.Join(alternatives, " | "))
}

func adminHelpError(detail string) error {
	return fmt.Errorf("%s\n%s", detail, adminUsageLine("help"))
}

func withAdminUsage(err error, form string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%v\n%v", err, adminUsage(form))
}

func requireAdminArgs(got []string, want int, form string) error {
	if len(got) != want {
		return adminUsage(form)
	}
	return nil
}

func formatAdminCommandHelp() string {
	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString("Nexus commands:\n")
	for _, spec := range adminCommandSpecs {
		fmt.Fprintf(&b, "  %-35s %s\n", adminCommandForm(spec.form), spec.description)
	}
	b.WriteString("\n\n")
	return b.String()
}

func runServerLifecycleOne(cmd string, target int, env *Env) error {
	switch cmd {
	case "start":
		return env.StartServer(target)
	case "stop":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return env.StopServer(ctx, target, 2*time.Second)
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return env.RestartServer(ctx, target, 2*time.Second)
	}
}

func runServerLifecycleAll(cmd string, env *Env) error {
	switch cmd {
	case "start":
		if env.StartServersAll == nil {
			return fmt.Errorf("start all unavailable")
		}
		return env.StartServersAll()
	case "stop":
		if env.StopServersAll == nil {
			return fmt.Errorf("stop all unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return env.StopServersAll(ctx, 2*time.Second)
	default:
		if env.RestartServersAll == nil {
			return fmt.Errorf("restart all unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return env.RestartServersAll(ctx, 2*time.Second)
	}
}

func execServerLifecycleCommand(cmd string, cmdArgs []string, env *Env) (string, error) {
	form := cmd + " <idx|host|all>"
	if err := requireAdminArgs(cmdArgs, 1, form); err != nil {
		return "", err
	}

	targetArg := strings.TrimSpace(cmdArgs[0])
	var err error
	if strings.EqualFold(targetArg, "all") {
		err = runServerLifecycleAll(cmd, env)
	} else {
		target, parseErr := resolvePoolTarget(targetArg, env)
		if parseErr != nil {
			return "", withAdminUsage(parseErr, form)
		}
		err = runServerLifecycleOne(cmd, target, env)
	}
	if err != nil {
		return "", withAdminUsage(err, form)
	}
	return "complete\n", nil
}

func execBackendListCommand(cmdArgs []string, env *Env) (string, error) {
	form := "slist " + poolTargetForm
	if err := requireAdminArgs(cmdArgs, 1, form); err != nil {
		return "", err
	}
	if env.BackendSnapshots == nil || env.ServerSnapshots == nil {
		return "", fmt.Errorf("server manager not available")
	}

	allPools := env.ServerSnapshots()
	targetArg := strings.TrimSpace(cmdArgs[0])
	target := 0
	selectedPools := allPools
	if !strings.EqualFold(targetArg, "all") {
		parsed, err := resolvePoolTarget(targetArg, env)
		if err != nil {
			return "", withAdminUsage(err, form)
		}
		target = parsed
		if target > 0 && target <= len(allPools) {
			selectedPools = allPools[target-1 : target]
		}
	}

	servers, err := env.BackendSnapshots(target)
	if err != nil {
		return "", withAdminUsage(err, form)
	}
	return formatNexusBackendList(allPools, selectedPools, servers), nil
}

// execNexusCommand dispatches a Nexus admin command and returns its reply.
func execNexusCommand(args string, env *Env) (string, error) {
	if env == nil {
		return "", fmt.Errorf("server manager not available")
	}

	parts, splitErr := shlex.Split(strings.TrimSpace(args))
	if splitErr != nil {
		return "", adminHelpError(fmt.Sprintf("invalid Nexus command line: %v", splitErr))
	}
	if len(parts) == 0 {
		return formatAdminCommandHelp(), nil
	}
	if strings.EqualFold(parts[0], "nexus") {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return formatAdminCommandHelp(), nil
	}

	cmd := strings.ToLower(parts[0])
	cmdArgs := parts[1:]

	switch cmd {
	case "help":
		if err := requireAdminArgs(cmdArgs, 0, "help"); err != nil {
			return "", err
		}
		return formatAdminCommandHelp(), nil

	case "tail":
		if err := requireAdminArgs(cmdArgs, 0, "tail"); err != nil {
			return "", err
		}
		return formatNexusTailReply(env.TailNexusLog(defaultServerTailLines)), nil

	case "slist":
		switch len(cmdArgs) {
		case 0:
			return formatNexusPoolList(env.ServerSnapshots()), nil
		case 1:
			return execBackendListCommand(cmdArgs, env)
		default:
			return "", adminUsage("slist [" + poolTargetForm + "]")
		}

	case "session":
		return execSessionCommand(cmdArgs, env)

	case "launch":
		form := "launch <binary> [args...]"
		if len(cmdArgs) < 1 {
			return "", adminUsage(form)
		}
		binary := cmdArgs[0]
		launchArgs := append([]string(nil), cmdArgs[1:]...)
		if err := env.LaunchServer(binary, launchArgs); err != nil {
			return "", withAdminUsage(err, form)
		}
		return "server launched\n", nil

	case "remove":
		form := "remove <idx|host>"
		if err := requireAdminArgs(cmdArgs, 1, form); err != nil {
			return "", err
		}
		target, err := resolvePoolTarget(cmdArgs[0], env)
		if err != nil {
			return "", withAdminUsage(err, form)
		}
		if err := env.RemoveServer(target); err != nil {
			return "", withAdminUsage(err, form)
		}
		return "server removed\n", nil

	case "start", "stop", "restart":
		return execServerLifecycleCommand(cmd, cmdArgs, env)
	}

	return "", adminHelpError(fmt.Sprintf("unknown Nexus command %q", parts[0]))
}
