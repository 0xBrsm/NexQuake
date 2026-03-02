# Data and Servers

## Managing Game Data

NexQuake uses a three-layer virtual filesystem to separate client and server data. `GAME_DIR` (default `/app/game`) is the root for all game data, configuration, and server definitions. The [Quick Start](QUICKSTART.md) process automatically downloads and installs the data needed to get NexQuake up and running:

- **pak0.pak**: Shareware data (Episode 1).
- **pak1.pak**: Freeware version of registered data (without maps) that is sufficient to run mods.

If you own the full version of Quake or you want to configure mods yourself:
1. Bind mount your `game` directory to `./game:/app/game`
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

`servers.ini` in `GAME_DIR` defines which servers Nexus manages on startup. Each line is a server binary followed by its launch arguments. NexQuake is drop-in compatible with any protocol 15 (NetQuake) server binary; the bundled `nqserver` is the default, but you can substitute any conformant binary by putting it in `SERVER_DIR` (searched before `BIN_DIR`). If `servers.ini` is missing, the default [Quick Start](QUICKSTART.md) process will still launch a basic FFA server.

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
| `-dedicated <N>` | Maximum client count. Stock NetQuake caps at 16. Use higher values only with custom binaries that actually support them. Lower the visible limit with `maxplayers <M>` in config (`M` cannot exceed `N`). |
| `-port 0` | Bind to a random open port. On startup entries, this enables Nexus dynamic scaling for that server line: `slist` shows one pool row with aggregate player counts, and the port shown is a real backend port selected by least-loaded round-robin at browse time. `connect <port>` connects directly to that specific backend. Scaling lifecycle/autoscale keeps one backend routable while draining/despawning idle replicas. |
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

If `timelimit` is enabled and no players are connected, the engine also forces `changelevel` at `(timelimit + 1)` minutes to avoid the known no-player mapchange hang in stock QuakeC. It picks the next `mapcycle` map when set, otherwise uses the map's `trigger_changelevel` target, falling back to reloading the current map if one doesn't exist.

### Entity Overrides

Some mods, like CTF, ship with `.ent` files. These traditionally require extracting and recompiling the retail maps with qbsp. Instead, NexQuake includes a server-side patch for `.ent` files that overrides entity placement in maps when loaded. Just place the files (e.g. `dm4.ent`) in `game/<mod>/server/maps/`.

## Server Scaling

Nexus can run multiple backend instances for the same server line and distribute players across them automatically. This is useful when a single server reaches its player cap during peak hours.

### Enabling Scaling

Add `-port 0` to a `servers.ini` line to enable scaling for that entry:

```ini
@def -dedicated 16 -port 0 -mem 16
nqserver @def -game id1 +hostname "FragFest"
```

`-port 0` tells the OS to assign a random free port. Nexus detects this, groups all instances launched from that line into a pool, and presents the pool as a single server in the browser list.

Set `POOL_SIZE` to the maximum number of backend instances Nexus will spawn (default `1`, which disables scale-out):

```env
POOL_SIZE=10
```

### How It Works

The pool shows up as one row in the server list with aggregate player counts and an instance count. The port shown to players is one of the running backends, selected by least-loaded round-robin at browse time. Connecting directly to that port connects to that specific backend.

Nexus tracks how often players browse the pool (`slist` poll hits) over a 30-second window to estimate demand. When free slots across the pool fall below the headroom target — `max(4, ceil(joinRPS × 12s × 1.5))` — and the pool is below `POOL_SIZE`, a new backend instance is spawned.

### Backend Lifecycle

Each backend in a pool moves through four states:

| State | Meaning |
|-------|---------|
| `warming` | Process started; not yet seen in server-info poll. Not routable. |
| `active` | Seen in polls; eligible for slist backend selection. |
| `draining` | No players, headroom allows removal. Not preferred, but usable as fallback. |
| `terminating` | Selected for shutdown; removed when process exits. |

Backends move to `draining` only when at least two active backends remain and total free slots minus the backend's cap still meets headroom. Draining backends with zero players for 6 consecutive poll cycles (≈3 seconds) are shut down. The last running backend in a pool is never despawned.

### Observing Pools

`slist` in the console shows one row per pool with the instance count appended to the users column (e.g. `2/16 ×3` means 2 players across 3 instances with a 16-player aggregate cap).

`rcon` on a pool entry works only when the pool has exactly one running backend. With multiple backends, target a specific instance by its listen port instead.

## Background Music (BGM)

Nexus streams `.ogg` or `.mp3` BGM tracks from `CD_DIR` (default `/app/cd`) to the client. Bind your audio files to that directory and ensure the file names include the original Quake track numbers (2-11) either at the start or end of the filename (for example `02-Quake Theme.ogg` or `track02.mp3`).

In the browser UI, CD buttons are mapped to native Quake `cd` commands (`cd on/off`, `cd pause/resume`, `cd stop`, `cd loop`). See the [UI Guide](UI.md#cd-mode-and-native-quake-cd-commands) for control details.

## Client Game Data

The browser client includes a lightweight, minimal local data management UI accessed by the gear icon in the upper right of the browser window. Players can manage their local data here including upload/download of supported game files (`.cfg`, `.dem`, `.pak`, `.pcx`) and audio files (`.ogg`, `.mp3`). See the [UI Guide](UI.md) for controls and workflows.

If the same file exists in both server-provided game data and user-local browser data, the local user file takes precedence.

Although NexQuake is intended to host all the files needed to play, you can configure it to require players to supply their own game data. To do this, put all server-side game data in `game/<mod>/server`. Players will then have to upload their own `.pak` files through the NexQuake UI in order to start the game or join a server.
