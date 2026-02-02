/*
Added by initialed85
*/
// net_websocket.c
// Modified by NexQuake:
// - Dynamic WebSocket URL (derived from window.location, with optional override)
// - No UUID hello frame (nexus is a simple datagram tunnel)
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
#define MAX_WS_CTL_MESSAGES 512

// WebSocket frames carry a small routing header plus one NetQuake UDP datagram.
// Header:
//   u8  routing_byte (server_id)
//   u16 udp_port_be
// Payload:
//   u8[] raw NetQuake datagram (<= NET_DATAGRAMSIZE)
#define WS_ROUTING_HEADER_SIZE 3
#define MAX_WS_MESSAGE_SIZE (NET_DATAGRAMSIZE + WS_ROUTING_HEADER_SIZE)

// Servers listen on the standard NetQuake port (no -port).
#define NEXQUAKE_SERVER_LISTEN_PORT 26000

// Nexus uses a "virtual addressing" scheme on loopback:
// - server selection is encoded via 127.255.255.<server_id>
// - nexus assigns each WS client a unique UDP source IP in 127.1.1.<octet> (server-side only)
#define NEXQUAKE_SERVER_PREFIX24 (((uint32_t)127 << 16) | ((uint32_t)255 << 8) | (uint32_t)255)

static uint32_t WebSocket_GetAddrPrefix24(struct qsockaddr *addr)
{
	if (!addr)
		return 0;
	return ((uint32_t)(byte)addr->sa_data[2] << 16) |
		((uint32_t)(byte)addr->sa_data[3] << 8) |
		(uint32_t)(byte)addr->sa_data[4];
}

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
static int ws_next_socket_id = 1;
static int ws_open_socket_count = 0;

struct
{
	uint16_t read_index;
	uint16_t write_index;
	WsMessage messages[MAX_WS_MESSAGES];
} wsDataMessages = {0};

struct
{
	uint16_t read_index;
	uint16_t write_index;
	WsMessage messages[MAX_WS_CTL_MESSAGES];
} wsCtlMessages = {0};

static void WebSocket_ResetMessageQueue(void)
{
	wsDataMessages.read_index = 0;
	wsDataMessages.write_index = 0;
	wsCtlMessages.read_index = 0;
	wsCtlMessages.write_index = 0;
}

static void WebSocket_QueueMessage(
	uint16_t *read_index,
	uint16_t *write_index,
	WsMessage *messages,
	uint16_t capacity,
	const void *data,
	unsigned int length,
	const char *queue_name)
{
	uint16_t next = (uint16_t)((*write_index + 1u) % capacity);
	if (next == *read_index)
	{
		static int overflow_warnings = 0;
		if (overflow_warnings < 5)
		{
			int depth = (*write_index >= *read_index)
				? (int)(*write_index - *read_index)
				: (int)(*write_index + capacity - *read_index);
			Sys_Printf("_WebSocket_onmessage: %s overflow (depth: %d); dropping oldest packet.\n", queue_name, depth);
			Con_Printf("_WebSocket_onmessage: %s overflow (depth: %d); dropping oldest packet.\n", queue_name, depth);
			overflow_warnings++;
		}

		messages[*read_index].length = 0;
		*read_index = (uint16_t)((*read_index + 1u) % capacity);
		next = (uint16_t)((*write_index + 1u) % capacity);
	}

	messages[*write_index].length = length;
	memcpy(messages[*write_index].data, data, length);
	*write_index = next;
}

static void WebSocket_WaitForOnOpen(int timeout_ms)
{
	// Emscripten WebSocket callbacks run on the JS event loop. If Quake sends
	// packets (e.g., server browser broadcast) before onopen fires, returning 0
	// here makes discovery flaky. Yield briefly so onopen can be delivered.
	int waited = 0;
	while (ws_opened && !ws_onopen_handled && waited < timeout_ms)
	{
		emscripten_sleep(10);
		waited += 10;
	}
}

