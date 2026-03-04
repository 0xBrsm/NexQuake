# Changelog

Code evolution in `0xBrsm/NexQuake` — client, Nexus (relay), and server.

Versioning note: entries through `0.19.x` represent pre-1.0 development history. Legacy `0.20.0` is the stable-versioning commitment point and maps to `1.0.0`.

## 1.8.0

### Added
- Touch text entry added with a dedicated top-of-screen bar for mobile console/chat/menu text, kept in sync with engine buffers.
- Touch controls added a ninth slot.
- Touch controls updated default binds and glyphs.

### Changed
- WASM touch/menu text handling is simplified: new menu text helpers keep JS and C in lockstep, and overlay resize/focus logic is shared to avoid stray inputs when the keyboard appears.
- Startup prefetch warms common `gfx` assets so the first overlay/console paint is smoother on the web client.

### Fixed
- Nexus relay writes now apply a deadline to prevent rare WebSocket stalls under load; the stress-test harness handles larger UDP bursts more safely.

## 1.7.7

### Fixed
- Browser `connect` and `rcon` target resolution now stay aligned across reconnects and scaled server pools, including exact hostname targeting for RCON and host-cache invalidation after WebSocket reconnects.
- Nexus now chunks oversized admin replies and preserves joinable candidate ports in pooled server listings, avoiding silent client-side drops and broken joins from `slist`.

### Changed
- `rcon nexus slist` now shows pool summaries by default and can drill into grouped backend details with `all`, a pool index, or a pool hostname.
- Nexus pool admin commands and server-targeted `rcon` usage now consistently accept pool indices/hostnames, with pool-targeted RCON fan-out to every running backend in that pool.

## 1.7.6

### Fixed
- WASM touch tap detection now uses event timestamps and guarded async key pulses, improving tap reliability and avoiding stray gameplay input while the overlay editor is open.
- `POOL_SIZE=1` entries now behave as ordinary non-autoscaled servers in Nexus and `slist`, without demand tracking or pool instance-count suffixes.

## 1.7.5

### Changed
- Refactored `cd_wasm.c`, `in_wasm.c`, `sys_wasm.c`, and `vid_wasm.c`, plus related shell styles, while preserving existing WASM client behavior.
- Refreshed product docs, README content, and the GitHub Pages docs hub, including the new `docs/CONTROLS.md` guide.

### Fixed
- Browser shell now defaults the multiplayer TCP/IP join cursor to the search field and blocks gameplay keys while the editor overlay is open.

## 1.7.4

### Changed
- Refactored the WASM client shell JavaScript into clearer startup, touch, and overlay concern boundaries while preserving existing gameplay and operator-facing behavior.
- Simplified overlay startup wiring and shared fullscreen request handling across startup and touch input paths.

## 1.7.3

### Fixed
- WASM client audio pause/resume now uses smoother fade and delayed suspend/resume handling on mobile browsers, reducing audible pop/click artifacts and improving visibility-transition stability.

## 1.7.2

### Changed
- Refactored Nexus handler and relay internals (route split, package boundary cleanup, and dead-code removal) while preserving external tunnel protocol semantics.

## 1.7.1

### Fixed
- Nexus relay now allows server-observed return-path UDP source ports in addition to primary managed listen ports, preventing post-connect gameplay hangs when server traffic shifts to ephemeral ports.

## 1.7.0

### Added
- Mobile-focused touch controls for browser play, including on-screen action glyphs, configurable touch layout behavior, and touch-driven menu navigation.
- WASM client gamepad support with bind-driven action routing so touch/gamepad inputs map through standard Quake command binds.

### Fixed
- Mobile fullscreen and safe-area handling now keeps the overlay/menu surfaces usable across orientation and settings transitions.

### Changed
- Client video mode and canvas sizing behavior now adapts more consistently across mobile and desktop aspect ratios, with cleaner runtime mode selection.
- Browser shell startup/UI modules were reorganized to simplify input and overlay behavior across touch, mouse, and controller use.

## 1.6.2

### Fixed
- Client `slist` row formatting now uses bounded writes for user-count and row rendering, preventing hostcache line buffer overflows.

