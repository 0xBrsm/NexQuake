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

#else /* !__EMSCRIPTEN__ — no-op stub for non-WASM builds */

void CL_Prefetch (char model_precache[][MAX_QPATH], int nummodels,
                  char sound_precache[][MAX_QPATH], int numsounds)
{
	(void)model_precache; (void)nummodels;
	(void)sound_precache; (void)numsounds;
}

#endif
