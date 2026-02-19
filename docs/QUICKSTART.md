# Quick Start

The quickest way to get a NexQuake server running.

### 1. Run

```bash
docker run -p 1337:1337 ghcr.io/0xBrsm/nexquake
```

### 2. Play

Open [http://localhost:1337](http://localhost:1337) in your browser.

That's it. Nexus bootstraps game data automatically on first boot using the default manifest, which includes the Quake 1.06 shareware and an open-source [LibreQuake](https://github.com/lavenderdotpet/LibreQuake) `pak1.pak`. This gives you a registered engine with Episode 1 single-player and full multiplayer mod support.

## Bootstrapping with More Mods

Set the `QUICKSTART` variable to pull additional mods at startup:

```bash
docker run -p 1337:1337 -e QUICKSTART=id1,ctf,arena ghcr.io/0xBrsm/nexquake
```

See the [manifests documentation](../manifests/README.md) for the full list of available packages and how to create your own.

## Using Your Own Data

To use retail PAK files, custom mods, or your own server configuration, bind-mount a data directory:

```bash
docker run -p 1337:1337 -v ./game:/app/game ghcr.io/0xBrsm/nexquake
```

See the [Usage Guide](USAGE.md) for data directory layout, mod installation, and server configuration.
