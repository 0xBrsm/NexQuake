/*
 * net_ws_vnet.c
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

#include <emscripten/websocket.h>

#include "net_ws_transport.h"
#include "net_ws_vnet.h"

// Overlay-only virtual address for stock Quake address APIs.
// Routing is strictly by destination port.
#define NEXQUAKE_SERVER_IP_OCTET_0 13
#define NEXQUAKE_SERVER_IP_OCTET_1 37

enum
{
	WS_CONTROL_SOCKET = 1,
	WS_GAME_SOCKET = 2
};

extern void Rcon_RegisterCommands(void);
extern cvar_t hostname;

static int net_controlsocket;
static qboolean ws_control_socket_open = false;
static int ws_game_socket_refs = 0;

static byte ws_client_virtual_ip[4] = {0, 0, 0, 0};
static qboolean ws_client_virtual_ip_set = false;
static uint16_t ws_server_id_by_route_port[65536];

static int WebSocket_ClampPort(int port)
{
	if (port < 0)
		return 0;
	if (port > 65535)
		return 65535;
	return port;
}

static qboolean WebSocket_IsServicePort(int port)
{
	return port > 0 && port <= 65535;
}

static qboolean WebSocket_HasServerIDPrefix(struct qsockaddr *addr)
{
	return (byte)addr->sa_data[2] == NEXQUAKE_SERVER_IP_OCTET_0 &&
		(byte)addr->sa_data[3] == NEXQUAKE_SERVER_IP_OCTET_1;
}

static void WebSocket_SetServerIDPort(struct qsockaddr *addr, int server_id_port)
{
	server_id_port = WebSocket_ClampPort(server_id_port);

	addr->sa_data[2] = NEXQUAKE_SERVER_IP_OCTET_0;
	addr->sa_data[3] = NEXQUAKE_SERVER_IP_OCTET_1;
	addr->sa_data[4] = (byte)((server_id_port >> 8) & 0xff);
	addr->sa_data[5] = (byte)(server_id_port & 0xff);
}

static void WebSocket_FillServerAddr(struct qsockaddr *addr, int route_port, int server_id_port)
{
	Q_memset(addr, 0, sizeof(*addr));
	addr->sa_family = AF_INET;

	route_port = WebSocket_ClampPort(route_port);
	addr->sa_data[0] = (byte)((route_port >> 8) & 0xff);
	addr->sa_data[1] = (byte)(route_port & 0xff);

	if (server_id_port <= 0)
		server_id_port = route_port;
	WebSocket_SetServerIDPort(addr, server_id_port);
}

static void WebSocket_FillClientAddr(struct qsockaddr *addr, int port)
{
	Q_memset(addr, 0, sizeof(*addr));
	addr->sa_family = AF_INET;

	port = WebSocket_ClampPort(port);
	addr->sa_data[0] = (byte)((port >> 8) & 0xff);
	addr->sa_data[1] = (byte)(port & 0xff);

	if (ws_client_virtual_ip_set)
	{
		addr->sa_data[2] = ws_client_virtual_ip[0];
		addr->sa_data[3] = ws_client_virtual_ip[1];
		addr->sa_data[4] = ws_client_virtual_ip[2];
		addr->sa_data[5] = ws_client_virtual_ip[3];
	}
}

static int WebSocket_GetAddrPort(struct qsockaddr *addr)
{
	return (((byte)addr->sa_data[0]) << 8) | ((byte)addr->sa_data[1]);
}

static void WebSocket_SetAddrPort(struct qsockaddr *addr, int port)
{
	port = WebSocket_ClampPort(port);
	addr->sa_data[0] = (byte)((port >> 8) & 0xff);
	addr->sa_data[1] = (byte)(port & 0xff);

	// Host-cache and connect-path callers may provide a zeroed address and only
	// set the port. Stamp a deterministic simulated server-id prefix in that case.
	if (!WebSocket_HasServerIDPrefix(addr))
		WebSocket_SetServerIDPort(addr, port);
}

static void WebSocket_UpdateMyTCPIPAddress(void)
{
	struct qsockaddr addr;
	char *colon;

	WebSocket_FillClientAddr(&addr, 0);
	Q_strcpy(my_tcpip_address, WebSocket_AddrToString(&addr));
	colon = Q_strrchr(my_tcpip_address, ':');
	if (colon)
		*colon = 0;
}

static void WebSocket_ResetClientVirtualIP(void)
{
	Q_memset(ws_client_virtual_ip, 0, sizeof(ws_client_virtual_ip));
	ws_client_virtual_ip_set = false;
}

static void WebSocket_SetClientVirtualIP(const byte *ip4)
{
	ws_client_virtual_ip[0] = ip4[0];
	ws_client_virtual_ip[1] = ip4[1];
	ws_client_virtual_ip[2] = ip4[2];
	ws_client_virtual_ip[3] = ip4[3];
	ws_client_virtual_ip_set = true;
	WebSocket_UpdateMyTCPIPAddress();
}

void WebSocketVNet_SetClientVirtualIP(const byte *ip4)
{
	WebSocket_SetClientVirtualIP(ip4);
}

static void WebSocket_ResetServerPortMap(void)
{
	Q_memset(ws_server_id_by_route_port, 0, sizeof(ws_server_id_by_route_port));
}

static void WebSocket_MapServerRoutePort(int route_port, int server_id_port)
{
	if (!WebSocket_IsServicePort(route_port) || !WebSocket_IsServicePort(server_id_port))
		return;
	ws_server_id_by_route_port[(uint16_t)route_port] = (uint16_t)server_id_port;
}

static int WebSocket_ServerIDForRoutePort(int route_port)
{
	int mapped;

	if (!WebSocket_IsServicePort(route_port))
		return 0;

	mapped = (int)ws_server_id_by_route_port[(uint16_t)route_port];
	return WebSocket_IsServicePort(mapped) ? mapped : route_port;
}

static qboolean WebSocket_ParseCCREPAcceptPort(const byte *payload, int payload_len, int *accept_port)
{
	uint32_t control;
	int port;

	if (payload_len < 9)
		return false;

	control = ((uint32_t)payload[0] << 24) |
		((uint32_t)payload[1] << 16) |
		((uint32_t)payload[2] << 8) |
		(uint32_t)payload[3];

	if ((control & (~NETFLAG_LENGTH_MASK)) != NETFLAG_CTL)
		return false;
	if ((int)(control & NETFLAG_LENGTH_MASK) != payload_len)
		return false;
	if (payload[4] != CCREP_ACCEPT)
		return false;

	// CCREP_ACCEPT payload writes the game-port with MSG_WriteLong (little-endian).
	port = (int)((uint32_t)payload[5] |
		((uint32_t)payload[6] << 8) |
		((uint32_t)payload[7] << 16) |
		((uint32_t)payload[8] << 24));
	if (!WebSocket_IsServicePort(port))
		return false;

	*accept_port = port;
	return true;
}

static void WebSocket_ResetState(void)
{
	ws_control_socket_open = false;
	ws_game_socket_refs = 0;
	WebSocket_ResetClientVirtualIP();
	WebSocket_ResetServerPortMap();
}

static qboolean WebSocket_EnsureTransportOpen(void)
{
	if (WebSocketTransport_IsOpen())
		return true;

	// Keep socket ref state; only reset derived routing info before reconnect.
	WebSocket_ResetClientVirtualIP();
	WebSocket_ResetServerPortMap();
	if (WebSocketTransport_Open() < 0)
		return false;
	WebSocket_UpdateMyTCPIPAddress();
	return true;
}

int WebSocket_Init(void)
{
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

	if (!WebSocket_EnsureTransportOpen())
		return -1;

	if (!ws_control_socket_open)
	{
		ws_control_socket_open = true;
		return WS_CONTROL_SOCKET;
	}

	if (ws_game_socket_refs < 32767)
	{
		ws_game_socket_refs++;
		return WS_GAME_SOCKET;
	}

	Sys_Printf("WebSocket_OpenSocket: exhausted game socket refs\n");
	return -1;
}

int WebSocket_CloseSocket(int socket)
{
	if (socket == WS_CONTROL_SOCKET)
		ws_control_socket_open = false;
	else if (socket == WS_GAME_SOCKET)
	{
		if (ws_game_socket_refs > 0)
			ws_game_socket_refs--;
	}

	if (!ws_control_socket_open && ws_game_socket_refs == 0)
	{
		WebSocket_ResetState();
		WebSocketTransport_Close();
	}

	return 0;
}

int WebSocket_Connect(int socket, struct qsockaddr *addr)
{
	(void)addr;
	(void)socket;

	if (!WebSocket_EnsureTransportOpen())
		return -1;

	return 0;
}

int WebSocket_CheckNewConnections(void)
{
	return -1;
}

int WebSocket_Read(int socket, byte *buf, int len, struct qsockaddr *addr)
{
	int src_port = 0;
	int ret;

	if (!WebSocket_EnsureTransportOpen())
		return -1;

	if (socket == WS_CONTROL_SOCKET && ws_control_socket_open)
	{
		ret = WebSocketTransport_ReadControl(buf, len, &src_port);
		if (ret > 0)
			WebSocket_FillServerAddr(addr, src_port, WebSocket_ServerIDForRoutePort(src_port));
		return ret;
	}

	if (socket != WS_GAME_SOCKET || ws_game_socket_refs <= 0)
		return -1;

	ret = WebSocketTransport_ReadData(buf, len, &src_port);
	if (ret > 0)
	{
		int accept_port = 0;
		if (WebSocket_ParseCCREPAcceptPort(buf, ret, &accept_port))
			WebSocket_MapServerRoutePort(accept_port, src_port);
		WebSocket_FillServerAddr(addr, src_port, WebSocket_ServerIDForRoutePort(src_port));
	}
	return ret;
}

int WebSocket_Write(int socket, byte *buf, int len, struct qsockaddr *addr)
{
	int dst_port;

	(void)socket;

	if (!WebSocket_EnsureTransportOpen())
		return -1;
	if (!addr)
		return -1;

	dst_port = WebSocket_GetAddrPort(addr);
	if (!WebSocket_IsServicePort(dst_port))
		return -1;

	return WebSocketTransport_SendFrame(dst_port, buf, len);
}

int WebSocket_Broadcast(int socket, byte *buf, int len)
{
	(void)socket;

	if (!WebSocket_EnsureTransportOpen())
		return -1;

	return WebSocketTransport_SendFrame(0, buf, len);
}

char *WebSocket_AddrToString(struct qsockaddr *addr)
{
	static char buffer[22];
	int a = 0;
	int b = 0;
	int c = 0;
	int d = 0;
	int port = 0;

	if (addr)
	{
		a = (int)(byte)addr->sa_data[2];
		b = (int)(byte)addr->sa_data[3];
		c = (int)(byte)addr->sa_data[4];
		d = (int)(byte)addr->sa_data[5];
		port = WebSocket_GetAddrPort(addr);
	}

	snprintf(buffer, sizeof(buffer), "%d.%d.%d.%d:%d", a, b, c, d, port);
	return buffer;
}

int WebSocket_StringToAddr(char *string, struct qsockaddr *addr)
{
	char *end;
	long port;

	if (!string || !addr)
		return -1;

	while (*string == ' ' || *string == '\t')
		string++;
	if (!*string)
		return -1;

	// NexQuake only accepts direct port targets here. Host aliases are resolved
	// earlier via hostcache -> cname in NET_Connect.
	port = strtol(string, &end, 10);
	if (end == string)
		return -1;
	while (*end == ' ' || *end == '\t')
		end++;
	if (*end != '\0')
		return -1;

	if (!WebSocket_IsServicePort((int)port))
		return -1;

	WebSocket_FillServerAddr(addr, (int)port, (int)port);
	return 0;
}

int WebSocket_GetSocketAddr(int socket, struct qsockaddr *addr)
{
	(void)socket;
	WebSocket_FillClientAddr(addr, 0);
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
	if (!addr1 || !addr2)
		return -1;
	if (addr1->sa_family != addr2->sa_family)
		return -1;

	if (Q_memcmp(addr1->sa_data + 2, addr2->sa_data + 2, 4))
		return -1;

	if (Q_memcmp(addr1->sa_data, addr2->sa_data, 2))
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
