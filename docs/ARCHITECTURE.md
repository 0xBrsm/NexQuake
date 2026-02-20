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
| Input | SDL_PollEvent loop | emscripten_set_keydown/keyup/mousemove callbacks |
| Audio | SDL_OpenAudioDevice | EM_JS ScriptProcessorNode + WASM heap ring buffer |
| Binary | ~200KB SDL shim in WASM | Zero middleware |

### Input: Event-Driven vs Polling

SDL uses a polling model: call `SDL_PollEvent()` in the main loop and process the queue. Emscripten's HTML5 API is event-driven: register callbacks and get called directly on keydown/keyup/mousemove/wheel.

NexQuake registers callbacks that fire `Key_Event()` immediately, making `Sys_SendKeyEvents()` a no-op. Key mapping uses a 256-byte lookup table (DOM `keyCode` to Quake key code) instead of the original switch statement.

Pointer lock uses `emscripten_request_pointerlock()` on first canvas click. Mouse movement reports `movementX`/`movementY` deltas for mouselook.

### Audio: WebAudio Ring Buffer

Quake's mixer writes interleaved 16-bit stereo samples to a DMA buffer. NexQuake exposes this buffer to JavaScript:

```c
static int16_t dma_buffer[DMA_BUFFER_SAMPLES];  // Quake writes here
static int audio_read_cursor;                   // JS advances this
```

A `ScriptProcessorNode` callback reads from the WASM heap on demand:

```js
node.onaudioprocess = function(e) {
    // Read int16 pairs from WASM heap, convert to float, output to speakers
};
```

No locks are needed because the callback runs on the main thread in browsers, and Quake always writes ahead of the read cursor. Buffer size is 512 frames (down from 2048) for low latency.

`SNDDMA_GetDMAPos()` returns the JS read cursor. `SNDDMA_Submit()` is a no-op. The standard Quake mixer (`S_PaintChannels`) works unchanged.

AudioContext auto-resume on first user gesture handles browser autoplay policies.

### CD Audio

Quake's CD audio system originally played music tracks from a physical CD-ROM drive. NexQuake replaces this with digital audio streaming from a server-side directory (`CD_DIR`).

Nexus scans `CD_DIR` for `.ogg` and `.mp3` files and includes the resulting track index in the `/start` quickstart payload. CD audio bytes are then fetched through hash-addressed `/nq/<hash>` URLs (backed internally by the CD stream resolver). Track numbers are extracted from filenames (e.g., `02-intro.ogg` is track 2).

`cd_wasm.c` replaces the original `cd_audio.c` with Emscripten `EM_JS` bindings that drive an HTML5 `<audio>` element. The JavaScript layer resolves tracks through a two-tier system: first checking user-uploaded files in the Emscripten virtual filesystem, then falling back to the server manifest. Playback respects the `bgmvolume` cvar and handles browser autoplay policies with resume-on-user-gesture logic.

## Stateless WebSocket Tunnel

Game proxies typically parse packets, maintain session state, and layer their own protocol on top. NexQuake takes a simpler approach: the tunnel forwards raw datagrams without inspecting them.

### Frame Format

Every WebSocket binary frame contains:

```
[udp_port : u16 big-endian] [raw NetQuake datagram]
```

Two bytes of routing header, then the exact bytes that would go over UDP. Nexus reads the destination port, forwards the datagram to `127.0.0.1:<port>`, and sends replies back with the server source port in the same 2-byte header slot.

### Why This Works

NetQuake's connection handshake switches ports mid-connect: the server replies to a connect request with a per-client "game port." Traditional proxies need to track this state. NexQuake avoids it by including the port in every frame, so the client tells Nexus where to send and Nexus tells the client where the reply came from.

Because Nexus never parses the datagram payload, it has no knowledge of whether a packet is a connect request, a position update, or a chat message. This keeps the relay free of game-specific bugs, decoupled from protocol versions, and small.

### Multi-Server Routing

Server selection is port-based: each server's listen port is set explicitly in `servers.ini` via the `-port` flag (or assigned by the OS when `-port 0` is used). Nexus discovers the actual port at startup by querying the server console.

Control/broadcast traffic uses `udp_port = 0`. For `slist`, Nexus detects `CCREQ_SERVER_INFO` and replies with aggregated `CCREP_SERVER_INFO` data built from its polled server cache. This replaces NetQuake's UDP broadcast, which never worked well and doesn't work across loopback addresses on Linux anyway.

## Port-Only Relay Addressing

NexQuake routes exclusively by UDP port:

- Browser frames carry destination port in the 2-byte WS header
- Nexus forwards to `127.0.0.1:<port>`
- Server addresses are stored in the client hostcache as port-only
- Players connect by hostname or port

### Virtual qsockaddr Synthesis

Quake's networking code passes `qsockaddr` structures through every address API (`GetSocketAddr`, `GetNameFromAddr`, `GetAddrFromName`, `AddrCompare`, etc.). The vnet driver in `net_ws_vnet.c` synthesizes these structures so the rest of the engine works without modification:

