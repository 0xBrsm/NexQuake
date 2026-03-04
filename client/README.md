# Web Client

The browser client: Quake compiled to WebAssembly with a from-scratch native WASM platform layer for software rendering. See [`ARCHITECTURE.md`](../docs/ARCHITECTURE.md) for the full technical breakdown (GPU-side palette conversion, audio pipeline, input handling).

The client patches and overlays are a mix of required and additive features. These files overlay the upstream id Software Quake source during build. The build system clones `id-Software/Quake`, applies patches, copies these overlays in, and compiles with Emscripten. The output is `index.html`, `index.js`, `index.wasm`, `index.data`, plus the static shell assets (`shell-nq.css`, `shell-loader.css`, `shell-ui.css`, `shell-touch.css`, `favicon.svg`).

## Features

### 1. WASM Platform Layer

**Required.** Quake's original platform code targets DOS/Win32 system calls (VGA framebuffer, DMA sound, raw keyboard scancodes). None of these exist in a browser. This feature replaces every platform interface with direct Emscripten/browser APIs: WebGL2 for video, WebAudio for sound, HTML5 events for input, and IDBFS for persistence.

| File | Purpose |
|------|---------|
| `sys_wasm.c` | System layer: `emscripten_set_main_loop`, file I/O, timing, IDBFS persistence, deferred shutdown. |
| `vid_wasm.c` | Video and input: WebGL2 context, GPU-side palette rendering (R8 framebuffer + RGBA palette + fragment shader lookup), HTML5 input callbacks, pointer lock. |
| `snd_wasm.c` | Audio: WASM heap ring buffer read by a `ScriptProcessorNode` callback. Single-threaded, no locks. |
| `cd_wasm.c` | Emulated CD audio: `EM_JS` browser-side implementation for Quake `cd` commands, with shell overlay controls. |
| `patches/net.h.patch` | Strict `PollProcedure` function pointer signature. WASM enforces exact signature matching. |
| `patches/net_main.c.patch` | `void*` poll signatures and `emscripten_sleep` yields in blocking loops. |
| `patches/net_dgrm.c.patch` | `void*` poll signatures and `emscripten_sleep` yields during connect. |

### 2. WebSocket Networking

**Required.** Browsers cannot open UDP sockets. This feature replaces Quake's UDP networking with WebSocket transport, routing all traffic through Nexus relay. Players connect by port number (e.g. `connect 26000`); the relay maps ports to game servers. All overlay files, no patches to upstream.

| File | Purpose |
|------|---------|
| `net_ws_transport.c/h` | WebSocket lifecycle, callbacks, frame queues, 2-byte port-header framing. |
| `net_ws_vnet.c/h` | Virtual LAN driver implementing Quake's `net_landriver` interface. Synthesizes virtual `qsockaddr` structures for Quake's address APIs. |
| `net_bsd.c` | Driver table registering only the WebSocket landriver. |

### 3. Game Directory Switching

**Quality-of-life addition.** Original Quake requires restarting the executable to change game directories that rely on client-side resources. Since the browser client loads via a URL, restarting means a full page reload and re-downloading all assets. This feature adds runtime game directory switching so the client can seamlessly connect to servers running different game dirs (e.g. `id1` to `ctf`) without reloading. On connect, the client detects the server's game directory from `hostcache`, snapshots/restores the filesystem search paths, and notifies JavaScript to fetch the new game data.

| File | Purpose |
|------|---------|
| `patches/common.h.patch` | `COM_SwitchGame` declaration. |
| `patches/common.c.patch` | Core implementation. Baseline search path snapshot, restore, `.usr` user-overlay links, JS notification via `Module.nexquakeSwitchGameData()`. |
| `com_gameswitch.c/h` | Standalone game-switch module extracted from the patch. |
| `patches/host.c.patch` | Adds `fs_hunklevel` for safe Hunk free/realloc during directory switch. |
| `patches/cl_main.c.patch` | Resets game directory to base on disconnect. |
| `patches/cl_parse.c.patch` | Auto-switches game directory on connect based on server's gamedir. |

### 4. RCON

**Quality-of-life addition.** Stock WinQuake has a hidden `cmd` admin feature, but this only forwards commands to the currently connected server. NexQuake adds a browser-safe `rcon` command over Nexus control channel so operators can target any server by hostcache name/port or Nexus itself for system-wide actions. See [Admin Guide](../docs/ADMIN.md) for details.

| File | Purpose |
|------|---------|
| `cmd_rcon.c` | `rcon` console command: sends commands over Nexus control channel with host/port targeting. |

### 5. Nexus Server List

**Quality-of-life addition.** Quake's original server browser sends LAN broadcast packets and waits 1.5 seconds for responses. NexQuake runs on a loopback network, which does not support broadcast. Instead, Nexus provides an aggregated server list over the relay connection. This feature parses the batched response format, adds a gamedir column so players can see which mod each server runs, and provides UI improvements (centered layout, console auto-close, `smenu` shortcut command).

