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

		function appendRconToken(url) {
			try {
				var tok = g.localStorage && g.localStorage.getItem("nq_rcon_token");
				if (tok) {
					var sep = (url.indexOf("?") === -1) ? "?" : "&";
					return url + sep + "token=" + encodeURIComponent(tok);
				}
			} catch (e) {}
			return url;
		}

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
			return stringToNewUTF8(appendRconToken(override));
		}

		var loc = g.location;
		if (!loc || !loc.host) {
			return stringToNewUTF8(DEFAULT_WEBSOCKET_URL);
		}

		var wsProto = (loc.protocol === "https:") ? "wss:" : "ws:";
		var wsUrl = wsProto + "//" + loc.host + "/ws";
		wsUrl = appendRconToken(wsUrl);

		return stringToNewUTF8(wsUrl);
	} catch (e) {
		return stringToNewUTF8(DEFAULT_WEBSOCKET_URL);
	}
});

EM_JS(void, WebSocket_SetRconTokenFromPassword, (const char *password), {
	try {
		var pw = UTF8ToString(password);
		if (!pw) return;

		var prefix = "NexQuake:rcon:v1:";
		var input = prefix + pw;

		var data;
		if (typeof TextEncoder !== "undefined") {
			data = new TextEncoder().encode(input);
		} else {
			// Minimal UTF-8 encoder fallback.
			var out = [];
			for (var i = 0; i < input.length; i++) {
				var c = input.charCodeAt(i);
				if (c < 0x80) {
					out.push(c);
				} else if (c < 0x800) {
					out.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
				} else if (c >= 0xd800 && c <= 0xdbff && i + 1 < input.length) {
					var c2 = input.charCodeAt(++i);
					var u = 0x10000 + ((c - 0xd800) << 10) + (c2 - 0xdc00);
					out.push(
						0xf0 | (u >> 18),
						0x80 | ((u >> 12) & 0x3f),
						0x80 | ((u >> 6) & 0x3f),
						0x80 | (u & 0x3f)
					);
				} else {
					out.push(
						0xe0 | (c >> 12),
						0x80 | ((c >> 6) & 0x3f),
						0x80 | (c & 0x3f)
					);
				}
			}
			data = new Uint8Array(out);
		}

		function rotr(x, n) {
			return (x >>> n) | (x << (32 - n));
		}

		var K = [
			0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
			0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
			0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
			0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
			0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
			0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
			0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
			0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
		];

		var H0 = 0x6a09e667 | 0;
		var H1 = 0xbb67ae85 | 0;
		var H2 = 0x3c6ef372 | 0;
		var H3 = 0xa54ff53a | 0;
		var H4 = 0x510e527f | 0;
		var H5 = 0x9b05688c | 0;
		var H6 = 0x1f83d9ab | 0;
		var H7 = 0x5be0cd19 | 0;

		var l = data.length;
		var withOne = l + 1;
		var rem = withOne % 64;
		var pad = rem <= 56 ? (56 - rem) : (56 + 64 - rem);
		var total = withOne + pad + 8;
		var buf = new Uint8Array(total);
		buf.set(data, 0);
		buf[l] = 0x80;

		// 64-bit big-endian bit length.
		var bitLenHi = Math.floor(l / 536870912); // l * 8 / 2^32
		var bitLenLo = ((l << 3) >>> 0);
		var o = total - 8;
		buf[o + 0] = (bitLenHi >>> 24) & 0xff;
		buf[o + 1] = (bitLenHi >>> 16) & 0xff;
		buf[o + 2] = (bitLenHi >>> 8) & 0xff;
		buf[o + 3] = bitLenHi & 0xff;
		buf[o + 4] = (bitLenLo >>> 24) & 0xff;
		buf[o + 5] = (bitLenLo >>> 16) & 0xff;
		buf[o + 6] = (bitLenLo >>> 8) & 0xff;
		buf[o + 7] = bitLenLo & 0xff;

		var w = new Int32Array(64);
		for (var i = 0; i < total; i += 64) {
			for (var t = 0; t < 16; t++) {
				var p = i + (t * 4);
				w[t] = ((buf[p] << 24) | (buf[p + 1] << 16) | (buf[p + 2] << 8) | (buf[p + 3])) | 0;
			}
			for (t = 16; t < 64; t++) {
				var x = w[t - 15];
				var y = w[t - 2];
				var s0 = (rotr(x, 7) ^ rotr(x, 18) ^ (x >>> 3)) | 0;
				var s1 = (rotr(y, 17) ^ rotr(y, 19) ^ (y >>> 10)) | 0;
				w[t] = (w[t - 16] + s0 + w[t - 7] + s1) | 0;
			}

			var a = H0, b = H1, c = H2, d = H3, e = H4, f = H5, g = H6, h = H7;
			for (t = 0; t < 64; t++) {
				var S1 = (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) | 0;
				var ch = ((e & f) ^ (~e & g)) | 0;
				var t1 = (h + S1 + ch + K[t] + w[t]) | 0;
				var S0 = (rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) | 0;
				var maj = ((a & b) ^ (a & c) ^ (b & c)) | 0;
				var t2 = (S0 + maj) | 0;
				h = g;
				g = f;
				f = e;
				e = (d + t1) | 0;
				d = c;
				c = b;
				b = a;
				a = (t1 + t2) | 0;
			}

			H0 = (H0 + a) | 0;
			H1 = (H1 + b) | 0;
			H2 = (H2 + c) | 0;
			H3 = (H3 + d) | 0;
			H4 = (H4 + e) | 0;
			H5 = (H5 + f) | 0;
			H6 = (H6 + g) | 0;
			H7 = (H7 + h) | 0;
		}

		var hash = new Uint8Array(32);
		var hs = [H0, H1, H2, H3, H4, H5, H6, H7];
		for (var j = 0; j < 8; j++) {
			var v = hs[j] >>> 0;
			hash[j * 4 + 0] = (v >>> 24) & 0xff;
			hash[j * 4 + 1] = (v >>> 16) & 0xff;
			hash[j * 4 + 2] = (v >>> 8) & 0xff;
			hash[j * 4 + 3] = v & 0xff;
		}

		var b64 = "";
		if (typeof Buffer !== "undefined" && Buffer.from) {
			b64 = Buffer.from(hash).toString("base64");
		} else if (typeof btoa !== "undefined") {
			var bin = "";
			for (j = 0; j < hash.length; j++) bin += String.fromCharCode(hash[j]);
			b64 = btoa(bin);
		} else {
			return;
		}

		var tok = b64.split("+").join("-").split("/").join("_");
		while (tok.length > 0 && tok.charCodeAt(tok.length - 1) === 61) tok = tok.slice(0, -1);
		var g = (typeof globalThis !== "undefined")
			? globalThis
			: ((typeof self !== "undefined") ? self : window);
		try {
			if (g.localStorage) g.localStorage.setItem("nq_rcon_token", tok);
		} catch (e) {}
	} catch (e) {}
});

