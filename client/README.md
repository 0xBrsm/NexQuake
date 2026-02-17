# WASM Client

The browser client: Quake compiled to WebAssembly with direct Emscripten APIs and WebSocket multiplayer.

These files overlay the upstream id Software Quake source during build. The build system clones `id-Software/Quake`, applies patches, copies these overlays in, and compiles with Emscripten. The output is `index.html`, `shell.css`, `favicon.svg`, `index.js`, and `index.wasm`.

## Files

### Platform Layer

These replace the original Quake platform code with direct browser API calls.

| File | Purpose | Why It's Needed |
|------|---------|-----------------|
| `sys_wasm.c` | **System layer.** Main loop (`emscripten_set_main_loop`), file I/O (stdio), timing (`gettimeofday`), memory allocation, IDBFS persistence (config/saves survive page reload), deferred shutdown for WASM safety. | Quake needs a system layer for every platform. This one targets Emscripten directly -- no SDL, no POSIX assumptions that don't hold in a browser. |
| `vid_wasm.c` | **Video and input.** Creates WebGL2 context, implements GPU-side palette rendering (8-bit R8 framebuffer texture + RGBA palette texture + fragment shader lookup), handles fullscreen triangle draw. Registers HTML5 input callbacks (keydown, keyup, mousemove, mousedown, mouseup, wheel) that fire `Key_Event()` directly. Manages pointer lock for mouselook. | Quake's software renderer produces 8-bit indexed pixels. This file gets them on screen via WebGL2 without the CPU-side palette conversion every other port does. Input is event-driven (not polled) because that's how browsers work. |
| `snd_wasm.c` | **Audio.** Exposes a ring buffer in WASM heap. A JavaScript `ScriptProcessorNode` callback reads interleaved int16 samples, converts to float, and outputs to speakers. `SNDDMA_GetDMAPos()` returns the JS read cursor. `SNDDMA_Submit()` is a no-op. Handles AudioContext creation and autoplay resume on user gesture. | Quake's mixer writes to a DMA buffer. This bridges that buffer to WebAudio with no locks (callback and mixer are on the same thread in browsers). |

### WebSocket Networking

These replace UDP sockets with WebSocket transport for browser multiplayer.

| File | Purpose | Why It's Needed |
|------|---------|-----------------|
| `net_ws_transport.c` | **WebSocket transport.** Owns the Emscripten websocket lifecycle, callbacks, frame queues, and 2-byte port-header framing. | Browsers can't open UDP sockets. This layer turns WebSocket frames into queued datagrams. |
| `net_ws_transport.h` | **Transport header.** Public transport API (including `WebSocketTransport_SendFrame`). | Used by `cmd_rcon.c` (nexus control-plane) and the vnet driver. |
| `net_ws_vnet.c` | **Virtual LAN + landriver.** Implements Quake's `net_landriver` interface over the WebSocket transport. Projects a fixed virtual server IP veneer and applies nexus-assigned local virtual client IP identity for stock Quake APIs. | Keeps the Quake networking stack believing it's talking to UDP sockets. |
| `net_ws_vnet.h` | **Driver header.** Public interface for the WebSocket landriver. | Declares the driver functions that `net_bsd.c` references. |
| `net_bsd.c` | **Driver table.** Replaces the original `net_bsd.c` to register only the WebSocket landriver (no UDP). | Quake's networking discovers available drivers from this table. In the browser, WebSocket is the only option. |
| `cmd_rcon.c` | **RCON commands.** Implements `rcon_password` + command framing for the Nexus control channel, with explicit host/port targeting and sensible fallbacks. | Supports `rcon <host|port> <cmd>`, uses the current server when connected for `rcon <cmd>`, and falls back to Nexus control (`target 0`) when disconnected. |

### Patches

Applied to upstream Quake source at build time after bugfix patches. Each patch
is NexQuake-specific and targeted at WASM/browser compatibility or NexQuake
features. No overlap with `bugfix/` patches (verified — hunks target different
line ranges even when patching the same file).

#### `chase.c.patch` — WASM link-time signature fix

Adds an explicit `extern` prototype for `SV_RecursiveHullCheck()`. Without it,
WASM's strict function signature checking causes a link-time mismatch error.
The original code relied on an implicit declaration, which C99 deprecated and
Emscripten rejects.

