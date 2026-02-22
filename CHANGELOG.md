# Changelog

Code evolution in `src/` — client, Nexus (relay), and server.

## 1.0.1

### Fixed
- Mouse wheel input in the WASM client is now deterministic across browser delta modes (`pixel`/`line`/`page`) and smoother on mixed ratchet/trackpad hardware.

### Changed
- Client bootstrap now seeds `autoexec.cfg` and `nexquake.cfg` into IDBFS once on first load.
- Quickstart defaults in `src/etc` were updated for the config split (`config.cfg` -> `nexquake.cfg`) and `autoexec.cfg` now executes `nexquake.cfg`.
- Built-in quickstart `id1` no longer installs a client cfg from `game.json`; client defaults now come from the first-load seed flow.

## 1.0.0

### Added
- Initial public NexQuake release.
- Browser Quake client (WASM), Nexus relay/admin service, and dedicated server runtime.
- Docker-first deployment with quickstart game-pack bootstrap.
- Admin authentication via OIDC JWT and in-game `rcon_password`.

### Notes
- First stable public release baseline.
- Prior `0.x` entries represent pre-release development history.

## 0.10.8.5

### Fixed
- Added the missing `SV_RecursiveHullCheck` declaration in shared `bugfix/world.h.patch` (applied to both client and server) to prevent strict-toolchain build failures and remove duplicate per-target patching

### Changed
- Build and release packaging now inject `NQ_VERSION` into Docker/WASM build inputs so shipped binaries and published images report a consistent release version

## 0.10.8.4

### Changed
- Simplified `compose.yml` for out-of-the-box usage with inline env defaults
- Added `compose.build.yml` for power-user and production image builds

## 0.10.8.3

### Added
- Quickstart download progress logging per mod with base vs game mod breakdown

### Fixed
- WebSocket reconnect socket state handling and widened session user column
- Session info with transient route ports now treats unknown ports as disconnected

### Changed
- Rcon admin requests are now audit-logged with admin tag before command output
- Renamed `og_ctf` quickstart entry to `og-ctf` and simplified download log format
- Updated etc configs, quickstart scan, and admin wait pacing

## 0.10.8.2

### Fixed
- Remote CD track URL resolution now uses the latest `/start` bundle data, preventing stale `/nq/<hash>` fetches after idle/reconnect cycles
- Mouse wheel handling now normalizes browser delta modes and accumulates partial deltas for more consistent Quake wheel ticks

### Changed
- Lowered client default `net_messagetimeout` from `300` to `10`

## 0.10.8.1

### Changed
- Refined client favicon rendering (updated SVG styling/color treatment and shell cache-buster) to improve visibility across themes and ensure browsers pick up favicon updates reliably

## 0.10.8

### Added
- Quickstart catalog support for local config assets via relative paths resolved from `CFG_DIR` (for example `client: ["config.cfg"]` for `id1`)
- New built-in quickstart catalog entries for `og-ctf` (3Wave CTF 2.51 server package) and `fvf` (Future vs Fantasy 4.2R)

### Changed
- Reworked quickstart bootstrap flow around `CFG_DIR/game.json` + `servers.ini` seeding, with `id1` always included and invalid `QUICKSTART` names explicitly skipped
- Consolidated quickstart and asset install logic into `src/nexus/internal/assets/game.go`, removing legacy split entrypoints and dead bootstrap code paths
- Updated built-in asset URLs to the `0xBrsm/QuakeAssets` repository (`q1/` layout), matching the renamed asset catalog structure
- Refreshed `src/` docs to match current quickstart behavior, catalog location, and asset source references

## 0.10.7

### Added
- Client startup argument pipeline with `CL_ARGS` and optional URL argument passthrough via `CL_URL_ARGS`
- Session-oriented admin command flow (`session ...`) with dedicated session command handling

### Changed
- Unified `/start` client configuration handling in Nexus/client startup wiring, including `CL_SMENU` and startup setting delivery
- Refactored Nexus admin/orchestration internals by splitting manager state/session logic into focused files and reducing duplicated dispatch/status parsing paths
- Updated server/docs defaults and operational documentation to align with the revised startup/admin flows

