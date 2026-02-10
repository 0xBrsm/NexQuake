/*
 * net_ws_transport.h
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * WebSocket transport for NexQuake. Owns the Emscripten websocket lifecycle and
 * queues incoming frames. Higher-level virtual LAN address veneer lives in
 * net_ws_vnet.c.
 */

#ifndef NEXQUAKE_NET_WS_TRANSPORT_H
#define NEXQUAKE_NET_WS_TRANSPORT_H

qboolean WebSocketTransport_IsOpen(void);

int WebSocketTransport_Open(void);
void WebSocketTransport_Close(void);

int WebSocketTransport_SendFrame(int dst_port, const byte *buf, int len);

int WebSocketTransport_ReadControl(byte *buf, int len, int *src_port);
int WebSocketTransport_ReadData(byte *buf, int len, int *src_port);

#endif // NEXQUAKE_NET_WS_TRANSPORT_H