#### `cl_main.c.patch` — mod switching initialization

Calls `COM_SwitchGame(NULL)` during `CL_Disconnect()` on Emscripten. Resets
the game directory to the base game when disconnecting, so the next connect
can switch to whatever mod the new server is running.

#### `common.h.patch` — `COM_SwitchGame` declaration

Declares `COM_SwitchGame()` (implemented in `common.c.patch`) so that
`cl_parse.c` can call it when connecting to a server running a different mod.

#### `common.c.patch` — mod directory switching (`COM_SwitchGame`)

Adds `COM_SwitchGame()`: a game directory cache that allows the client to
switch between mods (e.g. `id1` → `hipnotic`) at connect time without
restarting. Caches up to 64 mod search paths in a ring buffer backed by
`Hunk_Alloc`. On Emscripten, calls `Module.nexquakeSwitchGameData()` to
notify JavaScript so it can fetch the correct game data.

#### `host.c.patch` — Hunk level tracking for mod switching

Adds `fs_hunklevel` alongside `host_hunklevel` so `COM_SwitchGame()` can safely
free and reallocate the Hunk for different mod directories without corrupting
the host's high water mark. Ensures `Hunk_FreeToLowMark()` uses whichever mark
is higher.

#### `net.h.patch` — `PollProcedure` signature + `hostcache` gamedir field

Two changes:
1. Adds a `gamedir[16]` field to `hostcache_t` so the server list can display
   and match which game/mod each server is running.
2. Changes the `PollProcedure` function pointer from unprototyped `()` to
   `(void *arg)`. WASM enforces strict function signature matching — the
   original unprototyped callback crashes with "function signature mismatch."

#### `net_main.c.patch` — server list and poll infrastructure

Multiple NexQuake-specific changes to the network main loop:
1. **Aggregated server list.** Adds `slist_agg_done` flag and early-exit logic.
   When the Nexus returns a single aggregated server list packet, the client
   stops the default 1.5s LAN polling window immediately.
2. **Gamedir column.** Adds a `Game` column to the `slist` console output.
3. **`void*` poll signatures.** Updates `Slist_Send()` and `Slist_Poll()` to
   match the `(void *arg)` signature from `net.h.patch`.
4. **`SchedulePollProcedure` dedup.** Prevents scheduling the same poll
   procedure twice, which can corrupt the linked list and cause infinite loops.
5. **Emscripten yield.** Adds `emscripten_sleep(1)` in the blocking
   `NET_Connect` poll loop so WebSocket `onmessage` callbacks can fire.

#### `net_dgrm.c.patch` — datagram layer WASM fixes

NexQuake-specific changes to the datagram network layer:
1. **`void*` poll signatures.** Updates `Test_Poll()` and `Test2_Poll()` to
   match the `(void *arg)` signature from `net.h.patch`.
2. **Aggregated server list parsing.** Rewrites `_Datagram_SearchForHosts()` to
   parse the Nexus's batched server list format (count + per-server fields
   including port, name, map, gamedir, users, maxusers, protocol). Uses
   `Q_strncpy` for bounds-safe copies into `hostcache` entries.
3. **Emscripten yield during connect.** Adds `emscripten_sleep(1)` in the
   `_Datagram_CheckNewConnections` connect loop so WebSocket frames can arrive.

#### `menu.c.patch` — server list UI enhancements

Four changes to the server browser menus:
1. **Console auto-close.** Closes the console when opening the search menu so
   it doesn't obscure the UI.
2. **Gamedir column.** Adds a `Game` column to the server list using
   `NET_SlistFormatEntry()`, showing which mod each server runs.
3. **Centered layout.** Header row, separator rule, and entries are
   dynamically centered based on the widest entry.
4. **`smenu` command.** Registers a console command to open the search menu
   directly (shorthand for `menu_search`).

#### `cl_parse.c.patch` — mod switching and asset prefetch

Two features added to `CL_ParseServerInfo()`:
1. **Automatic mod switching.** On connect, looks up the server's `gamedir`
   from `hostcache` and calls `COM_SwitchGame()` to load the correct mod data
   before parsing precache lists.
