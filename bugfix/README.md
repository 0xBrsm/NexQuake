# Upstream WinQuake Bugfix Patches

Optional patches for bugs identified by auditing the original id Software
WinQuake source code. These target the vanilla codebase and contain no
NexQuake-specific changes. Applied by default during `prepare-upstream.sh`;
set `BUGFIX=0` to opt out.

Similar fixes exist in major Quake source ports (Quakespasm, TyrQuake, Mark V),
which confirms these are well-understood issues.

---

## common.c — `COM_FileBase` undefined behavior

**File:** `common.c.patch`
**Severity:** crash / undefined behavior

The original `COM_FileBase()` walks a pointer backward through the input string
looking for a `/` separator, but the loop condition (`*s2 && *s2 != '/'`) tests
for null while decrementing — it can walk before the start of the string,
producing undefined behavior or a crash. There is also an off-by-one in the
`strncpy` length calculation. The fix replaces the hand-rolled pointer
arithmetic with `strrchr()` calls and adds a null/empty input guard.

## common.c — `COM_Parse` buffer overflow

**File:** `com_parse.c.patch`
**Severity:** buffer overflow (remotely exploitable)

`COM_Parse()` writes tokens into the global `com_token` buffer (1024 bytes)
with no length check. Both the quoted-string path and the regular-word path
will happily write past the end of the buffer. This is exploitable via
malicious network data (crafted `.ent` files, `stuffcmd`, or progs strings).
The fix adds `sizeof(com_token)` bounds checks to both code paths.

## host_cmd.c — save/load path overflows and `Host_Say` buffer overflow

**File:** `host_cmd.c.patch`
**Severity:** buffer overflow (local console + remotely exploitable via cvar)

Four related overflows in host_cmd.c:

1. `Host_SavegameComment()` copies `cl.levelname` into the comment buffer via
   `memcpy` without checking its length against `SAVEGAME_COMMENT_LENGTH`.
2. `Host_Savegame_f()` uses `sprintf(name, "%s/%s", ...)` into a 256-byte
   buffer — a long save name from the console overflows it.
3. `Host_Loadgame_f()` uses the same unbounded `sprintf` for the load path.
4. `Host_Say()` uses `sprintf` to format a chat message into a 64-byte `text`
   buffer. The `hostname.string` cvar has no length limit, so a long hostname
   overflows the buffer before the subsequent truncation check has a chance
   to run.

The fix clamps the `memcpy` length and replaces `sprintf` with `snprintf`.

## net_dgrm.c — three networking bugs

**File:** `net_dgrm.c.patch`
**Severity:** buffer overflow (remotely exploitable) / connection handling

Three fixes in the datagram network layer:

1. **BAN_TEST POSIX struct portability.** The `#else` fallback for non-Windows
   builds manually defines `struct in_addr`, `struct sockaddr_in`, `AF_INET`,
   and declares `inet_ntoa`/`inet_addr`. These hand-rolled definitions are
   fragile and can cause problems when the struct layout does not match the
   platform. The fix replaces them with standard POSIX `#include` directives.

2. **`AddrCompare` connection matching (NAT bug).** `UDP_AddrCompare()` returns
   0 for an exact match, 1 for same-IP-different-port, and -1 for different IP.
   The original check `if (ret >= 0)` treats "same IP, different port" as a
   reconnection, so a second player behind the same NAT (same public IP,
   different source port) kicks the first player. The fix changes the check to
   `if (ret == 0)`.

3. **`Datagram_GetMessage` receive buffer overflow.** The packet reassembly
   loop `memcpy`s incoming data into `sock->receiveMessage` without checking
   whether the accumulated length exceeds `NET_MAXMESSAGE`. A malicious or
   malformed oversized packet can overflow the buffer. The fix adds bounds
   checks at each `memcpy` site.

## net.h — `htonl`/`ntohl` declaration conflict with standard headers

**File:** `net.h.patch`
**Severity:** build failure (Emscripten / non-Linux POSIX platforms)

