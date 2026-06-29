# Changelog

Code evolution in `0xBrsm/NexQuake` — client, Nexus (relay), and server.

Versioning note: entries through `0.19.x` represent pre-1.0 development history. Legacy `0.20.0` is the stable-versioning commitment point and maps to `1.0.0`.

## 1.14.1

### Fixed
- Particle effects (rocket trails, explosions) rendered about twice their
  intended size in Native mode at High detail. Particle size now tracks the
  view's projection scale instead of the raw render width, so it matches the
  rest of the scene at any aspect ratio, and is guarded against extreme
  render widths or very low `fov` values.

## 1.14.0

### Added
- Mid-game demo recording: `record` can now be started at any point during a
  live game, not only before connecting, capturing from the current frame.
- Instant replay: bind a key to `replay` to play back a rolling in-memory ring
  of the last few seconds of play. Recording and replay share a passive
  signon-preamble capture so playback is contiguous, and demos are written to
  the in-browser filesystem (no automatic download).
- Native (window-fill) video modes that render at the browser window's live
  aspect with no black bars and follow it on resize and device rotation;
  Classic mode keeps a fixed 4:3 image. A redesigned Video Options menu adds
  Mode (Classic/Native) and Detail (Low/Medium/High) rows plus a live
  Resolution readout. `vid_mode` now spans 0–5 (Classic/Native × Low/Med/High)
  and the selected mode persists across launches, so the client boots straight
  into it.

### Changed
- The software renderer now supports render widths up to 3840px to cover
  4K-wide and 32:9 ultrawide displays, and the viewmodel scales independently
  of aspect so the weapon no longer recedes on ultrawide or portrait viewports.

### Fixed
- Bounded the demo-filename copies in `CL_Record_f`/`CL_PlayDemo_f`
  (`bugfix/cl_demo.c.patch`): a long demo name typed at the console could
  overflow the stack via unchecked `sprintf`/`strcpy` plus the `.dem` append.

## 1.13.0

### Added
- `net_transport` cvar (`auto`|`tcp`|`udp`) to pin or auto-select the client
  network carrier.
- Touch "Customize Controls" menu for editing the on-screen control layout.

### Changed
- The server browser now updates live, streamed from Nexus over a
  session-scoped Server-Sent Events channel instead of on-demand
  control-protocol queries; a manifest-generation change also triggers a
  jittered client refetch to pick up server-added assets.
- Renamed the bootstrap/asset endpoints to `/gamedir` (per-session manifest +
  boot config) and `/pak/<hash>` (content-addressed asset bytes); on-the-wire
  JSON and the asset-ref header are unchanged.
- The client transport carrier is now lazy and per-connection (keep-warm
  retired) with a bounded WebTransport probe that falls back to TCP, and the
  indicator is redesigned into a compact UDP/TCP chip.
- Un-precached mod sounds now play non-blocking instead of stalling the client.
- Autoscaling now spawns on reactive free-slot headroom rather than
  server-browser poll rate.

### Fixed
- Eliminated the network-dependent menu-entry hitch caused by a synchronous
  manifest refetch on filesystem-lookup misses; new files are still picked up
  via async refresh.
- Client/transport config is now applied once at boot and no longer re-applied
  on manifest refetches (reconnect, lazy-load recovery), so a refetch can't
  reconfigure a live session.

## 1.12.1

### Changed
- Crosshair is now enabled by default.
- Mouselook requests raw (unaccelerated) pointer movement from the browser.

### Fixed
- HTTP/3 page loads no longer intermittently 404; the WebTransport h3 listener
  now serves the full app mux, not just `/connect`.
- Reduced first-use audio hitches by prefetching the client sound precache set
  and warming core UI sounds at boot.
- `rcon login` reports its outcome consistently by device (console always;
  mobile toast) across both edge-gated and PKCE flows.
- The `rcon login` landing page is rendered via string substitution instead of
  `fmt.Sprintf`, avoiding format-verb corruption from `%` in the HTML template.

## 1.12.0

