/*
 * NexQuake: Game-directory switching.
 *
 * Manages saving/restoring the engine's filesystem search-path baseline so the
 * WASM client can hot-swap game directories when connecting to different servers.
 */

#ifndef COM_GAMESWITCH_H
#define COM_GAMESWITCH_H

/*
 * Initialise baseline state after COM_InitFilesystem() has added the default
 * game directories.  Must be called once, at the end of COM_InitFilesystem().
 */
void COM_GameSwitch_Init (const char *basedir);

/*
 * Add the main game directory *and* the user-writable overlay for a game.
 * Replaces direct COM_AddGameDirectory() calls in COM_InitFilesystem().
 */
void COM_AddGameDirectories (const char *basedir, const char *gamedir);

/*
 * Switch the active game directory. Saves the current configuration, notifies
 * JavaScript layer, resets search paths to the baseline snapshot, and
 * optionally re-adds the new game's directories. Pass NULL or "" to revert
 * to the base game.
 */
void COM_SwitchGame (const char *gamedir);

#endif /* COM_GAMESWITCH_H */
