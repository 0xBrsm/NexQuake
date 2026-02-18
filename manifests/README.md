# Manifests

Manifests let you play immediately without setting up a game directory or volume mount. Set `QUICKSTART=<name[,name...]>` and Nexus downloads everything needed on first boot -- shareware PAK, libre PAK1, mod files -- directly into the game directory. No manual file management required.

## How It Works

1. On startup, Nexus resolves `${QUICKSTART:-minimal}` as one or more manifest names (comma-separated, in order)
2. If `${GAME_DIR}` is writable and the target game directory isn't already populated, Nexus downloads the listed assets
3. Each manifest entry specifies a game directory and download URLs organized by layer (common/client/server)
4. Assets are extracted and placed into `${GAME_DIR}/<mod>/<layer>/`
5. On subsequent boots, populated directories are skipped (idempotent)

## Available Manifests

| Manifest | `QUICKSTART=` | What You Get |
|----------|---------------|--------------|
| `minimal.json` | `minimal` (default) | Quake shareware (Episode 1) + LibreQuake PAK1. Enough to load mods and play multiplayer. |
| `full.json` | `full` | Everything in `minimal` plus Rocket Arena and Capture the Flag (3Wave CTF) mods with dedicated server configs. Full multiplayer setup. |
| `og.json` | `og` | Everything in `minimal` plus original 3Wave CTF v2.51 (server-only). Lighter CTF variant. |
| `ctf4.json` | `ctf4` | Everything in `minimal` plus 3Wave CTF v4.21d with common assets and server configs. |

All manifests include `id1` with the Quake 1.06 shareware (`quake106.zip`) and the LibreQuake PAK1 (`lq-pak1.zip`).

## The LibreQuake PAK1

Every manifest includes an open-source PAK1 based on the [LibreQuake](https://github.com/lavenderdotpet/LibreQuake) project. Without a PAK1, the engine runs in shareware mode and many community mods refuse to load. This PAK1 was prepared specifically for NexQuake:

- **File-for-file match with retail PAK1.** Every file in the retail PAK1 has a corresponding entry, so mods that check for specific files work correctly.
- **Sounds at original Quake resolution.** 8-bit/11kHz to match PAK0 shareware audio.
- **Freely redistributable.** Art assets under BSD-3-Clause (LibreQuake contributors). `pop.lmp` proof-of-purchase under GPL-2.0 (derived from id Software Quake source).

Users who own retail Quake can still provide their own PAK1 for the original assets.

## Quick Start

```bash
# Default: shareware + libre PAK1, ready to play
docker compose up --build

# Full multiplayer with mods
QUICKSTART=full docker compose up --build

# CTF only
QUICKSTART=ctf4 docker compose up --build

# Compose multiple manifests (applied in order)
QUICKSTART=minimal,ctf4 docker compose up --build
```

No `game/` directory, no volume mounts, no PAK file management. Nexus handles everything.

## Providing Your Own Data

Manifests are a convenience for getting started. For production or if you have retail PAK files:

1. Create a `game/` directory with your PAK files (see the [main README](../README.md) for layout)
2. Bind-mount it as `${GAME_DIR}`
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
  },
  {
    "game": "mymod",
    "common": ["https://example.com/mymod.zip"],
    "server": ["https://example.com/mymod-server-cfg.zip"]
  }
]
```

### Entry Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `game` | string | yes | Target game directory name (e.g. `id1`, `ctf`). |
| `common` | string[] | no | URLs for assets shared by both client and server. |
| `client` | string[] | no | URLs for client-only assets. |
| `server` | string[] | no | URLs for server-only assets (configs, `.ent` files). |
| `force` | bool | no | Re-download even if the directory is already populated. Default `false`. |

At least one of `common`, `client`, or `server` must be present and non-empty. URLs should point to `.zip` archives; Nexus extracts them into the appropriate layer subdirectory.

To use custom manifests, place them in the manifests directory and set `QUICKSTART` to one or more filenames (without the `.json` extension), comma-separated.

## Asset Repository

All built-in manifest URLs point to [0xBrsm/QuakeMods](https://github.com/0xBrsm/QuakeMods) on GitHub, which hosts the curated asset packages referenced by the manifests.
