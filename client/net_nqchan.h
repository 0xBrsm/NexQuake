/*
 * net_nqchan.h — NexQuake landriver: protocol + addressing over net_wasm.
 * SPDX-License-Identifier: GPL-3.0-only
 *
 * Implements Quake's net_landriver_t on top of net_wasm's transport
 * primitives. Owns NexQuake relay framing, CTL/DATA demux, ring buffers,
 * virtual 127.x.y.z addressing, route/server-id tracking, and the NQIP
 * identity handshake.
 */

#ifndef NEXQUAKE_NET_NQCHAN_H
#define NEXQUAKE_NET_NQCHAN_H

// Quake net_landriver_t interface.
int   NQChan_Init (void);
void  NQChan_Shutdown (void);
void  NQChan_Listen (qboolean state);
int   NQChan_OpenSocket (int port);
int   NQChan_CloseSocket (int socket);
int   NQChan_Connect (int socket, struct qsockaddr *addr);
int   NQChan_CheckNewConnections (void);
int   NQChan_Read (int socket, byte *buf, int len, struct qsockaddr *addr);
int   NQChan_Write (int socket, byte *buf, int len, struct qsockaddr *addr);
int   NQChan_Broadcast (int socket, byte *buf, int len);
char *NQChan_AddrToString (struct qsockaddr *addr);
int   NQChan_StringToAddr (char *string, struct qsockaddr *addr);
int   NQChan_GetSocketAddr (int socket, struct qsockaddr *addr);
int   NQChan_GetNameFromAddr (struct qsockaddr *addr, char *name);
int   NQChan_GetAddrFromName (char *name, struct qsockaddr *addr);
int   NQChan_AddrCompare (struct qsockaddr *addr1, struct qsockaddr *addr2);
int   NQChan_GetSocketPort (struct qsockaddr *addr);
int   NQChan_SetSocketPort (struct qsockaddr *addr, int port);

#endif
