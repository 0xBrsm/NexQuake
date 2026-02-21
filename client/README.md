# WASM Client

The browser client: Quake compiled to WebAssembly with a from-scratch native WASM platform layer for software rendering. See [`ARCHITECTURE.md`](../docs/ARCHITECTURE.md) for the full technical breakdown (GPU-side palette conversion, audio pipeline, input handling).

The client patches and overlays are a mix of required and additive features. These files overlay the upstream id Software Quake source during build. The build system clones `id-Software/Quake`, applies patches, copies these overlays in, and compiles with Emscripten. The output is `index.html`, `shell.css`, `favicon.svg`, `index.js`, and `index.wasm`.

## Features

### 1. WASM Platform Layer

**Required.** Quake's original platform code targets DOS/Win32 system calls (VGA framebuffer, DMA sound, raw keyboard scancodes). None of these exist in a browser. This feature replaces every platform interface with direct Emscripten/browser APIs: WebGL2 for video, WebAudio for sound, HTML5 events for input, and IDBFS for persistence.

| File | Purpose |
|------|---------|
| `sys_wasm.c` | System layer: `emscripten_set_main_loop`, file I/O, timing, IDBFS persistence, deferred shutdown. |
| `vid_wasm.c` | Video and input: WebGL2 context, GPU-side palette rendering (R8 framebuffer + RGBA palette + fragment shader lookup), HTML5 input callbacks, pointer lock. |
| `snd_wasm.c` | Audio: WASM heap ring buffer read by a `ScriptProcessorNode` callback. Single-threaded, no locks. |
| `cd_wasm.c` | Emulated CD audio: `EM_JS` bridges to the shell JS CD pipeline (`20-cdaudio.js`). |
| `net.h.patch` | Strict `PollProcedure` function pointer signature. WASM enforces exact signature matching. |
| `net_main.c.patch` | `void*` poll signatures and `emscripten_sleep` yields in blocking loops. |
| `net_dgrm.c.patch` | `void*` poll signatures and `emscripten_sleep` yields during connect. |

### 2. WebSocket Networking

**Required.** Browsers cannot open UDP sockets. This feature replaces Quake's UDP networking with WebSocket transport, routing all traffic through the Nexus relay. Players connect by port number (e.g. `connect 26000`); the relay maps ports to game servers. All overlay files, no patches to upstream.

| File | Purpose |
|------|---------|
| `net_ws_transport.c/h` | WebSocket lifecycle, callbacks, frame queues, 2-byte port-header framing. |
| `net_ws_vnet.c/h` | Virtual LAN driver implementing Quake's `net_landriver` interface. Synthesizes virtual `qsockaddr` structures for Quake's address APIs. |
| `net_bsd.c` | Driver table registering only the WebSocket landriver. |

### 3. Mod Switching

**Quality-of-life addition.** Original Quake requires restarting the executable to change mods that rely on client-side resources. Since the browser client loads via a URL, restarting means a full page reload and re-downloading all assets. This feature adds runtime game directory switching so the client can seamlessly connect to servers running different mods (e.g. `id1` to `ctf`) without reloading. On connect, the client detects the server's game mod from `hostcache`, snapshots/restores the filesystem search paths, and notifies JavaScript to fetch the new game mod's data.

| File | Purpose |
|------|---------|
| `common.h.patch` | `COM_SwitchGame` declaration. |
| `common.c.patch` | Core implementation. Baseline search path snapshot, restore, `.usr` user-overlay links, JS notification via `Module.nexquakeSwitchGameData()`. |
| `host.c.patch` | Adds `fs_hunklevel` for safe Hunk free/realloc during directory switch. |
| `cl_main.c.patch` | Resets game directory to base on disconnect. |
| `cl_parse.c.patch` | Auto-switches mod on connect based on server's gamedir. |

### 4. RCON

**Quality-of-life addition.** Stock WinQuake has a hidden `cmd` admin feature, but this only forwards commands to the currently connected server. NexQuake adds a browser-safe `rcon` command over the Nexus control channel so operators can target any server by hostcache name/port or Nexus itself for system-wide actions. See [RCON](../docs/RCON.md) for details.

| File | Purpose |
|------|---------|
| `cmd_rcon.c` | `rcon` console command: sends commands over the Nexus control channel with host/port targeting. |

### 5. Nexus Server List

**Quality-of-life addition.** Quake's original server browser sends LAN broadcast packets and waits 1.5 seconds for responses. NexQuake runs on a loopback network, which does not support broadcast. Instead, Nexus provides an aggregated server list over the relay connection. This feature parses the batched response format, adds a gamedir column so players can see which mod each server runs, and provides UI improvements (centered layout, console auto-close, `smenu` shortcut command).

