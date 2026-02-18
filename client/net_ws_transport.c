/*
 * net_ws_transport.c
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * This module is part of NexQuake and includes derivative work from
 * upstream websocket networking implementations by initialed85.
 * See ../ATTRIBUTIONS.md for upstream repositories, paths, and pinned commits.
 */

#include "quakedef.h"

#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <emscripten/emscripten.h>
#include <emscripten/websocket.h>

#include "net_ws_transport.h"

// WebSocket frame header:
//   u16 udp_port_be
#define WS_PORT_HEADER_SIZE 2
#define WS_CLIENT_IDENTITY_MAGIC_0 'N'
#define WS_CLIENT_IDENTITY_MAGIC_1 'Q'
#define WS_CLIENT_IDENTITY_MAGIC_2 'I'
#define WS_CLIENT_IDENTITY_MAGIC_3 'P'
#define WS_CLIENT_IDENTITY_PAYLOAD_SIZE 8

#define MAX_WS_MESSAGE_SIZE (NET_DATAGRAMSIZE + WS_PORT_HEADER_SIZE)
#define MAX_WS_DATA_MESSAGES 2048
#define MAX_WS_CTL_MESSAGES 512

typedef struct
{
	unsigned int length;
	byte data[MAX_WS_MESSAGE_SIZE];
} WsMessage;

static WsMessage wsDataMessages[MAX_WS_DATA_MESSAGES];
static uint16_t wsDataRead = 0;
static uint16_t wsDataWrite = 0;

static WsMessage wsCtlMessages[MAX_WS_CTL_MESSAGES];
static uint16_t wsCtlRead = 0;
static uint16_t wsCtlWrite = 0;

static qboolean ws_opened = false;
static qboolean ws_onopen_handled = false;
static qboolean ws_close_requested = false;
static EMSCRIPTEN_WEBSOCKET_T ws;
static const char *ws_last_send_error = "";

// Owned by net_ws_vnet.c.
extern void WebSocketVNet_SetClientVirtualIP(const byte *ip4);

// Resolve WebSocket URL at runtime.
// - Headless: uses Module.websocketUrl / Module.WEBSOCKET_URL.
// - Browser: uses ws(s)://<location.host>/ws.
EM_JS(char *, NQ_ConnectUrl, (), {
	var module = (typeof Module !== 'undefined') ? Module : {};
	var override = module.websocketUrl || module.WEBSOCKET_URL ||
		((typeof WEBSOCKET_URL !== 'undefined') ? WEBSOCKET_URL : '');
	if (override)
		return stringToNewUTF8(String(override));
	if (typeof location !== 'undefined' && location.host) {
		var wsProto = (location.protocol === 'https:') ? 'wss:' : 'ws:';
		return stringToNewUTF8(wsProto + '//' + location.host + '/ws');
	}
	return 0;
});

static void WebSocketTransport_ResetQueues(void)
{
	wsDataRead = wsDataWrite = 0;
	wsCtlRead = wsCtlWrite = 0;
}

static void WebSocketTransport_ResetState(void)
{
	ws_opened = false;
	ws_onopen_handled = false;
	ws_last_send_error = "";
	WebSocketTransport_ResetQueues();
}

static void WebSocketTransport_QueueMessage(WsMessage *messages, uint16_t capacity, uint16_t *read_index,
	uint16_t *write_index, const void *data, unsigned int length)
{
	uint16_t next;

	if (length > MAX_WS_MESSAGE_SIZE)
		return;

	next = (uint16_t)((*write_index + 1u) % capacity);
	if (next == *read_index)
		*read_index = (uint16_t)((*read_index + 1u) % capacity); // drop oldest

	messages[*write_index].length = length;
	memcpy(messages[*write_index].data, data, length);
	*write_index = next;
}

static int WebSocketTransport_DequeuePayload(WsMessage *messages, uint16_t capacity, uint16_t *read_index,
	uint16_t *write_index, byte *buf, int len, int *src_port)
{
	while (*read_index != *write_index)
	{
		WsMessage *msg = &messages[*read_index];
		unsigned int length = msg->length;
		unsigned int payload_length;

		msg->length = 0;
		*read_index = (uint16_t)((*read_index + 1u) % capacity);

		if (length < WS_PORT_HEADER_SIZE)
			continue;

		payload_length = length - WS_PORT_HEADER_SIZE;
		if (payload_length > (unsigned int)len)
			continue;

		if (src_port)
			*src_port = (((byte)msg->data[0]) << 8) | ((byte)msg->data[1]);

		memcpy(buf, msg->data + WS_PORT_HEADER_SIZE, payload_length);
		return (int)payload_length;
	}

	return 0;
}