## 0.10.6.4

### Changed
- Preserved original filename casing for user CD uploads in the browser overlay while keeping lowercase normalization for non-CD user files
- Lowercased runtime server VFS overlay symlink paths so classic uppercase mod assets resolve under Linux Quake's lowercase lookups
- Added runtime VFS case-collision guardrails so same-layer path collisions (e.g. `PROGS.DAT` vs `progs.dat`) fail fast instead of silently overwriting

## 0.10.6.3

### Changed
- Unified WebSocket transport browser-console logging paths in the client (`net_ws_transport.c`) so connect/error/close and queue-overflow diagnostics use a shared logger
- Clarified unload lifecycle naming in browser persistence glue: `NexQuake_OnPageUnload` and `requestUnloadShutdown`, with an explicit note that `pagehide`/`beforeunload` are unload signals (not tab-visibility changes)
- Added an explicit Nexus log event when a WebSocket session is promoted to admin via valid `rcon_password` frame auth

## 0.10.6.2

### Changed
- Renamed Nexus internal game-data package paths and symbols from `gamedata` to `assets`, with `assets.go`/`assets_test.go` renamed to `game.go`/`game_test.go`
- Standardized runtime game-data naming from `data` to `game` across env/config/routes (`GAME_DIR`, `/app/game`, `/game/...`, `/game-manifest`)

## 0.10.6.1

### Changed
- Nexus now promotes a connected client session to admin after a successful `rcon_password` admin frame, so `sessions` role output reflects that elevation during the same WebSocket lifetime
- Loader bootstrap phase labels/status text are centralized and phase-1 bootstrap logging now flows through the shared phase path

## 0.10.6

### Changed
- Split browser persistence roots into `/NexQuake/game` (mods/config) and `/NexQuake/cd` (uploaded CD tracks), with `/cd` wired directly to the CD root
- Renamed the user-overlay link namespace from `.nq` to `.usr` for clearer separation from remote asset roots
- Simplified overlay file operations to use canonical runtime paths directly (`/mod/...`, `/cd/...`) and removed intermediate path-mapping helpers
- Streamlined CD track display/state matching in the overlay after game/CD path separation

## 0.10.5

### Changed
- Refactored shell pre-js architecture into smaller concern-focused modules, splitting runtime bootstrap from remote VFS install/prefetch logic
- Rebalanced overlay responsibilities so CD state/controls and VFS/file-manager behavior live in dedicated modules
- Standardized shell module numbering by concern (`tens` for layers, `ones` for sublayers) and updated build prep file lists to match
- Simplified shell helper logic by deduplicating shared localStorage boolean reads/writes and minor review-driven duplication cleanup

## 0.10.4

### Added
- Session-scoped hashed asset gateway flow in Nexus (`/start` + `/nq/<hash>`) with manifest parsing/validation coverage
- RCON integration coverage additions aligned with updated admin auth behavior

### Changed
- Runtime asset bootstrap moved from direct `/data-manifest` consumption to `/start` bundle metadata plus hash-addressed asset fetches
- Client/headless asset resolution standardized on FNV-1a hashing using `X-NexQuake-Ref`, including CD remote track URL resolution

## 0.10.3

### Changed
- Nexus HTTP routing and wiring cleanup: removed redundant `/data-manifest/` route registration and simplified `newMux`/admin env function binding in `main.go`
- Nexus internals tightened across admin/orch/nqnet/gamedata packages by removing dead wrappers/helpers, collapsing duplicate logic, and trimming redundant checks
- Bugfix `net_dgrm.c` patch/docs now intentionally keep only the POSIX portability + receive-buffer overflow fixes (drops the prior NAT `AddrCompare` change)

### Added
- Expanded `src/client/README.md` with a feature-to-patch index and missing patch documentation (`cl_main.c.patch`, `menu.c.patch`)