| File | Purpose |
|------|---------|
| `net.h.patch` | `gamedir[16]` field in `hostcache_t`. |
| `net_main.c.patch` | Aggregated slist with early-exit, gamedir column in `slist` output, poll dedup. |
| `net_dgrm.c.patch` | Batched server list parsing (count + per-server fields). |
| `menu.c.patch` | Gamedir column, centered layout, console auto-close, `smenu` command. |

### 6. Asset Prefetch

**Quality-of-life addition.** Without this, connecting to a server triggers sequential synchronous downloads of models and sound files one at a time over HTTP. On a typical map this means dozens of assets loaded in series, producing multi-second connect times. This feature enqueues all precache paths into a JavaScript pipeline that fetches them concurrently (default 16 parallel workers), reducing connect time to roughly the cost of the single slowest asset.

| File | Purpose |
|------|---------|
| `cl_parse.c.patch` | Prefetch pipeline in `CL_ParseServerInfo`: enqueue, 30s wait, keepalive fix for mid-parse datagrams. |

## Shell JavaScript

The `shell/` directory contains the JavaScript runtime that quickstarts the WASM module, manages game data, and provides a browser-native overlay UI. Files are numbered `00-` through `60-` and load in order.

**Startup and VFS** — `00-core.js`, `10-module.js`, `11-remote-vfs.js`, `12-args.js`, `13-persist.js`

On page load, the shell fetches a manifest bundle from `/start`, builds a virtual filesystem in Emscripten's VFS, and syncs persistent user data from IndexedDB (IDBFS). Remote game assets are mounted as lazy nodes under `/nexusfs/<mod>/` and downloaded on first read via synchronous XHR with retry and exponential backoff. User mod files live in `/NexQuake/game/<mod>/` and are linked at `/nexusfs/.usr/<mod>/`; user CD uploads live in `/NexQuake/cd` and are exposed at `/cd`. This keeps Quake search paths layered so user files override remote assets. Asset URLs are computed from an FNV-1a hash of the manifest reference and file key, producing immutable CDN-friendly paths. Startup args come from Nexus runtime config (`CL_ARGS`), with optional URL arg append when `CL_URL_ARGS=1` (for example `?-nosound&+exec&ctf.cfg`). URL parsing splits on `&`, so each `&`-separated value maps to one argv token. Tokens are passed to Quake as command-line args (including normal `stuffcmds` handling for `+` tokens).

**CD Audio** — `20-cdaudio.js`, `cd_wasm.c`

Quake's CD audio system originally played music tracks from a physical CD-ROM drive. NexQuake replaces this with digital audio streaming through a two-tier resolution system. When the engine requests a track number, JavaScript first scans the user's `/cd/` browser store for uploaded files whose filenames contain the track number (e.g. `track02.ogg`, `#3.mp3`). If no local file matches, it falls back to the remote CD manifest served by Nexus. Playback uses an HTML5 `<audio>` element with smooth fade transitions on pause/stop and automatic resume on user gesture to handle browser autoplay policies. On the C side, `cd_wasm.c` implements Quake's `CDAudio_*` API by calling into JavaScript via `EM_JS` bridges, tracking the `bgmvolume` cvar each frame. All in-game `cd` commands control audio play as normal and reflect current state in the overlay UI. Users can upload their own music files through the overlay, which take priority over server-provided tracks.

**Overlay UI** — `50-overlay.js`, `51-overlay-core.js`, `52-overlay-cd.js`, `53-overlay-vfs.js`, `54-overlay-upload.js`, `59-overlay-events.js`

A settings panel layered over the game canvas. Provides a tabbed file browser (one tab per installed mod), a text editor for `.cfg` files, drag-and-drop file management between mod directories, CD playback controls with play/pause per track, file upload with progress and overwrite confirmation, and per-mod vs. shared config toggling. All state persists to IndexedDB on page hide.

**Error Handling** — `60-onerror.js`

Global exception handler that hides the loader and displays a status message on uncaught errors.

## Building

| File | Purpose |
|------|---------|
| `Makefile.emscripten` | Emscripten build configuration: source list, compiler flags (`-sMAX_WEBGL_VERSION=2`, `-sASYNCIFY`), linker settings. |
| `shell.html` | HTML template with quickstart logic. |

```bash
# Automated
build/build-client.sh

# Manual
git clone --depth 1 https://github.com/id-Software/Quake.git
cd Quake/WinQuake
patch -p0 < /path/to/client/*.patch
cp /path/to/client/*.c /path/to/client/*.h .
make -f Makefile.emscripten
```

Output: `index.html`, `shell.css`, `favicon.svg`, `index.js`, `index.wasm`
