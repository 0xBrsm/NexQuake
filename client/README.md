# WASM Client Source

Modifications to id Software's Quake to compile for WebAssembly.

**Original WASM port**: Gregory Maynard-Hoare ([GMH-Code/Quake-WASM](https://github.com/GMH-Code/Quake-WASM))
**WebSocket multiplayer**: initialed85 (added WebSocket networking layer)
**This fork**: Docker containerization and build improvements

## Files

**SDL Platform Layer** (Emscripten → browser APIs):
- `sys_sdl.c` - System layer (main loop, file I/O, timing)
- `vid_sdl.c` - Video/rendering
- `snd_sdl.c` - Audio

**WebSocket Networking**:
- `net_websocket.c/h` - WebSocket driver for multiplayer
- `net_bsd.c` - Driver tables wired for WebSocket transport

**Build System**:
- `Makefile.emscripten` - Emscripten build configuration
- `shell.html` - HTML template for browser client

## How It Works

These files overlay the id Software Quake GPL source during build:
1. Clone `id-Software/Quake` → `build/`
2. Copy selected `src/client/*` files → `build/`
3. Build with Emscripten: `make -f Makefile.emscripten`

Output: `index.html`, `index.js`, `index.wasm` (and optionally `index.data` if the build uses Emscripten file preloading)

## Key Modifications

- **SDL2 platform layer**: Emscripten maps SDL calls to Canvas/WebAudio (software renderer; no WebGL)
- **WebSocket networking**: Replaces UDP sockets for browser compatibility
- **WebSocket URL**: Derived from `window.location` by default (`ws(s)://<host>/ws`), with optional override via `?ws=...`
- **Emscripten integration**: Proper main loop and filesystem for browser environment

## Differences vs `initialed85/Quake-WASM` (Intentional)

This repo uses upstream `id-Software/Quake` as the base source during CI builds. Some `initialed85/Quake-WASM` files assume a different source layout (for example `--preload-file=id1` and direct inclusion of POSIX networking headers). To keep the overlay minimal and compatible with the upstream Quake tree, we intentionally diverge in a few places:

- `src/client/shell.html`: fetches `/data-manifest/id1` and mirrors `data/id1` into `/id1` in the virtual filesystem (lowercased paths) before Quake starts, instead of relying on `index.data` created by `--preload-file=id1`.
- `src/client/Makefile.emscripten`: drops `--preload-file=id1` and does not compile `net_udp.c` (browser networking uses the WebSocket landriver only).
- `src/client/sys_sdl.c`: fixes `Sys_FileWrite()` to use `fwrite()` (upstream `initialed85/Quake-WASM` uses `fread()` in this function).
- `src/client/net_bsd.c`: only registers the `WebSocket` landriver (no UDP landriver).
- `src/client/net_websocket.c`: derived from `initialed85` but adjusted to:
  - avoid `<netinet/in.h>` under Emscripten (conflicts with upstream Quake `net.h` prototypes),
  - derive the WebSocket URL from `window.location` (and support `?ws=` override),
  - not send the upstream “UUID hello” frame (nexus is a raw datagram tunnel).
