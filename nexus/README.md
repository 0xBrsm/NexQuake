# Nexus

Go HTTP server that ties everything together: serves the WASM client, serves game data, tunnels multiplayer traffic over WebSocket, and manages dedicated server processes.

## Files

### Core

| File | Purpose |
|------|---------|
| `main.go` | **Entry point.** HTTP server setup, route registration, auth initialization, server manager startup, signal handling, graceful shutdown. |
| `websock.go` | **WebSocket handler.** Upgrades `/ws` to WebSocket, reads/writes binary frames, manages per-connection state, tracks admin connections. |
| `udp.go` | **UDP relay.** Bidirectional relay between WebSocket frames and backend UDP sockets. Allocates per-connection loopback source IPs. Sends best-effort disconnect on WebSocket close. |
| `routing.go` | **Tunnel routing.** Parses the per-frame routing header (server_id + UDP port). Implements `slist` control-plane: handles broadcast frames, builds aggregated `CCREP_SERVER_INFO` responses from cache. |

### Server Management

| File | Purpose |
|------|---------|
| `servers.go` | **Server orchestrator.** Discovers game directories under `${DATA_DIR}`, spawns `nqserver` processes (one per mod), manages lifecycle (start, stop, signal). Builds merged runtime basedirs when data is read-only. |
| `slist.go` | **Server-info cache.** Polls running servers with connectionless `CCREQ_SERVER_INFO` on a round-robin schedule (one server every 500ms). Caches replies for fast `slist` responses. |
| `ip.go` | **Loopback IP allocation.** Infrastructure subnet `127.13.37.x` for servers (`.1..N`) and admins (`.255` downward). Client IPs are hashed from source IP into `127.0.0.0/8` (excluding reserved subnet). |

### Game Data

| File | Purpose |
|------|---------|
| `vfs.go` | **Manifest builder.** Scans `${DATA_DIR}/<mod>/common` and `${DATA_DIR}/<mod>/client`, builds a JSON manifest with per-file URLs. Implements Quake-like precedence (loose > PAK, higher PAK number wins). |
| `pak.go` | **PAK parser.** Reads PAK file headers, indexes entries, streams individual files via `/pak-extract/<mod>/<layer>/<pak>/<file>`. No pre-extraction needed. |
| `assets.go` | **Bootstrap.** Downloads game data on first run from a quickstart manifest (e.g., `minimal.json`). Only activates when `${DATA_DIR}` is writable. |

### Quake 1.06 Extraction (`quake106/`)

The `quake106` package is a standalone Go library that extracts PAK0.PAK directly from the original Quake 1.06 shareware distribution (`quake106.zip`). It implements LZH (LH5) decompression from scratch -- no cgo, no external dependencies -- to decompress `resource.1` inside the zip and extract the shareware PAK file and license text.

This is what makes Nexus's auto-bootstrap work: on first run, if no game data exists, Nexus downloads `quake106.zip`, hands it to this package, and gets a verified `pak0.pak` out the other side. Every step is SHA256-verified (zip, resource, extracted PAK) so the pipeline rejects corrupted or tampered downloads. This verification is required to conform with id Software's shareware license, which permits redistribution of the original, unmodified archive only.

| File | Purpose |
|------|---------|
| `quake106.go` | LZH decompression + PAK extraction. SHA256 verification at every stage. |

### Auth

| File | Purpose |
|------|---------|
| `auth.go` | **Authentication.** Two methods: RCON password (shared secret, token = base64(password)) and OIDC JWT (issuer + audience verification). Admin matcher logic (email, group, role). |

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

Go 1.25+. Standard library only (no external dependencies except `golang.org/x/net` for WebSocket).
