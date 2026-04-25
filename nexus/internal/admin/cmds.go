package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ServerInfo is a point-in-time view of a managed server for RPC responses.
type ServerInfo struct {
	Line          int    `json:"line"`     // 0-based server line index.
	Hostname      string `json:"hostname"` // Server hostname reported by the game server.
	MapName       string `json:"map_name,omitempty"`
	CandidatePort int    `json:"candidate_port,omitempty"`
	ListenPort    int    `json:"listen_port,omitempty"`
	GameDir       string `json:"game_dir,omitempty"`
	Players       int    `json:"players"`
	MaxPlayers    int    `json:"max_players"`
	Instances     int    `json:"instances,omitempty"`
	State         string `json:"state"`
}

// Env provides all external dependencies for admin command execution.
// Callers construct an Env at startup and pass it to [Dispatch]; all fields
// are optional unless the corresponding admin command is used.
type Env struct {
	ServerSnapshots     func() []ServerInfo
	InstanceSnapshots   func(target int) ([]ServerInfo, error)
	StartServer         func(target int) error
	StartServersAll     func() error
	StopServer          func(ctx context.Context, target int, killAfter time.Duration) error
	StopServersAll      func(ctx context.Context, killAfter time.Duration) error
	RestartServer       func(ctx context.Context, target int, killAfter time.Duration) error
	RestartServersAll   func(ctx context.Context, killAfter time.Duration) error
	RemoveServer        func(target int) error
	LaunchServer        func(binary string, args []string) error
	DispatchInstanceCmd func(port int, cmd, actorID string) (string, error)
	IsManagedListenPort func(port int) bool
	TailNexusLog        func(n int) []string
	Auditf              func(format string, args ...any)

	SessionSnapshots    func() []Snapshot
	SnapshotByVirtualIP func(nqip string) ([]*Session, []BanTarget)
	ReserveAndBlock     func(ip [4]byte, sourceKey string)
}

const defaultServerTailLines = 10

// --- Command registry -------------------------------------------------------

// Command describes a single JSON-RPC admin method. Description is the
// shared one-line purpose, used by both rcon.help (in-game rendering, keyed
// off HelpForm) and rpc.discover (structured self-description, keyed off
// Method). Commands with an empty HelpForm are omitted from rcon.help —
// they are not user-typed (e.g. [server.instance.command] is reached via
// `rcon <port> <cmd...>`).
type Command struct {
	Method      string
	HelpForm    string
	Description string
	ParseParams func(raw json.RawMessage) (any, error)
	Handler     func(env *Env, actor Actor, params any) (any, error)
}

var adminCommands = []Command{
	{Method: "rcon.help", HelpForm: "help", Description: "show in-game rcon command help",
		ParseParams: parseEmpty, Handler: rconHelpHandler},
	{Method: "rpc.discover", Description: "list all RPC methods with descriptions",
		ParseParams: parseEmpty, Handler: rpcDiscoverHandler},
	{Method: "logs.tail", HelpForm: "tail [N]", Description: "last N (default 10) Nexus log lines",
		ParseParams: parseLogsTail, Handler: logsTailHandler},
	{Method: "server.list", HelpForm: "server list", Description: "list managed servers",
		ParseParams: parseEmpty, Handler: serverListHandler},
	{Method: "server.instances", HelpForm: "server list <idx|all>", Description: "list instances of one or all servers",
		ParseParams: parseServerInstances, Handler: serverInstancesHandler},
	{Method: "server.start", HelpForm: "server start <idx|all>", Description: "start one or all servers",
		ParseParams: parseServerTarget, Handler: serverStartHandler},
	{Method: "server.stop", HelpForm: "server stop <idx|all> [secs]", Description: "stop one or all servers",
		ParseParams: parseServerStop, Handler: serverStopHandler},
	{Method: "server.restart", HelpForm: "server restart <idx|all> [secs]", Description: "restart one or all servers",
		ParseParams: parseServerStop, Handler: serverRestartHandler},
	{Method: "server.remove", HelpForm: "server remove <idx>", Description: "remove a stopped server",
		ParseParams: parseIndexOnly, Handler: serverRemoveHandler},
	{Method: "server.launch", HelpForm: "server launch <binary> [args...]", Description: "launch and register a new server",
		ParseParams: parseServerLaunch, Handler: serverLaunchHandler},
	{Method: "session.list", HelpForm: "session list", Description: "list active client sessions",
		ParseParams: parseEmpty, Handler: sessionListHandler},
	{Method: "session.info", HelpForm: "session info <nqip>", Description: "detail for one session",
		ParseParams: parseSessionLookup, Handler: sessionInfoHandler},
	{Method: "session.ban", HelpForm: "session ban <nqip>", Description: "ban session until Nexus restart",
		ParseParams: parseSessionLookup, Handler: sessionBanHandler},
	{Method: "server.instance.command", Description: "forward command to a specific instance",
		ParseParams: parseInstanceCommand, Handler: instanceCommandHandler},
}