### Changed
- Refactored Nexus relay internals into public package `nqrelay` as a pure WebSocket<->UDP relay API for import/vendoring use.
- Consolidated Nexus control-frame handling through a single orchestrator control handler for relay port `0` traffic.
- Moved `slist` response codec helpers into orchestration and removed duplicated relay-side server-list codec paths from the relay package.

## 1.6.1

### Fixed
- Nexus autoscaling now avoids premature pool scale-up during warmup demand windows.
- `POOL_SIZE` now defaults to `1` when unset, and single-instance `slist` rows no longer show an instance-count suffix.

### Changed
- Nexus pool routing now selects a backend listen port at `slist` request time instead of maintaining proxy-port session affinity.
- Removed pool proxy listener/routing paths and simplified Nexus pool orchestration around direct backend ports.

## 1.6.0

### Added
- Nexus dynamic server scaling for startup entries configured with `-port 0`, including demand-aware scale-up/scale-down headroom and per-line caps via `POOL_SIZE`.
- Session-affine relay routing for scaled server entries so join/handshake flows stay backend-consistent while preserving raw NetQuake datagram tunneling.

### Changed
- `slist` now reports aggregate users/maxusers plus instance counts for scaled server entries, with client server-list formatting updated to display the instance total.
- RCON target resolution for scaled server entries now requires a single routable backend listen port and returns an explicit ambiguity error otherwise.
- Nexus orchestration internals and docs now use explicit registry/policy/proxy lifecycle paths for reconcile heartbeat, draining, despawn, and routing decisions.
## 1.5.7

### Changed
- Production image publish workflow now pushes per-architecture images by digest.
- Quickstart README minor wording changes.

## 1.5.6

### Changed
- Dev image release workflows now publish from SemVer tag pushes with stable tags only (`<version>` and `latest`) and include tag-propagation handling to avoid transient release gate failures.

## 1.5.5

### Changed
- Production release metadata now enforces strict `MAJOR.MINOR.PATCH` tagging, explicit `NQ_VERSION` propagation, and aligned version signaling across mainline validation and runtime startup output.

## 1.5.4

### Fixed
- Nexus `/start` manifest generation now skips malformed `.pak` archives instead of failing the full bootstrap with HTTP `500`, so one bad pak no longer blocks client startup.
- Nexus now logs unreadable pak warnings with the offending pak path and parser error during manifest generation, making corrupt archive diagnosis actionable in production logs.

## 1.5.3

### Fixed
- Team Fortress quickstart map pack `q1/tf28maps.zip` now patches `storm1` (`FORTRESS/maps/STORM1.BSP`) by adding the missing `team_no` on an `info_player_teamspawn`, preventing map-load `Host_Error: Program error` failures.

### Changed
- Clarified quickstart examples wording in `docs/QUICKSTART.md` (`docker run` launch comment and compose snippet label).

## 1.5.2

### Changed
- Standardized Nexus naming/capitalization across docs and inline comments for consistency.
- Standardized internal docs link text style (human-readable Title Case in prose, filename-style where file identity is the point) and documented the rule in `agents/DOC_STYLE.md`.
- Expanded `docs/QUICKSTART.md` with clearer Docker Compose guidance for moving beyond basic `docker run` usage.

## 1.5.1

### Changed
- Quickstart `server.cfg` added for `fvf` with default Purge mode.
- Tweaked Arena and FFA quickstart server configs.

## 1.5.0

### Changed
- Client `connect` and `rcon` now share a single hostcache resolver that supports case-insensitive unique prefix matching (with explicit ambiguity rejection), so short server-name targets work consistently across both commands.
- Client hostcache server-name capacity was raised from `16` to `24` characters.
- `slist` console output and multiplayer server-list menu formatting were widened for the longer server-name column, and the server-list menu rows/cursor were recentered to keep the layout aligned.

## 1.4.1

### Fixed
- Client remote VFS lazy-load recovery now throws filesystem `ENOENT` on failed remote fetches and triggers throttled `/start` manifest refresh retries with backoff, preventing repeated manifest-refresh request loops after idle/resume transport failures.
- RCON server-command replies now continue capture long enough for outputs delayed by the audited `echo ...; wait; wait; wait; <cmd>` wrapper, so cvar queries like `timelimit` no longer return only the admin audit line.
- Docker `wasm-builder` now copies `etc/` into the build context so client seed cfgs (`autoexec.cfg`, `nexquake.cfg`) are present when bundling `/nqseed`, fixing CI image builds that failed with missing seed cfg errors.

