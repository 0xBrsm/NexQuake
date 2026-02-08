# NexQuake

Quake was released June 22, 1996 and changed video-gaming forever. **NexQuake** was built to recapture that experience without the setup friction. Playing Quake is now as easy as clicking a link, joining a server, and fragging your friends. The same brutal, satisfying gameplay that shipped in 1996, running natively in your browser in 2026.

## What It Is

NexQuake is a WebAssembly port of Quake with browser-native multiplayer. It takes the original id Software engine, compiles it to WASM, and connects players to dedicated servers through a lightweight Go relay that tunnels UDP over WebSocket.

The result is vanilla Quake (software renderer, original physics, original UI) playable in any modern browser with real-time multiplayer. No plugins, no installs, no compromise on the classic experience.

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

## Quick Start

### Docker (Recommended)

While you can certainly add and manage your own mods and configs, NexQuake can auto-bootstrap everything needed to play. No game data setup required:

```bash
# Run it
docker compose up --build

# Play
# Open http://localhost:1337
```

On first boot, Nexus downloads a copy of the original Quake 1.06 shareware and a [LibreQuake](https://github.com/lavenderdotpet/LibreQuake)-based freeware PAK1 automatically. This gives you a registered engine with Episode 1 single-player and full multiplayer mod support.

To bootstrap with additional mods (Rocket Arena, CTF, etc.), set the `QUICKSTART` variable:

```bash
QUICKSTART=full docker compose up --build
```

See [manifests/](manifests/) for the full list of available quickstart packages.

### Providing Your Own PAK Files

If you have retail Quake or want to use your own game data:

```bash
# 1. Set up game data
mkdir -p data/id1/common logs
cp /path/to/PAK0.PAK data/id1/common/     # shareware or full
cp /path/to/PAK1.PAK data/id1/common/     # optional, full version

# 2. Run
docker compose up --build
```

- **PAK0.PAK** (shareware): Download the [Quake 1.06 shareware](https://www.gamers.org/pub/idgames/idstuff/quake/quake106.zip) distribution
- **PAK1.PAK** (full): Purchase Quake on Steam or GOG, copy from the install directory
- **MODS**: Browse [NetQuake Mods](https://github.com/brstm/QuakeMods) and grab what you like.

NexQuake supports real-time merging of server-side .ent "entity" files for mods that traditionally required recompiling maps (e.g. CTF). Just drop the .ent files in /<mod>/server/maps/ and play.

### Data Directory Layout

```
data/
  id1/
    common/         Shared game files (PAK0.PAK, PAK1.PAK, autoexec.cfg)
    client/         Client-only overrides (optional)
    server/         Server-only overrides (optional)
logs/               Server logs and runtime state (auto-created)
```

One set of PAK files serves both browser clients and multiplayer servers. Nexus builds per-target views (client vs server) from the layered directory at runtime.

### Auto-Connect

If you just want to get playing with a single server, add `connect 127.13.37.1` to `data/id1/common/autoexec.cfg` and players join the server automatically on load.

## Architecture

```
Browser Tab (WASM)
    |
    |  HTTP: client files, game data manifests, PAK streaming
    |  WebSocket: /ws (binary frames with routing header)
    |
Nexus (Go, port 1337)
    |
    |  UDP: localhost relay
    |
NetQuake Servers (127.13.37.<id>:26000)
```

- **Single container**: Nexus handles HTTP, WebSocket, and server management. One port exposed.
- **Stateless tunnel**: Each WebSocket frame = routing header + raw UDP datagram. No game packet parsing.
- **LAN simulation**: Each browser tab gets a unique loopback IP. Servers see distinct "LAN clients" that can be kicked or banned.
- **Server discovery**: `slist` returns an aggregated server list from the Nexus cache. No flaky broadcast.

## Project Layout

```
client/           WASM client platform layer and WebSocket networking
server/           Dedicated server Makefile and patches
nexus/            Go relay, file serving, server orchestration
manifests/        Game data bootstrap configs
build/            Build system and upstream checkout scripts
Dockerfile        Multi-stage production image
compose.yml       Docker Compose configuration
ATTRIBUTIONS.md   Source provenance and GPL lineage
TECHNICAL.md      Technical deep dive for contributors
```

## Features

- **Browser-native**: Runs in Chrome, Firefox, Safari, Edge, and any browser with WebGL2
- **Real multiplayer**: Dedicated NetQuake servers with the standard protocol
- **Software renderer**: GPU-accelerated palette conversion of the original 8-bit framebuffer
- **Persistent saves**: Config and saves survive browser sessions via IndexedDB
- **Mod support**: Game directory switching at runtime without page reload
- **Self-contained**: Single Docker image, single port, optional TLS via reverse proxy
- **Auto-bootstrap**: Shareware game data downloads automatically on first run

## License

GPL-2.0-or-later, consistent with the original Quake GPL release.

Game data files (PAK files) have separate licensing. Shareware permits redistribution of official archives only. Full version has restrictive licensing; do not host PAK1.PAK publicly.
