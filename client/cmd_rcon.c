/*
 * cmd_rcon.c
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * This module is part of NexQuake and includes derivative work from
 * upstream websocket networking implementations by initialed85.
 * See ../ATTRIBUTIONS.md for upstream repositories, paths, and pinned commits.
 */

#include "quakedef.h"

#include <stdlib.h>

#include "net_ws_transport.h"

static qboolean rcon_commands_registered = false;

static cvar_t rcon_password = {"rcon_password", ""};

static void Rcon_f(void)
{
	char *target;
	char targetbuf[32];
	char *cmd;
	char *pw;
	int payload_len;
	byte *payload;
	int targetlen;
	int cmdlen;
	int pwlen;
	int argc;

	argc = Cmd_Argc();
	if (argc < 2)
	{
		Con_Printf("usage: rcon <cmd> | rcon <host|port> <cmd>\n");
		return;
	}

	targetbuf[0] = 0;
	cmd = Cmd_Args();
	target = targetbuf;
	if (argc > 2)
	{
		char *arg1 = Cmd_Argv(1);
		int i;

		if (Q_strcasecmp(arg1, "nexus") == 0)
		{
			Q_strncpy(targetbuf, "0", sizeof(targetbuf) - 1);
			targetbuf[sizeof(targetbuf) - 1] = 0;
		}

		// First, treat arg1 as a hostcache name and resolve to its port.
		for (i = 0; !targetbuf[0] && i < hostCacheCount; i++)
		{
			if (Q_strcasecmp(arg1, hostcache[i].name) == 0)
			{
				Q_strncpy(targetbuf, hostcache[i].cname, sizeof(targetbuf) - 1);
				targetbuf[sizeof(targetbuf) - 1] = 0;
				break;
			}
		}

		// Otherwise, accept direct numeric port targets.
		if (!targetbuf[0] && arg1[0] >= '0' && arg1[0] <= '9')
		{
			char *end;
			long port = strtol(arg1, &end, 10);
			if (end != arg1 && *end == '\0' && port >= 0 && port <= 65535)
			{
				Q_strncpy(targetbuf, arg1, sizeof(targetbuf) - 1);
				targetbuf[sizeof(targetbuf) - 1] = 0;
			}
		}

		if (targetbuf[0])
		{
			// Retokenize the args string so Cmd_Args() yields everything after the target.
			Cmd_TokenizeString(cmd);
			cmd = Cmd_Args();
		}
	}

	if (!targetbuf[0] && cls.state == ca_connected && cls.netcon)
	{
		int ld = cls.netcon->landriver;
		int i;

		if (ld >= 0 && ld < net_numlandrivers && net_landrivers[ld].initialized)
			for (i = 0; i < hostCacheCount; i++)
				if (hostcache[i].ldriver == ld &&
					net_landrivers[ld].AddrCompare(&cls.netcon->addr, &hostcache[i].addr) >= 0)
				{
					Q_strncpy(targetbuf, hostcache[i].cname, sizeof(targetbuf) - 1);
					targetbuf[sizeof(targetbuf) - 1] = 0;
					break;
				}
	}
	if (!targetbuf[0] && (cls.state != ca_connected || !cls.netcon))
	{
		// When disconnected, default bare "rcon <cmd>" to nexus control.
		Q_strncpy(targetbuf, "0", sizeof(targetbuf) - 1);
		targetbuf[sizeof(targetbuf) - 1] = 0;
	}

	cmdlen = Q_strlen(cmd);
	pw = rcon_password.string;
	pwlen = pw ? Q_strlen(pw) : 0;
	targetlen = Q_strlen(target);

	payload_len = pwlen + 1 + targetlen + 1 + cmdlen;
	payload = (byte *)Z_Malloc(payload_len);
	if (pwlen > 0)
		Q_memcpy(payload, pw, pwlen);
	payload[pwlen] = 0;
	if (targetlen > 0)
		Q_memcpy(payload + pwlen + 1, target, targetlen);
	payload[pwlen + 1 + targetlen] = 0;
	Q_memcpy(payload + pwlen + 1 + targetlen + 1, cmd, cmdlen);

	// Port 0 is nexus control traffic.
	if (WebSocketTransport_SendFrame(0, payload, payload_len) < 0)
		Con_Printf("rcon: send failed (%s)\n", WebSocketTransport_LastSendError());

	Z_Free(payload);
}

void Rcon_RegisterCommands(void)
{
	if (rcon_commands_registered)
		return;
	rcon_commands_registered = true;

	Cvar_RegisterVariable(&rcon_password);
	Cmd_AddCommand("rcon", Rcon_f);
}
