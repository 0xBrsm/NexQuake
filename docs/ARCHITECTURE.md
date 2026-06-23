# Architecture

This document covers the technical choices and philosophy behind NexQuake. It is written for contributors, curious engineers, and anyone who wants to understand how a 1996 game engine runs in a 2026 browser.

## Philosophy

NexQuake follows three principles:

1. **Don't fight the browser.** The client targets one platform: the browser. Since there is no need for cross-platform portability, NexQuake uses WebGL2, WebAudio, and HTML5 input directly rather than going through an abstraction layer like SDL.

2. **Don't fight Quake.** The goal is to play Quake as it was built; quirks, rough edges, and all. The engine has known bugs and idiosyncrasies; we preserve original behavior rather than fixing it for convenience. If default Quake does something a certain way, we (generally) keep it.

3. **Don't fight the network.** NetQuake's UDP implementation is essential to the feel of the game. The Nexus tunnel carries raw datagrams with a minimal routing header and stays out of the way.

## GPU-Side Palette Conversion

Quake renders to an 8-bit indexed framebuffer where each pixel is an index into a 256-color palette. The standard approach in most ports is to convert this on the CPU: loop over every pixel, look up the RGBA color, and upload the expanded texture. This works well on modern hardware, but NexQuake takes a simpler path by moving the conversion to the GPU.

```
# Standard approach:
upload framebuffer[] as GL_RGBA texture    // 4 bytes per pixel, CPU-expanded

# NexQuake approach:
upload framebuffer[] as GL_R8 texture      // 1 byte per pixel, raw indexed
upload palette[] as GL_RGBA8 texture       // 256 entries, updates rarely
fragment shader: color = palette[framebuffer_sample * 255]
```

The fragment shader does the palette lookup in parallel across all pixels. Per-frame CPU work drops to zero, and the texture upload shrinks by 4x. The palette texture only updates on damage flash or item pickup. The main benefit is simplicity; one texture upload, one shader, no intermediate buffer. Fewer moving parts, and the rendering stays closer to what Quake actually produces.

### Implementation

`vid_wasm.c` creates a WebGL2 context and two textures:

- **Framebuffer texture**: `GL_R8` at render resolution. Updated every frame via `glTexSubImage2D` with Quake's raw pixel buffer.
- **Palette texture**: `GL_RGBA8` at 256x1. Updated only on `VID_SetPalette()` calls.

The fullscreen draw uses `gl_VertexID` to generate a triangle that covers the viewport, so no vertex buffer is needed. The fragment shader samples the R8 texture, multiplies by 255 to get the palette index, and fetches the color with `texelFetch`.

This technique applies to any indexed-color engine ported to WebGL.

## Direct Emscripten Platform Layer