static void WebSocket_RconPassword_f(void);
static void WebSocket_Rcon_f(void);
static void WebSocket_RegisterCommands(void);

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
static qboolean ws_commands_registered = false;

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
// to gracefully shut down (writes config.cfg) before the browser tears down the tab.
EMSCRIPTEN_KEEPALIVE void NexQuake_OnPageHide(void)
{
	Host_Shutdown();
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
	WebSocket_RegisterCommands();
	return net_controlsocket;
}

static void WebSocket_RegisterCommands(void)
{
	if (ws_commands_registered)
		return;
	ws_commands_registered = true;

	Cmd_AddCommand("rcon_password", WebSocket_RconPassword_f);
	Cmd_AddCommand("rcon", WebSocket_Rcon_f);
}

static void WebSocket_RconPassword_f(void)
{
	if (Cmd_Argc() != 2)
	{
		Con_Printf("usage: rcon_password <password>\n");
		return;
	}

	const char *pw = Cmd_Argv(1);
	if (!pw || !pw[0])
	{
		Con_Printf("usage: rcon_password <password>\n");
		return;
	}

	WebSocket_SetRconTokenFromPassword(pw);
	Con_Printf("Password set. You must reload the client for it to take effect.\n");
}

static void WebSocket_Rcon_f(void)
{
	// Mirror Cmd_ForwardToServer behavior, but always omit the command name (like `cmd`).
	if (cls.state != ca_connected)
	{
		Con_Printf("Can't \"%s\", not connected\n", Cmd_Argv(0));
		return;
	}

	if (cls.demoplayback)
		return; // not really connected

	MSG_WriteByte(&cls.message, clc_stringcmd);
	if (Cmd_Argc() > 1)
		SZ_Print(&cls.message, Cmd_Args());
	else
		SZ_Print(&cls.message, "\n");
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

	(void)socket;

	// NetQuake's UDP landriver reads both control (NETFLAG_CTL) and data packets
	// from whatever socket is used during the current phase (connect uses a
	// freshly opened socket; slist uses the control socket). Since the WebSocket
	// tunnel is a single underlying transport, allow any socket to drain control
	// packets; otherwise CCREP_ACCEPT/REJECT can be stranded on net_controlsocket.
	//
	// Prefer control packets first so the connect handshake sees ACCEPT promptly.
	uint16_t read_index = wsCtlMessages.read_index;
	if (read_index != wsCtlMessages.write_index)
	{
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

	read_index = wsDataMessages.read_index;
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
