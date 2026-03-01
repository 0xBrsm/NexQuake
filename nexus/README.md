# Nexus

Go orchestration service that serves the WASM client, serves game data, tunnels multiplayer traffic over WebSocket, and manages dedicated server processes.

## Entry Files

| File | Purpose |
|------|---------|
| `main.go` | Process lifecycle only: init, runtime wiring, HTTP server start, signal handling, graceful shutdown, and CLI subcommands (`--version`, `--healthcheck`). |
| `connect.go` | HTTP mux and connection boundary: route registration, middleware wiring, WebSocket upgrade, source identity parsing, and pre-upgrade ban checks. |
| `control.go` | Relay control wiring: builds `nqrelay.FrameDispatch`, routes control frames to `slist` or admin handlers, and composes `admin.Env` from orchestration/session dependencies. |
| `util.go` | Shared runtime utilities: leveled logging (stderr + file + ring buffer), version/build metadata, env helpers, and HTTP response helpers. |

## Dependency Boundaries

```text
nqrelay        (leaf: stdlib + gorilla/websocket)
internal/orch  (leaf: stdlib + internal/assets)
internal/admin (leaf: stdlib + github.com/google/shlex)

package main   (sole integration point)
  -> nqrelay + internal/orch + internal/admin + internal/assets
```

Rules:

- `nqrelay`, `internal/orch`, and `internal/admin` do not import each other.
- `nqrelay` has no imports from `internal/*` and no app policy logic.
- All cross-subsystem wiring is done in package `main` (`connect.go` and `control.go`).

## Packages

| Dir | Purpose |
|-----|---------|
| `nqrelay/` | **Networking relay.** Standalone WebSocket<->UDP relay with session lifecycle, deterministic virtual IP allocation, and control-frame callback hooks. No HTTP/auth/application imports. |
| `internal/orch/` | **Orchestration.** Dedicated server launch/lifecycle, pool autoscaling/reconcile, server console capture, and `slist` polling/aggregation. No `nqrelay` or `admin` imports. |
| `internal/admin/` | **Admin control plane.** Auth + admin command parsing/dispatch via callback-driven `Env`; no direct imports from `orch` or `nqrelay`. |
| `internal/assets/` | **Game data gateway.** Quickstart manifests, VFS manifest construction, PAK indexing/streaming, CD index, and hash-addressed asset serving (`/start`, `/nq/<hash>`). |

### `internal/assets/` — Game Data

| File | Purpose |
|------|---------|
| `internal/assets/vfs.go` | **Manifest builder.** Scans `${GAME_DIR}/<mod>/common` + `${GAME_DIR}/<mod>/client` and builds JSON manifests with Quake precedence (loose > PAK, higher PAK number wins). |
| `internal/assets/cd.go` | **CD index.** Scans `${CD_DIR}` for `.ogg`/`.mp3` BGM tracks. |
| `internal/assets/pak.go` | **PAK parser.** Indexes PAK headers and exposes file offsets/sizes for stream extraction. |
| `internal/assets/manifest.go` | **Runtime gateway.** Serves quickstart metadata and hash-addressed asset reads. |
| `internal/assets/game.go` | **Quickstart + installers.** Seeds `servers.ini` and installs missing mod layers from `CFG_DIR/game.json` based on `QUICKSTART` and `servers.ini -game` entries. |

### Quake 1.06 Extraction (`quake106/`)

A standalone Go package that extracts `pak0.pak` directly from the original Quake 1.06 shareware distribution (`quake106.zip`). See the [quake106 README](./quake106/README.md).

| File | Purpose |
|------|---------|
| `quake106.go` | LZH decompression + PAK extraction with SHA256 verification. |

### `internal/orch/` — Orchestration

Manages dedicated server processes and scaled backend pools. Parses `.bat`-style `servers.ini`, starts processes under PTY for console capture, polls server info for `slist`, and runs pool reconcile/autoscale policy.

| File | Purpose |
|------|---------|
| `launcher.go` | `servers.ini` parser (`@macro` + `%arg` expansion), launch plan builder, and process start/stop wiring under PTY. |
| `manager.go` | `ServerManager` construction, logging hooks, and operator console relay formatting. |
| `registry.go` | Pool/server registry model, backend lifecycle state (`warming/active/draining/terminating`), and aggregate pool snapshot refresh. |
| `state.go` | In-memory state updates (resolved port/search-path + observed server-info), startup-online transitions, and per-update reconcile trigger. |
| `pool.go` | Pool policy + proxy routing internals: destination backend selection, affinity TTL tracking, demand accounting, and scale-up/drain/despawn decisions. |
| `ops.go` | High-level operations (`start`, `stop`, `restart`, `remove`, `launch`) by port/index. |
| `console.go` | PTY console I/O capture, listen-port detection, and filtered reads for command capture. |
| `rcon.go` | Server command execution with idle/max capture windows and formatted reply output. |
| `slist.go` | Server-info poller + aggregated `CCREP_SERVER_INFO` builder used for WebSocket `slist` responses. |

### `internal/admin/` — Admin

Authenticates admin sessions and handles rcon commands dispatched from the WebSocket control channel. Package dependencies are injected via `Env` callbacks so admin code remains isolated from orchestration and relay internals.

| File | Purpose |
|------|---------|
| `auth.go` | OIDC JWT verification (`AUTH_ISSUER`, `AUTH_AUDIENCE`, `AUTH_JWT_HEADER`) plus optional matcher allowlist (`AUTH_ADMIN_ID`) and `AUTH_RCON_PASSWORD` support. |
| `rcon.go` | Admin frame parser/dispatcher for Nexus-level and server-level command execution. |
| `cmds.go` | Nexus-level command handlers (`help`, `tail`, `slist`, `start`, `stop`, `restart`, `remove`, `launch`). |
| `sessions.go` | Session-focused commands and helpers (`session list`, `session info`, `session ban`, status slot/address matching for targeted kick). |

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
