/*
Copyright (C) 1996-1997 Id Software, Inc.
Copyright (C) 2024 NexQuake contributors

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

// Writes the captured signon preamble (demo-framed messages) to an open demo
// file, immediately after the caller has written the cd-track header line.
// Used by both mid-game "record" and "replay". anchortime is the server time
// (cl.mtime[0] for record, the oldest ring frame for replay) that the frames
// following the preamble will start at; the preamble's lone gameplay datagram
// is rebased just before it so the demo timeline is contiguous.
void CL_Demo_WritePreamble (FILE *f, float anchortime);

#endif // CL_REPLAY_H