#### Server addresses: 
Built from listen port number and unique client connection port number. The IP portion is generated from the 16 bytes of the server's listen port by assigning each of the two bytes to the last two octets of an IP address; so a server listening on port `26000` would be assigned IP `13.37.101.144` on the client. This ensures that even after a client has connected to a game server and been redirected by that server to a unique port, the client can continue to "know" which server in its hostcache it's connected to, enabling features like rcon. However, only the port field matters for routing and when Quake asks for an address string (e.g. for the server list or the `connect` status line), the driver returns just the port number.

#### Local client addresses: 
To keep stock Quake server behavior, Nexus assigns each WebSocket client a stable virtual loopback "NQIP" and binds that client's UDP relay socket to it. Each NQIP is built by hashing the client source IP's four (IPv4) or 16 (IPv6) bytes down to three bytes and assigning a loopback address like `127.A.B.C`. This way, the server sees each client as a (practically) unique IP address, so features like per-IP bans, per-IP rate limiting, and the status command's address display all work unmodified. On WebSocket open, Nexus sends a small control frame so the browser client can set its local NQIP for the vnet driver. The vnet driver stores this and returns it when Quake calls `GetSocketAddr` on the local socket. This gives each browser client a stable identity that the server sees as a unique IP. Routing still remains port-only on the tunnel itself.

#### Address comparison:
`AddrCompare` compares the full synthesized `qsockaddr`, which lets the engine distinguish between different servers (by port) and different clients (by virtual IP). This keeps all address handling inside the vnet driver. The rest of the Quake networking stack, the datagram layer, the connection logic, the server browser, sees well-formed `qsockaddr` values and never knows it's running over WebSocket.

## Build Architecture

### Patch-Based Overlay

NexQuake does not fork Quake. The upstream `id-Software/Quake` repository is checked out immutably into `build/tmp/WinQuake/` and never modified. At build time:

1. Copy the canonical source to a working directory
2. Apply `.patch` files from `client/` and `server/`
3. Copy overlay `.c`/`.h` files (new code that does not exist upstream)
4. Compile

This keeps project changes auditable. Every modification is a short patch file, scoped only to its impact. New functionality is a new file. The Quake source in git is always canonical.

### Multi-Stage Docker

The production Dockerfile builds all three components in isolated stages:

```
Stage 1: Go builder    -> Nexus binary (CGO_ENABLED=0, static)
Stage 2: C builder     -> nqserver (64-bit by default)
Stage 3: WASM builder  -> index.html + shell.css + index.js + index.wasm
Stage 4: Runtime       -> chainguard/wolfi-base + all artifacts
```

The final image contains only the runtime: no compilers, no source code, no build tools.

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

The browser client never downloads full PAK files. Nexus indexes PAK headers on startup and serves files through per-client hash-addressed URLs (`/nq/<hash>`). Internally, hash entries can resolve to loose files or offsets inside PAK archives, so when the client requests a texture, model, or sound, Nexus can seek into a PAK, read just that entry, and stream it directly. This means:

- No multi-megabyte PAK transfers to the browser
- No server-side extraction or disk duplication
- Files are served with correct HTTP caching headers
- Works with any PAK file (shareware, full, mods)

### Quickstart

On first run, Nexus loads the quickstart catalog from `${CFG_DIR}/game.json`. If `${GAME_DIR}/servers.ini` is missing, Nexus creates it from `${CFG_DIR}/servers.ini` and adds valid `QUICKSTART` game entries (`ffa` by default). Nexus then builds the effective install set from `servers.ini -game` values plus catalog entries marked with `base` (for example `id1`) and installs missing layer data for each selected entry (`common`, `client`, `server`). The built-in `id1` base entry installs Quake 1.06 shareware plus a NexQuake version of LibreQuake's `pak1.pak`, enough to boot the engine and play single-player. Users can still provide their own PAK files for full retail assets.

Shareware extraction is handled by the [`quake106` package](../nexus/quake106/README.md), which extracts `pak0.pak` directly from the original id Software shareware distribution with SHA256 verification at every stage.

All of this allows a fresh NexQuake instance to be a multiplayer, multi-server Quake experience with a single `docker compose up`.

## What's Next

- **FTEQW client support**: The [FTEQW project](https://github.com/fte-team/fteqw) is a fantastic example of community driven development. FTEQW already has a WASM version of their client. Connecting it to Nexus to allow a more modern play experience for those who want it should be very doable.
- **WebTransport**: WebTransport provides unreliable QUIC datagrams in the browser, the closest thing to native UDP available in a web context. Quake's netcode was designed for UDP: fire-and-forget datagrams, no head-of-line blocking, no retransmission of stale game state. WebSocket forces TCP semantics onto that model (ordered, reliable delivery), which adds overhead and unintended statefulness. WebTransport would let the tunnel behave the way Quake expects, and the tunnel architecture makes this a transport swap with only upside gameplay changes. The main impediment is wider support for WebTransport (Hello, Apple!).
- **AudioWorklet**: PostProcessorNode is deprecated, so at some point an AudioWorklet refactor will be required. But hopefully not soon.

## Contributing

NexQuake is GPL-2.0-or-later. Contributions are welcome. The best way to get started:

1. Read this document and the [README](./README.md)
2. Run the Docker quick start
3. Look at the patch files in `client/` and `server/` to understand the scope of changes
4. Check `nexus/` for Go contributions (well-tested, minimal dependencies)

The project values simplicity, authenticity, and minimal upstream diff.
