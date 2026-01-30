# NetQuake Server Source

Build configuration for a NetQuake dedicated server from id Software's WinQuake source.

## Files

- `Makefile.dedicated` - Builds full NetQuake with null drivers
- `sys_linux_stub.c` - Stub for `Sys_SendKeyEvents()` function
- `net_udp.c.patch` - Adds `-ip <address>` flag support for binding to specific IP addresses (enables multi-server on same port)
- `common.c.patch` - Fixes `COM_FileBase()` pointer-underflow (armhf crash)
- `sv_main.c.patch` - Optional QoL: supports `maps/<map>.ent` entity overrides on the server
- `archived/net_dgrm.c.patch` - Archived networking patch (not applied by default builds)

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
cp /path/to/src/server/Makefile.dedicated .

# Apply patches
patch -p0 < /path/to/src/server/net_udp.c.patch
patch -p0 < /path/to/src/server/common.c.patch

# Append stub to sys_linux.c
cat /path/to/src/server/sys_linux_stub.c >> sys_linux.c

# Build
make -f Makefile.dedicated                         # amd64 -> x32 binary (32-bit pointers on x86_64)
CC=arm-linux-gnueabihf-gcc ARCH=aarch64 make -f Makefile.dedicated  # arm64 -> 32-bit armhf binary
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

- amd64 hosts: outputs an x32 server binary (32-bit pointers on x86_64)
- arm64 hosts: outputs a 32-bit armhf server binary
- Any architecture with GCC (pure C, no assembly)
