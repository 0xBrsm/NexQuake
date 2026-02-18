# Nexus

Go HTTP server that ties everything together: serves the WASM client, serves game data, tunnels multiplayer traffic over WebSocket, and manages dedicated server processes.

## Files

### Entry

| File | Purpose |
|------|---------|
| `main.go` | **Entry point.** HTTP server setup, route registration, auth init, server manager startup, signal handling, graceful shutdown. |
| `util.go` | **Shared helpers.** Logging, env parsing, misc utilities. |

### Packages

Most nexus logic lives in internal packages:

| Dir | Purpose |
|-----|---------|
| `internal/nqnet/` | **Networking.** WebSocket upgrader, WebSocket<->UDP router, session registry, virtual IP allocator, tunnel frame helpers. |
| `internal/orch/` | **Orchestration.** Dedicated server launch planning, process lifecycle, server console capture/tail, server-info poller for `slist`. |
| `internal/admin/` | **Admin.** Auth (OIDC JWT + per-frame `rcon_password`), admin frame handler (`rcon`), nexus command dispatcher. |
| `internal/assets/` | **Game data.** Bootstrap manifests, VFS manifest builder, PAK indexing, and hash-addressed asset gateway. |

### Game Data

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

### Auth

| File | Purpose |
|------|---------|
| `auth.go` | **Authentication.** OIDC JWT for connection-level admin identity plus optional frame-level `rcon_password` checks. Admin matcher logic (email, group, role). |

### Utilities

| File | Purpose |
|------|---------|
| `util.go` | Shared helpers. |

## Building

```bash
cd src/nexus
go build -o nexus .
```

Or for a static binary (Docker):

```bash
CGO_ENABLED=0 go build -o nexus .
```

## Dependencies

Go 1.25+. Primary deps: `github.com/gorilla/websocket` (WebSocket), `github.com/creack/pty` (server console PTY), `github.com/google/shlex` (admin command splitting).
