# NexQuake

NexQuake is a WebAssembly port of Quake (1996) with WebSocket multiplayer. Play the classic FPS in your browser with a minimal, tunnel-style multiplayer setup (software renderer only).

## Quick Start

### Using Docker

Run with pre-built artifacts (from GitHub Actions or Releases):

```bash
# 1. Download artifacts from GitHub Actions PR run
#    Go to PR → Actions tab → Download artifacts

# 2. Extract artifacts to the apps/ directory
mkdir -p apps
tar -xzf nqwasm-client.tar.gz -C apps/
tar -xzf nqserver-x86_64.tar.gz -C apps/    # or nqserver-aarch64.tar.gz
tar -xzf nexus-amd64.tar.gz -C apps/        # or nexus-arm64.tar.gz

# 3. Create game data directory and add your PAK files
mkdir -p data/id1/common
cp /path/to/your/PAK0.PAK data/id1/common/

# 4. Create logs directory (will be auto-populated by servers)
mkdir -p logs

# 5. Run with docker compose
docker compose up

# 6. Open browser
# Visit: http://localhost:7071
```

**Directory Structure:**
- `apps/` - Application binaries and client files (read-only)
  - `nqwasm/` - WebAssembly client files
  - `nexus` - Go HTTP/WebSocket relay + server orchestrator binary
  - `nqserver` - NetQuake server binary
- `data/` - Game data (often mounted read-only; must be writable for auto-bootstrap)
  - `id1/`
    - `common/` - Shared game files (PAK0/PAK1 + optional loose files like `autoexec.cfg`)
    - `client/` - Optional client-only overrides
    - `server/` - Optional server-only overrides
- `logs/` - Server logs and runtime state (read-write)
  - `id1/` - Vanilla Quake server logs (auto-created)

The Dockerfile provides a lightweight runtime shell. Swap artifacts to test different PR builds without rebuilding the image.

### Getting Game Data

You need Quake's PAK files to play:
- **PAK0.PAK**: Shareware version (download Quake 1.06 shareware)
- **PAK1.PAK**: Full version (purchase required)

PAK files can be either uppercase (PAK0.PAK) or lowercase (pak0.pak).

**Container auto-bootstrap (optional)**:
- On nexus startup, if `${QUAKE_DATA_DIR}` (default `/data`) is **writable**, it can bootstrap missing game data into `/data/<game>/<layer>/` based on `gamedata.json`.
  - Config is loaded only when `GAMEDATA_PATH` is set (path to a JSON file).
  - Schema: array of entries, each `{ "game": "id1", "common": ["..."], "client": ["..."], "server": ["..."], "force": false }`. At least one of `common|client|server` must be present and non-empty. `force:true` overrides the “skip if directory already populated” guard.
  - Example manifests: `src/assets/minimal.json` and `src/assets/full.json`.

### How PAK Files Work

Both the WASM client and NetQuake servers share the same PAK files from the `data/` directory:

**Dynamic `/data/id1` Mirroring**
- Place PAK files in `data/id1/` directory on your host
- Nexus serves them at `http://localhost:7071/data/id1/...`
- WASM client fetches a directory listing from `http://localhost:7071/data-manifest/id1` and downloads everything into the virtual filesystem under `/id1` (lowercased paths) before Quake starts (PAKs + any loose files like `autoexec.cfg`)
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

- **upstream-ref.txt** - Source provenance and GPL licensing
- **AGENTS.md** and `.agents/*` - Deeper implementation notes (architecture, protocol, decisions)

## Credits

- **id Software** - Original Quake (GPL 2.0)
- **Gregory Maynard-Hoare** ([GMH-Code](https://github.com/GMH-Code/Quake-WASM)) - Original WASM port
- **initialed85** - WebSocket multiplayer layer and Go proxy
- **This fork** - Docker containerization and build system

## License

GPL-2.0-or-later (matches original Quake release)

**Note**: Game data files (PAK files) have separate licensing. Shareware version permits duplication of official archives only. Full version has restrictive licensing - do not host publicly.