// helpText and discoverMethods are built once in init() from adminCommands
// and served by rconHelpHandler / rpcDiscoverHandler. Routing reads through
// package-level vars (instead of iterating adminCommands inside the handlers)
// breaks the var-init cycle Go would otherwise flag.
var (
	helpText        string
	discoverMethods []RPCMethodInfo
	commandByMethod map[string]*Command
)

func init() {
	helpText = buildHelpText()
	discoverMethods = buildDiscoverMethods()
	commandByMethod = make(map[string]*Command, len(adminCommands))
	for i := range adminCommands {
		commandByMethod[adminCommands[i].Method] = &adminCommands[i]
	}
}

func lookupCommand(method string) (*Command, bool) {
	cmd, ok := commandByMethod[method]
	return cmd, ok
}

// --- Shared helpers ---------------------------------------------------------

func parseEmpty(_ json.RawMessage) (any, error) { return struct{}{}, nil }

func unmarshalParams[T any](raw json.RawMessage) (*T, error) {
	var p T
	if len(raw) == 0 || string(raw) == "null" {
		return &p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %v", err)
	}
	return &p, nil
}

// displayHostname coerces a potentially empty hostname into a human-readable
// placeholder for display enrichment (e.g. sessions linking to their current
// server). Not used for addressing — server identifiers are numeric indices.
func displayHostname(hostname string) string {
	if hostname == "" {
		return "UNNAMED"
	}
	return hostname
}

// parseServerTargetToken interprets a lifecycle target string. Accepts a
// positive 1-based server index (as shown in server.list) or the literal
// "all". Returns (index, isAll, error). Exactly one of index>0 or isAll=true
// on success. Hostnames are NOT accepted — servers are not guaranteed to
// carry one, so the registry index is the only stable identifier.
func parseServerTargetToken(target string) (int, bool, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return 0, false, &MethodError{Code: ErrCodeInvalidParams, Message: "target is required"}
	}
	if strings.EqualFold(target, "all") {
		return 0, true, nil
	}
	idx, err := strconv.Atoi(target)
	if err != nil || idx <= 0 {
		return 0, false, &MethodError{Code: ErrCodeInvalidParams, Message: fmt.Sprintf("invalid target %q: expected 1-based server index or \"all\"", target)}
	}
	return idx, false, nil
}

// --- server.list ------------------------------------------------------------

type ServerListResult struct {
	Servers []ServerInfo `json:"servers"`
}

func serverListHandler(env *Env, _ Actor, _ any) (any, error) {
	if env == nil || env.ServerSnapshots == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "server manager unavailable"}
	}
	return ServerListResult{Servers: env.ServerSnapshots()}, nil
}

// --- server.instances -------------------------------------------------------

type ServerInstancesParams struct {
	Index int `json:"index,omitempty"` // 1-based server index; 0/omitted = all servers
}

// ServerWithInstances is one server with its running instances nested.
// Server-level fields (the aggregate display) match ServerInfo; Instances holds
// the per-instance ServerInfos for the same server. The instance count is not
// repeated — len(Instances) serves that role.
type ServerWithInstances struct {
	Index         int          `json:"index"` // 1-based registry index
	Hostname      string       `json:"hostname"`
	GameDir       string       `json:"game_dir,omitempty"`
	State         string       `json:"state"`
	CandidatePort int          `json:"candidate_port,omitempty"`
	Players       int          `json:"players"`
	MaxPlayers    int          `json:"max_players"`
	Instances     []ServerInfo `json:"instances"`
}

type ServerInstancesResult struct {
	Servers []ServerWithInstances `json:"servers"`
}

