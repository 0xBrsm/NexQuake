# NetQuake Server Source

Build configuration for a NetQuake dedicated server from id Software's WinQuake source.

## Files

- `Makefile.dedicated` - Builds full NetQuake with null drivers
- `sys_linux_stub.c` - Stub for `Sys_SendKeyEvents()` function
- `net_udp.c.patch` - Adds `-ip <address>` flag support for binding to specific IP addresses (enables multi-server on same port)
- `common.c.patch` - Fixes `COM_FileBase()` pointer-underflow (armhf crash)
- `sv_main.c.patch` - Optional QoL: supports `maps/<map>.ent` entity overrides on the server
- `64bit/*.64bit.patch` - Optional patch set for 64-bit server builds

## Approach

Builds the **complete NetQuake engine** with null drivers for graphics/sound/input, then runs in dedicated mode. This avoids symbol resolution issues from trying to build server-only code.

**What's included**:
- Full WinQuake source (client + server + renderer)
- Null drivers (vid_null, snd_null, cd_null, in_null)
- Runs as dedicated server with `-dedicated` flag

**Why not headless-only**:
- WinQuake client/server code is tightly coupled
- Server-only builds have missing symbol errors
- Full build with null drivers is cleaner and more reliable

## Building

```bash
# Clone id Software source
git clone --depth 1 https://github.com/id-Software/Quake.git
cd Quake/WinQuake

# Copy overlay
cp /path/to/server/Makefile.dedicated .

# Apply patches
patch -p0 < /path/to/server/net_udp.c.patch
patch -p0 < /path/to/server/common.c.patch

# Append stub to sys_linux.c
cat /path/to/server/sys_linux_stub.c >> sys_linux.c

# Build
make -f Makefile.dedicated                                 # defaults to BITS=64 on typical toolchains

# Optional: drive the defaults via Docker-style platform strings
# (requires a matching toolchain/container)
PLATFORM=linux/arm/v7 make -f Makefile.dedicated            # implies BITS=32 TARGET=armhf
PLATFORM=linux/386 make -f Makefile.dedicated               # implies BITS=32 TARGET=i386
PLATFORM=linux/arm64 make -f Makefile.dedicated             # implies BITS=64
PLATFORM=linux/amd64 make -f Makefile.dedicated             # implies BITS=64

# Native 32-bit armhf build (recommended: run in a linux/arm/v7 container or armhf OS)
make -f Makefile.dedicated BITS=32 TARGET=armhf

# Native 32-bit i386 build (recommended: run in a linux/386 container or i386 OS)
make -f Makefile.dedicated BITS=32 TARGET=i386

# Optional: 64-bit build (requires extra patches)
patch -p0 < /path/to/server/64bit/net_dgrm.c.64bit.patch
patch -p0 < /path/to/server/64bit/pr_cmds.c.64bit.patch
patch -p0 < /path/to/server/64bit/host_cmd.c.64bit.patch
patch -p0 < /path/to/server/64bit/sv_main.c.64bit.patch
make -f Makefile.dedicated BITS=64
```

Output: `build-netquake-{arch}/nqserver`

## Running

```bash
./nqserver -dedicated
```

## NetQuake vs QuakeWorld

This builds **NetQuake** (original protocol):
- ✅ Compatible with standard Quake clients and WASM client
- ✅ Original game behavior and physics
- ✅ Multi-architecture (x86_64, ARM64)
- ❌ Larger binary (~580KB vs ~275KB for QW)

## Architecture Support

- arm/v7: supports native 32-bit armhf builds (`BITS=32 TARGET=armhf`)
- linux/386: supports native 32-bit i386 builds (`BITS=32 TARGET=i386`)
- amd64/arm64: defaults to 64-bit builds; use 32-bit only in a matching 32-bit userland/container