| File | Purpose |
|------|---------|
| `patches/net.h.patch` | `gamedir[16]` field in `hostcache_t`. |
| `patches/net_main.c.patch` | Aggregated slist with early-exit, gamedir column in `slist` output, poll dedup. |
| `net_slist.c` | Batched server list parsing (count + per-server fields), extracted from `net_dgrm.c.patch`. |
| `patches/net_dgrm.c.patch` | `void*` poll signatures and `emscripten_sleep` yields; no longer handles slist parsing. |
| `patches/menu.c.patch` | Gamedir column, centered layout, console auto-close, `smenu` command. |

### 6. Asset Prefetch

**Quality-of-life addition.** Without this, connecting to a server triggers sequential synchronous downloads of models and sound files one at a time over HTTP. On a typical map this means dozens of assets loaded in series, producing multi-second connect times. This feature enqueues all precache paths into a JavaScript pipeline that fetches them concurrently (default 16 parallel workers), reducing connect time to roughly the cost of the single slowest asset.

| File | Purpose |
|------|---------|
| `cl_prefetch.c/h` | Prefetch pipeline: enqueue model/sound precache lists, 30s wait, keepalive fix for mid-parse datagrams. Extracted from `cl_parse.c.patch`. |
| `patches/cl_parse.c.patch` | Calls `CL_Prefetch` from `CL_ParseServerInfo`; no longer contains the prefetch implementation inline. |

### 7. Touch Controls and Gamepad

**Quality-of-life addition.** Enables gameplay on mobile devices and controllers. Touch input uses a configurable on-screen button layout (9 bindable slots plus a menu button) with a virtual joystick zone on the left side of the screen and a swipe-look zone on the right. Buttons show SVG glyphs for their bound actions. Gamepad input uses the Gamepad API with standard mapping (A/B/X/Y, shoulder buttons, triggers, D-pad, sticks). Both touch and gamepad inputs are detected automatically; the on-screen touch overlay only appears when a coarse-pointer device is present. Mirrored layout and flip mode are supported for left-handed play.

