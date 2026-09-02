/*
Copyright (C) 1996-1997 Id Software, Inc.
Copyright (C) 2026 Brian St. Marie

SPDX-License-Identifier: GPL-3.0-only

This program is free software; you can redistribute it and/or
modify it under the terms of the GNU General Public License
as published by the Free Software Foundation; either version 2
of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.

See the GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program; if not, write to the Free Software
Foundation, Inc., 59 Temple Place - Suite 330, Boston, MA  02111-1307, USA.

*/
// cl_replay.c -- mid-game demo recording + instant-replay ring buffer
//
// Stock NetQuake refuses to start a demo mid-game: a .dem file must begin with
// the signon preamble (svc_serverinfo -> precache lists + baselines + static
// entities) or playback can't load the map. We work around that by passively
// capturing the live net stream while connected:
//
//   1. A *preamble* buffer holding every message from svc_serverinfo through
//      the final signon stage, demo-framed exactly as CL_WriteDemoMessage
//      writes them. Rebuilt whenever a new svc_serverinfo arrives (level
//      change / reconnect), so it always reflects the current level.
//
//   2. A *ring* buffer of the last replay_seconds seconds of post-signon
//      gameplay frames, likewise demo-framed and tagged with cl.time.
//
// With the preamble in hand:
//   - "record <name>" (patched CL_Record_f) can start at any time: it writes
//     the cd-track header + preamble, then stock cls.demorecording appends
//     live frames from then on.
//   - "replay [name]" writes preamble + ring + a trailing svc_disconnect.
//
// This follows the spirit of the proven mid-game-record pattern (QuakeSpasm's
// "demo_head"): the preamble holds the whole signon -- serverinfo, precaches,
// baselines, static entities, scores/lightstyles, svc_signonnum 1..3 -- but
// stops at the boundary where signon completes. The message that arrives at
// cls.signon == SIGNONS-1 is the first *live* frame (its svc_update promotes
// the client to SIGNONS); it, and everything after, is excluded from the
// preamble. So there is no spliced connect-time gameplay frame left over to be
// rendered and no svc_time rebasing: playback replays the signon, then opens
// directly on the first ring/live frame, with CL_LerpPoint snapping cl.time up
// to that frame's server time exactly as at the start of any demo.
//
// NOTE: NexQuake's WebSocket/WebTransport carrier piggybacks the reliable
// signon payload onto svc_time-led datagrams, so the preamble boundary keys off
// cls.signon, never the leading svc byte. The whole signon is svc_time frames
// here except the serverinfo block, so "skip svc_time frames" would drop it.

#include "quakedef.h"
#include "cl_replay.h"

// Demo frame header = int32 length + 3 * float32 view angles.
#define REPLAY_HDR_SIZE		(4 + 12)

#define REPLAY_SIGNON_MAX	(256 * 1024)		// preamble (precaches/baselines)
#define REPLAY_RING_MAX		(4 * 1024 * 1024)	// rolling gameplay window
#define REPLAY_MAX_FRAMES	8192

cvar_t replay_seconds = {"replay_seconds", "10", true};	// ring window; 0 = off

typedef struct
{
	int		start;		// offset of the framed message in replay_ring
	int		len;		// REPLAY_HDR_SIZE + message body
	float	time;		// cl.time when captured
} replay_frame_t;

static byte		*replay_signon;		// linear preamble buffer
static int		replay_signon_len;
static qboolean	replay_signon_done;	// signon completed; ring is now live

static byte		*replay_ring;		// circular gameplay buffer
static int		replay_w;		// next write offset
static int		replay_used;		// framed bytes currently held

static replay_frame_t	replay_frames[REPLAY_MAX_FRAMES];
static int		replay_head;		// oldest frame index
static int		replay_tail;		// next free frame index
static int		replay_count;

static int		replay_seq;		// auto-numbered filenames (per session)

static qboolean	replay_alloc_failed;

static void CL_Replay_f (void);

/*
==============
CL_Replay_Init
==============
*/
void CL_Replay_Init (void)
{
	Cvar_RegisterVariable (&replay_seconds);
	Cmd_AddCommand ("replay", CL_Replay_f);
}

/*
==============
CL_Replay_Reset

Drop everything captured for the current connection.
==============
*/
void CL_Replay_Reset (void)
{
	replay_signon_len = 0;
	replay_signon_done = false;
	replay_w = 0;
	replay_used = 0;
	replay_head = replay_tail = replay_count = 0;
}

