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
	NET_InvalidateHostCache ();
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

// cl_transport: client preference for the carrier. "auto" (default) prefers
// WebTransport (UDP) and falls back to WebSocket (TCP); "tcp" forces WebSocket;
// "udp" forces WebTransport with no fallback (won't connect where UDP/QUIC is
// blocked). Archived. The choice is frozen mid-connection; a change applies on
// the next connect.
cvar_t cl_transport = {"cl_transport", "auto", true};

void WASM_TransportInit (void)
{
	Cvar_RegisterVariable (&cl_transport);
}

// 0 = auto (any), 1 = tcp (WebSocket only), 2 = udp (WebTransport only)
static int wasm_transport_mode (void)
{
	if (!Q_strcasecmp (cl_transport.string, "tcp")) return 1;
	if (!Q_strcasecmp (cl_transport.string, "udp")) return 2;
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
// Transport selection. The ordered registry defines preference; the last
// entry is the baseline (the always-works substrate the send path may wait
// on). Non-baseline transports are warm-kept: their sessions start in the
// background at init and dead ones restart on a timer, but a session is only
// ever *adopted* once is_ready() says its handshake already landed — nothing
// waits on an unproven endpoint (the manifest can advertise WebTransport,
// but UDP-blocking networks, CDN hostnames, or a pending permission prompt
// can make it unreachable for this client). While a game socket is open the
// choice is frozen; between connections it is re-evaluated, so a warm
// transport that lands late is picked up by the next connection. Handshake
// waits live in WASM_SendPacket so Asyncify unwinds during emscripten_sleep
// never race with emscripten_set_main_loop.

static const wasm_transport_t *active = NULL;
static int active_index = -1;
static qboolean started = false;

#define WASM_WARM_RETRY_SEC 20.0

static qboolean warm_started[sizeof(transports) / sizeof(transports[0])];
static double   warm_retry_at[sizeof(transports) / sizeof(transports[0])];

// Keep non-baseline transports' sessions alive in the background. Warm
// transports are never ticked here — their frames stay queued JS-side until
// adoption, so a parallel session can't leak identity frames into the
// active one. The active transport's lifecycle belongs to selection below.
static void KeepWarmTransports (void)
{
	double now = -1.0; // fetched lazily — this runs per poll, the clock is a JS crossing
	int i;

	for (i = 0; i < transport_count - 1; i++)
	{
		const wasm_transport_t *t = transports[i];

		if (!transport_allowed (i)) continue;
		if ((t == active && started) || !t->is_available ()) continue;
		if (warm_started[i] && !t->is_closed ()) continue; // pending or ready
		if (now < 0) now = Sys_FloatTime ();
		if (now < warm_retry_at[i]) continue;
		if (warm_started[i]) t->close (); // reap the dead session first
		warm_started[i] = (t->start () == 0);
		warm_retry_at[i] = now + WASM_WARM_RETRY_SEC;
	}
}

static void AdoptTransport (int idx)
{
	const wasm_transport_t *t = transports[idx];
	char line[96];

	// A stale latch naming the transport being (re)adopted can no longer be
	// consumed by its intended event — the old socket instance is gone once a
	// new one starts — and would otherwise eat the NEW session's first
	// open/close announcement.
	if (suppress_close && Q_strcmp ((char *)suppress_close, (char *)t->name) == 0)
		suppress_close = NULL;
	if (announced_open && Q_strcmp ((char *)announced_open, (char *)t->name) == 0)
		announced_open = NULL;

	if (active && started && active != t)
	{
		// Promotion: announce one connection-level line; the old carrier's
		// close and the new one's ready-edge are both halves of this same
		// event. Close the old transport so its substrate releases the
		// connection, then drop frames it buffered — they belong to the
		// session it was carrying.
		suppress_close = active->name;
		announced_open = t->name;
		snprintf (line, sizeof(line), "Connection upgraded to %s", t->name);
		ConnectionEcho (line);
		active->close ();
		WASM_OnTransportReset ();
	}
	else if (t->is_ready ())
	{
		// Fresh adoption of an already-proven transport (warm WebTransport):
		// announce now; its ready-edge fires after adoption and stays quiet.
		announced_open = t->name;
		snprintf (line, sizeof(line), "Connected by %s", t->name);
		ConnectionEcho (line);
	}
	active = t;
	active_index = idx;
	started = true;
	WASM_NotifyTransport (t->name);
}

void WASM_EnsureTransportOpen (void)
{
	int i;

	KeepWarmTransports ();

	if (active && active->tick) active->tick ();

	// Active transport died: release it and re-select. Close it explicitly so
	// the substrate tears down its half (WebTransport in particular lingers as
	// a ghost session answering server keepalives otherwise), drop its
	// buffered frames, and put its warm slot on backoff so selection doesn't
	// instantly re-adopt the corpse.
	if (started && active->is_closed ())
	{
		active->close ();
		WASM_OnTransportReset ();
		started = false;
		WASM_NotifyTransport (""); // disconnected — hide the indicator
		if (active_index < transport_count - 1)
		{
			warm_started[active_index] = false;
			warm_retry_at[active_index] = Sys_FloatTime () + WASM_WARM_RETRY_SEC;
		}
	}

	// Between connections, adopt the highest-priority transport that is ready
	// right now (a warm session that landed since the last pick). Never tear
	// down a mid-handshake active to do it — closing a still-connecting
	// WebSocket trips browser warnings and races the Asyncify-suspended send
	// path; a non-ready active either finishes (upgrade next poll) or dies
	// (death path above).
	if (!started || (WASM_TransportIdle () && active_index > 0 && active->is_ready ()))
	{
		for (i = 0; i < (started ? active_index : transport_count); i++)
		{
			if (transport_allowed (i) && transports[i]->is_available () && transports[i]->is_ready ())
			{
				AdoptTransport (i);
				break;
			}
		}
	}

	// No transport is started here on purpose: polls (ring reads, init)
	// don't need one. The baseline starts lazily in the send path, so a warm
	// WebTransport that lands within the first seconds is adopted directly
	// and the WebSocket never has to connect at all.
}

// EnsureSendableTransport — the send path needs a live transport *now*.
// WASM_EnsureTransportOpen (called just before, with no Asyncify yield in
// between) already adopted any ready transport, so the only remaining move
// is starting the baseline (same-origin, reliable) without waiting on
// anything else. Failure here is a hard misconfiguration — log it loudly,
// since the poll path can no longer surface it.
static qboolean EnsureSendableTransport (void)
{
	int i;

	if (started) return true;

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

	// Shutdown closes everything, including warm background sessions. Each
	// transport's close() is a no-op when it isn't running.
	for (i = 0; i < transport_count; i++)
	{
		transports[i]->close ();
		warm_started[i] = false;
		warm_retry_at[i] = 0;
	}
	started = false;
	// Keep `active` — reopen re-evaluates from the registry as usual.
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
			!active->is_ready () && !active->is_closed () && waited < 2000;
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
