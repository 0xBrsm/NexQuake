# NexQuake

Quake was released June 22, 1996 and changed video-gaming forever. **NexQuake** was built to recapture that experience without the setup friction. Playing Quake is now as easy as clicking a link, joining a server, and fragging your friends. The same brutal, satisfying gameplay that shipped in 1996, running natively in your browser in 2026.

## What It Is

NexQuake is a WebAssembly port of Quake with browser-native multiplayer. It takes the original id Software engine, compiles it to WASM, and connects players to dedicated servers through a lightweight Go relay that tunnels UDP over WebSocket.

The result is default Quake (software renderer, original physics, original UI) playable in any modern browser with real-time multiplayer. No plugins, no installs, no compromise on the classic experience.

## Why It Exists

Modern source ports do amazing things. They also require downloading binaries, configuring engines, finding servers, and troubleshooting compatibility. The original Quake was pick-up-and-play: you installed it and fragged. NexQuake brings that simplicity back.

The goals are straightforward:

- **Zero friction**: A URL is the entire install process
- **Authentic gameplay**: Same movement, same weapons, same feel
- **Real multiplayer**: Dedicated servers with the standard NetQuake protocol
- **Self-hostable**: Pull one Docker image, open one port, go frag

## How It Works

Three components work together:

```
Browser  --WebSocket-->  Nexus  --UDP-->  NetQuake Server
```

**The WASM Browser Client** is the Quake engine compiled to WebAssembly with Emscripten. It uses WebGL2 for rendering (GPU-side palette conversion from the original 8-bit framebuffer), WebAudio for sound, and HTML5 events for input. Game files are streamed on demand from the server through a virtual filesystem.

**Nexus** is a Go orchestration server that serves client files, serves game data, manages servers, and tunnels multiplayer traffic. Each WebSocket frame carries a small routing header plus a raw NetQuake UDP datagram. Nexus acts as a transparent relay with multi-server routing; it never parses game packets.

**The Dedicated Server** is the original NetQuake engine running headless. Stock protocol, stock gameplay. Nexus spawns and manages server instances, one per game directory (mod).

Players use the standard Quake multiplayer connection experience; either use the Multiplayer menu or from the console (`~`) type `slist` to browse servers and `connect <host>` to join.

## Features

- **Browser-native**: Runs in Chrome, Firefox, Safari, Edge, and any browser with WebGL2
- **Real multiplayer**: Dedicated NetQuake servers with the standard protocol
- **Software renderer**: GPU-accelerated palette conversion of the original 8-bit framebuffer
- **Persistent saves**: Config and saves survive browser sessions via IndexedDB
- **Mod support**: Game directory switching at runtime without page reload
- **Self-contained**: Single Docker image, single port, optional TLS via reverse proxy
- **Auto-bootstrap**: Shareware game data downloads automatically on first run

## Documentation

- **[Quick Start](docs/QUICKSTART.md)**: Get up and running with Docker.
- **[Configuration](docs/CONFIGURATION.md)**: Environment variables and networking setup.
- **[Usage Guide](docs/USAGE.md)**: Managing game data, mods, and servers.
- **[Architecture](docs/ARCHITECTURE.md)**: Technical deep dive into system design.
- **[RCON Commands](docs/RCON.md)**: Server administration reference.
- **[Bug Fixes](bugfix/README.md)**: Security and stability patches for the upstream WinQuake source.

## Project Layout

```
client/           WASM client platform layer and WebSocket networking
server/           Dedicated server Makefile and patches
nexus/            Go relay, file serving, server orchestration
manifests/        Game data bootstrap configs
build/            Build system and upstream checkout scripts
docs/             Detailed documentation and guides
Dockerfile        Multi-stage production image
compose.yml       Docker Compose configuration
ATTRIBUTIONS.md   Source provenance and GPL lineage
```

## License

GPL-2.0-or-later, consistent with the original Quake GPL release.

Game data files (PAK files) have separate licensing. Shareware permits redistribution of official archives only. Full version has restrictive licensing; do not host PAK1.PAK publicly.