static void WebSocketTransport_WaitForOnOpen(int timeout_ms)
{
	int waited = 0;
	while (ws_opened && !ws_onopen_handled && waited < timeout_ms)
	{
		emscripten_sleep(10);
		waited += 10;
	}
}

static EM_BOOL _WebSocket_onopen(int eventType, const EmscriptenWebSocketOpenEvent *websocketEvent, void *userData)
{
	(void)eventType;
	(void)websocketEvent;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws)
		return EM_TRUE;

	ws_onopen_handled = true;
	Sys_Printf("_WebSocket_onopen: connected\n");
#ifdef __EMSCRIPTEN__
	EM_ASM({
		if (typeof Module === 'undefined') return;
		if (typeof Module.nexquakeOnWebSocketOpen !== 'function') return;
		try {
			Module.nexquakeOnWebSocketOpen();
		} catch (e) {
			if (typeof console !== 'undefined' && console.warn)
				console.warn('nexquakeOnWebSocketOpen failed:', e);
		}
	});
#endif
	return EM_TRUE;
}

static EM_BOOL _WebSocket_onerror(int eventType, const EmscriptenWebSocketErrorEvent *websocketEvent, void *userData)
{
	(void)eventType;
	(void)websocketEvent;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws)
		return EM_TRUE;

	Sys_Printf("_WebSocket_onerror: failed (see browser console)\n");
	return EM_TRUE;
}

static EM_BOOL _WebSocket_onclose(int eventType, const EmscriptenWebSocketCloseEvent *websocketEvent, void *userData)
{
	qboolean expected;

	(void)eventType;
	(void)websocketEvent;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws)
		return EM_TRUE;

	expected = ws_close_requested;
	ws_close_requested = false;

	WebSocketTransport_ResetState();

	Sys_Printf("_WebSocket_onclose: disconnected%s\n", expected ? " at user request" : " unexpectedly");
	return EM_TRUE;
}

static EM_BOOL _WebSocket_onmessage(int eventType, const EmscriptenWebSocketMessageEvent *websocketEvent, void *userData)
{
	int src_port;
	const byte *payload;
	int payload_len;
	qboolean is_control = false;

	(void)eventType;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws)
		return EM_TRUE;

	if (websocketEvent->isText)
		return EM_TRUE;

	if (websocketEvent->numBytes < WS_PORT_HEADER_SIZE || websocketEvent->numBytes > MAX_WS_MESSAGE_SIZE)
		return EM_TRUE;

	src_port = (((byte *)websocketEvent->data)[0] << 8) | ((byte *)websocketEvent->data)[1];
	payload = (const byte *)websocketEvent->data + WS_PORT_HEADER_SIZE;
	payload_len = (int)websocketEvent->numBytes - WS_PORT_HEADER_SIZE;

	if (payload_len >= (int)sizeof(int))
	{
		int control;
		memcpy(&control, payload, sizeof(control));
		control = BigLong(control);
		if ((control & (~NETFLAG_LENGTH_MASK)) == NETFLAG_CTL)
			is_control = true;
	}

	if (src_port == 0)
	{
		if (payload_len == WS_CLIENT_IDENTITY_PAYLOAD_SIZE &&
			payload[0] == WS_CLIENT_IDENTITY_MAGIC_0 &&
			payload[1] == WS_CLIENT_IDENTITY_MAGIC_1 &&
			payload[2] == WS_CLIENT_IDENTITY_MAGIC_2 &&
			payload[3] == WS_CLIENT_IDENTITY_MAGIC_3)
		{
			WebSocketVNet_SetClientVirtualIP(payload + 4);
			return EM_TRUE;
		}
		if (is_control)
		{
			WebSocketTransport_QueueMessage(wsCtlMessages, MAX_WS_CTL_MESSAGES, &wsCtlRead, &wsCtlWrite,
				websocketEvent->data, (unsigned int)websocketEvent->numBytes);
		}
		else if (payload_len > 0)
		{
			const char *control_payload = (const char *)payload;
			Con_Printf("%.*s", payload_len, control_payload);
			if (control_payload[payload_len - 1] != '\n')
				Con_Printf("\n");
		}
	}
	else
		WebSocketTransport_QueueMessage(wsDataMessages, MAX_WS_DATA_MESSAGES, &wsDataRead, &wsDataWrite,
			websocketEvent->data, (unsigned int)websocketEvent->numBytes);

	return EM_TRUE;
}

