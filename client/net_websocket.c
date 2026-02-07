/*
 * net_websocket.c
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
#include <stdio.h>
#include <stdlib.h>

#include <emscripten/emscripten.h>
#include <emscripten/websocket.h>

#include "net_websocket.h"

// Servers listen on the standard NetQuake port (no -port).
#define NEXQUAKE_SERVER_LISTEN_PORT 26000

// Nexus uses a "virtual addressing" scheme on loopback:
// - server selection is encoded via 127.13.37.<server_id>
#define NEXQUAKE_SERVER_PREFIX24 (((uint32_t)127 << 16) | ((uint32_t)13 << 8) | (uint32_t)37)
#define WS_BROADCAST_SERVER_ID 0xFF

// RCON command/token helpers (implemented in cmd_rcon.c / cmd_rcon_token.js).
extern char *NQ_ConnectUrl(void);
extern void Rcon_RegisterCommands(void);

// Low-level websocket transport (implemented in net_ws_transport.c).
extern int WebSocketTransport_OpenSocket(const char *ws_url);
extern int WebSocketTransport_CloseSocket(int socket);
extern int WebSocketTransport_Connect(int socket, byte server_id, int src_port);
extern int WebSocketTransport_Read(int socket, byte *buf, int len, byte *server_id, int *src_port);
extern int WebSocketTransport_SendFrame(byte routing_byte, int dst_port, byte *buf, int len);

extern cvar_t hostname;

static int net_controlsocket;

static uint32_t WebSocket_GetAddrPrefix24(struct qsockaddr *addr)
{
	if (!addr)
		return 0;
	return ((uint32_t)(byte)addr->sa_data[2] << 16) |
		((uint32_t)(byte)addr->sa_data[3] << 8) |
		(uint32_t)(byte)addr->sa_data[4];
}

static void WebSocket_FillAddrWithServerID(struct qsockaddr *addr, byte server_id, int port)
{
	if (!addr)
		return;

	Q_memset(addr, 0, sizeof(*addr));
	addr->sa_family = AF_INET;

	if (port < 0)
		port = 0;
	if (port > 65535)
		port = 65535;
	addr->sa_data[0] = (byte)((port >> 8) & 0xff);
	addr->sa_data[1] = (byte)(port & 0xff);

	addr->sa_data[2] = (byte)((NEXQUAKE_SERVER_PREFIX24 >> 16) & 0xff);
	addr->sa_data[3] = (byte)((NEXQUAKE_SERVER_PREFIX24 >> 8) & 0xff);
	addr->sa_data[4] = (byte)(NEXQUAKE_SERVER_PREFIX24 & 0xff);
	addr->sa_data[5] = server_id;
}

static byte WebSocket_ExtractServerID(struct qsockaddr *addr)
{
	if (!addr)
		return 0;
	if (WebSocket_GetAddrPrefix24(addr) == NEXQUAKE_SERVER_PREFIX24)
		return (byte)addr->sa_data[5];
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

// Called from shell.html on pagehide/beforeunload to give the client a chance
// to send a NetQuake disconnect and then flush config.cfg before tab teardown.
EMSCRIPTEN_KEEPALIVE void NexQuake_OnPageHide(void)
{
	if (cls.state == ca_connected)
		CL_Disconnect();
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

	if (Q_strcmp(hostname.string, "UNNAMED") == 0)
		Cvar_Set("hostname", "nexquake");

	if ((net_controlsocket = WebSocket_OpenSocket(0)) == -1)
		Sys_Error("WebSocket_Init: Unable to open control socket\n");

	WebSocket_GetSocketAddr(net_controlsocket, &addr);
	Q_strcpy(my_tcpip_address, WebSocket_AddrToString(&addr));
	colon = Q_strrchr(my_tcpip_address, ':');
	if (colon)
		*colon = 0;

	tcpipAvailable = true;
	Rcon_RegisterCommands();
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
	char *ws_url = NQ_ConnectUrl();
	int socket;

	if (!ws_url)
	{
		Sys_Printf("WebSocket_OpenSocket: failed to resolve websocket URL\n");
		Con_Printf("WebSocket_OpenSocket: failed to resolve websocket URL\n");
		return -1;
	}

	socket = WebSocketTransport_OpenSocket(ws_url);
	free(ws_url);
	return socket;
}

int WebSocket_CloseSocket(int socket)
{
	return WebSocketTransport_CloseSocket(socket);
}

int WebSocket_Connect(int socket, struct qsockaddr *addr)
{
	byte server_id = WebSocket_ExtractServerID(addr);
	int src_port = WebSocket_GetAddrPort(addr);

	if (server_id == 0 || src_port <= 0 || src_port > 65535)
		return WebSocketTransport_Connect(socket, 0, -1);

	return WebSocketTransport_Connect(socket, server_id, src_port);
}

int WebSocket_CheckNewConnections(void)
{
	return -1;
}

int WebSocket_Read(int socket, byte *buf, int len, struct qsockaddr *addr)
{
	byte server_id = 0;
	int src_port = 0;
	int read_result;

	read_result = WebSocketTransport_Read(socket, buf, len, &server_id, &src_port);
	if (read_result > 0)
		WebSocket_FillAddrWithServerID(addr, server_id, src_port);
	return read_result;
}

int WebSocket_Write(int socket, byte *buf, int len, struct qsockaddr *addr)
{
	byte server_id;
	int dst_port;

	(void)socket;

	server_id = WebSocket_ExtractServerID(addr);
	if (server_id == 0)
		return -1;

	dst_port = WebSocket_GetAddrPort(addr);
	if (dst_port <= 0 || dst_port > 65535)
		return -1;

	return WebSocketTransport_SendFrame(server_id, dst_port, buf, len);
}

int WebSocket_Broadcast(int socket, byte *buf, int len)
{
	(void)socket;
	return WebSocketTransport_SendFrame(WS_BROADCAST_SERVER_ID, 0, buf, len);
}

char *WebSocket_AddrToString(struct qsockaddr *addr)
{
	static char buffer[22];
	if (!addr)
	{
		snprintf(buffer, sizeof(buffer), "%d.%d.%d.%d:%d",
			(int)((NEXQUAKE_SERVER_PREFIX24 >> 16) & 0xff),
			(int)((NEXQUAKE_SERVER_PREFIX24 >> 8) & 0xff),
			(int)(NEXQUAKE_SERVER_PREFIX24 & 0xff),
			1,
			NEXQUAKE_SERVER_LISTEN_PORT);
		return buffer;
	}

	snprintf(buffer, sizeof(buffer), "%d.%d.%d.%d:%d",
		(int)(byte)addr->sa_data[2],
		(int)(byte)addr->sa_data[3],
		(int)(byte)addr->sa_data[4],
		(int)(byte)addr->sa_data[5],
		(((byte)addr->sa_data[0] << 8) | (byte)addr->sa_data[1]));
	return buffer;
}

int WebSocket_StringToAddr(char *string, struct qsockaddr *addr)
{
	int a, b, c, d, port;
	int consumed = 0;

	if (!string || !addr)
		return -1;

	if (sscanf(string, "%d.%d.%d.%d:%d%n", &a, &b, &c, &d, &port, &consumed) == 5)
	{
		while (string[consumed] == ' ' || string[consumed] == '\t')
			consumed++;
		if (string[consumed] != '\0')
			return -1;

		if ((((uint32_t)a << 16) | ((uint32_t)b << 8) | (uint32_t)c) == NEXQUAKE_SERVER_PREFIX24 &&
			d >= 1 && d <= 254 &&
			port == NEXQUAKE_SERVER_LISTEN_PORT)
		{
			WebSocket_FillAddrWithServerID(addr, (byte)d, port);
			return 0;
		}
		return -1;
	}

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
	if (addr)
	{
		Q_memset(addr, 0, sizeof(*addr));
		addr->sa_family = AF_INET;
	}
	return 0;
}

int WebSocket_GetNameFromAddr(struct qsockaddr *addr, char *name)
{
	(void)addr;
	Q_strcpy(name, "quake-wasm");
	return 0;
}

int WebSocket_GetAddrFromName(char *name, struct qsockaddr *addr)
{
	if (!addr || !name || !name[0])
		return -1;
	return WebSocket_StringToAddr(name, addr);
}

int WebSocket_AddrCompare(struct qsockaddr *addr1, struct qsockaddr *addr2)
{
	int i;
	if (!addr1 || !addr2)
		return -1;
	if (addr1->sa_family != addr2->sa_family)
		return -1;

	for (i = 2; i < 6; i++)
		if ((byte)addr1->sa_data[i] != (byte)addr2->sa_data[i])
			return -1;

	if ((byte)addr1->sa_data[0] != (byte)addr2->sa_data[0] ||
		(byte)addr1->sa_data[1] != (byte)addr2->sa_data[1])
		return 1;

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
