# Dedicated Server

NexQuake is built to support any dedicated Quake server running protocol 15. A Linux build of the complete WinQuake engine is provided here for ease of use, compiled with `sys_linux.c` and null drivers for graphics, sound, input, and CD. This avoids symbol resolution issues that occur when trying to build only the server code, since WinQuake's client and server are tightly coupled.

Three quality-of-life improvements are also included; native .ent support for maps, ephemeral port assignment, and a mapcycle cvar.

## Files

| File | Purpose |
|------|---------|
| `Makefile.dedicated` | Build configuration. Compiles the full WinQuake source with null drivers (`vid_null`, `snd_null`, `cd_null`, `in_null`). Handles cross-compilation, bitness selection, and architecture-specific flags via `PLATFORM` env var. Enables `-DIDGODS` so `idgods` and `please` are available. |
| `sys_linux_stub.c` | System stub. Provides `Sys_SendKeyEvents()` (required by the engine but unused in dedicated mode). Appended to upstream `sys_linux.c` during build. |
| `bugfix/*.patch` | Upstream bugfixes. Fixes buffer overflows, format string vulnerabilities, and other vanilla WinQuake bugs. Applied by `prepare-upstream.sh` before server patches (set `BUGFIX=0` to skip). See `bugfix/README.md`. |
| `64bit/*.64bit.patch` | 64-bit patches. Fix `string_t` pointer arithmetic for 64-bit builds where pointer subtraction overflows 32-bit offsets. Applied automatically on x86_64 and arm64. |
| `host.c.patch` | Optional map rotation host hooks. Adds/registers the `mapcycle` cvar and enforces idle timelimit `changelevel` when no clients are connected and QuakeC doesn't issue `changelevel` (mapcycle first, then `start` -> `e1m1`, then `trigger_changelevel` map target, otherwise current map). |
| `pr_cmds.c.patch` | Optional map rotation changelevel hook. Intercepts QuakeC-driven `changelevel` and applies map selection from `mapcycle` (CSV or file). See [`SETUP.md`](../docs/SETUP.md) for details. |
| `net_udp.c.patch` | Optional ephemeral port fix. Updates `net_hostport` after bind when the server starts with `-port 0`, so Nexus and operators see a reachable non-zero port. Makes configuring multiple servers much cleaner. |
| `sv_main.c.patch` | Optional map-load hardening + entity overrides. If a requested map BSP cannot be loaded, server spawn falls back to `maps/start.bsp`. Also loads `maps/<map>.ent` files used in some mods (e.g. CTF) to override entity placement in BSP maps without modifying the BSP. |

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

## IDGODS and `please`

This dedicated build is compiled with `IDGODS` to enable hidden per-server admin (`idgods`/`please`). See [`ADMIN.md`](../docs/ADMIN.md) for usage and promotion flow details.

## Architecture Support

| Platform | Bits | Notes |
|----------|------|-------|
| linux/amd64 | 64 | Default. `64bit/` patches applied automatically. |
| linux/arm64 | 64 | Default. `64bit/` patches applied automatically. |
| linux/arm/v7 | 32 | Native armhf (Raspberry Pi). |
| linux/386 | 32 | Native i386. |

The `64bit/` patches widen QuakeC `string_t` pointer arithmetic paths that would otherwise truncate on 64-bit. See `64bit/` and `bugfix/README.md` for details.