static void WebSocket_CloseUnderlying(void)
{
	if (!ws_opened)
		return;

	ws_close_requested = true;
	ws_opened = false;
	ws_onopen_handled = false;
	ws_open_socket_count = 0;
	WebSocket_ResetMessageQueue();

	emscripten_websocket_close(ws, 1000, "Closed by WebSocket_CloseSocket");
}

static void WebSocket_FillAddrWithServerID(struct qsockaddr *addr, byte server_id, int port)
{
	if (!addr)
		return;

	// For multi-server support, encode server routing info in the address.
	// Nexus prepends a server_id byte to packets. We use 127.255.255.x:26000
	// where x is the server_id (1-254). 127.255.255.255 is the broadcast address.
	Q_memset(addr, 0, sizeof(*addr));
	addr->sa_family = AF_INET;
	// sockaddr_in layout (without including <netinet/in.h>):
	// - sa_data[0..1] = port (big endian)
	// - sa_data[2..5] = IPv4 address
	if (port < 0)
		port = 0;
	if (port > 65535)
		port = 65535;
	addr->sa_data[0] = (byte)((port >> 8) & 0xff);
	addr->sa_data[1] = (byte)(port & 0xff);
	// IP: 127.255.255.server_id
	addr->sa_data[2] = (byte)((NEXQUAKE_SERVER_PREFIX24 >> 16) & 0xff);
	addr->sa_data[3] = (byte)((NEXQUAKE_SERVER_PREFIX24 >> 8) & 0xff);
	addr->sa_data[4] = (byte)(NEXQUAKE_SERVER_PREFIX24 & 0xff);
	addr->sa_data[5] = server_id;
}

// Called from shell.html on pagehide/beforeunload to give the client a chance
// to send a proper NetQuake disconnect before the browser tears down the tab.
EMSCRIPTEN_KEEPALIVE void NexQuake_OnPageHide(void)
{
	CL_Disconnect();
}

// Deterministic command injection for automation and debugging.
// Queues a console command as if typed, with a trailing newline.
EMSCRIPTEN_KEEPALIVE void NexQuake_ExecCommand(const char *cmd)
{
	if (!cmd || !cmd[0])
		return;
	Cbuf_AddText((char *)cmd);
	Cbuf_AddText("\n");
}

// Executes the command buffer immediately after queueing the provided command.
EMSCRIPTEN_KEEPALIVE void NexQuake_ExecCommandNow(const char *cmd)
{
	NexQuake_ExecCommand(cmd);
	Cbuf_Execute();
}

#ifndef HEADLESS
EMSCRIPTEN_KEEPALIVE void NexQuake_VFSReady(void)
{
	// No-op for the browser; headless builds define this in sys_node.c.
}
#endif

static byte WebSocket_ExtractServerID(struct qsockaddr *addr)
{
	if (!addr)
		return 0;

	// Extract server ID from last octet of IP address (127.255.255.x)
	if (WebSocket_GetAddrPrefix24(addr) == NEXQUAKE_SERVER_PREFIX24)
		return (byte)addr->sa_data[5];

	// Not an encoded server address.
	return 0;
}

static int WebSocket_GetAddrPort(struct qsockaddr *addr)
{
	if (!addr)
		return 0;
	return (((byte)addr->sa_data[0]) << 8) | ((byte)addr->sa_data[1]);
}