qboolean WebSocketTransport_IsOpen(void)
{
	return ws_opened;
}

int WebSocketTransport_Open(void)
{
	EmscriptenWebSocketCreateAttributes ws_attrs;
	char *ws_url;

	if (ws_opened)
		return 0;

	ws_url = NQ_ConnectUrl();
	if (!ws_url || !ws_url[0])
		return -1;

	ws_attrs.url = ws_url;
	ws_attrs.protocols = NULL;
	ws_attrs.createOnMainThread = EM_TRUE;

	ws_onopen_handled = false;
	WebSocketTransport_ResetQueues();

	if ((ws = emscripten_websocket_new(&ws_attrs)) <= 0)
	{
		Sys_Printf("WebSocketTransport_Open: failed to open socket\n");
		free(ws_url);
		return -1;
	}

	ws_opened = true;
	ws_close_requested = false;

	emscripten_websocket_set_onopen_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onopen);
	emscripten_websocket_set_onerror_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onerror);
	emscripten_websocket_set_onclose_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onclose);
	emscripten_websocket_set_onmessage_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onmessage);

	free(ws_url);
	return 0;
}

void WebSocketTransport_Close(void)
{
	if (!ws_opened)
		return;

	ws_close_requested = true;
	WebSocketTransport_ResetState();

	emscripten_websocket_close(ws, 1000, "Closed by WebSocketTransport_Close");
}

int WebSocketTransport_SendFrame(int dst_port, const byte *buf, int len)
{
	byte frame[NET_DATAGRAMSIZE + WS_PORT_HEADER_SIZE];
	EMSCRIPTEN_RESULT res;

	if (!ws_opened)
	{
		if (WebSocketTransport_Open() < 0)
		{
			ws_last_send_error = "websocket reconnect failed";
			return -1;
		}
	}
	if (!ws_onopen_handled)
	{
		WebSocketTransport_WaitForOnOpen(2000);
		if (!ws_opened)
		{
			ws_last_send_error = "websocket closed while connecting";
			return -1;
		}
		if (!ws_onopen_handled)
		{
			ws_last_send_error = "websocket still connecting";
			return -1;
		}
	}

	if (len < 0 || len > NET_DATAGRAMSIZE)
	{
		ws_last_send_error = "payload too large";
		return -1;
	}
	if (dst_port < 0 || dst_port > 65535)
	{
		ws_last_send_error = "invalid destination port";
		return -1;
	}

	frame[0] = (byte)((dst_port >> 8) & 0xff);
	frame[1] = (byte)(dst_port & 0xff);
	memcpy(frame + WS_PORT_HEADER_SIZE, buf, (size_t)len);

	res = emscripten_websocket_send_binary(ws, frame, (uint32_t)(len + WS_PORT_HEADER_SIZE));
	if (res < 0)
	{
		ws_last_send_error = "browser send failed";
		return -1;
	}

	ws_last_send_error = "";
	return len;
}

const char *WebSocketTransport_LastSendError(void)
{
	if (!ws_last_send_error[0])
		return "unknown error";
	return ws_last_send_error;
}

int WebSocketTransport_ReadControl(byte *buf, int len, int *src_port)
{
	if (!ws_opened)
		return -1;
	if (!ws_onopen_handled)
		return 0;
	return WebSocketTransport_DequeuePayload(wsCtlMessages, MAX_WS_CTL_MESSAGES, &wsCtlRead, &wsCtlWrite, buf, len, src_port);
}

int WebSocketTransport_ReadData(byte *buf, int len, int *src_port)
{
	if (!ws_opened)
		return -1;
	if (!ws_onopen_handled)
		return 0;
	return WebSocketTransport_DequeuePayload(wsDataMessages, MAX_WS_DATA_MESSAGES, &wsDataRead, &wsDataWrite, buf, len, src_port);
}
