/*
 * net_nqchan.c — NexQuake landriver. See net_nqchan.h.
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * Derivative work; see ../ATTRIBUTIONS.md.
 */

#include "quakedef.h"

#include <sys/types.h>
#include <sys/socket.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "net_nqchan.h"
#include "net_wasm.h"

#define NQ_PORT_HEADER_SIZE      2
#define NQ_MAX_DATA_MESSAGES     256
#define NQ_MAX_CTL_MESSAGES      64
// Must match trunk/protocol.go's defaultIdentityMagic: nexus sends
// NQ_IDENTITY_MAGIC followed by the 4-byte VirtualIP on port 0 at connect.
#define NQ_IDENTITY_MAGIC        "NQIP"
#define NQ_IDENTITY_MAGIC_LEN    4
#define NQ_IDENTITY_PAYLOAD_SIZE (NQ_IDENTITY_MAGIC_LEN + 4)
// Must match admin.ClientCommandPayload: nexus pushes a server→client console
// command on port 0 framed as NQ_RCON_MAGIC + UTF-8 cmd + 0x00. The client
// strips the prefix and feeds the command into Cbuf as if the user typed it.
#define NQ_RCON_MAGIC            "RCON"
#define NQ_RCON_MAGIC_LEN        4
#define NQ_SERVER_IP_OCTET_0     13
#define NQ_SERVER_IP_OCTET_1     37

enum { NQ_CONTROL_SOCKET = 1, NQ_GAME_SOCKET = 2 };

extern void Rcon_RegisterCommands (void);
extern void WASM_TransportInit (void);
extern cvar_t hostname;

//----------------------------------------------------------------------------
// Ring buffers — one shape, two instances (ctl, data).

typedef struct {
	unsigned int length;
	byte data[WASM_MAX_FRAME_SIZE];
} NqMessage;

typedef struct {
	NqMessage *slots;
	uint16_t cap, r, w, overflow;
	const char *name;
} NqRing;

static NqMessage nq_ctl_slots[NQ_MAX_CTL_MESSAGES];
static NqMessage nq_data_slots[NQ_MAX_DATA_MESSAGES];
static NqRing nq_ctl  = { nq_ctl_slots,  NQ_MAX_CTL_MESSAGES,  0, 0, 0, "ctl"  };
static NqRing nq_data = { nq_data_slots, NQ_MAX_DATA_MESSAGES, 0, 0, 0, "data" };

static void NqRing_Enqueue (NqRing *q, const byte *src, unsigned int length)
{
	uint16_t next;

	if (length > WASM_MAX_FRAME_SIZE) return;

	next = (uint16_t)((q->w + 1u) % q->cap);
	if (next == q->r)
	{
		unsigned int count = (unsigned int)q->overflow + 1u;
		if (count <= 5u || (count % 256u) == 0u)
		{
			int depth = (q->w >= q->r) ? (int)(q->w - q->r)
				: (int)(q->w + q->cap - q->r);
			WASM_Log (WASM_LOG_WARN, "NQChan: %s ring full (depth=%d, overflow #%u)",
				q->name, depth, count);
		}
		if (q->overflow < 0xffffu) q->overflow++;
		q->r = (uint16_t)((q->r + 1u) % q->cap);
	}
	q->slots[q->w].length = length;
	memcpy (q->slots[q->w].data, src, length);
	q->w = next;
}

static int NqRing_Dequeue (NqRing *q, byte *buf, int len, int *src_port)
{
	while (q->r != q->w)
	{
		NqMessage *msg = &q->slots[q->r];
		unsigned int length = msg->length;
		unsigned int payload_length;

		msg->length = 0;
		q->r = (uint16_t)((q->r + 1u) % q->cap);

		if (length < NQ_PORT_HEADER_SIZE) continue;
		payload_length = length - NQ_PORT_HEADER_SIZE;
		if (payload_length > (unsigned int)len) continue;

		if (src_port) *src_port = (msg->data[0] << 8) | msg->data[1];
		memcpy (buf, msg->data + NQ_PORT_HEADER_SIZE, payload_length);
		return (int)payload_length;
	}
	return 0;
}

//----------------------------------------------------------------------------
// Module state.

static int       nq_control_socket_handle;
static qboolean  nq_control_socket_open;
static int       nq_game_socket_refs;

static byte      nq_client_ip[4];
static qboolean  nq_client_ip_set;

// Engine-visible route port → actual server id reported via CCREP_ACCEPT,
// so slist/hostcache entries stay stable when nexus reassigns routes.
static uint16_t  nq_server_id_by_route[65536];