static qboolean Replay_EnsureBuffers (void)
{
	if (replay_alloc_failed)
		return false;
	if (!replay_signon)
		replay_signon = malloc (REPLAY_SIGNON_MAX);
	if (!replay_ring)
		replay_ring = malloc (REPLAY_RING_MAX);
	if (!replay_signon || !replay_ring)
	{
		free (replay_signon);
		replay_signon = NULL;
		free (replay_ring);
		replay_ring = NULL;
		replay_alloc_failed = true;
		Con_Printf ("replay: out of memory, disabling capture\n");
		return false;
	}
	return true;
}

// Build a demo frame header (length + current view angles) into dst.
static void Replay_BuildHeader (byte *dst, int cursize)
{
	int		i, len;
	float	f;

	len = LittleLong (cursize);
	memcpy (dst, &len, 4);
	for (i = 0; i < 3; i++)
	{
		f = LittleFloat (cl.viewangles[i]);
		memcpy (dst + 4 + i * 4, &f, 4);
	}
}

// --- ring helpers ---------------------------------------------------------

static void Replay_RingWrite (const byte *src, int n)
{
	int		first = REPLAY_RING_MAX - replay_w;	// contiguous room before wrap

	if (first > n)
		first = n;
	memcpy (replay_ring + replay_w, src, first);
	if (n > first)
		memcpy (replay_ring, src + first, n - first);
	replay_w = (replay_w + n) % REPLAY_RING_MAX;
	replay_used += n;
}

static void Replay_EvictOldest (void)
{
	replay_used -= replay_frames[replay_head].len;
	replay_head = (replay_head + 1) % REPLAY_MAX_FRAMES;
	replay_count--;
}

static void Replay_PushFrame (int cursize, float now)
{
	byte	hdr[REPLAY_HDR_SIZE];
	int		framed = REPLAY_HDR_SIZE + cursize;
	int		start;
	float	dur;

	if (framed > REPLAY_RING_MAX)
		return;					// pathological; should never happen

	// make room (space + frame-slot limits)
	while (replay_count > 0 &&
		(replay_used + framed > REPLAY_RING_MAX || replay_count >= REPLAY_MAX_FRAMES))
		Replay_EvictOldest ();

	start = replay_w;
	Replay_BuildHeader (hdr, cursize);
	Replay_RingWrite (hdr, REPLAY_HDR_SIZE);
	Replay_RingWrite (net_message.data, cursize);

	replay_frames[replay_tail].start = start;
	replay_frames[replay_tail].len = framed;
	replay_frames[replay_tail].time = now;
	replay_tail = (replay_tail + 1) % REPLAY_MAX_FRAMES;
	replay_count++;

	// trim to the rolling time window, keeping at least one frame
	dur = replay_seconds.value;
	if (dur < 0)
		dur = 0;
	while (replay_count > 1 && (now - replay_frames[replay_head].time) > dur)
		Replay_EvictOldest ();
}

// --- preamble helpers -----------------------------------------------------

static void Replay_AppendSignon (int cursize)
{
	int		framed = REPLAY_HDR_SIZE + cursize;

	if (replay_signon_len + framed > REPLAY_SIGNON_MAX)
	{
		// preamble overflow; keep what we have (demo may be incomplete)
		Con_DPrintf ("replay: preamble buffer full (%i bytes)\n", REPLAY_SIGNON_MAX);
		return;
	}
	Replay_BuildHeader (replay_signon + replay_signon_len, cursize);
	memcpy (replay_signon + replay_signon_len + REPLAY_HDR_SIZE,
		net_message.data, cursize);
	replay_signon_len += framed;
}

/*
==============
CL_Replay_Capture

Called from CL_GetMessage for every live message. net_message holds the raw
bytes and cls.signon reflects the state *before* this message is parsed. The
static signon messages (cls.signon < SIGNONS) go into the preamble; the first
gameplay datagram -- the one that would flip cls.signon to SIGNONS -- is the
boundary: it and every datagram after it belong to the live/ring stream.
==============
*/
void CL_Replay_Capture (void)
{
	int		cursize = net_message.cursize;

	if (cls.demoplayback || cursize <= 0)
		return;
	if (!Replay_EnsureBuffers ())
		return;

	if (cls.signon != SIGNONS)
	{
		// preamble: a fresh svc_serverinfo starts a new map -- the old ring
		// references stale precache indices, so reset everything.
		if (net_message.data[0] == svc_serverinfo)
			CL_Replay_Reset ();

		// The message that arrives at SIGNONS-1 is the final signon stage: its
		// first svc_update promotes the client to SIGNONS (see CL_ParseUpdate).
		// That is the first *live* frame, so we keep it -- and everything after
		// -- OUT of the preamble. Note NexQuake's transport piggybacks the
		// signon payload (baselines, svc_signonnum) onto svc_time-led datagrams,
		// so the boundary must key off cls.signon, NOT the message's leading
		// svc byte; everything up to SIGNONS-1 (serverinfo + signon datagrams)
		// is genuine signon data and belongs in the preamble.
		if (cls.signon == SIGNONS - 1)
		{
			replay_signon_done = true;
			return;
		}

		Replay_AppendSignon (cursize);
		return;
	}

	replay_signon_done = true;

	// the ring is only needed for "replay"; skip it when disabled
	if (replay_seconds.value > 0)
		Replay_PushFrame (cursize, cl.time);
}

