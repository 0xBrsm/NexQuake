# NexQuake

NexQuake is a unified multiplayer Quake platform: dedicated servers, orchestration, and browser client in one tiny Docker image. Players launch by URL and join from the in-game server list.

<!-- pages:home-features:start -->
- **Zero friction**: Click a link, and now you're playing.
- **Authentic gameplay**: Same movement, same weapons, same music, same feel.
- **Real multiplayer**: Pick your mods, run as many game servers as you want.
- **Self-hostable**: Pull one Docker image, open one port, go frag.
<!-- pages:home-features:end -->

Ready to play?

[`https://kitty1.quake.nexus`](https://kitty1.quake.nexus)

Rather host it yourself?

```bash
# launch with a single shareware FFA server
docker run -p 1337:1337 -e CL_ARGS=+connect ghcr.io/0xbrsm/nexquake
```
```bash
# launch with several popular server mods
docker run -p 1337:1337 -e CL_SMENU=1 -e QUICKSTART=all ghcr.io/0xbrsm/nexquake
```

For more options, check the [Quick Start Guide](docs/QUICKSTART.md). Or read on for the nitty gritty.

## The Details

Quake was released thirty years ago and changed video-gaming forever. For many of us, it was our first foray into true multiplayer gaming. NexQuake was built to recapture that experience without the setup hassle. With NexQuake, playing old-school Quake is as easy as clicking a link, picking a server, and pwning your friends. The same brutal, fast-paced gameplay from 1996, running natively in your browser in 2026.

NexQuake is a WebAssembly port of Quake with browser-native multiplayer. It runs the original id Software engine in the browser and connects players back to dedicated game servers on the host through a lightweight Go relay that tunnels UDP over WebSocket.

```
Browser Client  --WebSocket-->  Nexus  --UDP-->  NetQuake Server
```

The result is the original Quake multiplayer experience (software rendered, original physics, original UI) with no plugins, no user installs, and no compromise on the classic experience.

**The Browser Client** is the Quake engine compiled to WebAssembly with Emscripten. It uses WebGL2 for rendering (GPU-side palette conversion from the original 8-bit framebuffer), WebAudio for sound, and HTML5 events for input. Both gamepad and touch devices are supported. Game files and background music are streamed on demand from the server through a virtual filesystem.

**Nexus** is a Go orchestration server that serves client files, serves game data, manages servers, and tunnels multiplayer traffic. Each WebSocket frame carries a small routing header plus a raw NetQuake `protocol 15` datagram. Nexus acts as a transparent relay with multi-server routing; it never parses game packets.

**The Dedicated Server** is any original NetQuake engine running `-dedicated`. Stock protocol, stock gameplay. Nexus spawns and manages server instances with runtime flags and server cfgs just like the days of old. A dedicated server with [a few tweaks](./server/README.md) is included.

Players use the standard Quake multiplayer connection experience; use the Multiplayer menu or type `slist` from the console (`~`) to list servers and `connect <host>` to join.

### Features

- **Browser-native**: Runs in Chrome, Firefox, Safari, Edge, and any browser with WebGL2.
- **Real multiplayer**: Dedicated NetQuake servers centrally managed through Nexus.
- **Universal input**: Native touch controls and Gamepad API support.
- **CD audio emulation**: Stream the original Quake soundtrack in .ogg or .mp3 format.
- **Persistent saves**: Config and saves survive browser sessions via IndexedDB.
- **Mod support**: Game directory switching at runtime without page reload.
- **Self-contained**: Single Docker image, single port.
- **Easy to secure**: Supports OIDC via JWT tokens or just run it behind a reverse proxy.
- **Software renderer**: GPU-accelerated palette conversion of the original 8-bit framebuffer.
- **Auto-quickstart**: Shareware/freeware game data downloads automatically on first run.

### Documentation

- **[Repo Overview](docs/README.md)**: Index to all the documentation in the repo.
- **[Quick Start](docs/QUICKSTART.md)**: Get up and running quickly with Docker.
- **[Controls](docs/CONTROLS.md)**: Touch, gamepad, and video mode configuration.
- **[UI Guide](docs/UI.md)**: Using the in-browser settings panel for files, cfg editing, and CD controls.
- **[Setup Guide](docs/SETUP.md)**: Managing game data, mods, and servers.
- **[Environment](docs/ENVIRONMENT.md)**: Environment variables and networking setup.
- **[Admin Guide](docs/ADMIN.md)**: In-game `rcon` commands and the JSON-RPC HTTP API for external tools.
- **[Architecture](docs/ARCHITECTURE.md)**: Technical deep dive into system design.
- **[Bug Fixes](bugfix/README.md)**: Security and stability patches for the upstream WinQuake source.

## Project Layout

```
bugfix/           Patches for critical bugs in the Quake source
build/            Build system and upstream checkout scripts
client/           WASM client platform layer and WebSocket networking
docs/             Detailed documentation and guides
etc/              Quickstart catalog (`game.json`) and default runtime configs
nexus/            Go relay, file serving, server orchestration
server/           Dedicated server Makefile and patches
site/             GitHub Pages content
ATTRIBUTIONS.md   Source provenance and GPL lineage
CHANGELOG.md      Development and release history
compose.yml       Docker Compose configuration
Dockerfile        Multi-stage production image
README.md         You're still reading it (go play Quake!)
```

## License

GPL-2.0-or-later, consistent with the original Quake GPL release.

Game data files (PAK files) have separate licensing. Shareware permits redistribution of official archives only. Full version has restrictive licensing; do not host PAK1.PAK publicly.
