# WASM Client

The browser client: Quake compiled to WebAssembly with direct Emscripten APIs and WebSocket multiplayer.

These files overlay the upstream id Software Quake source during build. The build system clones `id-Software/Quake`, applies patches, copies these overlays in, and compiles with Emscripten. The output is `index.html`, `index.js`, and `index.wasm`.

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
| `net_websocket.c` | **WebSocket multiplayer driver.** Implements the Quake `net_landriver` interface over WebSocket. Opens connection to `/ws`, sends/receives binary frames with routing header (server_id + UDP port + raw datagram). Handles server selection, broadcast, and connection lifecycle. | Browsers can't open UDP sockets. This driver makes WebSocket look like UDP to the Quake networking stack. |
| `net_websocket.h` | **Driver header.** Public interface for the WebSocket landriver. | Declares the driver functions that `net_bsd.c` references. |
| `net_ws_transport.c` | **Transport layer.** WebSocket connection management, message queuing, frame callbacks. Handles the boundary between JavaScript WebSocket events and C-side packet delivery. | Separates transport concerns (connection, buffering, callbacks) from protocol concerns (routing, server selection) in the WebSocket driver. |
| `net_bsd.c` | **Driver table.** Replaces the original `net_bsd.c` to register only the WebSocket landriver (no UDP). | Quake's networking discovers available drivers from this table. In the browser, WebSocket is the only option. |
| `cmd_rcon.c` | **RCON commands.** Implements `rcon` console command for admin authentication and server commands. | Allows admin control of servers through the Quake console. |
| `cmd_rcon_token.js` | **RCON token bridge.** JavaScript helper that provides the auth token to the C RCON layer. | The auth token comes from the browser environment (URL param or cookie); this bridges it to C. |

### Patches

Applied to upstream Quake source at build time. Each patch is minimal and targeted.

| File | What It Patches | Why It's Needed |
|------|----------------|-----------------|
| `net.h.patch` | Function pointer signatures | WASM enforces strict signatures. Quake's `PollProcedure` used unprototyped callbacks -- crashes in WASM with `function signature mismatch`. |
| `net_main.c.patch` | Poll callback prototypes | Companion to `net.h.patch`. Makes poll callbacks take `void*` argument. |
| `net_dgrm.c.patch` | Datagram handling | Three fixes: (1) bounds check on fragmented message accumulation (prevents memory corruption), (2) browser yield during connect loops (Asyncify `emscripten_sleep` so WebSocket `onmessage` can fire), (3) ignore non-ACCEPT/REJECT control packets during connect (prevents `slist` replies from breaking handshake). |
| `chase.c.patch` | Chase camera | Minor fix for WASM compatibility. |
| `cl_parse.c.patch` | Client parsing | Adds precache prefetch: client fetches assets during map load for faster level transitions. |
| `common.c.patch` | Common utilities | Fixes for WASM environment compatibility. |
| `host.c.patch` | Host layer | Dynamic mod context support for game directory switching. |

### Build

| File | Purpose |
|------|---------|
| `Makefile.emscripten` | Emscripten build configuration. Source file list, compiler flags (`-sMAX_WEBGL_VERSION=2`, `-sASYNCIFY`), linker settings. No SDL references. |
| `shell.html` | HTML template for the browser client. Bootstrap logic: fetches `/data-manifest/id1`, creates virtual filesystem, downloads game data, starts Quake. Contains canvas element, loading UI, query parameter handling (`?ws=`, `?-nosound`, etc.). |

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
cp /path/to/client/*.c /path/to/client/*.h /path/to/client/*.js .
cp /path/to/client/Makefile.emscripten /path/to/client/shell.html .

# 4. Build
make -f Makefile.emscripten
```

Output: `index.html`, `index.js`, `index.wasm`

The automated build scripts (`build/build-client.sh`) handle all of this.

## Key Design Choices

- **No SDL**: Direct browser APIs only. Smaller binary, fewer layers, easier debugging.
- **GPU palette rendering**: 8-bit framebuffer uploaded as R8 texture, palette lookup in fragment shader. Eliminates per-frame CPU conversion.
- **Event-driven input**: Emscripten HTML5 callbacks fire `Key_Event()` directly. No polling loop.
- **WebSocket URL from location**: `ws(s)://<window.location.host>/ws` by default, overridable with `?ws=<url>`.
- **Vanilla UI preserved**: No custom menus. `connect`, `slist`, standard Quake console.
