/*
 * net_ws_transport.c
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * This module is part of NexQuake and includes derivative work from
 * upstream websocket networking implementations by initialed85.
 * See ../ATTRIBUTIONS.md for upstream repositories, paths, and pinned commits.
 */

#include "quakedef.h"

#include <sys/types.h>
#include <sys/socket.h>
#include <stdint.h>
#include <stdlib.h>

#include <emscripten/emscripten.h>
#include <emscripten/websocket.h>

// Incoming WebSocket frames are queued here until the Quake net loop drains them.
// Use a larger queue and drop the oldest packet on overflow, similar to UDP socket
// buffer pressure handling.
#define MAX_WS_MESSAGES 2048
#define MAX_WS_CTL_MESSAGES 512
#define MAX_WS_SOCKETS 64

// WebSocket frames carry a small routing header plus one NetQuake UDP datagram.
// Header:
//   u8  routing_byte (server_id)
//   u16 udp_port_be
#define WS_ROUTING_HEADER_SIZE 3
#define MAX_WS_MESSAGE_SIZE (NET_DATAGRAMSIZE + WS_ROUTING_HEADER_SIZE)

typedef struct
{
	unsigned int length;
	byte data[MAX_WS_MESSAGE_SIZE];
} WsMessage;

typedef struct
{
	uint16_t read_index;
	uint16_t write_index;
	uint16_t capacity;
	uint16_t overflow_warnings;
	const char *name;
	WsMessage *messages;
} WsMessageQueue;

typedef struct
{
	qboolean in_use;
	int socket_id;
	qboolean has_filter;
	byte server_id;
	int src_port;
} WsSocketState;

static qboolean ws_opened = false;
static qboolean ws_onopen_handled = false;
static qboolean ws_close_requested = false;
static EMSCRIPTEN_WEBSOCKET_T ws;
static int ws_next_socket_id = 1;
static int ws_open_socket_count = 0;

static WsMessage wsDataMessageStore[MAX_WS_MESSAGES];
static WsMessage wsCtlMessageStore[MAX_WS_CTL_MESSAGES];
static WsSocketState wsSockets[MAX_WS_SOCKETS];

static WsMessageQueue wsDataMessages = {
	.read_index = 0,
	.write_index = 0,
	.capacity = MAX_WS_MESSAGES,
	.overflow_warnings = 0,
	.name = "wsDataMessages",
	.messages = wsDataMessageStore,
};

static WsMessageQueue wsCtlMessages = {
	.read_index = 0,
	.write_index = 0,
	.capacity = MAX_WS_CTL_MESSAGES,
	.overflow_warnings = 0,
	.name = "wsCtlMessages",
	.messages = wsCtlMessageStore,
};

static void WebSocket_ResetQueue(WsMessageQueue *queue)
{
	queue->read_index = 0;
	queue->write_index = 0;
	queue->overflow_warnings = 0;
}

static void WebSocket_ResetMessageQueue(void)
{
	WebSocket_ResetQueue(&wsDataMessages);
	WebSocket_ResetQueue(&wsCtlMessages);
}

static void WebSocket_ResetSockets(void)
{
	Q_memset(wsSockets, 0, sizeof(wsSockets));
}

static WsSocketState *WebSocket_FindSocket(int socket_id)
{
	int i;
	for (i = 0; i < MAX_WS_SOCKETS; i++)
	{
		if (wsSockets[i].in_use && wsSockets[i].socket_id == socket_id)
			return &wsSockets[i];
	}
	return NULL;
}

static WsSocketState *WebSocket_AllocSocket(int socket_id)
{
	int i;
	for (i = 0; i < MAX_WS_SOCKETS; i++)
	{
		if (!wsSockets[i].in_use)
		{
			wsSockets[i].in_use = true;
			wsSockets[i].socket_id = socket_id;
			wsSockets[i].has_filter = false;
			wsSockets[i].server_id = 0;
			wsSockets[i].src_port = -1;
			return &wsSockets[i];
		}
	}
	return NULL;
}

