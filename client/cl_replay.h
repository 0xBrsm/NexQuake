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
// cl_replay.h -- mid-game demo recording + instant-replay ring buffer
//
// Both features rest on one foundation: a passively captured signon "preamble"
// for the current level. Stock NetQuake refuses to start a demo mid-game only
// because that preamble is missing; once we have it, "record" can begin at any
// time and "replay" can dump the last few seconds of play.

#ifndef CL_REPLAY_H
#define CL_REPLAY_H

// Registers the "replay" command and replay_* cvars. Called from CL_Init.
void CL_Replay_Init (void);

// Captures the freshly received live net_message into the preamble and ring.
// Called from CL_GetMessage for every message read off the wire (never during
// demo playback).
void CL_Replay_Capture (void);

// Drops the captured preamble and ring buffer. Called from CL_Disconnect.
void CL_Replay_Reset (void);

// True once a complete signon preamble has been captured for the current
// connection -- i.e. a valid demo can be seeded mid-game.
qboolean CL_Demo_HavePreamble (void);

// Writes the captured static signon preamble (demo-framed messages) to an open
// demo file, immediately after the caller has written the cd-track header line.
// Used by both mid-game "record" and "replay". The preamble contains no gameplay
// datagram, so it is written verbatim; the frames the caller appends afterwards
// (live for record, the ring for replay) carry their own svc_time.
void CL_Demo_WritePreamble (FILE *f);

#endif // CL_REPLAY_H
