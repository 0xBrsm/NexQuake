# Nexus Service

Go orchestration service that serves the WASM client, serves game data, tunnels multiplayer traffic over WebSocket, and manages dedicated server processes.

## Package Layout

| Dir | Purpose |
|-----|---------|
| `trunk/` | **Networking relay.** Standalone WebSocket↔UDP relay with deterministic VirtualIP (`127.x.x.x`) allocation and control-frame callback hooks. No HTTP/auth/application imports. |
| `internal/orch/` | **Orchestration.** Dedicated server launch/lifecycle, instance autoscaling/reconcile, server console capture, and `slist` polling/aggregation. No `trunk` or `admin` imports. |
| `internal/access/` | **HTTP access policy.** Caller identity, OIDC JWT verification, RCON/admin authorization rules, and source-IP blocklist. |
| `internal/clients/` | **Client presence.** Joined runtime view of access identities and active trunk sessions, keyed by VirtualIP for client list/info/ban consumers. |
| `internal/admin/` | **Admin control plane.** `/rcon` JSON-RPC 2.0 dispatch and client/server commands over narrow consumer-side interfaces. |
| `internal/assets/` | **Game data gateway.** Quickstart manifests, VFS manifest construction, PAK indexing/streaming, CD index, and hash-addressed asset serving (`/start`, `/nq/<hash>`). |
| `quake106/` | **Shareware extractor.** Standalone Go package that extracts `pak0.pak` from the Quake 1.06 shareware archive with SHA256 verification. See [quake106/README.md](./quake106/README.md). |

## Dependency Boundaries

```text
trunk          (leaf: stdlib + gorilla/websocket)
internal/orch  (leaf: stdlib + internal/assets)
internal/access (leaf: stdlib + go-oidc)
internal/clients -> internal/access + trunk
internal/admin  -> internal/clients + internal/orch (consumer-side interfaces)

package main
  -> trunk + internal/orch + internal/access + internal/clients + internal/admin + internal/assets
```

Rules:

- `trunk` and `internal/orch` do not import `internal/admin`.
- `trunk` has no imports from `internal/*` and no app policy logic.
- Cross-subsystem construction is done in package `main` (`main.go`, `connect.go`, and `control.go`).

## Entry Files

| File | Purpose |
|------|---------|
| `main.go` | Process lifecycle only: init, runtime wiring, HTTP server start, signal handling, graceful shutdown, and CLI subcommands (`--version`, `--healthcheck`). |
| `connect.go` | HTTP mux and connection boundary: route registration (`/health`, `/connect`, `POST /rcon` JSON-RPC, `GET /rcon` OIDC-popup landing page, `/start`, `/nq/`, `/`), top-level access gate wrapping, shared transport session setup, and `/rcon` JSON-RPC handler. |
| `control.go` | Conn control wiring: builds `trunk.FrameDispatch`, routes control frames to `slist`, and composes `admin.Env` from orchestration/session dependencies. |
| `util.go` | Shared runtime utilities: leveled logging (stderr + file + ring buffer), version/build metadata, env helpers, and HTTP response helpers. |

## trunk/

Per-client browser↔UDP tunnel. See [trunk/README.md](./trunk/README.md) for vendoring notes and the public API.

| File | Purpose |
|------|---------|
| `relay.go` | Manages one `Conn` per client: tunnel read/write loops, UDP socket ownership, lifecycle, and control-frame dispatch. |
| `protocol.go` | Defines the 2-byte-port + payload binary frame format and builds/parses identity frames. |
| `options.go` | Functional options (`WithDispatch`, `WithLogger`) consumed by `NewConn`. |
| `ws.go` | WebSocket `Transport` adapter and `Upgrader`; the only file importing `gorilla/websocket`. |
| `udp.go` | UDP socket I/O: reads datagrams from the backend, writes outbound frames to it. |
| `vip.go` | Allocates unique `127.x.x.x` VirtualIPs from source keys. |

## internal/access/

Resolves HTTP callers and centralizes access policy before route-specific code runs. All HTTP handlers, including `/health`, `/start`, `/nq/`, `/connect`, `/rcon`, and the static client file server, sit behind the same source-IP block check. `/rcon` additionally asks for admin capability.

| File | Purpose |
|------|---------|
| `auth.go` | OIDC JWT verification (`AUTH_ISSUER`, `AUTH_AUDIENCE`, `AUTH_JWT_HEADER`), `Authorization` scheme parsing, optional matcher allowlist (`AUTH_ADMIN_ID`), and `AUTH_RCON_PASSWORD` policy for admin capability. |
| `gate.go` | Request-level access facade, resolved `access.Client`, top-level HTTP gate, source-IP blocklist, and admin authorization. |
| `identity.go` | Client source IP and identity-key resolution (`AUTH_CLIENT_IP_HEADER`) for proxied deployments. |

