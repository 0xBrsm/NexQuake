# Dedicated Server

A Linux build of the complete WinQuake engine, compiled with null drivers and run in headless dedicated mode.

## Approach

Builds the **complete WinQuake engine for Linux** with null drivers for graphics, sound, input, and CD, then runs in dedicated mode (`-dedicated`). This avoids symbol resolution issues that occur when trying to build only the server code -- WinQuake's client and server are tightly coupled. Despite the "Win" in WinQuake, the engine compiles cleanly on Linux with `sys_linux.c` as the system layer.

## Files

| File | Purpose | Why It's Needed |
|------|---------|-----------------|
| `Makefile.dedicated` | **Build configuration.** Compiles the full WinQuake source with null drivers (`vid_null`, `snd_null`, `cd_null`, `in_null`). Supports platform detection via `PLATFORM` env var (linux/amd64, linux/arm64, linux/arm/v7, linux/386). Defaults to 32-bit on most platforms. | The build entry point. Handles cross-compilation, bitness selection, and architecture-specific flags. |
| `sys_linux_stub.c` | **System stub.** Provides `Sys_SendKeyEvents()` (required by the engine but unused in dedicated mode). Appended to the upstream `sys_linux.c` during build. | WinQuake expects this function even when no keyboard input is possible. Without it, the linker fails. |
| `bugfix/common.c.patch` | **Archived pointer underflow fix.** Preserves a prior `COM_FileBase()` hardening patch for reference/testing, but is not applied by current build scripts. | Kept to allow regression testing and quick re-enable if needed. |
| `sv_main.c.patch` | **Entity overrides.** Allows the server to load `maps/<map>.ent` files to override entity placement in BSP maps. Optional quality-of-life feature. | Lets server admins customize spawn points, item placement, and other entities without modifying the BSP file. |
| `net_udp.c.patch` | **Ephemeral port fix.** Updates `net_hostport` after bind when the server starts with `-port 0`. | Enables dynamic UDP port assignment while still reporting a reachable non-zero port to Nexus and operators. |
| `64bit/*.64bit.patch` | **64-bit patches.** Fix `string_t` pointer-subtraction truncation and related issues for 64-bit builds. Applied only when building with `BITS=64`. | QuakeC uses 32-bit offsets for string references. On 64-bit, pointer arithmetic overflows. These patches widen the affected paths. |

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

# Append stub
cat /path/to/server/sys_linux_stub.c >> sys_linux.c

# Build (defaults to 32-bit on amd64)
make -f Makefile.dedicated

# Platform-specific builds
PLATFORM=linux/arm/v7 make -f Makefile.dedicated    # 32-bit ARM
PLATFORM=linux/arm64  make -f Makefile.dedicated     # 64-bit ARM
PLATFORM=linux/amd64  make -f Makefile.dedicated     # 64-bit x86 (uses x32)
PLATFORM=linux/386    make -f Makefile.dedicated     # 32-bit x86
```

Output: `build-netquake-<arch>/nqserver`

The automated build script (`build/build-server.sh`) handles all of this.

## Running

```bash
./nqserver -dedicated -port 26000
```

## Why 32-Bit by Default

QuakeC stores string references as 32-bit offsets (`string_t`). On 64-bit systems, pointer subtraction like `sv.name - pr_strings` silently truncates, causing crashes during server initialization (typically in `worldspawn` / `ED_LoadFromFile`). Building as 32-bit avoids this entirely. The `64bit/` patches exist for environments that require native 64-bit binaries.

## Architecture Support

| Platform | Bits | Notes |
|----------|------|-------|
| linux/arm/v7 | 32 | Native armhf (Raspberry Pi) |
| linux/386 | 32 | Native i386 |
| linux/amd64 | 32 | x32 ABI (32-bit pointers on 64-bit kernel) |
| linux/arm64 | 64 | Requires `64bit/` patches |
| linux/amd64 (64-bit) | 64 | Requires `64bit/` patches |
