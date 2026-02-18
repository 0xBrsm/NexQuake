# Dedicated Server

A Linux build of the complete WinQuake engine, compiled with null drivers and run in headless dedicated mode.

## Approach

Builds the **complete WinQuake engine for Linux** with null drivers for graphics, sound, input, and CD, then runs in dedicated mode (`-dedicated`). This avoids symbol resolution issues that occur when trying to build only the server code, since WinQuake's client and server are tightly coupled. Despite the "Win" in WinQuake, the engine compiles cleanly on Linux with `sys_linux.c` as the system layer.

## Files

| File | Purpose |
|------|---------|
| `Makefile.dedicated` | Build configuration. Compiles the full WinQuake source with null drivers (`vid_null`, `snd_null`, `cd_null`, `in_null`). Handles cross-compilation, bitness selection, and architecture-specific flags via `PLATFORM` env var. |
| `sys_linux_stub.c` | System stub. Provides `Sys_SendKeyEvents()` (required by the engine but unused in dedicated mode). Appended to upstream `sys_linux.c` during build. |
| `bugfix/*.patch` | Upstream bugfixes. Fixes buffer overflows, format string vulnerabilities, and other vanilla WinQuake bugs. Applied by `prepare-upstream.sh` before server patches (set `BUGFIX=0` to skip). See `bugfix/README.md`. |
| `sv_main.c.patch` | Entity overrides. Loads `maps/<map>.ent` files to override entity placement in BSP maps without modifying the BSP file. |
| `net_udp.c.patch` | Ephemeral port fix. Updates `net_hostport` after bind when the server starts with `-port 0`, so Nexus and operators see a reachable non-zero port. |
| `host.c.patch` | Map-cycle cvar registration. Adds the `mapcycle` cvar declaration and registers it during host init. |
| `pr_cmds.c.patch` | Map-cycle changelevel hook. Intercepts QuakeC-driven `changelevel` and applies map selection from `mapcycle` (CSV or file). |
| `64bit/*.64bit.patch` | 64-bit patches. Fix `string_t` pointer arithmetic for 64-bit builds where pointer subtraction overflows 32-bit offsets. Applied automatically on x86_64 and arm64. |

## Building

```bash
# Clone upstream source
git clone --depth 1 https://github.com/id-Software/Quake.git
cd Quake/WinQuake

# Copy Makefile
cp /path/to/server/Makefile.dedicated .

# Apply required patches
patch -p0 < /path/to/server/sv_main.c.patch
patch -p0 < /path/to/server/net_udp.c.patch
patch -p0 < /path/to/server/host.c.patch
patch -p0 < /path/to/server/pr_cmds.c.patch

# Append stub
cat /path/to/server/sys_linux_stub.c >> sys_linux.c

# Build
make -f Makefile.dedicated

# Platform-specific builds
PLATFORM=linux/arm/v7 make -f Makefile.dedicated    # 32-bit ARM
PLATFORM=linux/arm64  make -f Makefile.dedicated     # 64-bit ARM
PLATFORM=linux/amd64  make -f Makefile.dedicated     # 64-bit x86
PLATFORM=linux/386    make -f Makefile.dedicated     # 32-bit x86
```

Output: `build-netquake-<arch>/nqserver`

The automated build script (`build/build-server.sh`) handles all of this.

## Running

```bash
./nqserver -dedicated -port 26000
```

### Map Cycle

`mapcycle` supports two formats:

- Filename (preferred when the path exists): `mapcycle mapcycle.txt`
- Inline list fallback: `mapcycle dm2,dm3,dm4`
- Inline list fallback with spaces: `mapcycle "dm2 dm3 dm4"`

The engine tries to load the cvar value as a file first; if not found, it tokenizes the value itself.
Parsing uses Quake token rules (`COM_Parse`): commas/newlines/tabs are treated as separators and `//` comments are ignored.

## Architecture Support

| Platform | Bits | Notes |
|----------|------|-------|
| linux/amd64 | 64 | Default. `64bit/` patches applied automatically. |
| linux/arm64 | 64 | Default. `64bit/` patches applied automatically. |
| linux/arm/v7 | 32 | Native armhf (Raspberry Pi). |
| linux/386 | 32 | Native i386. |

The `64bit/` patches widen QuakeC `string_t` pointer arithmetic paths that would otherwise truncate on 64-bit. See `64bit/` and `bugfix/README.md` for details.
