/*
Added by initialed85
*/
// net_websocket.c
// Modified by WebQuake:
// - Dynamic WebSocket URL (derived from window.location, with optional override)
// - No UUID hello frame (gateway is a simple datagram tunnel)
// - Avoid <netinet/in.h> prototype conflicts under Emscripten

#include "quakedef.h"

#include <sys/types.h>
#include <sys/socket.h>
#include <stdint.h>
#include <stdlib.h>

#include <emscripten/emscripten.h>
#include <emscripten/websocket.h>

#include "net_websocket.h"

#define DEFAULT_WEBSOCKET_URL "ws://localhost:7071/ws"
// Incoming WebSocket frames are queued here until the Quake net loop drains them.
// In browsers, the main loop can be throttled (background tabs, long frames),
// while WebSocket events may still deliver bursts of packets. A small ring buffer
// can overflow and force disconnects. Use a larger queue and drop the oldest
// packet on overflow (mirrors how UDP socket buffers behave under load).
#define MAX_WS_MESSAGES 2048

// For NetQuake datagrams, payloads should fit in NET_DATAGRAMSIZE.
#define MAX_WS_MESSAGE_SIZE NET_DATAGRAMSIZE

EM_JS(char *, WebSocket_GetUrl, (), {
	try {
		var g = (typeof globalThis !== "undefined")
			? globalThis
			: ((typeof self !== "undefined") ? self : window);

		// Optional overrides:
		// - ?ws=ws(s)://host:port/ws
		// - window.WEBSOCKET_URL
		// - Module.websocketUrl / Module.WEBSOCKET_URL
		var search = "";
		if (typeof g.location !== "undefined" && g.location && g.location.search) {
			search = g.location.search;
		}
		var params = new URLSearchParams(search || "");
		var module = g.Module || {};
		var override =
			params.get("ws") ||
			g.WEBSOCKET_URL ||
			module.websocketUrl ||
			module.WEBSOCKET_URL;
		if (override) {
			return stringToNewUTF8(override);
		}

		var loc = g.location;
		if (!loc || !loc.host) {
			return stringToNewUTF8(DEFAULT_WEBSOCKET_URL);
		}

		var wsProto = (loc.protocol === "https:") ? "wss:" : "ws:";
		return stringToNewUTF8(wsProto + "//" + loc.host + "/ws");
	} catch (e) {
		return stringToNewUTF8(DEFAULT_WEBSOCKET_URL);
	}
});

typedef struct
{
	unsigned int length;
	byte data[MAX_WS_MESSAGE_SIZE];
} WsMessage;

extern cvar_t hostname;

static int net_controlsocket;
static unsigned long myAddr;
static qboolean ws_opened = false;
static qboolean ws_onopen_handled = false;
static qboolean ws_close_requested = false;
static EMSCRIPTEN_WEBSOCKET_T ws;

struct
{
	uint16_t read_index;
	uint16_t write_index;
	WsMessage messages[MAX_WS_MESSAGES];
} wsMessages = {0};

static void WebSocket_ResetMessageQueue(void)
{
	wsMessages.read_index = 0;
	wsMessages.write_index = 0;
}

static void WebSocket_CloseUnderlying(void)
{
	if (!ws_opened)
		return;

	ws_close_requested = true;
	ws_opened = false;
	ws_onopen_handled = false;
	WebSocket_ResetMessageQueue();

	emscripten_websocket_close(ws, 1000, "Closed by WebSocket_CloseSocket");
}

static void WebSocket_FillDummyAddr(struct qsockaddr *addr)
{
	if (!addr)
		return;

	// WebSocket landriver doesn't have a real UDP endpoint address, but NetQuake
	// validates reply addresses. Provide a stable dummy sockaddr.
	Q_memset(addr, 0, sizeof(*addr));
	addr->sa_family = AF_INET;
	// sockaddr_in layout (without including <netinet/in.h>):
	// - sa_data[0..1] = port (big endian)
	// - sa_data[2..5] = IPv4 address
	// Use a fixed dummy endpoint 6.9.42.0:13337.
	addr->sa_data[0] = 0x34;
	addr->sa_data[1] = 0x19;
	addr->sa_data[2] = 6;
	addr->sa_data[3] = 9;
	addr->sa_data[4] = 42;
	addr->sa_data[5] = 0;
}

//=============================================================================

EM_BOOL _WebSocket_onopen(int eventType, const EmscriptenWebSocketOpenEvent *websocketEvent, void *userData)
{
	(void)eventType;
	(void)websocketEvent;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws)
		return EM_TRUE;

	ws_onopen_handled = true;
	Sys_Printf("_WebSocket_onopen: connected\n");
	return EM_TRUE;
}

