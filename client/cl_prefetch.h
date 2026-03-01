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

#endif /* CL_PREFETCH_H */