## 1.4.0

### Fixed
- Dedicated server map rotation now forces `changelevel` at `(timelimit + 1)` minutes when no players are connected, preventing the known no-player QuakeC mapchange hang (`mapcycle` first, then `start` -> `e1m1`, then map `trigger_changelevel` target, otherwise current map).
- Dedicated server map spawn now falls back to `maps/start.bsp` when the requested startup map is missing, preventing failed server activation on bad map names.
- Dedicated server `mapcycle` selection now skips missing map BSPs and advances to the next valid entry, avoiding repeated fallback loops when cycle lists include maps not present on disk.
- Dedicated server `mapcycle` parsing now dedupes consecutive valid map tokens so QW-style `localinfo <from> <to>` cfg chains can be used as mapcycle files without self-looping on repeated adjacent maps.
- Client cfg seed marker handling now only skips reseeding when the marker contains the success value (`1`), avoiding false-positive "already seeded" states from partial/empty marker writes.
- Client seed packaging now resolves cfg defaults from `src/etc/client/` (with legacy fallback), fixing build failures after the `src/etc` config split.

### Changed
- Server `host.c` idle rotation now reuses the shared `mapcycle` selector from `pr_cmds.c` to keep map parsing behavior consistent and DRY.
- Server/docs now document idle `mapcycle` fallback behavior under `timelimit`.
- Quickstart defaults now ship mod-specific server config assets for `arena` and `ffa` (including `ffa` mapcycle list), and `fortress` now includes `tf28maps.zip` in common assets.

## 1.3.1

### Fixed
- Mouse wheel input in the WASM client is now deterministic across browser delta modes (`pixel`/`line`/`page`) and smoother on mixed ratchet/trackpad hardware.

### Changed
- Client bootstrap now seeds `autoexec.cfg` and `nexquake.cfg` into IDBFS once on first load.
- Quickstart defaults in `src/etc` were updated for the config split (`config.cfg` -> `nexquake.cfg`) and `autoexec.cfg` now executes `nexquake.cfg`.
- Built-in quickstart `id1` no longer installs a client cfg from `game.json`; client defaults now come from the first-load seed flow.

## 1.3.0

### Added
- Browser Quake client (WASM), Nexus relay/admin service, and dedicated server runtime.
- Docker-first deployment with quickstart game-pack bootstrap.

### Changed
- Omitting AUTH_ADMIN_ID now promotes any authenticated user with JWT. Intended for admin-only OIDC endpoints, use with caution.

## 1.2.1

### Fixed
- Added the missing `SV_RecursiveHullCheck` declaration in shared `bugfix/world.h.patch` (applied to both client and server) to prevent strict-toolchain build failures and remove duplicate per-target patching

### Changed
- Build and release packaging now inject `NQ_VERSION` into Docker/WASM build inputs so shipped binaries and published images report a consistent release version

## 1.2.0

### Changed
- Simplified `compose.yml` for out-of-the-box usage with inline env defaults
- Added `compose.build.yml` for power-user and production image builds

## 1.1.0

### Added
- Quickstart download progress logging per mod with base vs game mod breakdown

### Fixed
- WebSocket reconnect socket state handling and widened session user column
- Session info with transient route ports now treats unknown ports as disconnected

### Changed
- Rcon admin requests are now audit-logged with admin tag before command output
- Renamed `og_ctf` quickstart entry to `og-ctf` and simplified download log format
- Updated etc configs, quickstart scan, and admin wait pacing

## 1.0.2

### Fixed
- Remote CD track URL resolution now uses the latest `/start` bundle data, preventing stale `/nq/<hash>` fetches after idle/reconnect cycles
- Mouse wheel handling now normalizes browser delta modes and accumulates partial deltas for more consistent Quake wheel ticks

### Changed
- Lowered client default `net_messagetimeout` from `300` to `10`

## 1.0.1

### Changed
- Refined client favicon rendering (updated SVG styling/color treatment and shell cache-buster) to improve visibility across themes and ensure browsers pick up favicon updates reliably

## 1.0.0