2. **Parallel asset prefetch (Emscripten).** Enqueues all precache model and
   sound paths into a JavaScript prefetch pipeline (`Module.nexquakePrefetchEnqueue`).
   Waits up to 30 seconds for all fetches to complete before continuing with
   Quake's synchronous load path. Eliminates "death by sequential latency"
   when loading large maps over the network.
3. **Keepalive fix.** Changes `CL_KeepaliveMessage()` to use direct buffer
   indexing instead of `MSG_ReadByte()`, which can misread data when called
   mid-parse (e.g. during serverinfo precache). Downgrades the error to
   `Con_DPrintf` instead of `Host_Error` since non-nop datagrams arriving
   during load are harmless.

### Build

| File | Purpose |
|------|---------|
| `Makefile.emscripten` | Emscripten build configuration. Source file list, compiler flags (`-sMAX_WEBGL_VERSION=2`, `-sASYNCIFY`), linker settings. No SDL references. |
| `shell.html` | HTML template for the browser client. Bootstrap logic: fetches `/start` (manifest bundle + hash context), maps assets to `/nq/<hash>`, creates virtual filesystem, starts Quake. Contains canvas element, loading UI, and query parameter handling (for example `?-nosound`). |

## Feature Index

Patches are organized per upstream file (one `.patch` per file touched), but
each patch serves one or more higher-level features. This index maps features
to their constituent patches and overlay files so you can trace all the code
for a given feature in one place.

Several patches contain hunks for multiple features (noted with "partial"
below). This is why patches are organized per file rather than per feature —
splitting cross-cutting hunks across feature-specific patches would cause
them to break when any one feature patch is skipped, since line offsets in the
shared upstream file would shift.

### WASM Compatibility

Foundation fixes required for the Quake source to compile and run under
Emscripten. Everything else depends on these.

| File | What it contributes |
|------|---------------------|
| `chase.c.patch` | Explicit `extern` prototype for `SV_RecursiveHullCheck()` |
| `net.h.patch` *(partial)* | `PollProcedure` signature: `()` → `(void *arg)` |
| `net_main.c.patch` *(partial)* | `void*` poll signatures, `emscripten_sleep` yield in connect loop |
| `net_dgrm.c.patch` *(partial)* | `void*` poll signatures, `emscripten_sleep` yield in search/connect loops |

Platform overlay files: `sys_wasm.c`, `vid_wasm.c`, `snd_wasm.c`

### Mod Switching

Runtime game directory switching so the client can connect to servers running
different mods (e.g. `id1` → `hipnotic`) without restarting.

| File | What it contributes |
|------|---------------------|
| `common.h.patch` | `COM_SwitchGame()` declaration |
| `common.c.patch` | `COM_SwitchGame()` implementation (ring buffer cache, JS notification) |
| `host.c.patch` | `fs_hunklevel` for safe Hunk free/realloc across mod switches |
| `cl_main.c.patch` | Resets game directory on disconnect |
| `cl_parse.c.patch` *(partial)* | Auto-switches to server's gamedir on connect |

### Nexus Server List

Aggregated server list from the Nexus relay, with gamedir display and
formatted UI.

| File | What it contributes |
|------|---------------------|
| `net.h.patch` *(partial)* | `gamedir[16]` field in `hostcache_t` |
| `net_main.c.patch` *(partial)* | Aggregated slist with early-exit, gamedir column in console output, poll dedup |
| `net_dgrm.c.patch` *(partial)* | Batched server list parsing (Nexus protocol) |
| `menu.c.patch` | Server list UI: gamedir column, centered layout, console auto-close, `smenu` command |

### Asset Prefetch

Parallel download of models and sounds over HTTP during connect, eliminating
sequential latency.

| File | What it contributes |
|------|---------------------|
| `cl_parse.c.patch` *(partial)* | JavaScript prefetch pipeline, 30s wait, keepalive fix for mid-parse datagrams |

### WebSocket Networking

Replaces UDP sockets with WebSocket transport for browser multiplayer.

Overlay files only (no patches): `net_ws_transport.c`, `net_ws_transport.h`,
`net_ws_vnet.c`, `net_ws_vnet.h`, `net_bsd.c`, `cmd_rcon.c`

### Cross-Cutting Patches