EM_BOOL _WebSocket_onerror(int eventType, const EmscriptenWebSocketErrorEvent *websocketEvent, void *userData)
{
	(void)eventType;
	(void)websocketEvent;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws)
		return EM_TRUE;

	Sys_Printf("_WebSocket_onerror: failed (see browser console)\n");
	return EM_TRUE;
}

EM_BOOL _WebSocket_onclose(int eventType, const EmscriptenWebSocketCloseEvent *websocketEvent, void *userData)
{
	(void)eventType;
	(void)websocketEvent;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws)
		return EM_TRUE;

	qboolean expected = ws_close_requested;
	ws_close_requested = false;

	ws_opened = false;
	ws_onopen_handled = false;
	WebSocket_ResetMessageQueue();

	if (!expected)
		Sys_Printf("_WebSocket_onclose: disconnected unexpectedly\n");
	else
		Sys_Printf("_WebSocket_onclose: disconnected at user request\n");

	return EM_TRUE;
}

EM_BOOL _WebSocket_onmessage(int eventType, const EmscriptenWebSocketMessageEvent *websocketEvent, void *userData)
{
	(void)eventType;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws)
		return EM_TRUE;

	if (websocketEvent->isText)
		return EM_TRUE;

	if (websocketEvent->numBytes > MAX_WS_MESSAGE_SIZE)
	{
		Sys_Printf("_WebSocket_onmessage: message too large (%d bytes, max %d); disconnecting.\n",
			websocketEvent->numBytes, MAX_WS_MESSAGE_SIZE);
		WebSocket_CloseUnderlying();
		return EM_FALSE;
	}

	// Queue packet (ring buffer).
	{
		uint16_t next = (uint16_t)((wsMessages.write_index + 1u) % MAX_WS_MESSAGES);
		if (next == wsMessages.read_index)
		{
			static int overflow_warnings = 0;
			if (overflow_warnings < 5)
			{
				int depth = (wsMessages.write_index >= wsMessages.read_index)
					? (int)(wsMessages.write_index - wsMessages.read_index)
					: (int)(wsMessages.write_index + MAX_WS_MESSAGES - wsMessages.read_index);
				Sys_Printf("_WebSocket_onmessage: wsMessages overflow (depth: %d); dropping oldest packet.\n", depth);
				Con_Printf("_WebSocket_onmessage: wsMessages overflow (depth: %d); dropping oldest packet.\n", depth);
				overflow_warnings++;
			}

			wsMessages.messages[wsMessages.read_index].length = 0;
			wsMessages.read_index = (uint16_t)((wsMessages.read_index + 1u) % MAX_WS_MESSAGES);
			next = (uint16_t)((wsMessages.write_index + 1u) % MAX_WS_MESSAGES);
		}

		wsMessages.messages[wsMessages.write_index].length = (unsigned int)websocketEvent->numBytes;
		memcpy(wsMessages.messages[wsMessages.write_index].data, websocketEvent->data, websocketEvent->numBytes);

		wsMessages.write_index = next;
	}

	return EM_TRUE;
}

//=============================================================================

int WebSocket_Init(void)
{
	struct qsockaddr addr;
	char *colon;

	if (COM_CheckParm("-nowebsocket"))
	{
		Sys_Printf("WebSocket_Init: -nowebsocket flag given (disabling WebSockets)\n");
		return -1;
	}

	if (!emscripten_websocket_is_supported())
		Sys_Error("WebSocket_Init: emscripten_websocket_is_supported() says WebSockets aren't supported\n");

	// In the browser, treat the local address as "unknown"; the WebSocket landriver
	// uses a dummy sockaddr for validation.
	myAddr = 0;

	if (Q_strcmp(hostname.string, "UNNAMED") == 0)
	{
		Cvar_Set("hostname", "webquake");
	}

	if ((net_controlsocket = WebSocket_OpenSocket(0)) == -1)
		Sys_Error("WebSocket_Init: Unable to open control socket\n");

	WebSocket_GetSocketAddr(net_controlsocket, &addr);
	Q_strcpy(my_tcpip_address, WebSocket_AddrToString(&addr));
	colon = Q_strrchr(my_tcpip_address, ':');
	if (colon)
		*colon = 0;

	tcpipAvailable = true;
	return net_controlsocket;
}

void WebSocket_Shutdown(void)
{
	WebSocket_Listen(false);
	WebSocket_CloseSocket(net_controlsocket);
}