static void WebSocket_QueueMessage(WsMessageQueue *queue, const void *data, unsigned int length)
{
	uint16_t next = (uint16_t)((queue->write_index + 1u) % queue->capacity);
	if (next == queue->read_index)
	{
		if (queue->overflow_warnings < 5)
		{
			int depth = (queue->write_index >= queue->read_index)
				? (int)(queue->write_index - queue->read_index)
				: (int)(queue->write_index + queue->capacity - queue->read_index);
			Sys_Printf("_WebSocket_onmessage: %s overflow (depth: %d); dropping oldest packet.\n", queue->name, depth);
			Con_Printf("_WebSocket_onmessage: %s overflow (depth: %d); dropping oldest packet.\n", queue->name, depth);
			queue->overflow_warnings++;
		}

		queue->messages[queue->read_index].length = 0;
		queue->read_index = (uint16_t)((queue->read_index + 1u) % queue->capacity);
		next = (uint16_t)((queue->write_index + 1u) % queue->capacity);
	}

	queue->messages[queue->write_index].length = length;
	memcpy(queue->messages[queue->write_index].data, data, length);
	queue->write_index = next;
}

// Returns:
//  -1 = queue empty
//   0 = packet dropped/invalid
//  >0 = payload bytes copied
static int WebSocket_QueueDepth(WsMessageQueue *queue)
{
	if (queue->write_index >= queue->read_index)
		return (int)(queue->write_index - queue->read_index);
	return (int)(queue->write_index + queue->capacity - queue->read_index);
}

static uint16_t WebSocket_PrevIndex(WsMessageQueue *queue, uint16_t index)
{
	return (uint16_t)(index == 0 ? (queue->capacity - 1u) : (index - 1u));
}

static void WebSocket_RemoveAt(WsMessageQueue *queue, uint16_t index)
{
	uint16_t prev_write = WebSocket_PrevIndex(queue, queue->write_index);
	while (index != prev_write)
	{
		uint16_t next = (uint16_t)((index + 1u) % queue->capacity);
		queue->messages[index] = queue->messages[next];
		index = next;
	}
	queue->messages[prev_write].length = 0;
	queue->write_index = prev_write;
}

// Returns:
//  -1 = no packet available for this socket
//   0 = packet dropped/invalid
//  >0 = payload bytes copied
static int WebSocket_DequeueMessage(WsMessageQueue *queue, byte *buf, int len, byte *server_id, int *src_port,
	qboolean use_filter, byte filter_server_id, int filter_src_port)
{
	int depth = WebSocket_QueueDepth(queue);
	int scanned = 0;

	while (scanned < depth)
	{
		uint16_t index = (uint16_t)((queue->read_index + scanned) % queue->capacity);
		unsigned int length = queue->messages[index].length;
		unsigned int payload_length;
		byte msg_server_id;
		int msg_src_port;

		if (length < WS_ROUTING_HEADER_SIZE)
		{
			WebSocket_RemoveAt(queue, index);
			depth--;
			continue;
		}

		msg_server_id = queue->messages[index].data[0];
		msg_src_port = (((byte)queue->messages[index].data[1]) << 8) |
			((byte)queue->messages[index].data[2]);

		if (use_filter && (msg_server_id != filter_server_id || msg_src_port != filter_src_port))
		{
			scanned++;
			continue;
		}

		payload_length = length - WS_ROUTING_HEADER_SIZE;
		if (payload_length > (unsigned int)len)
		{
			Con_Printf("WebSocket_Read: packet too large (%u bytes, max %d); dropping\n", payload_length, len);
			WebSocket_RemoveAt(queue, index);
			depth--;
			continue;
		}

		if (server_id)
			*server_id = msg_server_id;
		if (src_port)
			*src_port = msg_src_port;

		memcpy(buf, queue->messages[index].data + WS_ROUTING_HEADER_SIZE, payload_length);
		WebSocket_RemoveAt(queue, index);
		return (int)payload_length;
	}

	return -1;
}

static void WebSocket_WaitForOnOpen(int timeout_ms)
{
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
	WebSocket_ResetSockets();

	emscripten_websocket_close(ws, 1000, "Closed by WebSocket_CloseSocket");
}

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
	qboolean expected;

	(void)eventType;
	(void)websocketEvent;
	if ((EMSCRIPTEN_WEBSOCKET_T)(uintptr_t)userData != ws)
		return EM_TRUE;

	expected = ws_close_requested;
	ws_close_requested = false;

	ws_opened = false;
	ws_onopen_handled = false;
	ws_open_socket_count = 0;
	WebSocket_ResetMessageQueue();
	WebSocket_ResetSockets();

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
			WebSocket_QueueMessage(&wsCtlMessages, websocketEvent->data, (unsigned int)websocketEvent->numBytes);
		else
			WebSocket_QueueMessage(&wsDataMessages, websocketEvent->data, (unsigned int)websocketEvent->numBytes);
	}

	return EM_TRUE;
}

