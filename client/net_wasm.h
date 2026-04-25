/*
 * net_wasm.h — Portable browser transport shell for Quake-on-WASM projects.
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * Transport-level primitives only:
 *   - Pluggable backend vtable (wasm_backend_t) for browser substrates
 *   - Logging bridge to the JS console
 *   - Opaque send/receive of raw byte buffers
 *
 * Handshake waits live HERE, in WASM_SendPacket, never on the init path —
 * so Asyncify unwinds during emscripten_sleep don't race with
 * emscripten_set_main_loop. A project-specific protocol adapter (e.g.
 * net_nqchan.c) implements Quake's net_landriver_t on top of these
 * primitives and defines WASM_OnPacket to receive parsed bytes.
 */

#ifndef NEXQUAKE_NET_WASM_H
#define NEXQUAKE_NET_WASM_H

#define WASM_MAX_FRAME_SIZE (NET_DATAGRAMSIZE + 2)

enum { WASM_LOG_INFO = 0, WASM_LOG_WARN = 1, WASM_LOG_ERROR = 2 };

void WASM_Log (int level, const char *fmt, ...);

// Lifecycle hooks — each backend calls these on its underlying event or
// polled state transition. `transport` is the display name (matches
// wasm_backend_t.name). Shared behavior (logging format, open-hook fire,
// NET_InvalidateHostCache) lives here so every backend stays identical.
void WASM_OnOpen  (const char *transport);
void WASM_OnError (const char *transport, const char *reason);
void WASM_OnClose (const char *transport, qboolean expected);

// Backend interface. Each substrate is a bag of function pointers with these
// exact semantics: start() is non-blocking, send_raw() never sleeps. All
// waiting happens one layer up in WASM_SendPacket. Adding a new substrate =
// filling out this struct and exporting a wasm_<name>_backend instance.
typedef struct {
	qboolean (*is_available) (void); // runtime capability check
	qboolean (*is_ready) (void);     // connected and able to send
	qboolean (*is_closed) (void);    // failed or torn down; won't recover
	int (*start) (void);             // kick off connect; non-blocking; 0 = started, -1 = unavailable
	int (*send_raw) (const byte *frame, int len); // precondition: is_ready()
	void (*close) (void);
	void (*tick) (void);             // optional per-poll hook (e.g. drain JS queue); may be NULL
	const char *(*last_error) (void);
	const char *name;
} wasm_backend_t;

extern const wasm_backend_t wasm_ws_backend;

qboolean    WASM_EnsureTransportOpen (void);
void        WASM_CloseTransport (void);
int         WASM_SendPacket (const byte *packet, int len);
const char *WASM_LastSendError (void);

// Provided by the protocol adapter; substrates call this on receive.
void WASM_OnPacket (const byte *packet, int length);

#endif
