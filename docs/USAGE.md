# Usage Guide

## Data Directory Layout

`GAME_DIR` (default `/app/game`) is the root for all game data, configuration, and server definitions.

```
game/
  servers.ini           Server launch plan (see below)
  id1/                  Base Quake game directory
    common/             Shared assets (PAK0.PAK, PAK1.PAK, autoexec.cfg)
    client/             Client-only overrides (optional)
    server/             Server-only configs (config.cfg, .ent files)
  ctf/                  Example mod directory
    common/             Mod assets (pak0.pak)
    server/             Server configs
logs/                   Runtime logs (auto-created)
```

## Layers

NexQuake uses a three-layer virtual filesystem to separate what the web client can download from what only the server can see.

- **common/**: Visible to both the web client and dedicated server. Game assets (`.pak` files, maps, models, sounds) go here.
- **client/**: Visible only to the web client. Use for client-side `config.cfg` or replacement textures.
- **server/**: Visible only to the dedicated server. Use for `config.cfg`, `motd.txt`, and `.ent` entity override files.

This separation prevents players from downloading server-side secrets like your RCON password in `config.cfg`.

## Retail PAK Files

If you own the full version of Quake (from Steam, GOG, etc.), place your retail PAK files in the common layer:

```bash
cp /path/to/quake/id1/PAK*.PAK game/id1/common/
docker compose restart
```

- **PAK0.PAK**: Shareware data (Episode 1). Included automatically by the quickstart bootstrap.
- **PAK1.PAK**: Registered data (Episodes 2-4). Provide your own for the full game; the quickstart bootstrap includes a LibreQuake substitute that is sufficient to load mods.

## Mods

To install a mod (e.g. Capture the Flag):

1. Create the mod directory: `mkdir -p game/ctf/common`
2. Copy the mod's assets into `game/ctf/common/`
3. Define a server for it in `servers.ini` (see below)

## Entity Overrides

NexQuake supports server-side `.ent` files that override entity placement in BSP maps. Extract the entity lump from a map, modify it, save it as `<mapname>.ent` (e.g. `dm4.ent`), and place it in `game/<mod>/server/maps/`.

## Configuration Files

The WinQuake engine automatically executes `config.cfg` and `autoexec.cfg` on startup:

- Client defaults: `game/id1/client/autoexec.cfg`
- Server defaults: `game/id1/server/config.cfg` (or `autoexec.cfg`)

Custom config files (e.g. `server.cfg`) require explicit loading via `exec server.cfg` in your `autoexec.cfg` or `+exec server.cfg` in the launch arguments.

### Auto-Connect

To make the web client automatically join a server on load, add this to `game/id1/client/autoexec.cfg`:

```quake
connect 26000
```

## Server Launch Plan (`servers.ini`)

`servers.ini` in `GAME_DIR` defines which servers Nexus manages on startup. Each line is a server binary followed by its launch arguments. NexQuake is drop-in compatible with any protocol 15 (NetQuake) server binary; the bundled `nqserver` is the default, but you can substitute any conformant binary.

### Example

```ini
# Define a reusable argument group
@standard_dm -dedicated 16 -port 0 +deathmatch 1 +timelimit 20 +fraglimit 30

# Deathmatch servers
nqserver @standard_dm +hostname "NexQuake DM #1" +map dm4
nqserver @standard_dm +hostname "NexQuake DM #2" +map dm6

# CTF server (requires data in game/ctf/)
nqserver -dedicated 16 -port 0 -game ctf +hostname "NexQuake CTF" +map ctf2m3
```

### Key Arguments

| Argument | Purpose |
|------|---------|
| `-dedicated <N>` | Maximum client slot count (memory allocation). Lower the visible limit with `maxplayers <M>` in config, but `M` cannot exceed `N`. |
| `-port 0` | Bind to a random open port. Prevents "Address already in use" when running multiple servers. Nexus detects the assigned port and routes traffic automatically. |
| `+hostname "Name"` | Set a unique server name. Critical when multiple servers share a game directory, since they share `config.cfg` and would otherwise have identical hostnames. |
| `@groupname ...` | Define a reusable macro for common flags like `-dedicated 16 -port 0`. |

If `servers.ini` is missing, Nexus launches a single default instance: `nqserver -dedicated`.