void WebSocket_Listen(qboolean state)
{
	(void)state;
}

int WebSocket_OpenSocket(int port)
{
	(void)port;
	if (ws_opened)
		return (int)ws;

	char *ws_url = WebSocket_GetUrl();
	EmscriptenWebSocketCreateAttributes ws_attrs = {
		ws_url,
		NULL,
		EM_TRUE,
	};

	ws_onopen_handled = false;
	WebSocket_ResetMessageQueue();

	if ((ws = emscripten_websocket_new(&ws_attrs)) <= 0)
	{
		free(ws_url);
		Sys_Printf("WebSocket_OpenSocket: failed to open socket\n");
		Con_Printf("WebSocket_OpenSocket: failed to open socket\n");
		return -1;
	}

	free(ws_url);
	ws_opened = true;
	ws_close_requested = false;

	emscripten_websocket_set_onopen_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onopen);
	emscripten_websocket_set_onerror_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onerror);
	emscripten_websocket_set_onclose_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onclose);
	emscripten_websocket_set_onmessage_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onmessage);

	return (int)ws;
}

int WebSocket_CloseSocket(int socket)
{
	(void)socket;
	WebSocket_CloseUnderlying();
	return 0;
}

int WebSocket_Connect(int socket, struct qsockaddr *addr)
{
	(void)socket;
	(void)addr;

	// Wait for WebSocket onopen before NetQuake starts the handshake.
	while (ws_opened && !ws_onopen_handled)
		emscripten_sleep(100);

	return 0;
}

int WebSocket_CheckNewConnections(void)
{
	return -1;
}

int WebSocket_Read(int socket, byte *buf, int len, struct qsockaddr *addr)
{
	(void)socket;

	if (!ws_opened)
		return -1;
	if (!ws_onopen_handled)
		return 0;

	uint16_t read_index = wsMessages.read_index;
	if (read_index == wsMessages.write_index)
		return 0;

	unsigned int length = wsMessages.messages[read_index].length;
	if (length > (unsigned int)len)
	{
		Con_Printf("WebSocket_Read: packet too large (%u bytes, max %d); dropping\n", length, len);
		wsMessages.messages[read_index].length = 0;
		wsMessages.read_index = (uint16_t)((read_index + 1u) % MAX_WS_MESSAGES);
		return 0;
	}

	memcpy(buf, wsMessages.messages[read_index].data, length);
	wsMessages.messages[read_index].length = 0;
	wsMessages.read_index = (uint16_t)((read_index + 1u) % MAX_WS_MESSAGES);

	WebSocket_FillDummyAddr(addr);
	return (int)length;
}

int WebSocket_Write(int socket, byte *buf, int len, struct qsockaddr *addr)
{
	(void)socket;
	(void)addr;

	if (!ws_opened)
		return -1;
	if (!ws_onopen_handled)
		return 0;

	EMSCRIPTEN_RESULT res;
	res = emscripten_websocket_send_binary(ws, buf, (uint32_t)len);
	if (res < 0)
		return -1;
	return len;
}

int WebSocket_Broadcast(int socket, byte *buf, int len)
{
	(void)socket;
	(void)buf;
	(void)len;
	// No UDP broadcast in the browser.
	return 0;
}

char *WebSocket_AddrToString(struct qsockaddr *addr)
{
	(void)addr;
	static char buffer[22];
	sprintf(buffer, "%s", "6.9.42.0:13337");
	return buffer;
}

int WebSocket_StringToAddr(char *string, struct qsockaddr *addr)
{
	(void)string;
	WebSocket_FillDummyAddr(addr);
	return 0;
}

int WebSocket_GetSocketAddr(int socket, struct qsockaddr *addr)
{
	(void)socket;
	WebSocket_FillDummyAddr(addr);
	return 0;
}

int WebSocket_GetNameFromAddr(struct qsockaddr *addr, char *name)
{
	(void)addr;
	sprintf(name, "%s", "quake-wasm\x00");
	return 0;
}

int WebSocket_GetAddrFromName(char *name, struct qsockaddr *addr)
{
	(void)name;
	return WebSocket_StringToAddr(name ? name : "", addr);
}

int WebSocket_AddrCompare(struct qsockaddr *addr1, struct qsockaddr *addr2)
{
	(void)addr1;
	(void)addr2;
	return 0;
}

int WebSocket_GetSocketPort(struct qsockaddr *addr)
{
	(void)addr;
	return 13337;
}

int WebSocket_SetSocketPort(struct qsockaddr *addr, int port)
{
	(void)addr;
	(void)port;
	return 0;
}