## 0.10.2.1

### Added
- Empty server mod directories now propagate to the client manifest set, so config-only mods (no shipped data files) still appear as usable client mod folders

## 0.10.2

### Changed
- No product-facing behavior changes in `src/` (internal maintenance release)

## 0.10.1.1

### Fixed
- Overlay/input UX regressions: arrow-key passthrough with UI open, modal interaction behavior, and sidebar/file interaction polish

## 0.10.1

### Changed
- VFS bootstrap and mod switching flow simplified around `.nqremote` searchpath layering and updated manifest/install behavior

## 0.10.0

### Added
- Modular shell UI architecture for overlay/file-manager behavior, including expanded CD management workflows
- Browser CD audio backend plus Nexus CD manifest streaming (`/cd-manifest`) with integrated client/server track handling

## 0.9.4

### Added
- Overlay file move by dragging files onto a game directory tab
- Upload progress and storage sync status in the upload message area

### Changed
- Mod switching now rebuilds searchpaths from a base snapshot instead of using cached mod searchpaths
- Global-config mode now skips auto-`exec quake.rc` on mod switch

### Fixed
- Upload and drag-move now prompt before overwriting existing files
- Upload queue is locked while busy to prevent overlapping writes

## 0.9.3.1

### Changed
- SVG favicon redesigned with overlapping DpQuake N/Q glyphs and entwined layering effect

## 0.9.3

### Added
- Per-mod config support: `config.cfg` is saved/loaded on mod switch via `COM_SwitchGame()`, with a JS-togglable per-mod vs unified mode
- Game directory is reset to base on disconnect so config writes target the correct mod

### Changed
- Client shell user-file allowlist is centralized and now includes `.pak` for upload/listing/persistence

### Fixed
- Missing null termination on `com_gamedir` copy in unified-config path
- Upload validation error bubble now wraps and uses full row width before wrapping

## 0.9.2

### Added
- Nexus websocket keepalive pings for better idle resilience
- Server browser menu headings/centering improvements

### Changed
- Nexus `sessions` output columns reordered and now includes client source IP

### Fixed
- Client websocket reconnect-on-send (including `rcon`) and improved send-failed reasons
- `smenu` no longer flashes the full console backdrop when invoked from the console

## 0.9.1

### Added
- Dedicated server `mapcycle` support for deathmatch map rotation from either a file or inline list

### Changed
- Server `changelevel` flow now falls back to stock behavior when `mapcycle` is unset or invalid

## 0.9.0

### Added
- NexQuake in-browser VFS/settings panel ("Slipgate Complex") with per-mod tabs, drag-drop upload, inline `.cfg` editing, and file export/delete actions
- Branded NexQuake loading screen with status/progress presentation

### Changed
- Browser user-file persistence now restores/syncs `.cfg`, `.sav`, `.dem`, and `.pcx` under `/nexquake` across sessions
- Build-time version injection into `shell.html`, with consistent `v<version>` display in UI and WASM startup banner

### Fixed
- VFS overlay/input interactions and remote-file write fallback behavior for mixed base/mod data paths

## 0.8.1

### Added
- Comma-separated `QUICKSTART` manifests: `QUICKSTART=ctf,arena,tf` loads all three `.json` manifests in order, concatenating their game data entries

### Changed
- `loadGameDataEntries` refactored to iterate comma-separated names with per-name validation

## 0.8.0

### Added
- **Distributed Server Orchestration**: Nexus now coordinates multiple Quake instances via `servers.ini` with PTY-based console monitoring and dynamic port discovery.
- **Unified Admin Plane**: A new administrative Nexus RCON endpoint (port 0) for remote management of the server fleet, including live process controls (`start`/`stop`/`restart`) and session visibility.
- **Engine Stability Patches**: Standardized security and portability patches (addressing format strings, buffer overflows, and 64-bit alignment) and integrated into the core engine.
- **Browser UX Enhancements**: Fixed audio buffering jitter, improved vsync synchronization, and stabilized platform shutdown for a smoother browser experience.