static void NQ_ResetState (void)
{
	nq_ctl.r = nq_ctl.w = nq_ctl.overflow = 0;
	nq_data.r = nq_data.w = nq_data.overflow = 0;
	nq_client_ip_set = false;
	Q_memset (nq_client_ip, 0, sizeof(nq_client_ip));
	Q_memset (nq_server_id_by_route, 0, sizeof(nq_server_id_by_route));
}

// WASM_OnTransportReset — fired by net_wasm.c when it switches substrates
// (idle upgrade or fall-forward from a dead transport). Only the buffered
// receive frames are dropped: they belong to the session the old transport
// was carrying and would otherwise leak into the replacement. Durable
// routing state (client IP, route→server-id map) is left intact for the
// *next* connection: the new trunk session re-announces NQIP, and the
// route→server-id map outlives sessions. Note a live game connection does
// NOT survive a mid-game switch — the new session gets a fresh VirtualIP,
// so the dedicated server drops its packets until reconnect; switches are
// therefore made between connections whenever possible.
void WASM_OnTransportReset (void)
{
	nq_ctl.r  = nq_ctl.w  = nq_ctl.overflow  = 0;
	nq_data.r = nq_data.w = nq_data.overflow = 0;
}

//----------------------------------------------------------------------------
// Virtual address helpers (127.13.37.<server_id>:<route_port>).

static void WritePortBE (byte *dst, int port)
{
	dst[0] = (byte)((port >> 8) & 0xff);
	dst[1] = (byte)(port & 0xff);
}

static int ReadPortBE (const byte *src)
{
	return (src[0] << 8) | src[1];
}

static void FillServerAddr (struct qsockaddr *addr, int route_port, int server_id_port)
{
	Q_memset (addr, 0, sizeof(*addr));
	addr->sa_family = AF_INET;
	WritePortBE ((byte *)addr->sa_data, route_port);
	addr->sa_data[2] = NQ_SERVER_IP_OCTET_0;
	addr->sa_data[3] = NQ_SERVER_IP_OCTET_1;
	WritePortBE ((byte *)addr->sa_data + 4, server_id_port > 0 ? server_id_port : route_port);
}

static void FillClientAddr (struct qsockaddr *addr, int port)
{
	Q_memset (addr, 0, sizeof(*addr));
	addr->sa_family = AF_INET;
	WritePortBE ((byte *)addr->sa_data, port);
	if (nq_client_ip_set)
		memcpy ((byte *)addr->sa_data + 2, nq_client_ip, 4);
}

static int ServerIDForRoute (int route_port)
{
	int mapped;
	if (route_port <= 0 || route_port > 65535) return 0;
	mapped = (int)nq_server_id_by_route[(uint16_t)route_port];
	return (mapped > 0) ? mapped : route_port;
}

static void UpdateMyTCPIPAddress (void)
{
	struct qsockaddr addr;
	char *colon;
	FillClientAddr (&addr, 0);
	Q_strcpy (my_tcpip_address, NQChan_AddrToString (&addr));
	colon = Q_strrchr (my_tcpip_address, ':');
	if (colon) *colon = 0;
}

//----------------------------------------------------------------------------
// CCREP_ACCEPT parser + receive path.

static qboolean ParseCCREPAcceptPort (const byte *payload, int payload_len, int *accept_port)
{
	uint32_t control;
	int port;

	if (payload_len < 9) return false;

	memcpy (&control, payload, sizeof(control));
	control = BigLong (control);
	if ((control & (~NETFLAG_LENGTH_MASK)) != NETFLAG_CTL) return false;
	if ((int)(control & NETFLAG_LENGTH_MASK) != payload_len) return false;
	if (payload[4] != CCREP_ACCEPT) return false;

	memcpy (&port, &payload[5], 4);
	port = LittleLong (port);
	if (port <= 0 || port > 65535) return false;

	*accept_port = port;
	return true;
}