static void WebSocket_SetAddrPort(struct qsockaddr *addr, int port)
{
	if (!addr)
		return;
	if (port < 0)
		port = 0;
	if (port > 65535)
		port = 65535;
	addr->sa_data[0] = (byte)((port >> 8) & 0xff);
	addr->sa_data[1] = (byte)(port & 0xff);
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
	ws_open_socket_count = 0;
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
		qboolean is_control = false;
		if (websocketEvent->numBytes >= WS_ROUTING_HEADER_SIZE + sizeof(int) + 1)
		{
			int control;
			memcpy(&control, (byte *)websocketEvent->data + WS_ROUTING_HEADER_SIZE, sizeof(control));
			control = BigLong(control);
			if ((control & (~NETFLAG_LENGTH_MASK)) == NETFLAG_CTL)
				is_control = true;
		}

		if (is_control)
		{
			WebSocket_QueueMessage(
				&wsCtlMessages.read_index,
				&wsCtlMessages.write_index,
				wsCtlMessages.messages,
				MAX_WS_CTL_MESSAGES,
				websocketEvent->data,
				(unsigned int)websocketEvent->numBytes,
				"wsCtlMessages");
		}
		else
		{
			WebSocket_QueueMessage(
				&wsDataMessages.read_index,
				&wsDataMessages.write_index,
				wsDataMessages.messages,
				MAX_WS_MESSAGES,
				websocketEvent->data,
				(unsigned int)websocketEvent->numBytes,
				"wsDataMessages");
		}
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
		Cvar_Set("hostname", "nexquake");
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
	if (!ws_opened)
	{
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
	}

	ws_open_socket_count++;
	return ws_next_socket_id++;
}

int WebSocket_CloseSocket(int socket)
{
	(void)socket;
	if (ws_open_socket_count > 0)
		ws_open_socket_count--;
	if (ws_open_socket_count == 0)
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
	if (!ws_opened)
		return -1;
	// Avoid sending before the websocket is fully opened; the caller will retry via NET_Poll.
	if (!ws_onopen_handled)
		return 0;

	qboolean want_ctl = (socket == net_controlsocket);
	if (want_ctl)
	{
		uint16_t read_index = wsCtlMessages.read_index;
		if (read_index == wsCtlMessages.write_index)
			return 0;

		unsigned int length = wsCtlMessages.messages[read_index].length;

		if (length < WS_ROUTING_HEADER_SIZE)
		{
			wsCtlMessages.messages[read_index].length = 0;
			wsCtlMessages.read_index = (uint16_t)((read_index + 1u) % MAX_WS_CTL_MESSAGES);
			return 0;
		}

		byte server_id = wsCtlMessages.messages[read_index].data[0];
		int src_port = (((byte)wsCtlMessages.messages[read_index].data[1]) << 8) |
			((byte)wsCtlMessages.messages[read_index].data[2]);
		unsigned int payload_length = length - WS_ROUTING_HEADER_SIZE;

		if (payload_length > (unsigned int)len)
		{
			Con_Printf("WebSocket_Read: packet too large (%u bytes, max %d); dropping\n", payload_length, len);
			wsCtlMessages.messages[read_index].length = 0;
			wsCtlMessages.read_index = (uint16_t)((read_index + 1u) % MAX_WS_CTL_MESSAGES);
			return 0;
		}

		memcpy(buf, wsCtlMessages.messages[read_index].data + WS_ROUTING_HEADER_SIZE, payload_length);
		wsCtlMessages.messages[read_index].length = 0;
		wsCtlMessages.read_index = (uint16_t)((read_index + 1u) % MAX_WS_CTL_MESSAGES);

		WebSocket_FillAddrWithServerID(addr, server_id, src_port);
		return (int)payload_length;
	}

	uint16_t read_index = wsDataMessages.read_index;
	if (read_index == wsDataMessages.write_index)
		return 0;

	unsigned int length = wsDataMessages.messages[read_index].length;

	if (length < WS_ROUTING_HEADER_SIZE)
	{
		wsDataMessages.messages[read_index].length = 0;
		wsDataMessages.read_index = (uint16_t)((read_index + 1u) % MAX_WS_MESSAGES);
		return 0;
	}

	byte server_id = wsDataMessages.messages[read_index].data[0];
	int src_port = (((byte)wsDataMessages.messages[read_index].data[1]) << 8) |
		((byte)wsDataMessages.messages[read_index].data[2]);
	unsigned int payload_length = length - WS_ROUTING_HEADER_SIZE;

	if (payload_length > (unsigned int)len)
	{
		Con_Printf("WebSocket_Read: packet too large (%u bytes, max %d); dropping\n", payload_length, len);
		wsDataMessages.messages[read_index].length = 0;
		wsDataMessages.read_index = (uint16_t)((read_index + 1u) % MAX_WS_MESSAGES);
		return 0;
	}

	memcpy(buf, wsDataMessages.messages[read_index].data + WS_ROUTING_HEADER_SIZE, payload_length);
	wsDataMessages.messages[read_index].length = 0;
	wsDataMessages.read_index = (uint16_t)((read_index + 1u) % MAX_WS_MESSAGES);

	WebSocket_FillAddrWithServerID(addr, server_id, src_port);
	return (int)payload_length;
}

