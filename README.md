# NexQuake

NexQuake is a WebAssembly port of Quake (1996) with WebSocket multiplayer. Play the classic FPS in your browser with a minimal, tunnel-style multiplayer setup (software renderer only).

## Quick Start

### Using Docker

```bash
# 1. Create game data directory and add your PAK files
mkdir -p data/id1/common
cp /path/to/your/PAK0.PAK data/id1/common/

# 2. Create logs directory (will be auto-populated by servers)
mkdir -p logs

# 3. Run with docker compose (builds nexus + nqserver + nqwasm)
docker compose up --build

# 4. Open browser
# Visit: http://localhost:1337
```

**Directory Structure:**
- `data/` - Game data (often mounted read-only; must be writable for auto-bootstrap)
  - `id1/`
    - `common/` - Shared game files (PAK0/PAK1 + optional loose files like `autoexec.cfg`)
    - `client/` - Optional client-only overrides
    - `server/` - Optional server-only overrides
- `logs/` - Server logs and runtime state (read-write)
  - `id1/` - Vanilla Quake server logs (auto-created)

The Dockerfile builds the image including `nexus`, `nqserver`, and the `nqwasm` client.

### Getting Game Data

You need Quake's PAK files to play:
- **PAK0.PAK**: Shareware version (download Quake 1.06 shareware)
- **PAK1.PAK**: Full version (purchase required)

PAK files can be either uppercase (PAK0.PAK) or lowercase (pak0.pak).

**Container auto-bootstrap (optional)**:
- On nexus startup, if `${DATA_DIR}` (default `/app/data`) is **writable**, it can bootstrap missing game data into `<data>/<game>/<layer>/` based on `gamedata.json`.
  - Nexus will look for a quickstart manifest at `${DATA_DIR}/${QUICKSTART:-minimal}.json`. If it doesn't exist, bootstrap is a no-op.
  - Schema: array of entries, each `{ "game": "id1", "common": ["..."], "client": ["..."], "server": ["..."], "force": false }`. At least one of `common|client|server` must be present and non-empty. `force:true` overrides the “skip if directory already populated” guard.
  - Example manifests: `manifests/minimal.json` and `manifests/full.json`.
  - In the runtime image these ship as `/app/data/minimal.json` and `/app/data/full.json` (and disappear if you bind-mount your own `${DATA_DIR}`).

### How PAK Files Work

Both the WASM client and NetQuake servers share the same PAK files from the `data/` directory:

**Dynamic `/data/id1` Mirroring**
- Place PAK files in `data/id1/` directory on your host
- Nexus serves them at `http://localhost:1337/data/id1/...`
- WASM client fetches a directory listing from `http://localhost:1337/data-manifest/id1` and downloads everything into the virtual filesystem under `/id1` (lowercased paths) before Quake starts (PAKs + any loose files like `autoexec.cfg`)
- NetQuake servers read the same files from the `/data` volume bind

**Single source of truth**: One set of PAK files in `data/id1/` serves both browser clients and multiplayer servers. No build-time embedding required.

**How it works:**
1. Browser loads WASM client
2. Client fetches file list from `/data-manifest/id1`
3. Client downloads each file from `/data/id1/...` and writes it into the Emscripten virtual filesystem under `/id1/...` (lowercased)

### Auto-Connect

Put an `autoexec.cfg` in `data/id1/` with `connect 127.255.255.1`.

## Features

- **WASM Client**: Runs entirely in browser via WebAssembly
- **Multiplayer**: WebSocket nexus bridges browser clients to Quake servers
- **Renderer**: Software renderer only
- **Browser Storage**: Saves persist between sessions

## Local Development

Local development tooling (local build scripts, Playwright, devcontainers) is intentionally not committed in this repo. Use CI artifacts for builds and `docker compose up` for runtime testing.

## Architecture

```
Browser (WASM Client)
    ↕ WebSocket
Nexus (Go HTTP Server)
    ↕ UDP
NetQuake Server (id1)
```

Nexus (Go HTTP server) serves client files and relays WebSocket connections to UDP-based Quake servers.

## Documentation

- **ATTRIBUTIONS.md** - Source provenance and GPL licensing lineage
- **AGENTS.md** and `.agents/*` - Deeper implementation notes (architecture, protocol, decisions)

## Credits

- **id Software** - Original Quake (GPL 2.0)
- **Gregory Maynard-Hoare** ([GMH-Code](https://github.com/GMH-Code/Quake-WASM)) - Original WASM port
- **initialed85** - WebSocket multiplayer layer and Go proxy
- **This fork** - Docker containerization and build system

## License

GPL-2.0-or-later (matches original Quake release)

**Note**: Game data files (PAK files) have separate licensing. Shareware version permits duplication of official archives only. Full version has restrictive licensing - do not host publicly.
