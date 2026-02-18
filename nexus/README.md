# Nexus

Go HTTP server that ties everything together: serves the WASM client, serves game data, tunnels multiplayer traffic over WebSocket, and manages dedicated server processes.

## Entry

| File | Purpose |
|------|---------|
| `main.go` | HTTP server setup, route registration, WebSocket handler, CLI subcommands (`--version`, `--healthcheck`), auth init, server manager startup, signal handling, graceful shutdown. |
| `util.go` | Leveled logging (stderr + file + ring buffer for `rcon tail`), version info (`-ldflags`), env helpers, HTTP middleware (CORS, cache-control, content-type override). |

## Packages

| Dir | Purpose |
|-----|---------|
| `internal/nqnet/` | **Networking.** WebSocket upgrader, WebSocket<->UDP router, session registry, virtual IP allocator, tunnel frame helpers. |
| `internal/orch/` | **Orchestration.** Dedicated server launch planning, process lifecycle, server console capture/tail, server-info poller for `slist`. |
| `internal/admin/` | **Admin.** Auth (OIDC JWT + per-frame `rcon_password`), admin frame handler (`rcon`), nexus command dispatcher. |
| `internal/assets/` | **Game data.** Bootstrap manifests, VFS manifest builder, PAK indexing, and hash-addressed asset gateway. |

### `internal/assets/` — Game Data

| File | Purpose |
|------|---------|
| `internal/assets/vfs.go` | **Manifest builder.** Scans `${GAME_DIR}/<mod>/common` + `${GAME_DIR}/<mod>/client`, builds JSON manifests with Quake-like precedence (loose > PAK, higher PAK number wins). |
| `internal/assets/cd.go` | **CD index.** Scans `${CD_DIR}` for `.ogg`/`.mp3` tracks. |
| `internal/assets/pak.go` | **PAK parser.** Indexes PAK headers and exposes file offsets/sizes for extraction. |
| `internal/assets/manifest.go` | **Runtime gateway.** Serves `/start` bootstrap + `/nq/<hash>` asset requests for VFS and CD audio. |
| `internal/assets/game.go` | **Bootstrap.** Downloads game data on first run from a quickstart manifest (e.g. `minimal.json`). Only activates when `${GAME_DIR}` is writable. |

### Quake 1.06 Extraction (`quake106/`)

The `quake106` package is a standalone Go library that extracts PAK0.PAK directly from the original Quake 1.06 shareware distribution (`quake106.zip`). It implements LZH (LH5) decompression from scratch -- no cgo, no external dependencies -- to decompress `resource.1` inside the zip and extract the shareware PAK file and license text.

This is what makes Nexus's auto-bootstrap work: on first run, if no game data exists, Nexus downloads `quake106.zip`, hands it to this package, and gets a verified `pak0.pak` out the other side. Every step is SHA256-verified (zip, resource, extracted PAK) so the pipeline rejects corrupted or tampered downloads. This verification is required to conform with id Software's shareware license, which permits redistribution of the original, unmodified archive only.

| File | Purpose |
|------|---------|
| `quake106.go` | LZH decompression + PAK extraction. SHA256 verification at every stage. |

### `internal/orch/` — Orchestration

Manages dedicated server processes. Parses `servers.ini` into a launch plan, starts processes under PTY for console capture, polls server-info for the slist cache, and exposes operations for the admin rcon interface.

| File | Purpose |
|------|---------|
| `launcher.go` | `servers.ini` parser (with `@macro` expansion), launch plan builder, argument validation. |
| `manager.go` | Server process lifecycle: start under PTY, track by slot/port, wait for exit, graceful and forced shutdown. |
| `ops.go` | High-level server operations (start/stop/restart/remove/launch by port or index). Resolves targets, coordinates state transitions. |
| `console.go` | PTY-based server console I/O. Captures output lines, detects listen port from console, supports filtered reads for rcon command capture. |
| `rcon.go` | Server command execution: writes a command to the PTY, captures output with idle/max timeouts, formats the reply. |
| `slist.go` | Server-info poller. Periodically sends CCREQ_SERVER_INFO to each managed server and caches CCREP responses for the WebSocket slist handler. |

### `internal/admin/` — Admin

Authenticates admin sessions and handles rcon commands dispatched from the WebSocket layer.

| File | Purpose |
|------|---------|
| `auth.go` | Authentication. OIDC JWT verification (`AUTH_ISSUER`, `AUTH_AUDIENCE`, optional `AUTH_JWT_HEADER`, matcher list `AUTH_ADMIN_ID`) for connection-level admin identity, plus `AUTH_RCON_PASSWORD` for per-frame shared-secret auth. |
| `rcon.go` | Admin frame handler. Parses the rcon payload (password, optional target port, command), authorizes the frame, and dispatches to either a Nexus-level command or a server-level command. |
| `cmds.go` | Nexus-level admin command registry and execution: `help`, `tail`, `slist`, `sessions`, `start`, `stop`, `restart`, `remove`, `launch`, `ban`. |

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

Go 1.24+. Primary deps: `github.com/gorilla/websocket` (WebSocket), `github.com/coreos/go-oidc/v3` (OIDC JWT), `github.com/creack/pty` (server console PTY), `github.com/google/shlex` (command splitting).