### Changed
- **Architectural Refactor**: Decoupled the monolithic Nexus relay into domain-specific internal packages (`nqnet`, `orch`, `admin`, `gamedata`), eliminating global state and circular dependencies.
- **Runtime Observability**: Transitioned from static polling to real-time console monitoring, allowing the Nexus to detect effective server ports and game states automatically.

## 0.7.0

### Changed
- Replace SDL2 platform layer with direct Emscripten (WebGL2, WebAudio, HTML5 input)
- Split platform into `sys_wasm.c`, `vid_wasm.c`, `snd_wasm.c`
- GPU-side palette conversion: 8-bit R8 texture + fragment shader lookup
- Fullscreen triangle from `gl_VertexID` (no VBO needed)
- Event-driven input via Emscripten HTML5 callbacks (no polling)
- 256-byte keymap lookup table replacing 75-line switch statement

### Fixed
- Choppy audio: reduced ScriptProcessorNode buffer from 2048 to 512 frames

### Removed
- SDL2 dependency (`sys_sdl.c`, `vid_sdl.c`, `snd_sdl.c`)
- Dead WASM_SAVE_PAKS code (game data uses HTTP cache)
- Unused stubs, globals, and dedicated-server code paths

## 0.6.0

### Added
- Mod switching (hot swap): client can change game directory at runtime without reload
- Common, host, and network patches for dynamic mod context (`common.c.patch`, `host.c.patch`, `net_main.c.patch`)
- Server polling for live game state (`slist.go`)

### Changed
- Nexus VFS and routing updated for per-mod file serving

## 0.5.0

### Changed
- Refactor WebSocket stack: split `net_websocket.c` into transport (`net_ws_transport.c`) and protocol layers
- Extract RCON into standalone `cmd_rcon.c` with JS token bridge (`cmd_rcon_token.js`)
- Upgrade Go from 1.21 to 1.25; adopt `net/http` routing and modern stdlib APIs

### Added
- Source attribution documentation (`ATTRIBUTIONS.md`)

## 0.4.0

### Added
- Authentication system for admin privileges (`auth.go`)
- RCON: client console commands relayed through Nexus to server (`net_websocket.c`, `auth.go`)
- Persistent filesystem: user config and saves survive browser sessions (`sys_sdl.c`, `shell.html`)

## 0.3.0

### Added
- PAK exploding and individual file streaming (`vfs.go`, `pak.go`)
- Asset manifest system for game data management (`assets.go`, `quake106.go`)
- Precache prefetch: client fetches assets during map load (`cl_parse.c.patch`)
- Server `.ent` file overrides for custom entity placement (`sv_main.c.patch`)

### Changed
- Extract quake106 asset handling into standalone Go package (`quake106/`)

### Fixed
- 32-bit pointer alignment for server builds

## 0.2.0

### Added
- Server browser: aggregated server list replies from Nexus (`net_websocket.c`)
- Nexus routing and server info caching (`routing.go`, `slist.go`)

### Changed
- Split Nexus into focused modules: `vfs.go`, `servers.go`, `udp.go`, `websock.go`
- Rename gateway to Nexus

### Fixed
- Reconnect stability after server disconnect

## 0.1.0

### Added
- Go relay server (Nexus): WebSocket-to-UDP bridge for browser clients (`main.go`, `ws_handler.go`, `udp_relay.go`)
- NetQuake dedicated server with selective upstream patching (`src/server/`)
- Control message protocol between WASM client and Nexus (`net_websocket.c`)

### Fixed
- Server crash on client disconnect (`net_websocket.c`, `udp_relay.go`)
- Compilation errors in `net_websocket.c` across architectures

---

Built on the [Quake GPL source](https://github.com/id-Software/Quake) (1999),
the [Quake-WASM](https://github.com/GMH-Code/Quake-WASM) port by Gregory Maynard-Hoare,
and WebSocket multiplayer by [initialed85](https://github.com/initialed85).