### Added
- Native HTTPS + WebTransport via a single `EXTERNAL_URL` switch (see
  `src/docs/ENVIRONMENT.md` "Run Paths"). Unset keeps plain HTTP + WebSocket;
  `EXTERNAL_URL=https://host` makes Nexus own the public endpoint, sourcing one
  cert from `CERT_DIR` (BYO `cert.pem`/`key.pem`, else Let's Encrypt `autocert`)
  for both HTTPS and QUIC. Bind + TLS is a fatal startup gate.
- Active transport indicator: a lower-right "Connected by: WebTransport (UDP) /
  WebSocket (TCP)" readout in the browser shell; double-click to collapse,
  hidden on touch.

### Changed
- Nexus owns its public endpoint; a reverse proxy is no longer required (the
  behind-a-front shape stays via `AUTH_CLIENT_IP_HEADER`).
  `WT_CERT_DIR`/`WT_CERT_ROTATE_DAYS` replaced by `CERT_DIR`.
- Admin auth is verify-only (no server-side login flow): Nexus verifies an OIDC
  JWT (Bearer or `AUTH_JWT_HEADER`-injected). Direct-exposure `rcon login` runs
  Authorization Code + PKCE as a public client (`POST /rcon/session` → httpOnly
  `nq_session`); new optional `AUTH_CLIENT_ID`. `AUTH_RCON_PASSWORD` still works.
- `AUTH_ADMIN_ID` is now fail-closed: unset grants admin to no verified login
  (was: any); set `=any` or list claim matchers (`groups:admins`,
  `email:you@example.com`). Admin methods are named **password** and **SSO** in
  the boot summary and docs.
- `fov_adapt` defaults to `1` (pure Hor+, was `0.5`) and is now archived.
- Connection lines ("Connected by X", "Connection upgraded to X", …) now print
  to the browser console instead of the in-game console (plus the lower-right
  overlay).
- Reduced log noise on a public endpoint: per-connection upgrade failures and
  full-tx-queue drops no longer warn per event, and the benign quic-go
  receive-buffer warning is silenced.

### Fixed
- Hor+ FOV scaling applies on the same refdef recalc as a video mode change;
  previously a boot-time or mid-session mode switch left FOV unscaled until
  another recalc was forced.

### Removed
- The auto-rotating self-signed WebTransport cert manager; WT uses the
  `CERT_DIR` cert now.
- Client network diagnostics: the `net_diag` cvar and `netdiag`/`netdiag reset`
  commands.

## 1.11.0

### Added
- WebTransport (QUIC/HTTP3) transport for the browser client (`net_wt.c`, nexus
  `wt.go`, `trunk/webtransport/`). The client tests for a landed WT handshake and
  adopts it over the existing WebSocket path; falls forward to WebSocket automatically
  when WT is absent or fails.
- Active transport surfaced to the player on connect/upgrade; `rcon` appends
  the source transport in parens after the client IP.

### Changed
- emsdk toolchain pinned to 5.0.0 in local and headless builds to match production;
  prevents the lazy-VFS guard regression introduced by emsdk 5.0.3.

### Fixed
- Oversized WT datagrams are now dropped silently (like UDP loss) instead of killing
  the session.
- Dead WT transport is closed on WebSocket fall-forward so the server correctly reaps
  the orphaned session.
- Asyncify re-entry: JS transport callbacks no longer call back into the WASM engine
  or delete sockets during the boot path.
- emsdk 5.0.3 lazy-VFS guard: replaced `node.contents` check with the size field to
  avoid false-positive boot failures ("Corrupted data file").

## 1.10.0

### Added
- `rcon login` (WASM): opens an OIDC popup against `GET /rcon`; Nexus pushes
  `rcon: authenticated.` over the trunk control channel on completion. Sidesteps
  COOP-same-origin restrictions for proxy-gated deployments.
- `error.data.hint` on `401` responses: operator-facing auth hint built from configured methods.
- `trunk` public API additions: `Session.SendControl/End`, `SessionByVirtualIP`,
  `SessionInfo.ActiveServerPort`, control-channel handler hooks.

### Changed
- Admin RPC: `session.*` → `client.*`; `client.ban` now disconnects the WASM engine via
  `Cbuf_AddText` and returns `disconnected` count and `source_ips[]`.
- `server.instance.command` uses random `__NQX_*` sentinels instead of a time-window
  heuristic — long replies no longer truncate; adjacent commands can't interleave.
- Nexus top-level split by concern (`connect.go`, `log.go`, `env.go`, `ws.go`, `version.go`);
  `access` split into `auth.go`, `gate.go`, `identity.go`; console-bound text lowercased.
- Docs: `ADMIN.md`, `nexus/README.md`, `client/README.md` aligned to current shape.

## 1.9.1

### Changed
- Browser client networking is now a pluggable transport shell (`net_wasm.c`, `net_nqchan.c`, `net_ws.c`); the WebSocket transport is unchanged but additional transports can be added without reworking the channel layer. WASM→JS bridge symbols are unified under the `NQWasm_` prefix.
- Nexus relay extracted into the transport-agnostic `trunk` package (renamed from `nqrelay`) with an exported `Transport` interface and a single `NewConn` constructor. Public RPC and HTTP surfaces are unchanged.
- Internal cleanup: dead code removed (`deadcode`/`staticcheck` clean for `src/nexus`); `internal/session` folded into `internal/admin` now that the cross-package coupling that forced the split is gone.
- Docs: `client/README.md`, `nexus/README.md`, and `ATTRIBUTIONS.md` updated to match the new file/symbol layout.

## 1.9.0

### Added
- `POST /rcon` JSON-RPC 2.0 endpoint for admin control. External tools can list servers/sessions, run lifecycle operations (`server.start/stop/restart/remove/launch`), forward commands to a specific instance (`server.instance.command`), ban sessions (`session.ban`), and discover available methods via `rpc.discover`. All admin commands share the same registry as the in-game `rcon` surface. See [ADMIN.md](docs/ADMIN.md) for the full reference.
- `Authorization: Rcon <password>` scheme for admin credentials (RFC 7235). OIDC JWT continues to use the header configured by `AUTH_JWT_HEADER` (default `Authorization`).
- `rcon.help` and `rpc.discover` as first-class admin commands.

### Changed
- Admin commands moved into a flat namespace: `server.list`, `server.instances`, `server.start`, `server.stop`, `server.restart`, `server.remove`, `server.launch`, `server.instance.command`, `session.list`, `session.info`, `session.ban`, `logs.tail`.
- Per-instance dispatch accepts port only. `rcon <host> <cmd>` (slist-hostname) and `rcon <idx> <cmd>` (pool slot index) are no longer recognized — use `rcon <port> <cmd...>` with the listen port shown in `rcon nexus server list`. The fan-out-to-all-backends behavior those forms provided is gone with the server/instance rework.
- Terminology: Pool → Server, Backend → Instance, VIP → NQIP across log output, command help, and RPC field names.
- Env var `POOL_SIZE` renamed to `SV_MAX_INSTANCES`. Operators with `POOL_SIZE=N` in their environment must update.

## 1.8.5

### Added
- `fov_adapt` cvar (default `0.5`) controls the blend between literal horizontal FOV and pure Hor+ aspect scaling. `0` gives stock Quake behavior (vertical FOV shrinks on widescreens); `1` gives pure Hor+ (vertical preserved, horizontal widens aggressively on ultrawide); values in between trade peripheral vision for edge comfort.

### Changed
- Browser client applies Hor+ aspect scaling at refdef calculation time: `fov` is interpreted as the intended horizontal FOV at 4:3 and wider viewports widen fov_x based on `fov_adapt`. Previously an ultrawide mode left the vertical view as a narrow slit; the `fov` cvar is no longer mutated on mode changes.
- Nexus watches `GAME_DIR/<mod>/{common,server}/` with fsnotify and reconciles the dedicated server's runtime filesystem per event. Files dropped into the game directory after server start are visible to running servers without a restart. `.pak` archives still require a server restart because the engine caches pak directories at load.
- Browser client recovers from VFS-manifest misses transparently within a single `fopen`: both stale-hash XHR 404s and brand-new paths trigger a synchronous manifest refresh and retry in place. Files dropped into the game directory are reachable in the current session without a page reload.

### Fixed
- Video mode menu no longer shows the "Weapon hidden in widescreen" notice; with Hor+ in place, the weapon renders correctly at any aspect.

## 1.8.4

### Added
- Site now generates static HTML documentation pages from Markdown sources, with client-side navigation between doc routes.
- Homepage PLAY NOW button auto-launches the game on desktop browsers via `?autostart=1`; touch devices retain the ENTER screen for fullscreen/landscape activation.
- Site publishes `sitemap.xml` and `robots.txt` for search-engine indexing.

### Changed
- Gameswitch connection lookup moved from inline `cl_parse.c` patch logic into `com_gameswitch.c` (`COM_SwitchGameForConnection`).
- Homepage SEO improved with structured data, Open Graph / Twitter meta tags, and semantic HTML headings.
- Dead client-side docs route helpers removed from static shell generator in favor of the new static page pipeline.

## 1.8.3

### Added
- Browser client startup now supports `-notouch` and `-nojoy` so operators can explicitly disable touch and gamepad input paths.

### Changed
- Browser client startup now preloads `/start` before WASM instantiation, derives imported WASM memory from the startup `-mem` value, and defaults the client heap to 64 MB.
- Client install-icon packaging now uses `client/shell/pwa-icon.svg` as the source asset and generates PNG install icons when raster tooling is available.

### Fixed
- Game-directory switching now restores the base hunk level before mounting mod content, preventing stale allocator state from leaking across client gameswitches.
- Generated NexQuake install icons now keep the legacy glyph placement while restoring the missing `Q` segment in the mark.

## 1.8.2

### Added
- Web app install metadata now includes dedicated NexQuake PWA icons (`192`, `512`, and Apple touch) so home-screen installs use branded assets.

### Changed
- Client packaging now ships `manifest.webmanifest` and install icon files alongside the WASM shell output, with shell metadata links resolved relative to the install path.

### Fixed
- Client server-list refresh now clears stale hostcache entries before rebuilding results, avoiding leftover server metadata between `slist` runs.

## 1.8.1

### Added
- The in-game settings logo now links to the documentation site as a direct help entry point.

### Changed
- The docs site now ships with pre-rendered navigation and home-page content so the initial page is readable before client-side navigation boots.

### Fixed
- The ninth touch-control slot now maps cleanly through the WASM input layer, bind names, and client docs.
- Browser `quit` now returns to a fresh loader page again, with mobile fullscreen re-entered on the next normal `ENTER` tap instead of getting stuck after teardown.

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