int WebSocket_Write(int socket, byte *buf, int len, struct qsockaddr *addr)
{
	(void)socket;

	if (!ws_opened)
		return -1;
	if (!ws_onopen_handled)
	{
		WebSocket_WaitForOnOpen(2000);
		if (!ws_opened)
			return -1;
		if (!ws_onopen_handled)
			return 0;
	}
	if (len < 0 || len > NET_DATAGRAMSIZE)
		return -1;

	// Extract server ID from address and prepend to packet for nexus routing
	byte server_id = WebSocket_ExtractServerID(addr);
	if (server_id == 0)
		return -1;
	int dst_port = WebSocket_GetAddrPort(addr);
	if (dst_port <= 0 || dst_port > 65535)
		return -1;

	// Stack buffer: avoids per-packet heap allocations.
	byte frame[NET_DATAGRAMSIZE + WS_ROUTING_HEADER_SIZE];
	frame[0] = server_id;
	frame[1] = (byte)((dst_port >> 8) & 0xff);
	frame[2] = (byte)(dst_port & 0xff);
	memcpy(frame + WS_ROUTING_HEADER_SIZE, buf, (size_t)len);

	EMSCRIPTEN_RESULT res = emscripten_websocket_send_binary(ws, frame, (uint32_t)(len + WS_ROUTING_HEADER_SIZE));

	if (res < 0)
		return -1;
	return len;
}

int WebSocket_Broadcast(int socket, byte *buf, int len)
{
	(void)socket;

	if (!ws_opened)
		return -1;
	if (!ws_onopen_handled)
	{
		WebSocket_WaitForOnOpen(2000);
		if (!ws_opened)
			return -1;
		if (!ws_onopen_handled)
			return 0;
	}
	if (len < 0 || len > NET_DATAGRAMSIZE)
		return -1;

	// Use server_id = 0xFF as broadcast flag for nexus to fan out to all servers.
	// Broadcast targets the default listen port (26000).
	byte frame[NET_DATAGRAMSIZE + WS_ROUTING_HEADER_SIZE];
	frame[0] = 0xFF;  // Broadcast marker
	frame[1] = 0;
	frame[2] = 0;
	memcpy(frame + WS_ROUTING_HEADER_SIZE, buf, (size_t)len);

	EMSCRIPTEN_RESULT res = emscripten_websocket_send_binary(ws, frame, (uint32_t)(len + WS_ROUTING_HEADER_SIZE));

	if (res < 0)
		return -1;
	return len;
}

char *WebSocket_AddrToString(struct qsockaddr *addr)
{
	static char buffer[22];
	if (!addr)
	{
			sprintf(buffer, "%d.%d.%d.%d:%d",
				(int)((NEXQUAKE_SERVER_PREFIX24 >> 16) & 0xff),
				(int)((NEXQUAKE_SERVER_PREFIX24 >> 8) & 0xff),
				(int)(NEXQUAKE_SERVER_PREFIX24 & 0xff),
				1,
				NEXQUAKE_SERVER_LISTEN_PORT);
			return buffer;
		}

	int a = (byte)addr->sa_data[2];
	int b = (byte)addr->sa_data[3];
	int c = (byte)addr->sa_data[4];
	int d = (byte)addr->sa_data[5];
	int port = ((byte)addr->sa_data[0] << 8) | (byte)addr->sa_data[1];
	sprintf(buffer, "%d.%d.%d.%d:%d", a, b, c, d, port);
	return buffer;
}

