/*
 * NexQuake: Game-directory switching.
 *
 * Hot-swaps the engine's filesystem search paths so the WASM client can
 * transparently follow a server into whatever game directory it is running.
 *
 * The approach:
 *   1. At startup, after COM_InitFilesystem() adds the base game dirs,
 *      COM_GameSwitch_Init() snapshots the search-path chain and gamedir.
 *   2. On every server connect (or disconnect) COM_SwitchGame() restores
 *      that baseline, then layers the new game's directories on top.
 *   3. A per-game config mode (controlled by JS Module.nqPerModConfig)
 *      decides whether config.cfg is saved per-game or unified.
 */

#include "quakedef.h"
#include "com_gameswitch.h"

#ifdef __EMSCRIPTEN__
#include <emscripten/emscripten.h>
#endif

/* Provided by host.c (added by host.c patch). */
extern int			fs_hunklevel;

/* Provided by host.c — not declared in any upstream header. */
extern void Host_WriteConfiguration (void);

/* --- private state ------------------------------------------------------ */

static char			com_gameswitch_basedir[MAX_OSPATH];
static struct searchpath_s	*com_gameswitch_base_searchpaths;
static char			com_gameswitch_base_gamedir[MAX_OSPATH];
static int			com_gameswitch_base_hunklevel;
static const char		*com_user_root = ".usr";

static void COM_GameSwitch_SetGameDir (char *gamedir)
{
	Q_strncpy (com_gamedir, gamedir, MAX_OSPATH - 1);
	com_gamedir[MAX_OSPATH - 1] = 0;
}

static void COM_GameSwitch_RestoreBase (void)
{
	com_searchpaths = com_gameswitch_base_searchpaths;
	COM_GameSwitch_SetGameDir (com_gameswitch_base_gamedir);
}

/* ------------------------------------------------------------------------ */

void COM_AddGameDirectories (const char *basedir, const char *gamedir)
{
	COM_AddGameDirectory (va("%s/%s", basedir, gamedir));
	COM_AddGameDirectory (va("%s/%s/%s", basedir, com_user_root, gamedir));
}

void COM_GameSwitch_Init (const char *basedir)
{
	Q_strncpy (com_gameswitch_basedir, basedir, sizeof(com_gameswitch_basedir) - 1);
	com_gameswitch_basedir[sizeof(com_gameswitch_basedir) - 1] = 0;

	com_gameswitch_base_searchpaths = com_searchpaths;
	Q_strncpy (com_gameswitch_base_gamedir, com_gamedir, sizeof(com_gameswitch_base_gamedir) - 1);
	com_gameswitch_base_gamedir[sizeof(com_gameswitch_base_gamedir) - 1] = 0;

	com_gameswitch_base_hunklevel = fs_hunklevel = Hunk_LowMark ();

#ifdef __EMSCRIPTEN__
	EM_ASM({
		if (typeof Module === 'undefined') return;
		Module.nexquakeBaseGameName = UTF8ToString($0);
	}, GAMENAME);
#endif
}

void COM_SwitchGame (const char *gamedir)
{
	char		game[16];
	const char	*requested_game = gamedir && gamedir[0] ? gamedir : GAMENAME;
	qboolean	per_game_config = true;

	Q_strncpy (game, requested_game, sizeof(game) - 1);
	game[sizeof(game) - 1] = 0;

	/* Save config before switching. In per-game mode the save targets the
	 * outgoing game directory (com_gamedir); in unified mode it targets the
	 * base game directory so no game-specific config.cfg is ever created. */
#ifdef __EMSCRIPTEN__
	per_game_config = EM_ASM_INT({ return Module.nqPerModConfig ? 1 : 0; });
	if (!per_game_config)
		COM_GameSwitch_SetGameDir (com_gameswitch_base_gamedir);
#endif
	Host_WriteConfiguration ();

#ifdef __EMSCRIPTEN__
	EM_ASM({
		if (typeof Module === 'undefined') return;
		if (typeof Module.nexquakeSwitchGameData !== 'function') return;
		try {
			Module.nexquakeSwitchGameData(UTF8ToString($0));
		} catch (e) {
			if (typeof console !== 'undefined' && console.error)
				console.error('nexquakeSwitchGameData failed:', e);
		}
	}, game);
#endif

	/* Reset to baseline, then add the game directory on top. */
	COM_GameSwitch_RestoreBase ();
	fs_hunklevel = com_gameswitch_base_hunklevel;

	if (Q_strcasecmp (game, GAMENAME) != 0 && com_gameswitch_basedir[0])
	{
		COM_AddGameDirectories (com_gameswitch_basedir, game);
		fs_hunklevel = Hunk_LowMark ();
	}

	if (per_game_config)
		Cbuf_InsertText ("exec quake.rc\n");
}
