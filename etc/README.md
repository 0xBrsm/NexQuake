# Quickstart Catalog

The files in `src/etc` define Nexus startup defaults:

- `game.json`: quickstart catalog for downloadable mod data
- `servers.ini`: default launch template copied on first run
- `client/autoexec.cfg` + `client/nexquake.cfg`: default client cfg files bundled into `index.data` and seeded on first browser load
- `arena/server.cfg`: Rocket Arena server config, referenced as a local path in `game.json`
- `ffa/server.cfg` + `ffa/ffa.txt`: ffa server config and map rotation list, referenced as local paths in `game.json`
- `fvf/server.cfg`: Future vs Fantasy server config, referenced as a local path in `game.json`

Use `QUICKSTART=<name[,name...]>` to include extra catalog entries at startup.
If `QUICKSTART` is unset, Nexus uses `QUICKSTART=ffa`.
Any catalog entry with a `base` field is always included.

## How It Works

1. Nexus loads `CFG_DIR/game.json`.
2. If `GAME_DIR/servers.ini` is missing, Nexus copies `CFG_DIR/servers.ini` and appends one `nqserver @def -game <name>` line per valid `QUICKSTART` game entry (`ffa` by default).
3. Nexus parses `GAME_DIR/servers.ini` for `-game` values and then always adds all `base` entries to the quickstart install set.
4. For each selected game, Nexus installs `common`, `server`, and `client` layer assets into `GAME_DIR/<mod>/<layer>/`.
5. A non-empty layer directory is skipped unless that catalog entry sets `"force": true`.

Client cfg seed flow:
- During client build, `client/autoexec.cfg` and `client/nexquake.cfg` are bundled into `index.data`.
- On the first browser launch only, the client copies them into `/NexQuake/game/<base-game>/` and writes a marker so this never repeats.

## Built-In Catalog Entries

| Game | `QUICKSTART` value | What You Get |
|------|--------------------|--------------|
| `id1` | n/a (`base` entry) | Quake 1.06 shareware `pak0.pak` plus LibreQuake-based `pak1.pak`. |
| `arena` | `arena` | Rocket Arena assets and server package. |
| `ctf` | `ctf` | 3Wave CTF 4.x assets plus 4.21d server package. |
| `ffa` | `ffa` (default) | Stock server config with `deathmatch 1` rules. |
| `fortress` | `fortress` | Team Fortress 2.8 server and client assets. |
| `fvf` | `fvf` | Future vs Fantasy 4.2R server and client assets. |

## QUICKSTART Behavior

- `QUICKSTART` supports comma-separated catalog names: `QUICKSTART=ctf,arena`
- If unset, `QUICKSTART` defaults to `ffa`
- `QUICKSTART=all` includes every `game` entry in `game.json`
- Unknown names are ignored
- Entries with a `base` field are always included

## `game.json` Schema

```json
[
  {
    "base": "id1",
    "common": ["https://example.com/pak0.zip", "https://example.com/pak1.zip"],
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

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `game` | string | one of `game`/`base` | Target game directory name (for example `ctf`, `arena`). |
| `base` | string | one of `game`/`base` | Base game directory that is always included (for example `id1`). |
| `common` | string[] | no | URLs or relative file paths for assets shared by client and server. |
| `client` | string[] | no | URLs or relative file paths for client-only assets. |
| `server` | string[] | no | URLs or relative file paths for server-only assets (for example configs, `.ent`). |
| `force` | bool | no | Overwrite even when the destination layer already has files. Default `false`. |

At least one of `common`, `client`, or `server` must be non-empty.
URLs/paths can point to `.zip` files (extracted into the layer) or direct files (copied by source filename).
Local paths without a URI scheme are resolved relative to `CFG_DIR` (for example `ffa/server.cfg`).

## Asset Repository

The built-in URLs in `game.json` primarily point to [0xBrsm/QuakeAssets](https://github.com/0xBrsm/QuakeAssets), with additional upstream mirrors when needed.