The original `net.h` contains fallback `extern unsigned long htonl(...)` / `ntohl`
declarations for platforms that are not Windows, Linux, or SunOS. These
declarations use `unsigned long`, but modern platform headers (including
Emscripten's `<arpa/inet.h>`) declare them as `uint32_t`. When the
`net_dgrm.c` POSIX struct fix (above) adds `#include <arpa/inet.h>`, both
declarations are visible and the compiler rejects the type mismatch. The fix
replaces the hand-rolled declarations with `#include <arpa/inet.h>`.

## pr_edict.c — `sprintf` overflows in value/global string functions

**File:** `pr_edict.c.patch`
**Severity:** buffer overflow (triggerable from progs.dat)

`PR_ValueString()` and `PR_UglyValueString()` use `sprintf(line, "%s", ...)`
to format an `ev_string` value from `progs.dat` into a static 256-byte buffer.
Since progs strings can be arbitrarily long, this overflows easily.
`PR_GlobalString()` is worse — it concatenates an offset number, a global name,
and the value string into a 128-byte buffer via `sprintf`. The fix replaces all
unbounded `sprintf` calls with `snprintf(line, sizeof(line), ...)`.


## sv_main.c — format string vulnerability in `SV_SendServerinfo`

**File:** `sv_main.c.patch`
**Severity:** format string vulnerability (exploitable from malicious BSP/mod)

`SV_SendServerinfo()` uses a progs string (the worldspawn `message` field) as
the **format argument** to `sprintf`:

```c
sprintf (message, pr_strings+sv.edicts->v.message);
```

If the level's worldspawn message contains printf format specifiers like `%s`
or `%n`, this produces undefined behavior — at best a crash, at worst a
controllable write. The progs string can originate from a malicious `.bsp` or
mod. The fix passes the string as a `"%s"` argument instead and uses `snprintf`
to bounds-check the 2048-byte buffer.

---

# 64-bit Portability Patches (`64bit/`)

Patches for pointer arithmetic bugs in the original WinQuake source that only
manifest when compiling for 64-bit platforms. On 32-bit systems these work by
coincidence (pointer differences fit in `int`); on 64-bit systems they produce
truncated garbage offsets and crash or corrupt data.

These are applied automatically by the build system when a 64-bit server build
is detected, regardless of the `BUGFIX=1` flag.

## net_dgrm.c — hand-rolled POSIX struct sizing

**File:** `64bit/net_dgrm.c.64bit.patch`
**Severity:** crash (64-bit only)

The `BAN_TEST` `#else` fallback manually defines `struct in_addr` using
`unsigned long S_addr`. On 32-bit, `unsigned long` is 4 bytes and matches
the real struct. On 64-bit, `unsigned long` is 8 bytes, making the struct
twice as large as the real one — all network address comparisons and packet
handling break completely.

**Note:** This fix overlaps with `net_dgrm.c.patch` (Fix 1 — POSIX struct
portability). When `BUGFIX=1`, the build script skips this patch automatically.

## pr_cmds.c — `pr_string_temp` pointer arithmetic truncation

**File:** `64bit/pr_cmds.c.64bit.patch`
**Severity:** crash / corrupt strings (64-bit only)

`PF_ftos()`, `PF_vtos()`, and `PF_etos()` format values into a global
`char pr_string_temp[128]` buffer in BSS, then return a progs string offset:

```c
G_INT(OFS_RETURN) = pr_string_temp - pr_strings;
```

On 64-bit, `pr_string_temp` (BSS) and `pr_strings` (Hunk) can be far apart in
memory. The pointer difference exceeds 32 bits and truncates to garbage. The
fix allocates the temp buffer inside the Hunk via a lazy-init helper so the
offset always fits in an `int`.

## host_cmd.c — netname pointer arithmetic truncation

**File:** `64bit/host_cmd.c.64bit.patch`
**Severity:** crash / corrupt player names (64-bit only)

`Host_Name_f()` sets a player's netname for the QuakeC VM:

```c
host_client->edict->v.netname = host_client->name - pr_strings;
```

`host_client->name` lives in the `client_t` struct, while `pr_strings` is a
separate Hunk allocation. On 64-bit the subtraction truncates to garbage —
other players see corrupt names or the server crashes. The fix copies the
name into the progs string heap with `ED_NewString()`.

## sv_main.c — worldmodel/mapname pointer arithmetic truncation

**File:** `64bit/sv_main.c.64bit.patch`
**Severity:** crash / corrupt level info (64-bit only)

`SV_SpawnServer()` sets progs global fields by subtracting `pr_strings` from
pointers outside the progs heap:

```c
ent->v.model       = sv.worldmodel->name - pr_strings;
pr_global_struct->mapname   = sv.name - pr_strings;
pr_global_struct->startspot = sv.startspot - pr_strings;  // Quake2
```

On 64-bit these pointer differences truncate to garbage offsets. QuakeC accesses
to worldmodel name, mapname, or startspot then index into random memory. The fix
copies each string into the progs heap with `ED_NewString()`.

