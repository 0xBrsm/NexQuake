# Usage Guide

## Managing Game Data

NexQuake uses a three-layer virtual filesystem to separate client and server data. `GAME_DIR` (default `/app/game`) is the root for all game data, configuration, and server definitions. The [quickstart](QUICKSTART.md) process automatically downloads and installs the data needed to get NexQuake up and running:

- **pak0.pak**: Shareware data (Episode 1).
- **pak1.pak**: Freeware version of registered data (without maps) that is sufficient to run mods.

If you own the full version of Quake or you want to configure mods yourself:
1. Bind mount your `game` directory to `game:/app/game`
2. Create the `id1` directory: `mkdir -p game/id1/common`
3. Copy your PAK files into `game/id1/common/`

To install a mod (e.g. Capture the Flag):
1. Create the mod directory: `mkdir -p game/ctf/common`
2. Copy the mod's shared assets into `game/ctf/common/`
3. Copy the mod's server-side assets (e.g. progs.dat) into `game/ctf/server/`
4. Configure server binary and launch arguments in `servers.ini` (see below)

#### Game Data Directory Layout
```
game/
  servers.ini           Server launch plan (see below)
  id1/                  Base Quake game directory
    common/             Shared assets (pak0.pak, pak1.pak)
  ctf/                  Example mod directory
    client/             Client-only files (autoexec.cfg, config.cfg)
    common/             Mod assets (pak0.pak)
    server/             Server-only files (progs.dat, maps/*.ent files, config.cfg)
logs/                   Runtime logs
```

## Configuring Servers

### Configuration Files

The Quake engine automatically executes `config.cfg` and `autoexec.cfg` on startup. Custom config files (e.g. `server.cfg`) require explicit loading via `exec server.cfg` in your `autoexec.cfg` or `+exec server.cfg` in the launch arguments.

See the [Quake Wiki](https://quake.fandom.com/wiki/Console_Commands_(Q1)#Server_Commands:) for a full list of server config and console commands.

### Server Launch Plan (`servers.ini`)

`servers.ini` in `GAME_DIR` defines which servers Nexus manages on startup. Each line is a server binary followed by its launch arguments. NexQuake is drop-in compatible with any protocol 15 (NetQuake) server binary; the bundled `nqserver` is the default, but you can substitute any conformant binary by putting it in `SERVER_DIR` (searched before `BIN_DIR`). If `servers.ini` is missing, the default [quickstart](QUICKSTART.md) process will still launch a basic FFA server.

#### Example `servers.ini`
```ini
# Define a reusable argument group
@def -dedicated 99 -port 0 -mem 16

# List game servers
nqserver @def -game id1 +hostname "FragFest"  +exec deathmatch1.cfg
nqserver @def -game id1 +hostname "Leetskool" +exec deathmatch2.cfg
nqserver @def -game ctf +hostname %game
```

### Server Arguments

| Argument | Purpose |
|-----|---------|
| `-dedicated <N>` | Maximum client count. Stock NetQuake caps at 16, but 99 is a good default if you use custom binaries that may have higher limits. Lower the visible limit with `maxplayers <M>` in config (`M` cannot exceed `N`). |
| `-port 0` | Bind to a random open port so you don't have to manually assign a port to each server. Nexus detects the ephemeral port and routes traffic automatically. |
| `-mem <MB>` | Specifies the RAM to reserve for game server memory. Default is 8 MB for Linux Quake, which may not be enough for some mods. Generally, `16` is a safe number.
| `+hostname "Name"` | Set a unique server name. Critical when multiple servers share a game directory, since they share `config.cfg` and may end up with identical hostnames. |
| `@groupname ...` | Define a reusable macro for common flags like `-dedicated 16 -port 0`. |
| `%name` | Placeholder token replaced by the first seen `-name <value>` or `+name <value>` from the same launch line (after `@group` expansion). Example: `+hostname ctf -game %hostname`. Unresolved placeholders pass through unchanged. |

Additional server arguments can be found in Quake's [TECHINFO.TXT](https://github.com/id-Software/Quake/blob/master/WinQuake/data/TECHINFO.TXT).

### Map Cycle

`mapcycle` supports two inputs:

- File containing one map per line: `mapcycle mapcycle.txt`
- CSV list of maps: `mapcycle dm2,dm3,dm4`

The engine tries to load the cvar value as a file first; if not found, it tokenizes the value itself.
Parsing uses Quake token rules (`COM_Parse`): commas/newlines/tabs are treated as separators and `//` comments are ignored.

### Entity Overrides

Some mods, like CTF, ship with `.ent` files. These traditionally require extracting and recompiling the retail maps with qbsp. Instead, NexQuake includes a server-side patch for `.ent` files that overrides entity placement in maps when loaded. Just place the files (e.g. `dm4.ent`) in `game/<mod>/server/maps/`.

## Background Music (BGM)

Nexus streams `.ogg` or `.mp3` BGM tracks from `CD_DIR` (default `/app/cd`) to the client. Bind your audio files to that directory and ensure the file names include the original Quake track numbers (2-11) either at the start or end of the filename (for example `02-Quake Theme.ogg` or `track02.mp3`).

In the browser UI, CD buttons are mapped to native Quake `cd` commands (`cd on/off`, `cd pause/resume`, `cd stop`, `cd loop`). See the [UI Overlay Guide](UI.md#cd-mode-and-native-quake-cd-commands) for control details.

## Client Game Data

The browser client includes a lightweight, minimal local data management UI accessed by the gear icon in the upper right of the browser window. Players can manage their local data here including upload/download of supported game files (`.cfg`, `.dem`, `.pak`, `.pcx`) and audio files (`.ogg`, `.mp3`). See the [UI Overlay Guide](UI.md) for controls and workflows.

If the same file exists in both server-provided game data and user-local browser data, the local user file takes precedence.

Although NexQuake is intended to host all the files needed to play, you can configure it to require players to supply their own game data. To do this, put all server-side game data in `game/<mod>/server`. Players will then have to upload their own `.pak` files through the NexQuake UI in order to start the game or join a server.