These patches appear in multiple features above. The table shows which hunks
serve which feature.

| Patch | Hunks | Features |
|-------|-------|----------|
| `net.h.patch` | `PollProcedure` signature | WASM Compat |
| | `gamedir[16]` field | Nexus Server List |
| `net_main.c.patch` | Aggregated slist, gamedir column, poll dedup | Nexus Server List |
| | `void*` signatures, `emscripten_sleep` | WASM Compat |
| `net_dgrm.c.patch` | Batched server list parsing | Nexus Server List |
| | `void*` signatures, `emscripten_sleep` | WASM Compat |
| `cl_parse.c.patch` | Auto mod switch on connect | Mod Switching |
| | Prefetch pipeline, keepalive fix | Asset Prefetch |

## Patch Overlap Analysis

The bugfix patches (`bugfix/`) and client patches both modify `common.c`,
`net_dgrm.c`, and `net.h`, but they target completely separate regions:

| File | Bugfix hunks | Client hunks | Conflict? |
|------|-------------|-------------|-----------|
| `common.c` | Lines 855–980 (COM_FileBase, COM_Parse) | Lines 21, 1239–1834 (include, COM_SwitchGame) | ❌ No |
| `net_dgrm.c` | Lines 29–50, 438–455 (structs, overflow) | Lines 54, 516, 644, 1103–1313 (signatures, slist, yield) | ❌ No |
| `net.h` | Lines 241–254 (htonl/ntohl declarations) | Lines 227, 314 (hostcache gamedir, PollProcedure sig) | ❌ No |

Bugfix patches are applied first by `prepare-upstream.sh`, then client patches
on top. The line offsets may shift slightly due to earlier patches adding/removing
lines, but `patch` handles this with fuzz matching.

## Build Process

```bash
# 1. Clone upstream Quake source
git clone --depth 1 https://github.com/id-Software/Quake.git
cd Quake/WinQuake

# 2. Apply patches
patch -p0 < /path/to/client/net.h.patch
patch -p0 < /path/to/client/net_main.c.patch
# ... etc

# 3. Copy overlay files
cp /path/to/client/*.c /path/to/client/*.h .
cp /path/to/client/Makefile.emscripten /path/to/client/shell/shell.html /path/to/client/shell/shell.css /path/to/client/shell/favicon.svg .

# 4. Build
make -f Makefile.emscripten
```

Output: `index.html`, `shell.css`, `favicon.svg`, `index.js`, `index.wasm`

The automated build scripts (`build/build-client.sh`) handle all of this.

## Key Design Choices

- **No SDL**: Direct browser APIs only. Smaller binary, fewer layers, easier debugging.
- **GPU palette rendering**: 8-bit framebuffer uploaded as R8 texture, palette lookup in fragment shader. Eliminates per-frame CPU conversion.
- **Event-driven input**: Emscripten HTML5 callbacks fire `Key_Event()` directly. No polling loop.
- **WebSocket URL**: `Module.websocketUrl` / `Module.WEBSOCKET_URL` if set (headless/tests), else `ws(s)://<window.location.host>/ws`; if neither exists, websocket init fails fast.
- **Default UI preserved**: No custom menus. `connect`, `slist`, standard Quake console.

## Necessity Review

All client patches are **necessary** for NexQuake:

| Patch | Verdict | Reason |
|-------|---------|--------|
| `chase.c.patch` | **Required** | WASM build fails without the explicit prototype |
| `cl_main.c.patch` | **Required** | Resets game directory on disconnect for clean mod switching |
| `common.h.patch` | **Required** | Declaration for COM_SwitchGame |
| `common.c.patch` | **Required** | Mod switching is core NexQuake functionality |
| `host.c.patch` | **Required** | Companion to common.c.patch (Hunk safety) |
| `net.h.patch` | **Required** | WASM crashes without strict function signatures |
| `net_main.c.patch` | **Required** | Aggregated slist, poll dedup, WASM yields |
| `net_dgrm.c.patch` | **Required** | Nexus server list protocol, WASM yields |
| `menu.c.patch` | **Required** | Server list gamedir column and centered layout |
| `cl_parse.c.patch` | **Required** | Mod switching + asset prefetch for playable load times |
