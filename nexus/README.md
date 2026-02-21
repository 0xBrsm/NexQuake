# Nexus

Go orchestration server that ties everything together: serves the WASM client, serves game data, tunnels multiplayer traffic over WebSocket, and manages dedicated server processes.

## Entry

| File | Purpose |
|------|---------|
| `main.go` | HTTP server setup, route registration, WebSocket handler, CLI subcommands (`--version`, `--healthcheck`), auth init, server manager startup, signal handling, graceful shutdown. |
| `util.go` | Leveled logging (stderr + file + ring buffer for `rcon tail`), path hashing, version info (`-ldflags`), env helpers, HTTP middleware (browser isolation headers, cache-control, content-type override). |

## Packages

| Dir | Purpose |
|-----|---------|
| `internal/nqnet/` | **Networking.** WebSocket upgrader, WebSocket<->UDP router, session registry, virtual IP allocator, tunnel frame helpers. |
| `internal/orch/` | **Orchestration.** Dedicated server launch planning, process lifecycle, server console capture/tail, server-info poller for `slist`. |
| `internal/admin/` | **Admin.** Auth (OIDC JWT + in-game `rcon_password`), admin frame handler for commands, Nexus command dispatcher. |
| `internal/assets/` | **Game data.** Quickstart manifests, VFS manifest builder, PAK indexing, BGM audio handling, and hash-addressed asset gateway. |

### `internal/assets/` — Game Data

| File | Purpose |
|------|---------|
| `internal/assets/vfs.go` | **Manifest builder.** Scans `${GAME_DIR}/<mod>/common` + `${GAME_DIR}/<mod>/client`, builds JSON manifests with Quake-like precedence (loose > PAK, higher PAK number wins). |
| `internal/assets/cd.go` | **CD index.** Scans `${CD_DIR}` for `.ogg`/`.mp3` BGM tracks. |
| `internal/assets/pak.go` | **PAK parser.** Indexes PAK headers and exposes file offsets/sizes for real-time extraction. |
| `internal/assets/manifest.go` | **Runtime gateway.** Serves `/start` quickstart + `/nq/<hash>` asset requests for VFS and CD audio. |
| `internal/assets/game.go` | **Quickstart + installers.** Seeds `servers.ini` and installs missing mod layers from `CFG_DIR/game.json` based on `QUICKSTART` and `servers.ini -game` entries. Also contains archive/download install helpers. |

### Quake 1.06 Extraction (`quake106/`)

A standalone Go library that extracts `pak0.pak` directly from the original Quake 1.06 shareware distribution (`quake106.zip`). See the [quake106 README](./quake106/README.md) for details.

| File | Purpose |
|------|---------|
| `quake106.go` | LZH decompression + PAK extraction with SHA256 verification. |

### `internal/orch/` — Orchestration

Manages dedicated server processes. Parses old-school, .bat-style `servers.ini` into a launch plan, starts processes under PTY for console capture, polls server-info for the slist cache, and exposes operations for the admin rcon interface.

| File | Purpose |
|------|---------|
| `launcher.go` | `servers.ini` parser (with `@macro` + `%arg` expansion), launch plan builder, argument validation. |
| `manager.go` | Server manager wiring + process lifecycle: start under PTY, wait for exit, graceful and forced shutdown. |
| `state.go` | In-memory server registry/state model, resolved port/search-path updates, and snapshot generation for operator/admin views. |
| `ops.go` | High-level server operations (start/stop/restart/remove/launch by port or index). Resolves targets, coordinates state transitions. |
| `console.go` | PTY-based server console I/O. Captures output lines, detects listen port from console, supports filtered reads for rcon command capture. |
| `rcon.go` | Server command execution: writes a command to the PTY, captures output with idle/max timeouts, formats the reply. |
| `slist.go` | Server-info poller. Periodically sends CCREQ_SERVER_INFO to each managed server and caches CCREP responses for the WebSocket slist handler. |

### `internal/admin/` — Admin

Authenticates admin sessions and handles rcon commands dispatched from the WebSocket layer.

| File | Purpose |
|------|---------|
| `auth.go` | Authentication. OIDC JWT verification (`AUTH_ISSUER`, `AUTH_AUDIENCE`, `AUTH_JWT_HEADER`) for connection-level admin identity with optional matcher list `AUTH_ADMIN_ID` (empty means any valid JWT is admin), plus optional `AUTH_RCON_PASSWORD` for in-game shared-secret auth. |
| `rcon.go` | Admin frame handler. Parses the rcon payload (password, optional target port, command), authorizes the frame, and dispatches to either a Nexus-level command or a server-level command. |
| `cmds.go` | Nexus-level command dispatch and non-session admin helpers (`help`, `tail`, `slist`, `start`, `stop`, `restart`, `remove`, `launch`). |
| `sessions.go` | Session-oriented admin commands and formatting/parsing helpers (`session list`, `session info`, `session ban`, status slot/address matching for targeted kick). |

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

Go 1.24+. Primary deps: `github.com/gorilla/websocket` (WebSocket), `github.com/coreos/go-oidc/v3` (OIDC JWT), `github.com/creack/pty` (server console PTY), `github.com/google/shlex` (arg splitting).