The original [Quake-WASM](https://github.com/GMH-Code/Quake-WASM) port by Gregory Maynard-Hoare proved that Quake C could run in a browser. That port used Emscripten's SDL2 shim (`-sUSE_SDL=2`), a solid approach that leverages SDL's well-tested abstractions. NexQuake replaced the SDL layer with direct Emscripten API calls in v0.7.0 to reduce the distance between the engine and the browser, cutting binary size and simplifying debugging.

### What Changed

| Component | Before (SDL2) | After (Direct) |
|-----------|---------------|----------------|
| Video | SDL_CreateWindow + SDL_CreateTexture | emscripten_webgl_create_context + WebGL2 |
| Input | SDL_PollEvent loop | `emscripten_set_keydown/keyup/mousemove` callbacks for keyboard and mouse; HTML5 touch callbacks (`touchstart/move/end`) for touch; Gamepad API polled per frame |
| Audio | SDL_OpenAudioDevice | EM_JS ScriptProcessorNode + WASM heap ring buffer |
| Binary | ~200KB SDL shim in WASM | Zero middleware |

### Input: Event-Driven vs Polling

SDL uses a polling model: call `SDL_PollEvent()` in the main loop and process the queue. Emscripten's HTML5 API is event-driven: register callbacks and get called directly on keydown/keyup/mousemove/wheel.

NexQuake registers callbacks that fire `Key_Event()` immediately, making `Sys_SendKeyEvents()` a no-op. Key mapping uses a 256-byte lookup table (DOM `keyCode` to Quake key code) instead of the original switch statement.

Pointer lock uses `emscripten_request_pointerlock()` on first canvas click. Mouse movement reports `movementX`/`movementY` deltas for mouselook.

### Touch Input

Touch registers three native callbacks (`touchstart`, `touchmove`, `touchend`) on the document — the same event-driven pattern as keyboard and mouse. The screen is split into two zones at 40% of the viewport width:

- **Left zone** — virtual joystick. A `touch_move` slot records the drag origin and current position; displacement is clamped to `TOUCH_MOVE_RADIUS` (60 px) and normalized to `[-1, 1]` axis values that drive `cmd->forwardmove`/`sidemove` via `analog_speed`.
- **Right zone** — swipe-look accumulator. Raw pixel deltas accumulate into `touch_look_x`/`touch_look_y` and are applied in `IN_Move()` the same way mouse deltas are, scaled by `touch_sensitivity`.

Tap detection fires `touch_tap1` or `touch_tap2` key events for touches that end within `touch_tap_ms` (default 220 ms) and `touch_tap_px` (default 20 px) of their start point. Nine bindable button slots (`touch1`–`touch9`) are tracked by DOM element identity, passed from JavaScript when a finger lands on a button; the C layer calls `Key_Event()` on `touchstart`/`touchend` for each slot.

The joystick ring visual and overlay hide timer are driven by `EM_JS` calls into the shell JavaScript layer (`js_joy_show`, `js_joy_move`, `js_joy_hide`, `js_set_touch_active`), keeping visual state out of C.

Text entry for touch is also driven from C: when Quake enters a text mode (console, menu field, or `messagemode`), `js_request_text_entry()` opens a DOM text bar. Input events feed back through `NQWasm_TextInputKey` so the engine receives the same key stream it would from a hardware keyboard. Closing the bar via `Esc`/back triggers `js_close_text_entry()` to keep the WASM text state and DOM in sync.

### Gamepad Input

Gamepad input uses a polling model — the inverse of the event-driven keyboard and mouse. `IN_PollGamepads()` runs each frame from `IN_Commands()` and calls `emscripten_get_gamepad_status()` for each connected device. The W3C standard mapping is assumed: 16 buttons (indices 0–15) and 4 axes.

Buttons transition through `joy_handle_button()`, which fires `Key_Event()` on state change, matching keyboard behavior. Analog triggers (LT/RT) use a threshold (`JOY_TRIGGER_THRESH = 0.5`) to convert their analog value to a digital key press. Left stick drives movement (`joy_move_x`/`joy_move_y`); right stick accumulates into `joy_look_x`/`joy_look_y`, which `IN_Move()` applies as look deltas scaled by `joy_sensitivity`. Only the first connected gamepad is used. On disconnect, all in-flight button presses are released before state is cleared.

### Per-Device Input Profiles

Sensitivity, lookspring, lookstrafe, and invert-pitch all need independent tuning per device. Rather than separate menu pages, `INPUT_PROFILE_PICK(mouse, touch, joy)` selects the right cvar at call time based on active device (`joy_connected` takes priority over `touch_active`):

```c
#define INPUT_PROFILE_PICK(mouse, touch, joy) \
    (joy_connected ? (joy) : (touch_active ? (touch) : (mouse)))
```

The Options menu calls `IN_SensitivityCvar()`, `IN_LookspringCvar()`, etc. — wrappers that apply this macro — so the same menu slot shows the correct label and reads/writes the correct cvar for whichever device is active. Plugging in a gamepad mid-session transparently redirects the menu without any restart.

### Audio: WebAudio Ring Buffer

Quake's mixer writes interleaved 16-bit stereo samples to a DMA buffer. NexQuake exposes this buffer to JavaScript:

```c
static int16_t dma_buffer[DMA_SAMPLES];  // Quake writes here
static int audio_read_cursor;                   // JS advances this
```

A `ScriptProcessorNode` callback reads from the WASM heap on demand:

```js
node.onaudioprocess = function(e) {
    // Read int16 pairs from WASM heap, convert to float, output to speakers
};
```

No locks are needed because the callback runs on the main thread in browsers, and Quake always writes ahead of the read cursor. The `ScriptProcessorNode` callback block size is 512 frames for low latency. The underlying DMA ring buffer is 16384 samples.

`SNDDMA_GetDMAPos()` returns the JS read cursor. `SNDDMA_Submit()` is a no-op. The standard Quake mixer (`S_PaintChannels`) works unchanged.

AudioContext auto-resume on first user gesture handles browser autoplay policies.

### CD Audio

Quake's CD audio system originally played music tracks from a physical CD-ROM drive. NexQuake replaces this with digital audio streaming from a server-side directory (`CD_DIR`).

Nexus scans `CD_DIR` for `.ogg` and `.mp3` files and includes the resulting track index in the `/gamedir` quickstart payload. CD audio bytes are then fetched through hash-addressed `/pak/<hash>` URLs (backed internally by the CD stream resolver). Track numbers are extracted from filenames (e.g., `02-intro.ogg` is track 2).

`cd_wasm.c` replaces the original `cd_audio.c` with Emscripten `EM_JS` bindings that drive an HTML5 `<audio>` element. The JavaScript layer resolves tracks through a two-tier system: first checking user-uploaded files in the Emscripten virtual filesystem, then falling back to the server manifest. Playback respects the `bgmvolume` cvar and handles browser autoplay policies with resume-on-user-gesture logic.

### Video Modes and FOV Scaling

The video mode list is built once at startup in `build_modelist()` from two aspect ratios — fixed 4:3 and the detected viewport aspect — each at three scale factors (25%, 50%, 100%), producing up to six entries. Duplicates are deduplicated. The first group (fixed 4:3) appear as **Classic Modes** in the Video Modes menu; the second group (viewport-matched) appear as **Fullscreen Modes**.

When `VID_SetMode()` changes resolution, `update_mode_fov()` adjusts the `fov` cvar to preserve the vertical field of view:

```
new_fov = atan(tan(old_fov / 2) × (new_aspect / old_aspect)) × 2
```

This keeps the vertical play area constant — switching to a widescreen viewport widens the horizontal view without compressing the vertical. The canvas CSS `--nq-ar` property is updated via `js_update_canvas_ar()` so the browser letterboxes or pillarboxes the canvas when the render aspect does not match the display.

## Stateless Tunnel

Game proxies typically parse packets, maintain session state, and layer their own protocol on top. NexQuake takes a simpler approach: the tunnel forwards raw datagrams without inspecting them.

### Frame Format

Every tunnel frame (WebSocket binary message or WebTransport datagram) contains:

```
[udp_port : u16 big-endian] [raw NetQuake datagram]
```

Two bytes of routing header, then the exact bytes that would go over UDP. Nexus reads the destination port, forwards the datagram to `127.0.0.1:<port>`, and sends replies back with the server source port in the same 2-byte header slot.

### Why This Works

NetQuake's connection handshake switches ports mid-connect: the server replies to a connect request with a per-client "game port." Traditional proxies need to track this state. NexQuake avoids it by including the port in every frame, so the client tells Nexus where to send and Nexus tells the client where the reply came from.

Because Nexus never parses the datagram payload, it has no knowledge of whether a packet is a connect request, a position update, or a chat message. This keeps the relay free of game-specific bugs, decoupled from protocol versions, and small.

### Multi-Server Routing

Server selection is port-based. `connect <port>` connects directly to that instance. For autoscaled servers (`-port 0` lines) there is no proxy port — load balancing happens at slist time.

When a client sends a `CCREQ_SERVER_INFO` browse request, `snapshotForSlist()` calls `pickServerInstanceLocked()` for each server. This selects the least-loaded routable instance (fill ratio `players/maxplayers`, round-robin tie-break) and puts that instance's actual listen port in the slist entry. The aggregate users/maxusers/instances reflect the whole server, but the port the client receives is a real instance port it connects to directly.

Instance selection order:

1. Prefer `active` instances with free slots.
2. If all `active` instances are full, include full ones rather than failing.
3. If no `active` instance exists, fall back to `draining` instances — free slots first, then full.
4. If no routable instance exists, the server is omitted from the slist response.

Each slist poll may return a different instance port for the same server as load shifts. There is no session affinity; the slist is the load balancer.

### Scaling Lifecycle and Autoscaling

Each `-port 0` startup line becomes a managed server with lifecycle states per instance:

- `warming`: process started but not yet seen in `CCREP_SERVER_INFO`.
- `active`: routable; eligible for slist instance selection.
- `draining`: still routable as fallback, but no longer preferred for new joins.
- `terminating`: selected for despawn; removed once stop completes.

Server reconcile runs in two loops:

- Event-driven: every server-info update for a scaled instance triggers reconcile.
- Heartbeat: every server-info poll tick (`500ms`) reconciles all servers, so autoscaling still progresses even when no new server-info packets arrive.

Scale-up and scale-down policy:

- Headroom target is `max(4, ceil(joinRPS * 12s * 1.5))`, where `joinRPS` is the rate of slist poll hits on that server over a `30s` sliding window.
- Scale-up happens when current free slots fall below target headroom, no scale-up is already in flight, cooldown (`30s`) has elapsed, and running instances are below `SV_MAX_INSTANCES`.
- Idle instances only move to `draining` when at least two active routable instances remain and enough headroom would still exist after draining.
- While scale-up is in flight, additional active instances are not moved to `draining`.
- Draining instances with zero players for 6 reconcile polls are despawned.
- A hard safety guard prevents despawning the last running instance of a server.

Control/broadcast traffic uses `udp_port = 0`. For `slist`, Nexus detects `CCREQ_SERVER_INFO` and replies with aggregated `CCREP_SERVER_INFO` data built from its polled cache:

- one row per autoscaled server (a load-balanced instance port, aggregate users/maxusers, instance count),
- plus non-scaled servers as direct entries.

This replaces NetQuake's UDP broadcast, which never worked well and doesn't work across loopback addresses on Linux anyway.

### Server List Aggregation

Standard NetQuake server browsing (`slist`) sends a UDP broadcast `CCREQ_SERVER_INFO` and waits for individual server replies. NexQuake runs on loopback, where broadcast never reaches game servers, so Nexus intercepts the request and replies with a single aggregated `CCREP_SERVER_INFO` packet containing the full server list.

`net_slist.c` parses this batch response: a count byte followed by per-server fields (port, name, map, gamedir, users, maxusers, instance count). An instance count of `0` means "display as a normal single-instance row"; positive values mean an autoscaled server row and are shown in the users column. Parsed entries are written directly into `hostcache[]` and the `slist_agg_done` flag short-circuits the normal poll loop so the client does not wait for individual server replies that will never come. A dynamic column layout adapts the console output width to the terminal, adding a gamedir column so players can see which mod each server runs.

## Port-Only Relay Addressing

NexQuake routes exclusively by UDP port:

- Browser frames carry destination port in the 2-byte tunnel header
- Nexus forwards to `127.0.0.1:<port>`
- Server addresses are stored in the client hostcache as port-only
- Players connect by hostname or port

### Virtual qsockaddr Synthesis

Quake's networking code passes `qsockaddr` structures through every address API (`GetSocketAddr`, `GetNameFromAddr`, `GetAddrFromName`, `AddrCompare`, etc.). The browser landriver shell in `net_wasm.c` delegates this to the NexQuake channel adapter in `net_nqchan.c`, which synthesizes these structures so the rest of the engine works without modification:

#### Server addresses: 
Built from listen port number and unique client connection port number. The IP portion is generated from the 16 bytes of the server's listen port by assigning each of the two bytes to the last two octets of an IP address; so a server listening on port `26000` would be assigned IP `13.37.101.144` on the client. This ensures that even after a client has connected to a game server and been redirected by that server to a unique port, the client can continue to "know" which server in its hostcache it's connected to, enabling features like rcon and join codes. However, only the port field matters for routing and when Quake asks for an address string (e.g. for the server list or the `connect` status line), the driver returns just the port number.

#### Local client addresses:
To keep stock Quake server behavior, Nexus assigns each tunnel client a stable virtual loopback "NQIP" and binds that client's UDP relay socket to it. NQIPs are deterministic `127.A.B.C` addresses allocated from a normalized client source key (for example trusted header IP or remote address), so reconnects from the same source key keep a stable identity while still avoiding collisions. This way, the server sees each client as a (practically) unique IP address, so features like per-IP bans, per-IP rate limiting, and the status command's address display all work unmodified. On session open, Nexus sends a small control frame so the browser client can set its local NQIP for the NQ landriver. The NQ landriver stores this and returns it when Quake calls `GetSocketAddr` on the local socket. This gives each browser client a stable identity that the server sees as a unique IP. Routing still remains port-only on the tunnel itself.

#### Address comparison:
`AddrCompare` compares the full synthesized `qsockaddr`, which lets the engine distinguish between different servers (by port) and different clients (by NQIP). This keeps all address handling inside the NQ landriver. The rest of the Quake networking stack, the datagram layer, the connection logic, the server browser, sees well-formed `qsockaddr` values and never knows it's running over a browser transport (WebSocket or WebTransport).

## Build Architecture

### Patch-Based Overlay

NexQuake does not fork Quake. The upstream `WinQuake/` source is fetched into `build/tmp/WinQuake/` as an immutable cache and never modified. At build time:

1. Copy the canonical source to a working directory
2. Apply `.patch` files from `client/` and `server/`
3. Copy overlay `.c`/`.h` files (new code that does not exist upstream)
4. Compile

This keeps project changes auditable. Every modification is a short patch file, scoped only to its impact. New functionality is a new file. The cached Quake source is always canonical.

### Multi-Stage Docker

The production Dockerfile builds all three components in isolated stages:

```
Stage 1: Go builder    -> Nexus binary (CGO_ENABLED=0, static)
Stage 2: C builder     -> nqserver (64-bit by default)
Stage 3: WASM builder  -> index.html + shell.css + favicon/manifest/icons + index.js + index.wasm
Stage 4: Runtime       -> chainguard/wolfi-base + all artifacts
```

The final ~10 MB image contains only the runtime: no compilers, no source code, no build tools.

### 64-Bit Server with QuakeC Patches

The dedicated server defaults to 64-bit on standard architectures (x86_64, arm64). QuakeC stores string references as 32-bit offsets (`string_t`), so 64-bit builds require patches in `server/64bit/` to widen the affected pointer-subtraction paths. These patches are applied automatically. 32-bit builds (armhf, i386) skip them and work without modification.

## Game Data Pipeline

### Layered Data Directories

Game data follows Quake's native directory convention with an added layer system:

```
game/<mod>/common/     Shared between client and server
game/<mod>/client/     Client-only overrides
game/<mod>/server/     Server-only overrides
```

Nexus builds a per-target manifest:
- Client manifest: `common` + `client` (client overrides common)
- Server runtime dir: `common` + `server` (server overrides common)

Within each layer, Quake's standard precedence applies: loose files override PAK members, and higher-numbered PAK files override lower ones.

### PAK Streaming

The browser client never downloads full PAK files. Nexus indexes PAK headers on startup and serves files through per-client hash-addressed URLs (`/pak/<hash>`). Internally, hash entries can resolve to loose files or offsets inside PAK archives, so when the client requests a texture, model, or sound, Nexus can seek into a PAK, read just that entry, and stream it directly. This means:

- No multi-megabyte PAK transfers to the browser
- No server-side extraction or disk duplication
- Files are served with correct HTTP caching headers
- Works with any PAK file (shareware, full, mods)

### Quickstart

On first run, Nexus loads the quickstart catalog from `${CFG_DIR}/game.json`. If `${GAME_DIR}/servers.ini` is missing, Nexus creates it from `${CFG_DIR}/servers.ini` and adds valid `QUICKSTART` game entries (`ffa` by default). Nexus then builds the effective install set from `servers.ini -game` values plus catalog entries marked with `base` (for example `id1`) and installs missing layer data for each selected entry (`common`, `client`, `server`). The built-in `id1` base entry installs Quake 1.06 shareware plus a NexQuake version of LibreQuake's `pak1.pak`, enough to boot the engine and play single-player. Users can still provide their own PAK files for full retail assets.

Shareware extraction is handled by the [`quake106` package](../nexus/quake106/README.md), which extracts `pak0.pak` directly from the original id Software shareware distribution with SHA256 verification at every stage.

All of this allows a fresh NexQuake instance to be a multiplayer, multi-server Quake experience with a single `docker compose up`.

### Client Asset Prefetch

Without prefetch, connecting to a server triggers sequential downloads of every model and sound in the map's precache list — dozens of assets fetched one at a time over HTTP, producing multi-second connect times on a cold cache.

`CL_Prefetch()` is called from `CL_ParseServerInfo()` with the full model and sound precache lists. It enqueues all paths into JavaScript via `EM_ASM` calls to `nexquakePrefetchEnqueue()`, then kicks off concurrent fetching (`nexquakePrefetchStart()`). The C side blocks with an `emscripten_sleep(1)` loop for up to 30 seconds waiting for `nexquakePrefetchBusy` to clear. JavaScript fetches up to `CL_CONCURRENCY` (default 16) assets in parallel; connect time drops to roughly the cost of the single slowest asset. Assets that fail during prefetch are logged and fall back to lazy per-file fetch during gameplay. On non-WASM builds the function is a no-op stub.

## What's Next

- **FTEQW client support**: The [FTEQW project](https://github.com/fte-team/fteqw) is a fantastic example of community driven development. FTEQW already has a WASM version of their client. Connecting it to Nexus to allow a more modern play experience for those who want it should be very doable.
- **AudioWorklet**: ScriptProcessorNode is deprecated, so at some point an AudioWorklet refactor will be required. But hopefully not soon.

(WebTransport, previously listed here, shipped: with `EXTERNAL_URL` set the tunnel carries unreliable QUIC datagrams — UDP-equivalent semantics with no head-of-line blocking — and falls back to WebSocket everywhere else. See the WebTransport section in [ENVIRONMENT.md](ENVIRONMENT.md).)

## Contributing

NexQuake is GPL-2.0-or-later. Contributions are welcome. The best way to get started:

1. Read this document and the [Documentation Index](./README.md)
2. Run the Docker quick start
3. Look at the patch files in `client/` and `server/` to understand the scope of changes
4. Check `nexus/` for Go contributions (well-tested, minimal dependencies)

The project values simplicity, authenticity, and minimal upstream diff.