void WASM_OnPacket (const byte *frame, int length)
{
	int src_port, payload_len;
	const byte *payload;
	qboolean is_control = false;

	if (length < NQ_PORT_HEADER_SIZE || length > WASM_MAX_FRAME_SIZE)
		return;

	src_port = ReadPortBE (frame);
	payload = frame + NQ_PORT_HEADER_SIZE;
	payload_len = length - NQ_PORT_HEADER_SIZE;

	if (payload_len >= (int)sizeof(int))
	{
		int control;
		memcpy (&control, payload, sizeof(control));
		control = BigLong (control);
		if ((control & (~NETFLAG_LENGTH_MASK)) == NETFLAG_CTL)
			is_control = true;
	}

	if (src_port == 0)
	{
		// NQIP identity handshake: nexus sends magic + 4-byte client IP.
		if (payload_len == NQ_IDENTITY_PAYLOAD_SIZE &&
			memcmp (payload, NQ_IDENTITY_MAGIC, NQ_IDENTITY_MAGIC_LEN) == 0)
		{
			memcpy (nq_client_ip, payload + NQ_IDENTITY_MAGIC_LEN, 4);
			nq_client_ip_set = true;
			UpdateMyTCPIPAddress ();
			return;
		}
		if (is_control)
		{
			NqRing_Enqueue (&nq_ctl, frame, (unsigned int)length);
		}
		else if (payload_len >= NQ_RCON_MAGIC_LEN &&
		         memcmp (payload, NQ_RCON_MAGIC, NQ_RCON_MAGIC_LEN) == 0)
		{
			const char *cmd = (const char *)payload + NQ_RCON_MAGIC_LEN;
			int cmd_len = payload_len - NQ_RCON_MAGIC_LEN;
			if (cmd_len > 0 && cmd[cmd_len - 1] == '\0') cmd_len--;
			if (cmd_len > 0)
			{
				char buf[WASM_MAX_FRAME_SIZE];
				if (cmd_len > (int)sizeof(buf) - 2) cmd_len = (int)sizeof(buf) - 2;
				memcpy (buf, cmd, (size_t)cmd_len);
				buf[cmd_len] = '\n';
				buf[cmd_len + 1] = '\0';
				Cbuf_AddText (buf);
			}
		}
		else if (payload_len > 0)
		{
			Con_Printf ("%.*s", payload_len, (const char *)payload);
			if (payload[payload_len - 1] != '\n') Con_Printf ("\n");
		}
		return;
	}

	NqRing_Enqueue (&nq_data, frame, (unsigned int)length);
}

//----------------------------------------------------------------------------
// Send path.

static int SendFramed (int dst_port, const byte *buf, int len)
{
	byte frame[WASM_MAX_FRAME_SIZE];

	if (len < 0 || len > NET_DATAGRAMSIZE) return -1;
	if (dst_port < 0 || dst_port > 65535) return -1;

	WritePortBE (frame, dst_port);
	memcpy (frame + NQ_PORT_HEADER_SIZE, buf, (size_t)len);

	if (WASM_SendPacket (frame, len + NQ_PORT_HEADER_SIZE) < 0) return -1;
	return len;
}



//----------------------------------------------------------------------------
// Quake net_landriver_t.

int NQChan_Init (void)
{
	if (Q_strcmp (hostname.string, "UNNAMED") == 0)
		Cvar_Set ("hostname", "NexQuake");

	if ((nq_control_socket_handle = NQChan_OpenSocket (0)) == -1)
		Sys_Error ("NQChan_Init: Unable to open control socket\n");

	tcpipAvailable = true;
	UpdateMyTCPIPAddress ();
	Rcon_RegisterCommands ();
	WASM_TransportInit ();		// register net_transport (before config exec)
	return nq_control_socket_handle;
}

void NQChan_Shutdown (void)
{
	NQChan_Listen (false);
	NQChan_CloseSocket (nq_control_socket_handle);
}

void NQChan_Listen (qboolean state) { (void)state; }

int NQChan_OpenSocket (int port)
{
	(void)port;
	WASM_EnsureTransportOpen ();

	if (!nq_control_socket_open)
	{
		nq_control_socket_open = true;
		return NQ_CONTROL_SOCKET;
	}
	if (nq_game_socket_refs >= 32767)
	{
		Sys_Printf ("NQChan_OpenSocket: exhausted game socket refs\n");
		return -1;
	}
	nq_game_socket_refs++;
	return NQ_GAME_SOCKET;
}

int NQChan_CloseSocket (int socket)
{
	if (socket == NQ_CONTROL_SOCKET)
		nq_control_socket_open = false;
	else if (socket == NQ_GAME_SOCKET && nq_game_socket_refs > 0)
		nq_game_socket_refs--;

	// The carrier exists only to serve a game connection: server discovery is
	// SSE/HTTP now (DEC-020) and nothing else rides it, so its lifetime tracks
	// the game sockets. Drop it the moment the last one closes — a disconnect,
	// a server hop, or a failed connect — so it isn't held open at the menu and
	// the next connect re-selects fresh (re-applying net_transport). The control
	// socket stays open for the whole session (opened in NQChan_Init), so the
	// old `!nq_control_socket_open` guard only ever fired at engine shutdown.
	if (nq_game_socket_refs == 0)
	{
		NQ_ResetState ();
		WASM_CloseTransport ();
	}
	return 0;
}

int NQChan_Connect (int socket, struct qsockaddr *addr)
{
	(void)addr; (void)socket;
	WASM_EnsureTransportOpen ();
	return 0;
}

int NQChan_CheckNewConnections (void) { return -1; }