int WebSocket_StringToAddr(char *string, struct qsockaddr *addr)
{
	if (!string || !addr)
		return -1;

	// Parse "127.255.255.x[:port]" format to extract server ID and port.
	int a, b, c, d, port;
	int consumed = 0;
	if (sscanf(string, "%d.%d.%d.%d:%d%n", &a, &b, &c, &d, &port, &consumed) == 5)
	{
		while (string[consumed] == ' ' || string[consumed] == '\t')
			consumed++;
		if (string[consumed] != '\0')
			return -1;

	// Only allow user-specified ports that match the server listen port.
		// The server may still switch to an accepted port during the handshake
		// (CCREP_ACCEPT); that port change happens internally and does not go
		// through this parser.
			if ((((uint32_t)a << 16) | ((uint32_t)b << 8) | (uint32_t)c) == NEXQUAKE_SERVER_PREFIX24 &&
				d >= 1 && d <= 254 &&
				port == NEXQUAKE_SERVER_LISTEN_PORT)
			{
				WebSocket_FillAddrWithServerID(addr, (byte)d, port);
				return 0;
			}
		return -1;
	}

	// The Quake UI and console commonly accept addresses without an explicit port.
	// Treat "127.255.255.x" as selecting server x (default port 26000).
	consumed = 0;
	if (sscanf(string, "%d.%d.%d.%d%n", &a, &b, &c, &d, &consumed) == 4)
	{
		while (string[consumed] == ' ' || string[consumed] == '\t')
			consumed++;
		if (string[consumed] != '\0')
			return -1;

			if ((((uint32_t)a << 16) | ((uint32_t)b << 8) | (uint32_t)c) == NEXQUAKE_SERVER_PREFIX24 &&
				d >= 1 && d <= 254)
			{
				WebSocket_FillAddrWithServerID(addr, (byte)d, NEXQUAKE_SERVER_LISTEN_PORT);
				return 0;
			}
	}

	return -1;
}

int WebSocket_GetSocketAddr(int socket, struct qsockaddr *addr)
{
	(void)socket;
	// This is Quake's "local address" display. In the browser we don't have a real
	// UDP interface, so report an explicit "unknown" address.
	if (addr)
	{
		Q_memset(addr, 0, sizeof(*addr));
		addr->sa_family = AF_INET;
		// Port 0 (big-endian).
		addr->sa_data[0] = 0;
		addr->sa_data[1] = 0;
		// IP: 0.0.0.0 (unknown / not applicable in browser).
		addr->sa_data[2] = 0;
		addr->sa_data[3] = 0;
		addr->sa_data[4] = 0;
		addr->sa_data[5] = 0;
	}
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
	if (!addr)
		return -1;

	if (!name || !name[0])
		return -1;

	// Stock NetQuake behavior is handled by the engine (hostcache mapping, etc).
	// In the browser we can't do synchronous DNS, so only accept explicit
	// addresses in our simulated server subnet.
	return WebSocket_StringToAddr(name, addr);
}

int WebSocket_AddrCompare(struct qsockaddr *addr1, struct qsockaddr *addr2)
{
	if (!addr1 || !addr2)
		return -1;
	if (addr1->sa_family != addr2->sa_family)
		return -1;

	// Match Quake's net driver semantics:
	//   0  => same host + same port
	//   1  => same host, different port
	//  -1  => different host
	for (int i = 2; i < 6; i++)
	{
		if ((byte)addr1->sa_data[i] != (byte)addr2->sa_data[i])
		{
			return -1;
		}
	}

	if ((byte)addr1->sa_data[0] != (byte)addr2->sa_data[0] ||
		(byte)addr1->sa_data[1] != (byte)addr2->sa_data[1])
	{
		return 1;
	}

	return 0;
}

int WebSocket_GetSocketPort(struct qsockaddr *addr)
{
	return WebSocket_GetAddrPort(addr);
}

int WebSocket_SetSocketPort(struct qsockaddr *addr, int port)
{
	WebSocket_SetAddrPort(addr, port);
	return 0;
}