// --- shared preamble export (used by record and replay) -------------------

qboolean CL_Demo_HavePreamble (void)
{
	return replay_signon_done && replay_signon_len > 0;
}

void CL_Demo_WritePreamble (FILE *f)
{
	if (replay_signon_len <= 0)
		return;
	// The preamble is the static signon only (no gameplay datagram), so it is
	// written verbatim -- there is no timestamp to rebase. The caller's next
	// frames (live for "record", ring for "replay") carry their own svc_time.
	fwrite (replay_signon, 1, replay_signon_len, f);
}

// --- replay command -------------------------------------------------------

static void Replay_WriteRingFrame (FILE *f, int start, int len)
{
	int		first = REPLAY_RING_MAX - start;
	if (first > len)
		first = len;
	fwrite (replay_ring + start, 1, first, f);
	if (len > first)
		fwrite (replay_ring, 1, len - first, f);
}

// Auto-named, map-aware filename: "<gamedir>/<map>-replay-NN.dem".
static void Replay_AutoName (char *out, int outsize)
{
	char	base[MAX_QPATH];

	if (cl.worldmodel && cl.worldmodel->name[0])
		COM_FileBase (cl.worldmodel->name, base);
	else
		strcpy (base, "replay");
	snprintf (out, outsize, "%s/%s-replay-%02i.dem", com_gamedir, base, replay_seq++);
}

static void CL_Replay_f (void)
{
	char	name[MAX_OSPATH];
	char	disc[REPLAY_HDR_SIZE + 1];
	FILE	*f;
	int		i, idx;
	float	span;

	if (cmd_source != src_command)
		return;

	if (cls.state != ca_connected || cls.demoplayback)
	{
		Con_Printf ("replay: not connected to a live game\n");
		return;
	}
	if (!CL_Demo_HavePreamble ())
	{
		Con_Printf ("replay: no replay buffer yet\n");
		return;
	}
	if (replay_count == 0)
	{
		Con_Printf ("replay: buffer is empty (replay_seconds 0?)\n");
		return;
	}

	if (Cmd_Argc () >= 2)
	{
		if (strstr (Cmd_Argv (1), ".."))
		{
			Con_Printf ("Relative pathnames are not allowed.\n");
			return;
		}
		// reserve room for COM_DefaultExtension's unchecked strcat of ".dem"
		snprintf (name, sizeof(name) - 5, "%s/%s", com_gamedir, Cmd_Argv (1));
		COM_DefaultExtension (name, ".dem");
	}
	else
	{
		Replay_AutoName (name, sizeof(name));
	}

	f = fopen (name, "wb");
	if (!f)
	{
		Con_Printf ("replay: couldn't open %s\n", name);
		return;
	}

	// cd track header line (-1 = no forced track), then the static signon
	// preamble; the ring frames that follow carry their own honest svc_time.
	fprintf (f, "%i\n", -1);
	CL_Demo_WritePreamble (f);

	// the rolling window, oldest to newest
	for (i = 0; i < replay_count; i++)
	{
		idx = (replay_head + i) % REPLAY_MAX_FRAMES;
		Replay_WriteRingFrame (f, replay_frames[idx].start, replay_frames[idx].len);
	}

	// trailing svc_disconnect so playback ends cleanly
	Replay_BuildHeader ((byte *)disc, 1);
	disc[REPLAY_HDR_SIZE] = svc_disconnect;
	fwrite (disc, 1, REPLAY_HDR_SIZE + 1, f);

	fclose (f);

	idx = (replay_tail - 1 + REPLAY_MAX_FRAMES) % REPLAY_MAX_FRAMES;
	span = replay_frames[idx].time - replay_frames[replay_head].time;
	Con_Printf ("replay: wrote %s (%.1fs, %i frames)\n", name, span, replay_count);
}
