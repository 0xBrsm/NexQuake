# Quick Start

The quickest way to get a NexQuake server running.

### 1. Run

```bash
# load a single shareware FFA server
docker run -p 1337:1337 -e CL_ARGS=+connect ghcr.io/0xbrsm/nexquake
```

### 2. Play

Open [http://localhost:1337](http://localhost:1337) in your browser.

That's it. Nexus loads game data automatically on first run from `CFG_DIR/game.json`. Entries marked with `base` are always included; by default this includes `id1` (Quake 1.06 shareware plus an open-source [LibreQuake](https://github.com/lavenderdotpet/LibreQuake) `pak1.pak`) so the engine can boot with Episode 1 and full multiplayer mod support. If `QUICKSTART` is unset, Nexus defaults to `ffa` for a vanilla deathmatch server.

## Quickstart with More Mods

Set `QUICKSTART` to choose different game entries at startup (base entries are still included automatically in the quickstart install set):

```bash
docker run -p 1337:1337 -e QUICKSTART=ctf,arena ghcr.io/0xbrsm/nexquake
```

See the [Quickstart Catalog](../etc/README.md) for available entries and schema details.

## Using Your Own Data

To use retail PAK files, custom mods, or your own server configuration, bind-mount a data directory:

```bash
docker run -p 1337:1337 -v ./game:/app/game ghcr.io/0xbrsm/nexquake
```

See the [Usage Guide](USAGE.md) for data directory layout, mod installation, and server configuration.

## Docker Compose

Use Docker Compose once you want to move beyond one-line `docker run` examples.

```yaml
# simplified example
services:
  nexquake:
    image: ghcr.io/0xbrsm/nexquake
    environment:
      - QUICKSTART=all
      - AUTH_RCON_PASSWORD=NexQuakeFTW
    volumes:
      - ./game:/app/game
    ports:
      - "1337:1337"
```

See [`compose.yml`](../compose.yml) for an expanded version.

For full environment variable reference, see [Configuration](CONFIGURATION.md). For data/mod layout and `servers.ini`, see [Usage Guide](USAGE.md).
