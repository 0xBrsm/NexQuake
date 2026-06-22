/*
 * NexQuake: Parallel asset prefetch for WASM client builds.
 *
 * When connecting to a server the engine receives lists of models and sounds
 * it will need.  On native Quake these are loaded synchronously from disk,
 * but in the browser each file is a network fetch.  This module hands the
 * full precache lists to the JavaScript VFS layer so it can fetch them in
 * parallel before the engine starts loading them one-by-one.
 */

#ifndef CL_PREFETCH_H
#define CL_PREFETCH_H

/*
 * Prefetch all precached models and sounds in parallel via the JavaScript VFS.
 * Blocks (via emscripten_sleep) until the JS layer signals completion or the
 * timeout expires.  On non-Emscripten builds this is a no-op.
 */
void CL_Prefetch (char model_precache[][MAX_QPATH], int nummodels,
                  char sound_precache[][MAX_QPATH], int numsounds);

/*
 * Non-blocking `play`/`playvol` for the WASM client.
 *
 * A `play foo.wav` for a file that isn't resident (e.g. a mod's stuffcmd
 * announcer/frag sounds, which are never precached) would otherwise trigger a
 * synchronous XHR in the lazy VFS and freeze the whole client.  NQSnd_PlayDeferred
 * plays immediately if the file is already resident; otherwise it kicks a
 * background fetch and queues the play.  NQSnd_PollDeferred, called once per frame
 * from S_Update, fires queued plays as soon as their file lands and drops ones
 * that never arrive within a short window.  On native builds these play
 * immediately (the file is local).
 */
void NQSnd_PlayDeferred (const char *name, float vol);
void NQSnd_PollDeferred (void);

#endif /* CL_PREFETCH_H */
