/*
 * cmd_rcon.c
 * SPDX-License-Identifier: GPL-3.0-only
 *
 * In-game rcon console command. Collects the user's arguments and the
 * rcon_password cvar, hands them off to the JS-side JSON-RPC client
 * (src/client/shell/55-rcon.js), and prints the formatted reply.
 */

#include "quakedef.h"

#include <emscripten.h>

static qboolean rcon_commands_registered = false;

static cvar_t rcon_password = {"rcon_password", ""};

extern int NQWasm_GetConnectedServerListenPort(void);

/*
 * js_rcon_exec dispatches `args_line` as a JSON-RPC request against /rcon.
 * password is used to build the Authorization: Rcon <password> header.
 * connected_port seeds the fallback target for bare server-console commands
 * (e.g. `rcon status` while connected). The formatted text reply is written
 * into out_buf (NUL-terminated, truncated if larger than out_len).
 */
EM_ASYNC_JS(void, js_rcon_exec,
	(const char *password, const char *args_line, int connected_port,
	 char *out_buf, int out_len),
{
	// Any throw or rejection here would abort the Asyncify resume and hang
	// the suspended engine frame — catch everything and return text instead.
	var reply;
	try {
		if (typeof Module.nqRcon !== 'function')
			throw new Error('rcon client unavailable');
		reply = await Module.nqRcon(
			UTF8ToString(password),
			UTF8ToString(args_line),
			connected_port
		);
	} catch (e) {
		reply = 'rcon error: ' + String(e && e.message || e) + '\n';
	}
	stringToUTF8(String(reply || ""), out_buf, out_len);
});

#define RCON_REPLY_BUF 65536
static char rcon_reply[RCON_REPLY_BUF];

static void Rcon_f(void)
{
	const char *password;
	const char *args_line;
	int connected_port;

	if (Cmd_Argc() < 2)
	{
		Con_Printf("usage: rcon <cmd>  (try: rcon help)\n");
		return;
	}

	password = rcon_password.string ? rcon_password.string : "";
	args_line = Cmd_Args();
	connected_port = NQWasm_GetConnectedServerListenPort();

	rcon_reply[0] = 0;
	js_rcon_exec(password, args_line, connected_port, rcon_reply, RCON_REPLY_BUF);
	Con_Printf("%s", rcon_reply);
}

void Rcon_RegisterCommands(void)
{
	if (rcon_commands_registered)
		return;
	rcon_commands_registered = true;

	Cvar_RegisterVariable(&rcon_password);
	Cmd_AddCommand("rcon", Rcon_f);
}
