# Bug Fix Catalog

Optional patches for bugs in the original id Software WinQuake source. These target the vanilla codebase and contain no NexQuake-specific changes. Applied by default during `prepare-upstream.sh`; set `BUGFIX=0` to opt out.

## Overview

| Patch | Severity | Fix |
|-------|----------|-----|
| `cl_demo.c.patch` | buffer overflow (local) | Unbounded `sprintf`/`strcpy` of demo filenames in `CL_Record_f`/`CL_PlayDemo_f` |
| `common.c.patch` | crash / UB | `COM_FileBase` walks pointer before string start |
| `com_parse.c.patch` | buffer overflow (remote) | `COM_Parse` writes past `com_token` buffer |
| `host_cmd.c.patch` | buffer overflow (remote) | Unbounded `sprintf` in save/load paths and `Host_Say` |
| `net_dgrm.c.patch` | buffer overflow (remote) | `Datagram_GetMessage` overflow + fragile POSIX struct defs |
| `net.h.patch` | build failure | `htonl`/`ntohl` type conflict with `<arpa/inet.h>` |
| `pr_edict.c.patch` | buffer overflow (progs) | `sprintf` overflows in `PR_ValueString`/`PR_GlobalString` |
| `sv_main.c.patch` | format string (remote) | Progs string used as `sprintf` format argument |
| `world.h.patch` | build failure | Missing prototype for `SV_RecursiveHullCheck` causes implicit-declaration errors |

## Details

#### `cl_demo.c.patch` — demo filenames

`CL_Record_f()` builds the demo path with an unbounded `sprintf` of the user-supplied demo name (`Cmd_Argv(1)`) into a `MAX_OSPATH` buffer, and `CL_PlayDemo_f()` `strcpy`s the name into a 256-byte buffer. Both then call `COM_DefaultExtension()`, whose unchecked `strcat` of `.dem` can write past the end. A long demo name typed at the console overflows the stack. Fix: bound both copies with `snprintf`, reserving space for the extension.

#### `common.c.patch` — `COM_FileBase`

`COM_FileBase()` walks a pointer backward looking for `/` but can walk before the start of the string (UB/crash). Also has an off-by-one in `strncpy` length. Fix: replace with `strrchr()` + null/empty guard.

#### `com_parse.c.patch` — `COM_Parse`

`COM_Parse()` writes tokens into `com_token` (1024 bytes) with no length check. Both the quoted-string and regular-word paths write past the end. Exploitable via crafted `.ent` files, `stuffcmd`, or progs strings. Fix: add `sizeof(com_token)` bounds checks to both paths.

#### `host_cmd.c.patch` — save/load + `Host_Say`

Four related overflows:

1. `Host_SavegameComment()` — unchecked `memcpy` of `cl.levelname` into comment buffer
2. `Host_Savegame_f()` — unbounded `sprintf` into 256-byte path buffer
3. `Host_Loadgame_f()` — same unbounded `sprintf` for load path
4. `Host_Say()` — `sprintf` into 64-byte buffer; `hostname.string` has no length limit

Fix: clamp `memcpy` length, replace `sprintf` with `snprintf`.

#### `net_dgrm.c.patch` — datagram layer

Two fixes:

1. **POSIX struct portability** — The `BAN_TEST` `#else` fallback hand-rolls `struct in_addr`/`sockaddr_in` definitions that can mismatch the platform layout. Fix: replace with standard POSIX `#include` directives.
2. **Receive buffer overflow** — `Datagram_GetMessage` reassembly loop `memcpy`s into `sock->receiveMessage` without checking against `NET_MAXMESSAGE`. Fix: add bounds checks at each `memcpy` site.

#### `net.h.patch` — `htonl`/`ntohl` declarations

Original `net.h` declares `htonl`/`ntohl` as `unsigned long` on non-Win/Linux/SunOS platforms. Modern headers (including Emscripten) declare them as `uint32_t`, causing a type conflict when `<arpa/inet.h>` is included. Fix: replace hand-rolled declarations with `#include <arpa/inet.h>`.

#### `world.h.patch` — `SV_RecursiveHullCheck` declaration

`chase.c` and related tracing paths call `SV_RecursiveHullCheck()` without a visible prototype in the stock headers. Older toolchains often warn and continue; newer toolchains can fail builds (especially with stricter defaults). Fix: add the canonical function declaration to `world.h`.

#### `pr_edict.c.patch` — value/global string functions

`PR_ValueString()`, `PR_UglyValueString()`, and `PR_GlobalString()` use `sprintf` to format arbitrarily-long progs strings into 128–256 byte buffers. Fix: replace with `snprintf(line, sizeof(line), ...)`.

#### `sv_main.c.patch` — `SV_SendServerinfo` format string

`SV_SendServerinfo()` passes a progs string directly as the format argument to `sprintf`:

```c
sprintf(message, pr_strings + sv.edicts->v.message);
```

Format specifiers (`%s`, `%n`) in a malicious BSP/mod produce UB or controllable writes. Fix: pass as `"%s"` argument + `snprintf` for the 2048-byte buffer.

## 64-bit Portability Patches

Patches for pointer arithmetic bugs that only manifest on 64-bit. On 32-bit, pointer differences fit in `int` by coincidence; on 64-bit they truncate to garbage. Applied automatically when the build system detects a 64-bit target (or `BITS=64`), independent of the `BUGFIX` flag.

| Patch | Severity | Fix |
|-------|----------|-----|
| `64bit/net_dgrm.c.64bit.patch` | crash | `unsigned long` struct sizing breaks on 64-bit |
| `64bit/pr_cmds.c.64bit.patch` | crash / corrupt strings | `pr_string_temp - pr_strings` truncates |
| `64bit/host_cmd.c.64bit.patch` | crash / corrupt names | `host_client->name - pr_strings` truncates |
| `64bit/sv_main.c.64bit.patch` | crash / corrupt level info | `worldmodel->name - pr_strings` truncates |

#### `64bit/net_dgrm.c.64bit.patch` — POSIX struct sizing

`BAN_TEST` fallback defines `struct in_addr` with `unsigned long S_addr` — 4 bytes on 32-bit, 8 bytes on 64-bit. The oversized struct breaks all address comparisons. Skipped automatically when `BUGFIX=1` (superseded by the full `net_dgrm.c.patch`).

#### `64bit/pr_cmds.c.64bit.patch` — `pr_string_temp` offset

`PF_ftos()`, `PF_vtos()`, `PF_etos()` return `pr_string_temp - pr_strings` as a 32-bit progs offset. On 64-bit, `pr_string_temp` (BSS) and `pr_strings` (Hunk) can be far apart, truncating the difference. Fix: allocate the temp buffer inside the Hunk via lazy-init so the offset fits in `int`.

#### `64bit/host_cmd.c.64bit.patch` — netname offset

`Host_Name_f()` sets `edict->v.netname = host_client->name - pr_strings`. The `client_t` name lives outside the progs heap, so on 64-bit the subtraction truncates — corrupt player names or crash. Fix: copy the name into the progs heap with `ED_NewString()`.

#### `64bit/sv_main.c.64bit.patch` — worldmodel/mapname offsets

`SV_SpawnServer()` computes progs offsets by subtracting `pr_strings` from pointers outside the progs heap (`sv.worldmodel->name`, `sv.name`, `sv.startspot`). All truncate on 64-bit. Fix: copy each string into the progs heap with `ED_NewString()`.