Per-device sensitivity cvars extend the Options menu: `sensitivity` (Quake's built-in) for mouse, `touch_sensitivity` for touch swipe-look, and `joy_sensitivity` for gamepad stick look. `lookspring` and `lookstrafe` are similarly per-device. The menu label updates to reflect the active input device.

FOV scales automatically with the canvas aspect ratio when changing video mode (configurable via `vid_mode`). Classic 4:3 (`vid_mode 0`) is the default.

| File | Purpose |
|------|---------|
| `in_wasm.c` | Input module: mouse pointer-lock, touch slot dispatch, virtual joystick, swipe-look, gamepad polling, per-device sensitivity cvars (`sensitivity`, `touch_sensitivity`, `joy_sensitivity`). |
| `patches/keys.c.patch` | Renames `JOY*`/`AUX*` key names to readable aliases (`JOY_A`, `JOY_B`, `TOUCH1`–`TOUCH9`, etc.) for bind commands. |
| `patches/menu.c.patch` | Per-device sensitivity/lookspring/lookstrafe labels and sliders; video mode menu with fixed and fullscreen sections. |
| `patches/host.c.patch` | FOV aspect-ratio scaling on video mode change. |

## Shell

The `shell/` directory contains the JavaScript runtime, HTML template, and CSS that quickstart the WASM module, manage game data, and provide a browser-native overlay UI. JS files are grouped by numbered buckets (`00`, `10s`, `20s`, `50s`, `60`) and load in lexicographic order via `--pre-js nq-pre.js` (the concatenated pre-JS injected into the Emscripten output). The HTML template (`shell.html`) is processed by Emscripten's `--shell-file` to produce the final `index.html`. The four CSS files are served as static assets alongside it.

**Startup and VFS** — `00-core.js`, `10-startup.js`, `11-startup-vfs.js`

On page load, the shell fetches a manifest bundle from `/start`, builds a virtual filesystem in Emscripten's VFS, and syncs persistent user data from IndexedDB (IDBFS). Remote game assets are mounted as lazy nodes under `/nexusfs/<mod>/` and downloaded on first read via synchronous XHR with retry and exponential backoff. User mod files live in `/NexQuake/game/<mod>/` and are linked at `/nexusfs/.usr/<mod>/`; user CD uploads live in `/NexQuake/cd` and are exposed at `/cd`. This keeps Quake search paths layered so user files override remote assets. Asset URLs are computed from an FNV-1a hash of the manifest reference and file key, producing immutable CDN-friendly paths. At first browser startup, bundled seed cfg files (`/nqseed/<base>/autoexec.cfg` and `/nqseed/<base>/nexquake.cfg`) are copied once into the user IDBFS tree and guarded by `/NexQuake/.nq.cfgseed-v1`. Startup args come from Nexus runtime config (`CL_ARGS`), with optional URL arg append when `CL_URL_ARGS=1` (for example `?-nosound&+exec&ctf.cfg`). URL parsing splits on `&`, so each `&`-separated value maps to one argv token. Tokens are passed to Quake as command-line args (including normal `stuffcmds` handling for `+` tokens).

`10-startup.js` drives the three-phase bootstrap (WASM instantiation → VFS build → IDBFS sync), progress bar, and the ENTER button flow. On touch devices it requests fullscreen and captures monitor dimensions before calling `Module.callMain()`. Game directory switches that require a page reload show a reload screen instead of trying to restart in-place.

**Touch UI** — `20-touch-glyphs.js`, `21-touch-controls.js`

Manages the on-screen touch overlay. `21-touch-controls.js` handles overlay visibility, button slot layout persistence (stored in `localStorage` as `nexquake.touch.layout.v1`), drag-and-drop slot repositioning, and touch-device detection (`nqIsTouchInput`). The overlay is hidden entirely on non-touch devices. `20-touch-glyphs.js` provides an SVG icon map (`NQ_TOUCH_GLYPHS`) keyed by normalized binding strings (e.g. `+attack`, `+jump`, `impulse 2`) so each on-screen button shows a weapon or action glyph instead of raw text.

**UI Panel** — `50-ui.js`, `51-ui-cd.js`, `52-ui-vfs.js`, `53-ui-upload.js`, `59-ui-events.js`

A settings panel layered over the game canvas. Provides a tabbed file browser (one tab per installed mod), a text editor for `.cfg` files, drag-and-drop file management between mod directories, CD playback controls with play/pause per track, file upload with progress and overwrite confirmation, and per-mod vs. shared config toggling. The overlay footer shows the running version and, when connected to a server, a copyable join code. All state persists to IndexedDB on page hide. `59-ui-events.js` wires all click, drag, keyboard, and pointer-lock events and manages overlay width so the panel does not occlude the in-game Options menu.

**CD Audio** — `51-ui-cd.js`

Quake's CD audio system originally played music tracks from a physical CD-ROM drive. NexQuake replaces this with digital audio streaming through a two-tier resolution system. When the engine requests a track number, JavaScript first scans the user's `/cd/` browser store for uploaded files whose filenames contain the track number (e.g. `track02.ogg`, `#3.mp3`). If no local file matches, it falls back to the remote CD manifest served by Nexus. Playback uses an HTML5 `<audio>` element with smooth fade transitions on pause/stop and automatic resume on user gesture to handle browser autoplay policies. On the C side, `cd_wasm.c` implements Quake's `CDAudio_*` API by calling into JavaScript via `EM_JS` bridges, tracking the `bgmvolume` cvar each frame. All in-game `cd` commands control audio play as normal and reflect current state in the overlay UI. Users can upload their own music files through the overlay, which take priority over server-provided tracks.

**Error Handling** — `60-onerror.js`

Global exception handler that hides the loader and displays a status message on uncaught errors.

**Shell CSS** — `shell-nq.css`, `shell-loader.css`, `shell-ui.css`, `shell-touch.css`

| File | Purpose |
|------|---------|
| `shell-nq.css` | Brand foundation: embedded `DpQuake` bitmap font and CSS custom properties for the NexQuake color palette. Imported first; all other shell CSS inherits these variables. |
| `shell-loader.css` | Loading screen (`#nq-loader`): full-viewport overlay with logo, progress bar, status text, and ENTER button. |
| `shell-ui.css` | Canvas/shell layout, settings overlay panel, cfg editor, confirm dialog, and branding row. |
| `shell-touch.css` | On-screen touch controls: joystick ring, swipe zones, button slots, and mirrored/flip-mode layout. |

## Building

| File | Purpose |
|------|---------|
| `Makefile.emscripten` | Emscripten build: source list, compiler flags (`-sMAX_WEBGL_VERSION=2`, `-sASYNCIFY`, `-DNEXQUAKE_VERSION`), linker settings including `--shell-file shell.html` and `--pre-js nq-pre.js`. |
| `shell/shell.html` | HTML template. Emscripten replaces `{{{ SCRIPT }}}` to produce `index.html`. |

```bash
# Manual
git clone --depth 1 https://github.com/id-Software/Quake.git
cd Quake/WinQuake
patch -p0 < /path/to/client/patches/*.patch
cp /path/to/client/*.c /path/to/client/*.h .
# Concatenate shell JS into pre-JS and copy shell template
cat /path/to/client/shell/[0-9]*.js > nq-pre.js
cp /path/to/client/shell/shell.html .
make -f Makefile.emscripten
```

Output: `index.html`, `index.js`, `index.wasm`, `index.data`; copy `shell/shell-nq.css`, `shell/shell-loader.css`, `shell/shell-ui.css`, `shell/shell-touch.css`, `shell/favicon.svg` alongside.
