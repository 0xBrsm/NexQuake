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

/* Load (precaching as needed) and immediately start a sound. Shared by the
 * WASM deferred-play path and the native build's direct play. */
static void NQSnd_PlayNow (const char *name, float vol)
{
	static int	hash = 345;
	sfx_t		*sfx = S_PrecacheSound ((char *)name);
	if (sfx)
		S_StartSound (hash++, 0, sfx, listener_origin, vol, 1.0);
}

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

/*
 * Non-blocking `play`/`playvol`.  See cl_prefetch.h.  Mods (e.g. Rocket Arena)
 * fire announcer/frag sounds via `stuffcmd "play ra/<x>.wav"` — a path that
 * bypasses precaching entirely, so the file is cold and the lazy VFS would block
 * the whole client on a synchronous XHR.  Instead we play immediately when the
 * file is resident, and otherwise queue the play behind a background fetch.
 */
#define NQ_PLAY_MAX	8
#define NQ_PLAY_STALE	1.5	/* seconds; a late announcer is worse than a dropped one */

typedef struct {
	char	name[MAX_QPATH];
	float	vol;
	double	queued;
} nq_pending_play_t;

static nq_pending_play_t	nq_pending[NQ_PLAY_MAX];
static int			nq_pending_count;

/*
 * Pure check: is the sound already downloaded into the VFS?  Returns 1 if
 * resident (safe to load+play now without a network stall), 0 otherwise.  No
 * side effects — the poll calls this every frame, so it must not re-fetch.  If
 * the VFS shim is absent (unexpected) we report resident so playback falls back
 * to the engine's normal synchronous load rather than going silent.
 */
static int NQSnd_Resident (const char *name)
{
	return EM_ASM_INT({
		if (typeof Module === 'undefined' ||
		    typeof Module.nexquakeSoundResident !== 'function')
			return 1;
		return Module.nexquakeSoundResident(UTF8ToString($0)) ? 1 : 0;
	}, name);
}

/* Kick a one-time background fetch for a cold sound (issued once per play). */
static void NQSnd_KickFetch (const char *name)
{
	EM_ASM({
		if (typeof Module === 'undefined') return;
		if (typeof Module.nexquakePrefetchEnqueue === 'function')
			Module.nexquakePrefetchEnqueue('sound/' + UTF8ToString($0));
		if (typeof Module.nexquakePrefetchStart === 'function')
			Module.nexquakePrefetchStart();
	}, name);
}

void NQSnd_PlayDeferred (const char *name, float vol)
{
	nq_pending_play_t	*p;

	if (!name || !name[0])
		return;
	if (NQSnd_Resident (name))
	{
		NQSnd_PlayNow (name, vol);
		return;
	}
	/* Cold: kick a one-time background fetch and queue the play for the poll. */
	NQSnd_KickFetch (name);
	if (nq_pending_count >= NQ_PLAY_MAX)
		return;			/* queue full: drop rather than stall */
	p = &nq_pending[nq_pending_count++];
	Q_strncpy (p->name, name, MAX_QPATH - 1);
	p->name[MAX_QPATH - 1] = 0;
	p->vol = vol;
	p->queued = Sys_FloatTime ();
}

void NQSnd_PollDeferred (void)
{
	double	now;
	int	i;

	if (nq_pending_count == 0)
		return;			/* common case: no clock crossing, no work */
	now = Sys_FloatTime ();

	for (i = 0; i < nq_pending_count; )
	{
		qboolean ready = NQSnd_Resident (nq_pending[i].name) ? true : false;

		if (ready || (now - nq_pending[i].queued) > NQ_PLAY_STALE)
		{
			if (ready)
				NQSnd_PlayNow (nq_pending[i].name, nq_pending[i].vol);
			nq_pending[i] = nq_pending[--nq_pending_count];	/* swap-remove */
		}
		else
			i++;
	}
}

#else /* !__EMSCRIPTEN__ — native builds load from local disk, so play directly */

void CL_Prefetch (char model_precache[][MAX_QPATH], int nummodels,
                  char sound_precache[][MAX_QPATH], int numsounds)
{
	(void)model_precache; (void)nummodels;
	(void)sound_precache; (void)numsounds;
}

void NQSnd_PlayDeferred (const char *name, float vol)
{
	if (!name || !name[0])
		return;
	NQSnd_PlayNow (name, vol);
}

void NQSnd_PollDeferred (void) { }

#endif