### Added
- Initial stable release
- Quickstart catalog support for local config assets via relative paths resolved from `CFG_DIR` (for example `client: ["config.cfg"]` for `id1`)
- New built-in quickstart catalog entries for `og-ctf` (3Wave CTF 2.51 server package) and `fvf` (Future vs Fantasy 4.2R)

### Changed
- Reworked quickstart bootstrap flow around `CFG_DIR/game.json` + `servers.ini` seeding, with `id1` always included and invalid `QUICKSTART` names explicitly skipped
- Consolidated quickstart and asset install logic into `src/nexus/internal/assets/game.go`, removing legacy split entrypoints and dead bootstrap code paths
- Updated built-in asset URLs to the `0xBrsm/QuakeAssets` repository (`q1/` layout), matching the renamed asset catalog structure
- Refreshed `src/` docs to match current quickstart behavior, catalog location, and asset source references

## 0.19.0

### Added
- Client startup argument pipeline with `CL_ARGS` and optional URL argument passthrough via `CL_URL_ARGS`
- Session-oriented admin command flow (`session ...`) with dedicated session command handling

### Changed
- Unified `/start` client configuration handling in Nexus/client startup wiring, including `CL_SMENU` and startup setting delivery
- Refactored Nexus admin/orchestration internals by splitting manager state/session logic into focused files and reducing duplicated dispatch/status parsing paths
- Updated server/docs defaults and operational documentation to align with the revised startup/admin flows

## 0.18.4

### Changed
- Preserved original filename casing for user CD uploads in the browser overlay while keeping lowercase normalization for non-CD user files
- Lowercased runtime server VFS overlay symlink paths so classic uppercase mod assets resolve under Linux Quake's lowercase lookups
- Added runtime VFS case-collision guardrails so same-layer path collisions (e.g. `PROGS.DAT` vs `progs.dat`) fail fast instead of silently overwriting

## 0.18.3

### Changed
- Unified WebSocket transport browser-console logging paths in the client (`net_ws_transport.c`) so connect/error/close and queue-overflow diagnostics use a shared logger
- Clarified unload lifecycle naming in browser persistence glue: `NexQuake_OnPageUnload` and `requestUnloadShutdown`, with an explicit note that `pagehide`/`beforeunload` are unload signals (not tab-visibility changes)
- Added an explicit Nexus log event when a WebSocket session is promoted to admin via valid `rcon_password` frame auth

## 0.18.2

### Changed
- Renamed Nexus internal game-data package paths and symbols from `gamedata` to `assets`, with `assets.go`/`assets_test.go` renamed to `game.go`/`game_test.go`
- Standardized runtime game-data naming from `data` to `game` across env/config/routes (`GAME_DIR`, `/app/game`, `/game/...`, `/game-manifest`)

## 0.18.1

### Changed
- Nexus now promotes a connected client session to admin after a successful `rcon_password` admin frame, so `sessions` role output reflects that elevation during the same WebSocket lifetime
- Loader bootstrap phase labels/status text are centralized and phase-1 bootstrap logging now flows through the shared phase path

## 0.18.0

### Changed
- Split browser persistence roots into `/NexQuake/game` (mods/config) and `/NexQuake/cd` (uploaded CD tracks), with `/cd` wired directly to the CD root
- Renamed the user-overlay link namespace from `.nq` to `.usr` for clearer separation from remote asset roots
- Simplified overlay file operations to use canonical runtime paths directly (`/mod/...`, `/cd/...`) and removed intermediate path-mapping helpers
- Streamlined CD track display/state matching in the overlay after game/CD path separation

## 0.17.1

### Changed
- Refactored shell pre-js architecture into smaller concern-focused modules, splitting runtime bootstrap from remote VFS install/prefetch logic
- Rebalanced overlay responsibilities so CD state/controls and VFS/file-manager behavior live in dedicated modules
- Standardized shell module numbering by concern (`tens` for layers, `ones` for sublayers) and updated build prep file lists to match
- Simplified shell helper logic by deduplicating shared localStorage boolean reads/writes and minor review-driven duplication cleanup

## 0.17.0

### Added
- Session-scoped hashed asset gateway flow in Nexus (`/start` + `/nq/<hash>`) with manifest parsing/validation coverage
- RCON integration coverage additions aligned with updated admin auth behavior

