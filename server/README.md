# NetQuake Server Source

Build configuration for a NetQuake dedicated server from id Software's WinQuake source.

## Files

- `Makefile.dedicated` - Builds full NetQuake with null drivers
- `sys_linux_stub.c` - Stub for `Sys_SendKeyEvents()` function
- `net_dgrm.c.patch` - Fixes Quake’s built-in `BAN_TEST` POSIX fallback structs on 64-bit (prevents bogus `You have been banned.` rejects)
- `net_main.c.patch` - Reduces `net_messagetimeout` from 300s to 30s (faster dead-client cleanup)
- `sv_main.c.patch` - Fixes 64-bit `string_t` assignments for worldspawn/map globals (uses `ED_NewString`)
- `host_cmd.c.patch` - Fixes 64-bit `string_t` assignments for player name globals (uses `ED_NewString`)
- `pr_cmds.c.patch` - Fixes 64-bit `string_t` return values for `ftos`/`vtos` (prevents disconnect crashes in QuakeC)

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

# Apply 64-bit progs string fixes
patch -p0 < /path/to/src/server/net_dgrm.c.patch
patch -p0 < /path/to/src/server/net_main.c.patch
patch -p0 < /path/to/src/server/sv_main.c.patch
patch -p0 < /path/to/src/server/host_cmd.c.patch
patch -p0 < /path/to/src/server/pr_cmds.c.patch

# Append stub to sys_linux.c
cat /path/to/src/server/sys_linux_stub.c >> sys_linux.c

# Build
make -f Makefile.dedicated           # x86_64
CC=aarch64-linux-gnu-gcc make -f Makefile.dedicated  # ARM64
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

- x86_64 (Intel/AMD 64-bit)
- ARM64/aarch64 (Apple Silicon, Raspberry Pi, AWS Graviton)
- Any architecture with GCC (pure C, no assembly)
