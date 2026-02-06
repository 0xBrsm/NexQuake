/*
 * cmd_rcon.c
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * This module is part of NexQuake and includes derivative work from
 * upstream websocket networking implementations by initialed85.
 * See ../ATTRIBUTIONS.md for upstream repositories, paths, and pinned commits.
 */

#include "quakedef.h"

extern void RconToken_SetFromPassword(const char *password);

static qboolean rcon_commands_registered = false;

static void RconPassword_f(void)
{
	const char *pw;

	if (Cmd_Argc() != 2)
	{
		Con_Printf("usage: rcon_password <password>\n");
		return;
	}

	pw = Cmd_Argv(1);
	if (!pw || !pw[0])
	{
		Con_Printf("usage: rcon_password <password>\n");
		return;
	}

	RconToken_SetFromPassword(pw);
	Con_Printf("Password set. You must reload the client for it to take effect.\n");
}

static void Rcon_f(void)
{
	if (cls.state != ca_connected)
	{
		Con_Printf("Can't \"%s\", not connected\n", Cmd_Argv(0));
		return;
	}

	if (cls.demoplayback)
		return;

	MSG_WriteByte(&cls.message, clc_stringcmd);
	if (Cmd_Argc() > 1)
		SZ_Print(&cls.message, Cmd_Args());
	else
		SZ_Print(&cls.message, "\n");
}

void Rcon_RegisterCommands(void)
{
	if (rcon_commands_registered)
		return;
	rcon_commands_registered = true;

	Cmd_AddCommand("rcon_password", RconPassword_f);
	Cmd_AddCommand("rcon", Rcon_f);
}
