/*
 * net_wasm.h — Portable browser transport shell for Quake-on-WASM projects.
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * Transport-level primitives only:
 *   - Pluggable transport vtable (wasm_transport_t) for browser substrates
 *   - Ordered transport registry in net_wasm.c
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

// Lifecycle hooks — each transport calls these on its underlying event or
// polled state transition. `transport` is the display name (matches
// wasm_transport_t.name). Shared behavior (logging format, open-hook fire)
// lives here so every transport stays identical.
void WASM_OnOpen  (const char *transport);
void WASM_OnError (const char *transport, const char *reason);
void WASM_OnClose (const char *transport, qboolean expected);

// Transport interface. Each substrate is a bag of function pointers with these
// exact semantics: start() is non-blocking, send_raw() never sleeps. All
// waiting happens one layer up in WASM_SendPacket. Adding a new substrate =
// filling out this struct, exporting a wasm_<name>_transport instance, and
// adding it to the registry in net_wasm.c.
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
} wasm_transport_t;

// Maintenance poll: keeps warm sessions alive, reaps a dead active
// transport, and adopts a ready upgrade between connections. Never starts
// the baseline and never fails — sendability is the send path's concern.
void        WASM_EnsureTransportOpen (void);
void        WASM_CloseTransport (void);
int         WASM_SendPacket (const byte *packet, int len);
const char *WASM_LastSendError (void);

// Provided by the protocol adapter; substrates call this on receive.
void WASM_OnPacket (const byte *packet, int length);

// Provided by the protocol adapter; called by the transport shell whenever
// it switches substrates (idle upgrade or fall-forward). The adapter should
// drop transient per-transport state (e.g. buffered receive frames) but
// preserve durable routing state used by the *next* connection — a live
// game connection does not survive a switch (the replacement session gets a
// fresh VirtualIP), which is why the shell prefers switching while idle.
void WASM_OnTransportReset (void);

#endif