func parseServerInstances(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[ServerInstancesParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Index < 0 {
		return nil, fmt.Errorf("invalid index %d: must be positive or omitted", p.Index)
	}
	return p, nil
}

func serverInstancesHandler(env *Env, _ Actor, params any) (any, error) {
	if env == nil || env.ServerSnapshots == nil || env.InstanceSnapshots == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "server manager unavailable"}
	}
	p := params.(*ServerInstancesParams)
	servers := env.ServerSnapshots()
	instances, err := env.InstanceSnapshots(p.Index)
	if err != nil {
		return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
	}

	byLine := make(map[int][]ServerInfo, len(servers))
	for _, b := range instances {
		byLine[b.Line] = append(byLine[b.Line], b)
	}

	out := make([]ServerWithInstances, 0, len(servers))
	for i, s := range servers {
		idx := i + 1
		if p.Index > 0 && idx != p.Index {
			continue
		}
		out = append(out, ServerWithInstances{
			Index:         idx,
			Hostname:      s.Hostname,
			GameDir:       s.GameDir,
			State:         s.State,
			CandidatePort: s.CandidatePort,
			Players:       s.Players,
			MaxPlayers:    s.MaxPlayers,
			Instances:     byLine[s.Line],
		})
	}
	return ServerInstancesResult{Servers: out}, nil
}

// --- server.start / stop / restart ------------------------------------------

type ServerTargetParams struct {
	Target string `json:"target"` // 1-based server index (as a string) or "all"
}

type ServerStopParams struct {
	Target       string `json:"target"` // 1-based server index (as a string) or "all"
	GraceSeconds int    `json:"grace_seconds,omitempty"`
}

type ServerLifecycleResult struct {
	OK bool `json:"ok"`
}

func parseServerTarget(raw json.RawMessage) (any, error) {
	return unmarshalParams[ServerTargetParams](raw)
}

func parseServerStop(raw json.RawMessage) (any, error) {
	return unmarshalParams[ServerStopParams](raw)
}

func serverStartHandler(env *Env, _ Actor, params any) (any, error) {
	idx, all, err := parseServerTargetToken(params.(*ServerTargetParams).Target)
	if err != nil {
		return nil, err
	}
	if all {
		if env == nil || env.StartServersAll == nil {
			return nil, &MethodError{Code: ErrCodeUnavailable, Message: "start all unavailable"}
		}
		if err := env.StartServersAll(); err != nil {
			return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
		}
		return ServerLifecycleResult{OK: true}, nil
	}
	if env == nil || env.StartServer == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "start unavailable"}
	}
	if err := env.StartServer(idx); err != nil {
		return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
	}
	return ServerLifecycleResult{OK: true}, nil
}

func graceDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 2 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func serverStopHandler(env *Env, _ Actor, params any) (any, error) {
	p := params.(*ServerStopParams)
	idx, all, err := parseServerTargetToken(p.Target)
	if err != nil {
		return nil, err
	}
	grace := graceDuration(p.GraceSeconds)
	if all {
		if env == nil || env.StopServersAll == nil {
			return nil, &MethodError{Code: ErrCodeUnavailable, Message: "stop all unavailable"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), grace+time.Second)
		defer cancel()
		if err := env.StopServersAll(ctx, grace); err != nil {
			return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
		}
		return ServerLifecycleResult{OK: true}, nil
	}
	if env == nil || env.StopServer == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "stop unavailable"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace+time.Second)
	defer cancel()
	if err := env.StopServer(ctx, idx, grace); err != nil {
		return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
	}
	return ServerLifecycleResult{OK: true}, nil
}

func serverRestartHandler(env *Env, _ Actor, params any) (any, error) {
	p := params.(*ServerStopParams)
	idx, all, err := parseServerTargetToken(p.Target)
	if err != nil {
		return nil, err
	}
	grace := graceDuration(p.GraceSeconds)
	if all {
		if env == nil || env.RestartServersAll == nil {
			return nil, &MethodError{Code: ErrCodeUnavailable, Message: "restart all unavailable"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), grace+3*time.Second)
		defer cancel()
		if err := env.RestartServersAll(ctx, grace); err != nil {
			return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
		}
		return ServerLifecycleResult{OK: true}, nil
	}
	if env == nil || env.RestartServer == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "restart unavailable"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace+3*time.Second)
	defer cancel()
	if err := env.RestartServer(ctx, idx, grace); err != nil {
		return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
	}
	return ServerLifecycleResult{OK: true}, nil
}

// --- server.remove ----------------------------------------------------------

type IndexParams struct {
	Index int `json:"index"` // 1-based server index
}

type ServerRemoveResult struct {
	Removed bool `json:"removed"`
}

func parseIndexOnly(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[IndexParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Index <= 0 {
		return nil, fmt.Errorf("server index is required (positive 1-based)")
	}
	return p, nil
}

func serverRemoveHandler(env *Env, _ Actor, params any) (any, error) {
	p := params.(*IndexParams)
	if env == nil || env.RemoveServer == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "remove unavailable"}
	}
	if err := env.RemoveServer(p.Index); err != nil {
		return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
	}
	return ServerRemoveResult{Removed: true}, nil
}

