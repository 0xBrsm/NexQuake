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
	Hostname   string // Server hostname reported by the game server.
	ListenPort int    // UDP/TCP listen port.
	GameDir    string // Quake game directory (e.g. "id1", "ctf").
	Players    int    // Current player count.
	MaxPlayers int    // Maximum allowed players; 0 means unknown.
	State      string // Lifecycle state string (e.g. "running", "stopped").
}

// Env provides all external dependencies for admin command execution.
// Callers construct an Env at startup and pass it to [HandleAdminFrame];
// all fields are optional unless the corresponding admin command is used.
type Env struct {
	// ServerSnapshots returns a current point-in-time list of managed servers.
	ServerSnapshots func() []ServerInfo
	// StartServer starts the server identified by idx or port.
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
	// ExecServerCmd sends a console command to a game server and returns its reply.
	// actorID is included in server-side audit logs.
	ExecServerCmd func(port int, cmd, actorID string) (string, error)
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
	Form        string
	Description string
}

var adminCommandSpecs = []adminCommandSpec{
	{Form: "help", Description: "show Nexus rcon commands"},
	{Form: "tail", Description: "show last 10 Nexus log lines"},
	{Form: "slist", Description: "list managed servers"},
	{Form: "session list", Description: "list connected client sessions"},
	{Form: "session info <idx>", Description: "show detailed info for one session"},
	{Form: "session ban <idx>", Description: "ban one session from all servers until Nexus restart"},
	{Form: "start <idx|port|all>", Description: "start one or all servers"},
	{Form: "stop <idx|port|all>", Description: "stop one or all servers"},
	{Form: "restart <idx|port|all>", Description: "restart one or all servers"},
	{Form: "remove <idx|port>", Description: "remove a stopped server from registry"},
	{Form: "launch <binary> [args...]", Description: "launch and register a new server"},
}

func formatNexusServerList(servers []ServerInfo) string {
	if len(servers) == 0 {
		return "\nNo Quake servers found.\n\n"
	}

	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString("#   Server          Port  Game            Users State\n")
	b.WriteString("--- --------------- ----- --------------- ----- --------\n")
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
		fmt.Fprintf(&b, "%-3d %-15.15s %5d %-15.15s %5s %-8.8s\n",
			i+1, server, s.ListenPort, gameDir, users, s.State)
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
		fmt.Fprintf(&b, "  %-28s %s\n", spec.Form, spec.Description)
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
	form := cmd + " <idx|port|all>"
	if err := requireAdminArgs(cmdArgs, 1, form); err != nil {
		return "", err
	}

	targetArg := strings.TrimSpace(cmdArgs[0])
	var err error
	if strings.EqualFold(targetArg, "all") {
		err = runServerLifecycleAll(cmd, env)
	} else {
		target, parseErr := parseNexusTarget(targetArg)
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

// execNexusCommand dispatches a Nexus admin command and returns its reply.
func execNexusCommand(args string, env *Env) (string, error) {
	if env == nil {
		return "", fmt.Errorf("server manager not available")
	}

	parts, splitErr := shlex.Split(strings.TrimSpace(args))
	if splitErr != nil || len(parts) == 0 {
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
		if err := requireAdminArgs(cmdArgs, 0, "slist"); err != nil {
			return "", err
		}
		return formatNexusServerList(env.ServerSnapshots()), nil

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
		form := "remove <idx|port>"
		if err := requireAdminArgs(cmdArgs, 1, form); err != nil {
			return "", err
		}
		target, err := parseNexusTarget(cmdArgs[0])
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

	return formatAdminCommandHelp(), nil
}
