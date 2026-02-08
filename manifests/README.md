# Quickstart Manifests

Manifests let you play immediately without setting up a data directory or volume mount. Set `QUICKSTART=<name>` and Nexus downloads everything needed on first boot -- shareware PAK, libre PAK1, mod files -- directly into the data directory. No manual file management required.

## How It Works

1. On startup, Nexus checks for `${QUICKSTART:-minimal}.json` in the manifests directory
2. If `${DATA_DIR}` is writable and the target game directory isn't already populated, Nexus downloads the listed assets
3. Each manifest entry specifies a game directory and download URLs organized by layer (common/client/server)
4. Assets are extracted and placed into `${DATA_DIR}/<mod>/<layer>/`
5. On subsequent boots, populated directories are skipped (idempotent)

## Available Manifests

| Manifest | `QUICKSTART=` | What You Get |
|----------|---------------|--------------|
| `minimal.json` | `minimal` (default) | Quake shareware (Episode 1) + LibreQuake PAK1. Enough to play single-player and multiplayer with community mods. |
| `full.json` | `full` | Everything in `minimal` plus Frag Arena and Capture the Flag (3Wave CTF) mods with dedicated server configs. Full multiplayer setup. |
| `og.json` | `og` | Everything in `minimal` plus original 3Wave CTF v2.51 (server-only). Lighter CTF variant. |
| `ctf4.json` | `ctf4` | Everything in `minimal` plus 3Wave CTF v4.21d with common assets and server configs. |

All manifests include `id1` with both `quake106.zip` (shareware PAK0) and `lq-pak1.zip` (LibreQuake PAK1). The differences are which additional mods are included.

## The LibreQuake PAK1 (`lq-pak1.zip`)

Every manifest includes an open-source PAK1 based on the [LibreQuake](https://github.com/lavenderdotpet/LibreQuake) project. This is important because without a PAK1, the Quake engine runs in shareware mode and many community mods refuse to load.

This PAK1 was specifically prepared for NexQuake:
- **Fileset matches retail PAK1 exactly** -- every file that exists in the retail PAK1 has a corresponding entry, so mods that check for specific files work correctly
- **Sounds compressed to original Quake resolution** -- matches the 8-bit/11kHz quality of PAK0 shareware sounds for audio parity across both PAKs
- **Open-source and freely redistributable** -- art assets under BSD-3-Clause (LibreQuake contributors), `pop.lmp` proof-of-purchase under GPL-2.0 (derived from id Software's Quake source)

With this PAK1 in place, the engine reports as "registered" and all standard community mods and maps work out of the box. Users who own retail Quake can still provide their own PAK1 for the original assets.

## Quick Start

```bash
# Default: shareware + libre PAK1, ready to play
docker compose up --build

# Full multiplayer with mods
QUICKSTART=full docker compose up --build

# CTF only
QUICKSTART=ctf4 docker compose up --build
```

No `data/` directory, no volume mounts, no PAK file management. Nexus handles everything.

## Providing Your Own Data

Manifests are a convenience for getting started. For production or if you have retail PAK files:

1. Create a `data/` directory with your PAK files (see the [main README](../README.md) for layout)
2. Bind-mount it as `${DATA_DIR}`
3. The bind mount shadows the built-in manifests, so Nexus uses your data directly

## Schema

```json
[
  {
    "game": "id1",
    "common": ["https://example.com/pak0.zip", "https://example.com/pak1.zip"],
    "client": [],
    "server": [],
    "force": false
  }
]
```

Each entry specifies a game directory and URLs for each layer. At least one of `common`, `client`, or `server` must be present and non-empty. Set `force: true` to re-download even if the directory is already populated.

## Asset Repository

All manifest URLs point to [brstm/QuakeMods](https://github.com/brstm/QuakeMods) on GitHub. This repository hosts the curated asset packages (shareware archives, mod distributions, the LibreQuake PAK1) referenced by the manifests.