// --- server.launch ----------------------------------------------------------

type ServerLaunchParams struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args,omitempty"`
}

type ServerLaunchResult struct {
	OK bool `json:"ok"`
}

func parseServerLaunch(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[ServerLaunchParams](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Binary) == "" {
		return nil, fmt.Errorf("binary is required")
	}
	return p, nil
}

func serverLaunchHandler(env *Env, _ Actor, params any) (any, error) {
	p := params.(*ServerLaunchParams)
	if env == nil || env.LaunchServer == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "launch unavailable"}
	}
	if err := env.LaunchServer(p.Binary, append([]string(nil), p.Args...)); err != nil {
		return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
	}
	return ServerLaunchResult{OK: true}, nil
}

// --- server.command (escape hatch: forward raw text to a server console) ---

type InstanceCommandParams struct {
	Port int    `json:"port"` // listen port of a live instance
	Cmd  string `json:"cmd"`
}

type InstanceCommandResult struct {
	Reply string `json:"reply"`
}

func parseInstanceCommand(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[InstanceCommandParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Port <= 0 || p.Port > 65535 {
		return nil, fmt.Errorf("port is required (1..65535)")
	}
	if strings.TrimSpace(p.Cmd) == "" {
		return nil, fmt.Errorf("cmd is required")
	}
	return p, nil
}

func instanceCommandHandler(env *Env, actor Actor, params any) (any, error) {
	p := params.(*InstanceCommandParams)
	if env == nil || env.DispatchInstanceCmd == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "server manager unavailable"}
	}
	reply, err := env.DispatchInstanceCmd(p.Port, p.Cmd, actor.ID)
	if err != nil {
		return nil, &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
	}
	return InstanceCommandResult{Reply: reply}, nil
}

// --- logs.tail --------------------------------------------------------------

type LogsTailParams struct {
	Lines int `json:"lines,omitempty"`
}

type LogsTailResult struct {
	Lines []string `json:"lines"`
}

func parseLogsTail(raw json.RawMessage) (any, error) {
	return unmarshalParams[LogsTailParams](raw)
}

func logsTailHandler(env *Env, _ Actor, params any) (any, error) {
	if env == nil || env.TailNexusLog == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "log tail unavailable"}
	}
	p := params.(*LogsTailParams)
	n := p.Lines
	if n <= 0 {
		n = defaultServerTailLines
	}
	raw := env.TailNexusLog(n)
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.ReplaceAll(line, "\r", "")
		line = strings.TrimRight(line, "\n")
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return LogsTailResult{Lines: out}, nil
}

// --- rcon.help --------------------------------------------------------------

type RconHelpResult struct {
	Text string `json:"text"`
}

func rconHelpHandler(_ *Env, _ Actor, _ any) (any, error) {
	return RconHelpResult{Text: helpText}, nil
}

func buildHelpText() string {
	formWidth := 0
	for _, c := range adminCommands {
		if c.HelpForm == "" {
			continue
		}
		if n := len(c.HelpForm); n > formWidth {
			formWidth = n
		}
	}

	var b strings.Builder
	b.WriteString("\nNexQuake commands:\n")
	for _, c := range adminCommands {
		if c.HelpForm == "" {
			continue
		}
		fmt.Fprintf(&b, "  rcon %-*s  %s\n", formWidth, c.HelpForm, c.Description)
	}
	b.WriteString("\nInstance console forms (target one server's console):\n")
	b.WriteString("  rcon <port> <cmd...>\n")
	b.WriteString("  rcon <cmd...>             when connected, uses the current listen port\n")
	b.WriteString("\nPrefix any admin command with `nexus` while connected (e.g. `rcon nexus server list`).\n\n")
	return b.String()
}

// --- rpc.discover -----------------------------------------------------------

type RPCMethodInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type RPCDiscoverResult struct {
	Methods []RPCMethodInfo `json:"methods"`
}

func rpcDiscoverHandler(_ *Env, _ Actor, _ any) (any, error) {
	return RPCDiscoverResult{Methods: discoverMethods}, nil
}

func buildDiscoverMethods() []RPCMethodInfo {
	out := make([]RPCMethodInfo, 0, len(adminCommands))
	for _, c := range adminCommands {
		out = append(out, RPCMethodInfo{Name: c.Method, Description: c.Description})
	}
	return out
}
