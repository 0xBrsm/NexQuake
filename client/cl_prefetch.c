/*
 * NexQuake: Parallel asset prefetch for WASM client builds.
 *
 * Hands the full model/sound precache lists to the JavaScript VFS layer so
 * it can fetch them all in parallel, then blocks until the JS side signals
 * completion (or a timeout expires).  This avoids the "death by latency"
 * problem of many sequential per-file fetches when the browser cache is cold.
 */

#include "quakedef.h"
#include "cl_prefetch.h"

#ifdef __EMSCRIPTEN__

#include <emscripten/emscripten.h>

void CL_Prefetch (char model_precache[][MAX_QPATH], int nummodels,
                  char sound_precache[][MAX_QPATH], int numsounds)
{
	int	i;

	/* 1. Reset the prefetch queue. */
	EM_ASM({
		if (typeof Module === 'undefined') return;
		if (typeof Module.nexquakePrefetchReset === 'function')
			Module.nexquakePrefetchReset();
	});

	/* 2. Enqueue every model and sound. */
	for (i = 1; i < nummodels; i++)
	{
		EM_ASM({
			if (typeof Module === 'undefined') return;
			if (typeof Module.nexquakePrefetchEnqueue === 'function')
				Module.nexquakePrefetchEnqueue(UTF8ToString($0));
		}, model_precache[i]);
	}
	for (i = 1; i < numsounds; i++)
	{
		EM_ASM({
			if (typeof Module === 'undefined') return;
			if (typeof Module.nexquakePrefetchEnqueue === 'function')
				Module.nexquakePrefetchEnqueue('sound/' + UTF8ToString($0));
		}, sound_precache[i]);
	}

	/* 3. Kick off the parallel fetches. */
	EM_ASM({
		if (typeof Module === 'undefined') return;
		if (typeof Module.nexquakePrefetchStart === 'function')
			Module.nexquakePrefetchStart();
	});

	/* 4. Spin until the JS layer is done or we hit the timeout. */
	{
		double start      = Sys_FloatTime ();
		double wait_limit = 30.0;

		while ((Sys_FloatTime () - start) < wait_limit)
		{
			int busy = EM_ASM_INT({
				if (typeof Module === 'undefined') return 0;
				return Module.nexquakePrefetchBusy ? 1 : 0;
			});
			if (!busy)
				break;
			emscripten_sleep (1);
		}

		{
			int failed = EM_ASM_INT({
				if (typeof Module === 'undefined' || !Module.nexquakePrefetchFailures) return 0;
				return Object.keys(Module.nexquakePrefetchFailures).length;
			});
			if (failed > 0)
				Con_DPrintf ("NexQuake prefetch: %d assets failed; falling back to lazy fetch\n", failed);
		}
	}
}

/*
 * NQWasm_PrefetchKnownSounds warms every sound currently in the engine's sfx
 * registry (known_sfx). It exists to cover the client-side precaches that no
 * server sound_precache list carries — the temp-entity sounds from CL_InitTEnts
 * (rocket explosion weapons/r_exp3.wav, ricochets, tink, monster hits) and the
 * ambient sounds from S_Init — which CL_Prefetch therefore misses. Without this,
 * their first in-game play (e.g. a rocket frag) triggers a blocking synchronous
 * fetch and a visible hitch.
 *
 * Called once from the shell right after Host_Init has populated the registry
 * (see 10-startup.js). Unlike CL_Prefetch it does NOT block: there's no loading
 * screen here to hide a wait behind, and these sounds aren't needed until
 * gameplay, so a background warm suffices. Files already resident — bundled base
 * assets, or anything a prior fetch warmed — are skipped JS-side, and server
 * sounds added later are handled by CL_Prefetch on connect.
 */
EMSCRIPTEN_KEEPALIVE void NQWasm_PrefetchKnownSounds (void)
{
	extern sfx_t	*known_sfx;
	extern int	 num_sfx;
	int		 i;

	if (!known_sfx || num_sfx <= 0)
		return;

	for (i = 0; i < num_sfx; i++)
	{
		if (!known_sfx[i].name[0])
			continue;
		EM_ASM({
			if (typeof Module === 'undefined') return;
			if (typeof Module.nexquakePrefetchEnqueue === 'function')
				Module.nexquakePrefetchEnqueue('sound/' + UTF8ToString($0));
		}, known_sfx[i].name);
	}

	EM_ASM({
		if (typeof Module === 'undefined') return;
		if (typeof Module.nexquakePrefetchStart === 'function')
			Module.nexquakePrefetchStart();
	});
}

#else /* !__EMSCRIPTEN__ — no-op stub for non-WASM builds */

void CL_Prefetch (char model_precache[][MAX_QPATH], int nummodels,
                  char sound_precache[][MAX_QPATH], int numsounds)
{
	(void)model_precache; (void)nummodels;
	(void)sound_precache; (void)numsounds;
}

#endif
