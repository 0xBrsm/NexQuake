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

void WASM_OnOpen (const char *transport)
{
	WASM_Log (WASM_LOG_INFO, "%s: connected", transport);
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
	WASM_Log (expected ? WASM_LOG_INFO : WASM_LOG_WARN, "%s: %s", transport,
		expected ? "disconnected at user request" : "disconnected unexpectedly");
	NET_InvalidateHostCache ();
}

//----------------------------------------------------------------------------
// Transport registry. Ordered by preference.

extern const wasm_transport_t wasm_ws_transport;

static const wasm_transport_t * const transports[] = {
	&wasm_ws_transport,
};

static const int transport_count = sizeof(transports) / sizeof(transports[0]);

//----------------------------------------------------------------------------
// Transport selection. The ordered registry defines transport preference; this
// layer only walks that list. On disconnect+reconnect, keep the previously-
// selected transport when it still starts cleanly. Handshake waits live in
// WASM_SendPacket so Asyncify unwinds during emscripten_sleep never race with
// emscripten_set_main_loop.

static const wasm_transport_t *active = NULL;
static int active_index = -1;
static qboolean started = false;

static qboolean start_transport_from (int first)
{
	int i;

	for (i = first; i < transport_count; i++)
	{
		const wasm_transport_t *transport = transports[i];
		if (!transport->is_available ()) continue;
		active = transport;
		active_index = i;
		if (active->start () == 0) { started = true; return true; }
	}
	return false;
}

qboolean WASM_EnsureTransportOpen (void)
{
	if (active && active->tick) active->tick ();
	if (active && active->is_ready ()) return true;

	if (!started)
	{
		return start_transport_from (active_index < 0 ? 0 : active_index);
	}

	// Started but current transport died. Fall forward through the registry; if
	// there is no later transport, retry the current one once.
	if (active->is_closed ())
	{
		started = false;
		if (start_transport_from (active_index + 1)) return true;
		return start_transport_from (active_index);
	}

	// Started, handshaking — caller's Send path will wait for ready.
	return true;
}

void WASM_CloseTransport (void)
{
	if (active) active->close ();
	started = false;
	// Keep `active` — reopen uses the same transport.
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
		if (!WASM_EnsureTransportOpen ()) return -1;
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
