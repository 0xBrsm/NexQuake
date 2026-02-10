/*
 * net_ws_vnet.h
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * WebSocket landriver interface for NexQuake. Implements Quake's
 * `net_landriver` APIs on top of the WebSocket transport in
 * net_ws_transport.c.
 */

int  WebSocket_Init (void);
void WebSocket_Shutdown (void);
void WebSocket_Listen (qboolean state);
int  WebSocket_OpenSocket (int port);
int  WebSocket_CloseSocket (int socket);
int  WebSocket_Connect (int socket, struct qsockaddr *addr);
int  WebSocket_CheckNewConnections (void);
int  WebSocket_Read (int socket, byte *buf, int len, struct qsockaddr *addr);
int  WebSocket_Write (int socket, byte *buf, int len, struct qsockaddr *addr);
int  WebSocket_Broadcast (int socket, byte *buf, int len);
char *WebSocket_AddrToString (struct qsockaddr *addr);
int  WebSocket_StringToAddr (char *string, struct qsockaddr *addr);
int  WebSocket_GetSocketAddr (int socket, struct qsockaddr *addr);
int  WebSocket_GetNameFromAddr (struct qsockaddr *addr, char *name);
int  WebSocket_GetAddrFromName (char *name, struct qsockaddr *addr);
int  WebSocket_AddrCompare (struct qsockaddr *addr1, struct qsockaddr *addr2);
int  WebSocket_GetSocketPort (struct qsockaddr *addr);
int  WebSocket_SetSocketPort (struct qsockaddr *addr, int port);
