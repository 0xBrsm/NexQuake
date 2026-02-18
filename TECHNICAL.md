# Technical Deep Dive

This document covers the technical choices and philosophy behind NexQuake. It is written for contributors, curious engineers, and anyone who wants to understand how a 1996 game engine runs in a 2026 browser.

## Philosophy

NexQuake follows three principles:

1. **Don't fight the browser.** The client targets one platform: the browser. Since there is no need for cross-platform portability, NexQuake uses WebGL2, WebAudio, and HTML5 input directly rather than going through an abstraction layer like SDL.

2. **Don't fight Quake.** The goal is to play Quake as it was built, quirks, rough edges, and all. The engine has known bugs and idiosyncrasies; we preserve original behavior rather than fixing it for convenience. Patches over rewrites. Overlays over forks. If default Quake does something a certain way, we (generally) keep it.

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

The original [Quake-WASM](https://github.com/GMH-Code/Quake-WASM) port by Gregory Maynard-Hoare proved that Quake could run in a browser. That port used Emscripten's SDL2 shim (`-sUSE_SDL=2`), a solid approach that leverages SDL's well-tested abstractions. NexQuake replaced the SDL layer with direct Emscripten API calls in v0.7.0 to reduce the distance between the engine and the browser, cutting binary size and simplifying debugging.

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
static int audio_read_cursor;                     // JS advances this
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

## Stateless WebSocket Tunnel

Game proxies typically parse packets, maintain session state, and layer their own protocol on top. NexQuake takes a simpler approach: the tunnel forwards raw datagrams without inspecting them.

### Frame Format

Every WebSocket binary frame contains:

```
[udp_port : u16 big-endian] [raw NetQuake datagram]
```

Two bytes of routing header, then the exact bytes that would go over UDP. Nexus reads the destination port, forwards the datagram to `${NQSERVER_IP}:<port>` (default `127.13.37.9`), and sends replies back with the server source port in the same 2-byte header slot.

### Why This Works

NetQuake's connection handshake switches ports mid-connect: the server replies to a connect request with a per-client "game port." Traditional proxies need to track this state. NexQuake avoids it by including the port in every frame, so the client tells Nexus where to send and Nexus tells the client where the reply came from.

Because Nexus never parses the datagram payload, it has no knowledge of whether a packet is a connect request, a position update, or a chat message. This keeps the relay free of game-specific bugs, decoupled from protocol versions, and small.

### Multi-Server Routing

Server selection is port-based: managed id `N` listens on loopback `:(26000+N)` (default = `:26000`).

Control/broadcast traffic uses `udp_port = 0`. For `slist`, Nexus detects `CCREQ_SERVER_INFO` and replies with aggregated `CCREP_SERVER_INFO` data built from its polled server cache. This replaces NetQuake's UDP broadcast, which never worked well and doesn't work across loopback addresses on Linux anyway.

## Port-Only Relay Addressing

NexQuake routes exclusively by UDP port:

- Browser frames carry destination port in the 2-byte WS header
- Nexus forwards to `${NQSERVER_IP}:<port>` (default `127.13.37.9`)
- Client-facing Quake addresses are virtualized as `0.0.0.0:<port>` (routing keys only on the port)

To keep stock Quake behavior and server-side IP semantics, nexus still assigns each WebSocket client a stable virtual loopback IP (`127.x.y.z`) and binds that client's UDP relay socket to it. On WebSocket open, nexus sends a small control frame so the browser client can set its local NQIP for Quake address APIs. Routing still remains port-only.

## Build Architecture

### Patch-Based Overlay

NexQuake does not fork Quake. The upstream `id-Software/Quake` repository is checked out pristine into `build/tmp/WinQuake/` and never modified. At build time:

1. Copy the pristine source to a working directory
2. Apply `.patch` files from `client/` or `server/`
3. Copy overlay `.c`/`.h` files (new code that does not exist upstream)
4. Compile

This keeps upstream changes auditable. Every modification is a patch file. New functionality is a new file. The Quake source in git is always pristine.

### Multi-Stage Docker

The production Dockerfile builds all three components in isolated stages:

```
Stage 1: Go builder    -> Nexus binary (CGO_ENABLED=0, static)
Stage 2: C builder     -> nqserver (32-bit by default)
Stage 3: WASM builder  -> index.html + shell.css + index.js + index.wasm
Stage 4: Runtime       -> chainguard/wolfi-base + all artifacts
```

The final image contains only the runtime: no compilers, no source code, no build tools.

### 32-Bit Server Default

The dedicated server builds as 32-bit by default, even on 64-bit hosts. This avoids QuakeC `string_t` pointer-subtraction crashes (`sv.name - pr_strings` truncation on 64-bit). Optional 64-bit patches exist in `server/64bit/` for hosts that need them.

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

The browser client never downloads full PAK files. Nexus indexes PAK headers on startup and serves files through hash-addressed URLs (`/nq/<hash>`). Internally, hash entries can resolve to loose files or offsets inside PAK archives, so when the client requests a texture, model, or sound, Nexus can seek into a PAK, read just that entry, and stream it directly. This means:

- No multi-megabyte PAK transfers to the browser
- No server-side extraction or disk duplication
- Files are served with correct HTTP caching headers
- Works with any PAK file (shareware, full, mods)

### Quickstart Bootstrap and the `quake106` Package

On first run, if `${GAME_DIR}` is writable, Nexus downloads game data from a manifest file (e.g., `minimal.json`). The default manifest bootstraps Quake 1.06 shareware and a NexQuake version of LibreQuake's pak1.pak, which is enough to boot the engine and play single-player. Users can provide their own PAK files for the full game.

The `nexus/quake106/` package is a standalone Go package that extracts `pak0.pak` directly from the original id Software FTP Quake 1.06 shareware distribution (`quake106.zip`). The original shareware archive uses a multi-part LHA-compressed installer format from 1996, so this package implements LZH (LH5) decompression from scratch in pure Go (no cgo, no external binaries, no shell calls) to decompress the `resource.1` segment and extract the PAK file and license text.

Every step is SHA256-verified; the zip file itself, the `resource.1` entry, and the extracted `pak0.pak`. If any hash does not match the known-good value, extraction fails. This guarantees that Nexus only serves authentic, unmodified shareware data, which is important both for correctness (the engine expects specific file layouts) and for conformance with id Software's shareware license, which permits redistribution of the original, unmodified archive only.

All of this allows a fresh NexQuake instance to be a multiplayer, multi-server Quake experience with a single `docker compose up`.

## Environment Configuration

NexQuake is configured entirely through environment variables, with no config files or command-line flags for Nexus. This keeps Docker deployment straightforward:

| Variable | Default | Purpose |
|----------|---------|---------|
| `HTTP_PORT` | `1337` | Listen port |
| `GAME_DIR` | `/app/game` | Game data root |
| `LOGS_DIR` | `/app/logs` | Server state (read-write) |
| `QUICKSTART` | `minimal` | Bootstrap manifest |
| `AUTH_ISSUER` | (unset) | OIDC provider URL for admin auth |
| `AUTH_RCON_PASSWORD` | (unset) | Shared secret for in-game `rcon_password` validation |
| `LOG_LEVEL` | `info` | Logging verbosity |

## What's Next

- **WebTransport**: Replace WebSocket with WebTransport for lower latency. WebTransport provides unreliable QUIC datagrams in the browser, the closest thing to native UDP available in a web context. Quake's netcode was designed for UDP: fire-and-forget datagrams, no head-of-line blocking, no retransmission of stale game state. WebSocket forces TCP semantics onto that model (ordered, reliable delivery), which adds latency and can cause stalls on packet loss. WebTransport would let the tunnel behave the way Quake expects, and the tunnel architecture makes this a transport swap with no game-side changes.

## Contributing

NexQuake is GPL-2.0-or-later. Contributions are welcome. The best way to get started:

1. Read this document and the [README](./README.md)
2. Run the Docker quick start
3. Look at the patch files in `client/` and `server/` to understand the scope of changes
4. Check `nexus/` for Go contributions (well-tested, standard library only)

The project values simplicity, authenticity, and minimal upstream diff.