int WebSocketTransport_OpenSocket(const char *ws_url)
{
	int socket_id;
	WsSocketState *state;

	if (!ws_opened)
	{
		EmscriptenWebSocketCreateAttributes ws_attrs;

		if (!ws_url || !ws_url[0])
			return -1;

		ws_attrs.url = ws_url;
		ws_attrs.protocols = NULL;
		ws_attrs.createOnMainThread = EM_TRUE;

		ws_onopen_handled = false;
		WebSocket_ResetMessageQueue();

		if ((ws = emscripten_websocket_new(&ws_attrs)) <= 0)
		{
			Sys_Printf("WebSocket_OpenSocket: failed to open socket\n");
			Con_Printf("WebSocket_OpenSocket: failed to open socket\n");
			return -1;
		}

		ws_opened = true;
		ws_close_requested = false;

		emscripten_websocket_set_onopen_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onopen);
		emscripten_websocket_set_onerror_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onerror);
		emscripten_websocket_set_onclose_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onclose);
		emscripten_websocket_set_onmessage_callback(ws, (void *)(uintptr_t)ws, _WebSocket_onmessage);
	}

	socket_id = ws_next_socket_id++;
	state = WebSocket_AllocSocket(socket_id);
	if (!state)
	{
		Sys_Printf("WebSocket_OpenSocket: socket table full\n");
		Con_Printf("WebSocket_OpenSocket: socket table full\n");
		return -1;
	}

	ws_open_socket_count++;
	return socket_id;
}

int WebSocketTransport_CloseSocket(int socket)
{
	WsSocketState *state = WebSocket_FindSocket(socket);
	if (state)
	{
		state->in_use = false;
		state->socket_id = 0;
		state->has_filter = false;
		state->server_id = 0;
		state->src_port = -1;
	}

	if (state && ws_open_socket_count > 0)
		ws_open_socket_count--;
	if (ws_open_socket_count == 0)
		WebSocket_CloseUnderlying();
	return 0;
}

int WebSocketTransport_Connect(int socket, byte server_id, int src_port)
{
	WsSocketState *state = WebSocket_FindSocket(socket);
	if (!state)
		return -1;

	if (server_id != 0 && src_port > 0 && src_port <= 65535)
	{
		state->has_filter = true;
		state->server_id = server_id;
		state->src_port = src_port;
	}
	else
	{
		state->has_filter = false;
		state->server_id = 0;
		state->src_port = -1;
	}

	while (ws_opened && !ws_onopen_handled)
		emscripten_sleep(100);

	return 0;
}

int WebSocketTransport_Read(int socket, byte *buf, int len, byte *server_id, int *src_port)
{
	WsSocketState *state;
	int read_result;

	state = WebSocket_FindSocket(socket);
	if (!state)
		return -1;

	if (!ws_opened)
		return -1;
	if (!ws_onopen_handled)
		return 0;

	read_result = WebSocket_DequeueMessage(&wsCtlMessages, buf, len, server_id, src_port,
		state->has_filter, state->server_id, state->src_port);
	if (read_result > 0)
		return read_result;

	if (!state->has_filter)
		return 0;

	read_result = WebSocket_DequeueMessage(&wsDataMessages, buf, len, server_id, src_port,
		state->has_filter, state->server_id, state->src_port);
	if (read_result >= 0)
		return read_result;

	return 0;
}

int WebSocketTransport_SendFrame(byte routing_byte, int dst_port, byte *buf, int len)
{
	byte frame[NET_DATAGRAMSIZE + WS_ROUTING_HEADER_SIZE];
	EMSCRIPTEN_RESULT res;

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
	if (routing_byte == 0)
		return -1;
	if (dst_port < 0 || dst_port > 65535)
		return -1;

	frame[0] = routing_byte;
	frame[1] = (byte)((dst_port >> 8) & 0xff);
	frame[2] = (byte)(dst_port & 0xff);
	memcpy(frame + WS_ROUTING_HEADER_SIZE, buf, (size_t)len);

	res = emscripten_websocket_send_binary(ws, frame, (uint32_t)(len + WS_ROUTING_HEADER_SIZE));
	if (res < 0)
		return -1;

	return len;
}
