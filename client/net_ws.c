/*
 * net_ws.c — WebSocket transport for net_wasm.c.
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * Fills out wasm_ws_transport. Non-blocking start(); send_raw() assumes
 * is_ready() per the contract in net_wasm.h. No sleeps here — all waits
 * live in WASM_SendPacket.
 *
 * Derivative work; see ../ATTRIBUTIONS.md.
 */

#include "quakedef.h"

#include <stdint.h>
#include <stdlib.h>

#include <emscripten/emscripten.h>
#include <emscripten/websocket.h>

#include "net_wasm.h"

static const char              ws_name[] = "WebSocket";
static qboolean                ws_started = false;
static qboolean                ws_connected = false;
static qboolean                ws_close_requested = false;
static EMSCRIPTEN_WEBSOCKET_T  ws;
static const char             *ws_last_error = "";

// Headless: Module.websocketUrl / Module.WEBSOCKET_URL.
// Browser:  ws(s)://<location.host>/connect.
EM_JS (char *, WS_ConnectUrl, (), {
	var override = Module.websocketUrl || Module.WEBSOCKET_URL || "";
	if (override) return stringToNewUTF8(String(override));
	if (typeof location !== 'undefined' && location.host) {
		var proto = (location.protocol === 'https:') ? 'wss:' : 'ws:';
		return stringToNewUTF8(proto + '//' + location.host + '/connect');
	}
	return 0;
});

static EM_BOOL WS_OnOpen (int eventType, const EmscriptenWebSocketOpenEvent *evt, void *userData)
{
	(void)eventType; (void)evt;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws) return EM_TRUE;
	ws_connected = true;
	WASM_OnOpen (ws_name);
	return EM_TRUE;
}

static EM_BOOL WS_OnError (int eventType, const EmscriptenWebSocketErrorEvent *evt, void *userData)
{
	(void)eventType; (void)evt;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws) return EM_TRUE;
	WASM_OnError (ws_name, NULL);
	return EM_TRUE;
}

static EM_BOOL WS_OnClose (int eventType, const EmscriptenWebSocketCloseEvent *evt, void *userData)
{
	qboolean expected;

	(void)eventType; (void)evt;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws) return EM_TRUE;

	expected = ws_close_requested;
	ws_close_requested = false;
	ws_started = false;
	ws_connected = false;
	ws_last_error = "";
	WASM_OnClose (ws_name, expected);
	// The dead handle is deleted lazily in WS_Start, never here: deleting a
	// socket from inside its own callback races the dispatcher's bookkeeping.
	return EM_TRUE;
}

static EM_BOOL WS_OnMessage (int eventType, const EmscriptenWebSocketMessageEvent *evt, void *userData)
{
	(void)eventType;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws) return EM_TRUE;
	if (evt->isText) return EM_TRUE;
	WASM_OnPacket ((const byte *)evt->data, (int)evt->numBytes);
	return EM_TRUE;
}

//----------------------------------------------------------------------------
// wasm_transport_t.

static qboolean WS_IsAvailable (void) { return true; }
static qboolean WS_IsReady (void)     { return ws_started && ws_connected; }
static qboolean WS_IsClosed (void)    { return !ws_started && !ws_connected; }

static int WS_Start (void)
{
	EmscriptenWebSocketCreateAttributes attrs;
	char *url;

	if (ws_started) return 0;

	url = WS_ConnectUrl ();
	if (!url || !url[0])
	{
		WASM_Log (WASM_LOG_ERROR, "%s: missing URL", ws_name);
		ws_last_error = "missing URL";
		free (url);
		return -1;
	}

	attrs.url = url;
	attrs.protocols = NULL;
	attrs.createOnMainThread = EM_TRUE;

	// Release the previous (dead) socket's JS object and callback
	// registrations before creating its replacement — they otherwise
	// accumulate per reconnect cycle.
	if (ws)
	{
		emscripten_websocket_delete (ws);
		ws = 0;
	}

	if ((ws = emscripten_websocket_new (&attrs)) <= 0)
	{
		WASM_Log (WASM_LOG_ERROR, "%s: start failed", ws_name);
		ws_last_error = "start failed";
		free (url);
		return -1;
	}

	ws_started = true;
	ws_connected = false;
	ws_close_requested = false;
	ws_last_error = "";

	emscripten_websocket_set_onopen_callback (ws, (void *)(uintptr_t)ws, WS_OnOpen);
	emscripten_websocket_set_onerror_callback (ws, (void *)(uintptr_t)ws, WS_OnError);
	emscripten_websocket_set_onclose_callback (ws, (void *)(uintptr_t)ws, WS_OnClose);
	emscripten_websocket_set_onmessage_callback (ws, (void *)(uintptr_t)ws, WS_OnMessage);

	free (url);
	return 0;
}

static int WS_SendRaw (const byte *frame, int len)
{
	if (emscripten_websocket_send_binary (ws, (void *)frame, (uint32_t)len) < 0)
	{
		ws_last_error = "browser send failed";
		return -1;
	}
	ws_last_error = "";
	return len;
}

static void WS_Close (void)
{
	if (!ws_started) return;
	ws_close_requested = true;
	ws_started = false;
	ws_connected = false;
	ws_last_error = "";
	emscripten_websocket_close (ws, 1000, "Closed by WS_Close");
}

static const char *WS_LastError (void)
{
	return ws_last_error[0] ? ws_last_error : "unknown error";
}

const wasm_transport_t wasm_ws_transport = {
	WS_IsAvailable, WS_IsReady, WS_IsClosed,
	WS_Start, WS_SendRaw, WS_Close,
	NULL, // no per-poll tick; onmessage pushes to WASM_OnPacket directly
	WS_LastError, ws_name
};