## internal/orch/

Manages dedicated server processes and their scaled instances. Parses `.bat`-style `servers.ini`, starts processes under PTY for console capture, polls server info for `slist`, and runs server/instance reconcile and autoscale policy.

| File | Purpose |
|------|---------|
| `launcher.go` | `servers.ini` parser (with `@macro` + `%arg` expansion), launch plan builder, and process start/stop wiring under PTY. |
| `manager.go` | `ServerManager` construction, shared logging hooks, and operator console relay formatting. |
| `registry.go` | Server/instance registry model, instance lifecycle state (`warming/active/draining/terminating`), and aggregate server snapshot refresh. |
| `state.go` | In-memory instance state updates (resolved port/search-path + observed server-info), startup-online transitions, and per-update reconcile trigger. |
| `server.go` | Server-level instance selection (least-loaded, round-robin tie-break), slist-poll demand accounting, headroom calculation, autoscale scale-up/drain/despawn decisions, and reconcile loops (event-driven + heartbeat). |
| `ops.go` | High-level lifecycle operations (start/stop/restart/remove/launch by 1-based server index). Resolves targets and coordinates instance transitions. |
| `console.go` | PTY-based server console I/O. Captures output lines, detects listen port from console, supports filtered reads for rcon command capture. |
| `rcon.go` | Instance command dispatch (`DispatchInstanceCmd`): brackets the user command with random `__NQX_*` sentinels via `echo`, writes the framed line to the instance PTY, and returns exactly the lines the server emits between BEGIN and END. A safety timeout bounds hung-server cases; there is no idle-wait heuristic. |
| `slist.go` | Server-info poller. Sends `CCREQ_SERVER_INFO` in round-robin, updates cache for WebSocket `slist`, and drives periodic reconcile heartbeat. |

## internal/admin/

Handles JSON-RPC 2.0 commands received on authorized `POST /rcon` requests. Command handlers call small consumer-side interfaces for source blocking, client presence, and server orchestration. The package does not resolve HTTP callers, track active tunnel connections, or make top-level access decisions.

| File | Purpose |
|------|---------|
| `rpc.go` | JSON-RPC 2.0 machinery: `Request`/`Response` envelopes, `ParseRequest`, `Dispatch`, `MethodError`, and per-RPC audit logging. |
| `cmds.go` | Command registry and shared handlers: `rcon.help`, `rpc.discover`, and `logs.tail`. |
| `server.go` | Server commands and orch dispatch (`server.list`, `server.instances`, lifecycle, launch, and instance console commands). |
| `client.go` | Joined active-client commands and helpers (`client.list`, `client.info`, `client.ban`). |

## internal/clients/

Tracks live client presence by joining access identity metadata recorded at `/connect` with active trunk sessions. The registry is keyed by trunk VirtualIP (`nqip`) so multiple sessions from the same source IP remain distinct.

| File | Purpose |
|------|---------|
| `clients.go` | Active client registry, stable connection list, lookup by NQIP, and best-effort disconnect helper. |

## internal/assets/

| File | Purpose |
|------|---------|
| `vfs.go` | **Manifest builder.** Scans `${GAME_DIR}/<mod>/common` + `${GAME_DIR}/<mod>/client` and builds JSON manifests with Quake precedence (loose > PAK, higher PAK number wins). |
| `cd.go` | **CD index.** Scans `${CD_DIR}` for `.ogg`/`.mp3` BGM tracks. |
| `pak.go` | **PAK parser.** Indexes PAK headers and exposes file offsets/sizes for stream extraction. |
| `manifest.go` | **Runtime gateway.** Serves quickstart metadata and hash-addressed asset reads. |
| `game.go` | **Quickstart + installers.** Seeds `servers.ini` and installs missing mod layers from `CFG_DIR/game.json` based on `QUICKSTART` and `servers.ini -game` entries. |

## quake106/

Standalone Go package that extracts `pak0.pak` from the Quake 1.06 shareware archive. See [quake106/README.md](./quake106/README.md).

| File | Purpose |
|------|---------|
| `quake106.go` | LZH decompression + PAK extraction with SHA256 verification. |

## Building

```bash
cd src/nexus
go build -o nexus .
```

Static binary (Docker):

```bash
CGO_ENABLED=0 go build -o nexus .
```

## Dependencies

Go 1.24+. Primary dependencies:

- `github.com/gorilla/websocket` (WebSocket tunnel)
- `github.com/coreos/go-oidc/v3` (OIDC JWT verification)
- `github.com/creack/pty` (server console PTY)
- `github.com/google/shlex` (command argument splitting)