int NQChan_Read (int socket, byte *buf, int len, struct qsockaddr *addr)
{
	int src_port = 0;
	int ret;

	WASM_EnsureTransportOpen ();

	if (socket == NQ_CONTROL_SOCKET && nq_control_socket_open)
	{
		ret = NqRing_Dequeue (&nq_ctl, buf, len, &src_port);
		if (ret > 0) FillServerAddr (addr, src_port, ServerIDForRoute (src_port));
		return ret;
	}
	if (socket != NQ_GAME_SOCKET || nq_game_socket_refs <= 0) return -1;

	ret = NqRing_Dequeue (&nq_data, buf, len, &src_port);
	if (ret > 0)
	{
		int accept_port = 0;
		if (ParseCCREPAcceptPort (buf, ret, &accept_port) &&
			src_port > 0 && src_port <= 65535)
			nq_server_id_by_route[(uint16_t)accept_port] = (uint16_t)src_port;
		FillServerAddr (addr, src_port, ServerIDForRoute (src_port));
	}
	return ret;
}

int NQChan_Write (int socket, byte *buf, int len, struct qsockaddr *addr)
{
	int dst_port;
	(void)socket;
	if (!addr) return -1;
	WASM_EnsureTransportOpen ();

	dst_port = ReadPortBE ((byte *)addr->sa_data);
	if (dst_port <= 0 || dst_port > 65535) return -1;

	return SendFramed (dst_port, buf, len);
}

int NQChan_Broadcast (int socket, byte *buf, int len)
{
	(void)socket;
	WASM_EnsureTransportOpen ();
	return SendFramed (0, buf, len);
}

char *NQChan_AddrToString (struct qsockaddr *addr)
{
	static char buffer[22];
	int a = 0, b = 0, c = 0, d = 0, port = 0;

	if (addr)
	{
		a = (byte)addr->sa_data[2];
		b = (byte)addr->sa_data[3];
		c = (byte)addr->sa_data[4];
		d = (byte)addr->sa_data[5];
		port = ReadPortBE ((byte *)addr->sa_data);
	}
	snprintf (buffer, sizeof(buffer), "%d.%d.%d.%d:%d", a, b, c, d, port);
	return buffer;
}

int NQChan_StringToAddr (char *string, struct qsockaddr *addr)
{
	char *end;
	long port;

	if (!string || !addr) return -1;
	while (*string == ' ' || *string == '\t') string++;
	if (!*string) return -1;

	port = strtol (string, &end, 10);
	if (end == string) return -1;
	while (*end == ' ' || *end == '\t') end++;
	if (*end != '\0') return -1;
	if (port <= 0 || port > 65535) return -1;

	FillServerAddr (addr, (int)port, (int)port);
	return 0;
}

int NQChan_GetSocketAddr (int socket, struct qsockaddr *addr)
{
	(void)socket;
	FillClientAddr (addr, 0);
	return 0;
}

int NQChan_GetNameFromAddr (struct qsockaddr *addr, char *name)
{
	(void)addr;
	Q_strcpy (name, "quake-wasm");
	return 0;
}

int NQChan_GetAddrFromName (char *name, struct qsockaddr *addr)
{
	if (!addr || !name || !name[0]) return -1;
	return NQChan_StringToAddr (name, addr);
}

int NQChan_AddrCompare (struct qsockaddr *addr1, struct qsockaddr *addr2)
{
	if (!addr1 || !addr2) return -1;
	if (addr1->sa_family != addr2->sa_family) return -1;
	if (Q_memcmp (addr1->sa_data + 2, addr2->sa_data + 2, 4)) return -1;
	if (Q_memcmp (addr1->sa_data, addr2->sa_data, 2)) return 1;
	return 0;
}

int NQChan_GetSocketPort (struct qsockaddr *addr)
{
	return ReadPortBE ((byte *)addr->sa_data);
}

int NQChan_SetSocketPort (struct qsockaddr *addr, int port)
{
	if (port < 0) port = 0;
	if (port > 65535) port = 65535;
	WritePortBE ((byte *)addr->sa_data, port);

	// Host-cache/connect-path callers may pass a zeroed addr with only the
	// port set; stamp the server-id prefix so comparisons line up.
	if ((byte)addr->sa_data[2] != NQ_SERVER_IP_OCTET_0 ||
		(byte)addr->sa_data[3] != NQ_SERVER_IP_OCTET_1)
	{
		addr->sa_data[2] = NQ_SERVER_IP_OCTET_0;
		addr->sa_data[3] = NQ_SERVER_IP_OCTET_1;
		WritePortBE ((byte *)addr->sa_data + 4, port);
	}
	return 0;
}
