/*
 * net_wasm.c — Portable browser transport shell. See net_wasm.h.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

#include "quakedef.h"

#include <stdarg.h>
#include <stdio.h>

#include <emscripten/emscripten.h>

#include "net_wasm.h"

EM_JS (void, WASM_JsLog, (int level, const char *msg), {
	var text = UTF8ToString(msg);
	if (typeof console === 'undefined' || !console) return;
	if (level == 2 && console.error) { console.error(text); return; }
	if (level == 1 && console.warn) { console.warn(text); return; }
	if (console.info) console.info(text); else if (console.log) console.log(text);
});

void WASM_Log (int level, const char *fmt, ...)
{
	char msg[224];
	va_list args;
	va_start (args, fmt);
	vsnprintf (msg, sizeof(msg), fmt, args);
	va_end (args);
	WASM_JsLog (level, msg);
}

EM_JS (void, WASM_FireOpenHook, (), {
	if (typeof Module.onWasmTransportOpen !== 'function') return;
	try { Module.onWasmTransportOpen(); }
	catch (e) { if (console.warn) console.warn('onWasmTransportOpen failed:', e); }
});

// Connection-level messages (DEC-018): the player sees one story about the
// connection, not two competing per-transport streams — "Connected by X"
// when a carrier comes up fresh, "Connection upgraded to WebTransport" as a
// single line covering both halves of a promotion, "Connection closed ..."
// with the carrier in parens. These go to the *browser* console, not the game
// console: a plain console.info can't re-enter the renderer, so it stays safe
// even when a transport callback fires while a C frame is Asyncify-suspended —
// unlike a game-console draw, which is why this no longer routes via Cbuf.
static void ConnectionEcho (const char *line)
{
	WASM_Log (WASM_LOG_INFO, "%s", line);
}

// Mirror the active transport to the browser shell, which shows it as a
// lower-right "Connected by: <transport>" indicator (shell 00-core.js +
// shell-ui.css). Called from AdoptTransport on the main loop — never from a
// transport JS callback — so this synchronous DOM update can't re-enter a
// suspended engine frame. An empty name hides the indicator (disconnected).
EM_JS (void, WASM_NotifyTransport, (const char *name), {
	var n = name ? UTF8ToString (name) : "";
	if (typeof Module.nqSetTransport === "function")
		try { Module.nqSetTransport (n); } catch (e) {}
});

// Set when an adoption already told the story, so the transport's own
// ready-edge / close event doesn't repeat or contradict it.
static const char *announced_open = NULL;
static const char *suppress_close = NULL;

void WASM_OnOpen (const char *transport)
{
	char line[96];

	if (announced_open && Q_strcmp ((char *)announced_open, (char *)transport) == 0)
		announced_open = NULL;
	else
	{
		snprintf (line, sizeof(line), "Connected by %s", transport);
		ConnectionEcho (line);
	}
	WASM_FireOpenHook ();
}

void WASM_OnError (const char *transport, const char *reason)
{
	if (reason && reason[0])
		WASM_Log (WASM_LOG_ERROR, "%s: error: %s", transport, reason);
	else
		WASM_Log (WASM_LOG_ERROR, "%s: error", transport);
}

void WASM_OnClose (const char *transport, qboolean expected)
{
	char line[96];

	if (suppress_close && Q_strcmp ((char *)suppress_close, (char *)transport) == 0)
		suppress_close = NULL; // teardown half of an upgrade, already reported
	else
	{
		snprintf (line, sizeof(line), "Connection closed %s (%s)",
			expected ? "by client" : "unexpectedly", transport);
		ConnectionEcho (line);
		// Unexpected closes also log synchronously: the echo only prints if
		// the main loop keeps draining Cbuf, and a disconnect coinciding
		// with a wedged engine is exactly when the evidence matters.
		if (!expected)
			WASM_Log (WASM_LOG_WARN, "%s", line);
	}
	// The hostcache is owned by the always-on SSE stream (DEC-020), not the
	// game carrier — leave it alone. Invalidating it here was a pre-SSE relic
	// (when the server list rode the carrier); now that the carrier is torn
	// down on every disconnect, clearing it would wrongly empty an SSE-fed
	// list that only repopulates on its next *change*.
}

//----------------------------------------------------------------------------
// Transport registry. Ordered by preference.

extern const wasm_transport_t wasm_wt_transport;
extern const wasm_transport_t wasm_ws_transport;

static const wasm_transport_t * const transports[] = {
	&wasm_wt_transport,
	&wasm_ws_transport,
};

static const int transport_count = sizeof(transports) / sizeof(transports[0]);

// net_transport: client preference for the carrier. "auto" (default) prefers
// WebTransport (UDP) and falls back to WebSocket (TCP); "tcp" forces WebSocket;
// "udp" forces WebTransport with no fallback (won't connect where UDP/QUIC is
// blocked). Not archived: when "auto" tries UDP and it is black-holed, the
// connect path flips this to "tcp" for the rest of the session (see
// EnsureSendableTransport), and leaving it unarchived means the next launch
// starts at "auto" and retries UDP fresh. The choice is frozen mid-connection;
// a change applies on the next connect.
cvar_t net_transport = {"net_transport", "auto", false};

// net_timeout: milliseconds to wait for a carrier (either protocol) to finish
// its handshake and become sendable before the send fails and retries. The
// general connection-establishment bound, used for both WebSocket and
// WebTransport. Archived.
cvar_t net_timeout = {"net_timeout", "2000", true};

// net_udp_failover: milliseconds "auto" waits for WebTransport (UDP) at connect
// before failing over to WebSocket (TCP). Only "auto" fails over, so this is
// paid until a timeout flips net_transport to "tcp" for the session; 0 fails
// over immediately. Forced "udp" never fails over — it waits net_timeout like
// any committed carrier. Archived.
cvar_t net_udp_failover = {"net_udp_failover", "400", true};

void WASM_TransportInit (void)
{
	Cvar_RegisterVariable (&net_transport);
	Cvar_RegisterVariable (&net_timeout);
	Cvar_RegisterVariable (&net_udp_failover);
}

// 0 = auto (any), 1 = tcp (WebSocket only), 2 = udp (WebTransport only)
static int wasm_transport_mode (void)
{
	if (!Q_strcasecmp (net_transport.string, "tcp")) return 1;
	if (!Q_strcasecmp (net_transport.string, "udp")) return 2;
	return 0;
}

static qboolean transport_allowed (int idx)
{
	switch (wasm_transport_mode ())
	{
	case 1:  return transports[idx] == &wasm_ws_transport;
	case 2:  return transports[idx] == &wasm_wt_transport;
	default: return true;
	}
}

//----------------------------------------------------------------------------
// Transport selection. The ordered registry defines preference; index 0 is the
// preferred carrier (WebTransport/UDP) and the last entry is the baseline (the
// always-works substrate, WebSocket/TCP). Selection is lazy and per-connection:
// the carrier is brought up by the first send (EnsureSendableTransport) and
// torn down on disconnect, so every connect re-selects from scratch. In "auto"
// the preferred carrier gets a bounded head-start (net_udp_failover) to land
// its handshake before falling back to the baseline. Handshake waits live in
// WASM_SendPacket, so the Asyncify unwinds during emscripten_sleep never race
// with emscripten_set_main_loop.

static const wasm_transport_t *active = NULL;
static qboolean started = false;

static void AdoptTransport (int idx)
{
	const wasm_transport_t *t = transports[idx];
	char line[96];

	// A stale latch naming the transport being adopted can no longer be
	// consumed by its intended event — the old socket instance is gone once a
	// new one starts — and would otherwise eat the new session's first
	// open/close announcement.
	if (suppress_close && Q_strcmp ((char *)suppress_close, (char *)t->name) == 0)
		suppress_close = NULL;
	if (announced_open && Q_strcmp ((char *)announced_open, (char *)t->name) == 0)
		announced_open = NULL;

	if (t->is_ready ())
	{
		// Already-proven transport (WebTransport landed within the head-start):
		// announce now; its own ready-edge fires after adoption and stays quiet.
		announced_open = t->name;
		snprintf (line, sizeof(line), "Connected by %s", t->name);
		ConnectionEcho (line);
	}
	active = t;
	started = true;
	WASM_NotifyTransport (t->name);
}

void WASM_EnsureTransportOpen (void)
{
	if (active && active->tick) active->tick ();

	// Active carrier died mid-connection: close it so the substrate releases
	// its half (WebTransport in particular lingers as a ghost answering server
	// keepalives otherwise), drop its buffered frames, and hide the chip. The
	// next send re-establishes a carrier. Polls (ring reads, init) need no
	// transport, so nothing is started here — the carrier comes up lazily in
	// the send path.
	if (started && active->is_closed ())
	{
		active->close ();
		WASM_OnTransportReset ();
		started = false;
		WASM_NotifyTransport (""); // disconnected — hide the indicator
	}
}

// EnsureSendableTransport — the send path needs a live transport *now*.
// In "auto", bring up the preferred carrier (WebTransport/UDP, index 0) and
// give it a bounded head-start (net_udp_failover) to land its handshake; adopt
// it if it does. If it doesn't, UDP is black-holed on this network: fall back
// to the WebSocket baseline and flip net_transport to "tcp" so the rest of the
// session goes straight to WebSocket (it is unarchived, so the next launch
// retries UDP). Forced "tcp"/"udp" never auto-flip. Failure to start any
// transport is a hard misconfiguration — log it loudly, since the poll path can
// no longer surface it.
static qboolean EnsureSendableTransport (void)
{
	int i, waited, wait_ms;

	if (started) return true;

	// auto only: bring up the preferred carrier and wait briefly for it.
	if (wasm_transport_mode () == 0 && transports[0]->is_available () &&
		transports[0]->start () == 0)
	{
		wait_ms = (int)net_udp_failover.value;
		for (waited = 0;
			!transports[0]->is_ready () && !transports[0]->is_closed () &&
				waited < wait_ms;
			waited += 10)
			emscripten_sleep (10);

		if (transports[0]->is_ready ())
		{
			AdoptTransport (0);
			return true;
		}

		// UDP unreachable here — reap the handshake and degrade for the session.
		transports[0]->close ();
		Cvar_Set ("net_transport", "tcp");
	}

	// Baseline = lowest-preference allowed transport: WebSocket for auto/tcp,
	// WebTransport for udp (which has no TCP fallback, by design).
	for (i = transport_count - 1; i >= 0 && !transport_allowed (i); i--)
		;
	if (i < 0) i = transport_count - 1;
	if (!transports[i]->is_available () || transports[i]->start () != 0)
	{
		WASM_Log (WASM_LOG_ERROR, "no transport could start: %s",
			transports[i]->last_error ());
		return false;
	}
	AdoptTransport (i);
	return true;
}

void WASM_CloseTransport (void)
{
	int i;

	// Called on game disconnect (carrier lifetime tracks the game connection)
	// and at engine shutdown. close() is a no-op on a transport that isn't
	// running.
	for (i = 0; i < transport_count; i++)
		transports[i]->close ();
	started = false;
	WASM_NotifyTransport (""); // carrier gone — hide the transport chip
	// Keep `active` — the next connect re-selects from the registry.
}

int WASM_SendPacket (const byte *packet, int len)
{
	int attempt, attempts, waited;

	if (len < 0 || len > WASM_MAX_FRAME_SIZE) return -1;

	// One pass per registered transport gives the manager room to fall forward
	// if the current transport dies mid-handshake.
	attempts = transport_count > 0 ? transport_count : 1;
	for (attempt = 0; attempt < attempts; attempt++)
	{
		WASM_EnsureTransportOpen ();
		if (!EnsureSendableTransport ()) return -1;
		if (active->is_ready ()) return active->send_raw (packet, len);

		for (waited = 0;
			!active->is_ready () && !active->is_closed () && waited < (int)net_timeout.value;
			waited += 10)
			emscripten_sleep (10);

		if (active->is_ready ()) return active->send_raw (packet, len);
		// Closed while waiting; next loop iteration advances the transport.
	}
	return -1;
}

const char *WASM_LastSendError (void)
{
	return active ? active->last_error () : "no transport";
}