### Changed
- Runtime asset bootstrap moved from direct `/data-manifest` consumption to `/start` bundle metadata plus hash-addressed asset fetches
- Client/headless asset resolution standardized on FNV-1a hashing using `X-NexQuake-Ref`, including CD remote track URL resolution

## 0.16.1

### Changed
- Nexus HTTP routing and wiring cleanup: removed redundant `/data-manifest/` route registration and simplified `newMux`/admin env function binding in `main.go`
- Nexus internals tightened across admin/orch/nqnet/gamedata packages by removing dead wrappers/helpers, collapsing duplicate logic, and trimming redundant checks
- Bugfix `net_dgrm.c` patch/docs now intentionally keep only the POSIX portability + receive-buffer overflow fixes (drops the prior NAT `AddrCompare` change)

### Added
- Expanded `src/client/README.md` with a feature-to-patch index and missing patch documentation (`cl_main.c.patch`, `menu.c.patch`)

## 0.16.0

### Added
- Empty server mod directories now propagate to the client manifest set, so config-only mods (no shipped data files) still appear as usable client mod folders

## 0.15.3

### Changed
- No product-facing behavior changes in `src/` (internal maintenance release)

## 0.15.2

### Fixed
- Overlay/input UX regressions: arrow-key passthrough with UI open, modal interaction behavior, and sidebar/file interaction polish

## 0.15.1

### Changed
- VFS bootstrap and mod switching flow simplified around `.nqremote` searchpath layering and updated manifest/install behavior

## 0.15.0

### Added
- Modular shell UI architecture for overlay/file-manager behavior, including expanded CD management workflows
- Browser CD audio backend plus Nexus CD manifest streaming (`/cd-manifest`) with integrated client/server track handling

## 0.14.0

### Added
- Overlay file move by dragging files onto a game directory tab
- Upload progress and storage sync status in the upload message area

### Changed
- Mod switching now rebuilds searchpaths from a base snapshot instead of using cached mod searchpaths
- Global-config mode now skips auto-`exec quake.rc` on mod switch

### Fixed
- Upload and drag-move now prompt before overwriting existing files
- Upload queue is locked while busy to prevent overlapping writes

## 0.13.1

### Changed
- SVG favicon redesigned with overlapping DpQuake N/Q glyphs and entwined layering effect

## 0.13.0

### Added
- Per-mod config support: `config.cfg` is saved/loaded on mod switch via `COM_SwitchGame()`, with a JS-togglable per-mod vs unified mode
- Game directory is reset to base on disconnect so config writes target the correct mod

### Changed
- Client shell user-file allowlist is centralized and now includes `.pak` for upload/listing/persistence

### Fixed
- Missing null termination on `com_gamedir` copy in unified-config path
- Upload validation error bubble now wraps and uses full row width before wrapping

## 0.12.0

### Added
- Nexus websocket keepalive pings for better idle resilience
- Server browser menu headings/centering improvements

### Changed
- Nexus `sessions` output columns reordered and now includes client source IP

### Fixed
- Client websocket reconnect-on-send (including `rcon`) and improved send-failed reasons
- `smenu` no longer flashes the full console backdrop when invoked from the console

## 0.11.0

### Added
- Dedicated server `mapcycle` support for deathmatch map rotation from either a file or inline list

### Changed
- Server `changelevel` flow now falls back to stock behavior when `mapcycle` is unset or invalid

## 0.10.0

### Added
- NexQuake in-browser VFS/settings panel ("Slipgate Complex") with per-mod tabs, drag-drop upload, inline `.cfg` editing, and file export/delete actions
- Branded NexQuake loading screen with status/progress presentation

### Changed
- Browser user-file persistence now restores/syncs `.cfg`, `.sav`, `.dem`, and `.pcx` under `/nexquake` across sessions
- Build-time version injection into `shell.html`, with consistent `v<version>` display in UI and WASM startup banner

### Fixed
- VFS overlay/input interactions and remote-file write fallback behavior for mixed base/mod data paths

## 0.9.0

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
- **Runtime Observability**: Transitioned from static polling to real-time console monitoring, allowing Nexus to detect effective server ports and game states automatically.

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
