// snd_wasm.c -- WebAudio sound
#include "quakedef.h"
#include <emscripten.h>
#include <string.h>
#include <stdint.h>

#define DMA_SAMPLES 16384
static dma_t the_shm;
static int snd_inited;
static int16_t dma_buffer[DMA_SAMPLES];
static int audio_read_cursor;
static uint32_t audio_submit_seq;
static int snd_paused;
extern int desired_speed;
extern int snd_blocked;

EM_JS(int, js_audio_init, (int rate, int buf, int samples, int cursor, int submit_seq_ptr), {
	try {
		var AC = window.AudioContext || window.webkitAudioContext;
		if (typeof AC !== 'function') return 0;

		var ctx;
		try { ctx = new AC({sampleRate: rate}); }
		catch (e) { ctx = new AC(); }

		if (typeof ctx.createScriptProcessor !== 'function') return 0;

		var ua = navigator.userAgent || '';
		var blockSize = /android|iphone|ipad|ipod/i.test(ua) ? 1024 : 512;
		var node = ctx.createScriptProcessor(blockSize, 0, 2);
		var mask = samples - 1;
		var base = buf >> 1;
		var ca = cursor >> 2;
		var sa = submit_seq_ptr >> 2;
		var staleBudget = Math.min(samples >> 1, Math.max(512, (rate * 0.35) | 0));
		var lastSubmitSeq = 0;
		var framesSinceSubmit = staleBudget;
		var fadeGain = 0.0;
		var fadeStep = 1.0 / 256.0;
		var scale = 1.0 / 32768.0;

		var a = {
			ctx: ctx, node: node,
			paused: false, suspendToken: 0,
			targets: null, events: null, resume: null
		};

		node.onaudioprocess = function(e) {
			var L = e.outputBuffer.getChannelData(0);
			var R = e.outputBuffer.getChannelData(1);

			var seq = HEAPU32[sa] >>> 0;
			if (seq !== lastSubmitSeq) {
				lastSubmitSeq = seq;
				framesSinceSubmit = 0;
			} else {
				framesSinceSubmit += L.length;
			}

			var active = !a.paused && framesSinceSubmit < staleBudget;

			// Fast path: fully silent and should stay silent
			if (!active && fadeGain <= 0.0) {
				L.fill(0);
				R.fill(0);
				return;
			}

			var target = active ? 1.0 : 0.0;
			var p = HEAP32[ca];
			for (var i = 0; i < L.length; i++) {
				if (fadeGain < target) {
					fadeGain += fadeStep;
					if (fadeGain > 1.0) fadeGain = 1.0;
				} else if (fadeGain > target) {
					fadeGain -= fadeStep;
					if (fadeGain < 0.0) fadeGain = 0.0;
				}
				var q = p & mask;
				L[i] = HEAP16[base + q] * scale * fadeGain;
				R[i] = HEAP16[base + q + 1] * scale * fadeGain;
				p += 2;
			}
			HEAP32[ca] = p & mask;
		};

		node.connect(ctx.destination);

		// Resume context on user interaction (browser autoplay policy)
		var resume = function() {
			if (a.paused || ctx.state !== 'suspended') return;
			ctx.resume();
		};
		var canvas = document.getElementById('canvas');
		var targets = canvas ? [document, canvas] : [document];
		var events = ['click', 'keydown', 'mousedown', 'touchstart'];
		for (var t = 0; t < targets.length; t++)
			for (var ev = 0; ev < events.length; ev++)
				targets[t].addEventListener(events[ev], resume, true);

		a.targets = targets;
		a.events = events;
		a.resume = resume;
		Module._nq_audio = a;
		return 1;
	} catch (e) {
		console.warn("js_audio_init:", e);
		return 0;
	}
});

EM_JS(void, js_audio_shutdown, (), {
	var a = Module._nq_audio;
	if (!a) return;
	Module._nq_audio = null;
	if (a.suspendToken) clearTimeout(a.suspendToken);
	for (var t = 0; t < a.targets.length; t++)
		for (var ev = 0; ev < a.events.length; ev++)
			a.targets[t].removeEventListener(a.events[ev], a.resume, true);
	a.node.onaudioprocess = null;
	try { a.node.disconnect(); } catch (e) {}
	try { a.ctx.close(); } catch (e) {}
});

EM_JS(void, js_audio_set_paused, (int paused), {
	var a = Module._nq_audio;
	if (!a) return;
	if (a.suspendToken) {
		clearTimeout(a.suspendToken);
		a.suspendToken = 0;
	}
	a.paused = !!paused;
	if (paused) {
		// Delay suspend so the callback can fade out first
		a.suspendToken = setTimeout(function() {
			a.suspendToken = 0;
			if (a.paused) try { a.ctx.suspend(); } catch(e) {}
		}, 200);
	} else {
		try {
			var p = a.ctx.resume();
			if (p && p.catch) p.catch(function(){});
		} catch(e) {}
	}
});

static void SNDDMA_SetPaused(int paused)
{
	if (!snd_inited || !!paused == !!snd_paused)
		return;

	if (paused)
	{
		snd_paused = 1;
		snd_blocked++;
		js_audio_set_paused(1);
		return;
	}

	memset(dma_buffer, 0, sizeof(dma_buffer));
	audio_read_cursor = shm->samplepos;
	js_audio_set_paused(0);
	if (snd_blocked > 0)
		snd_blocked--;
	snd_paused = 0;
}

qboolean SNDDMA_Init(void) {
	if (snd_inited)
		return true;

	int rate = desired_speed ? desired_speed : 11025;
	memset(dma_buffer, 0, sizeof(dma_buffer));
	audio_read_cursor = 0;
	audio_submit_seq = 0;
	snd_paused = 0;
	shm = &the_shm;
	shm->splitbuffer = 0;
	shm->samplebits = 16;
	shm->speed = rate;
	shm->channels = 2;
	shm->samples = DMA_SAMPLES;
	shm->samplepos = 0;
	shm->buffer = (unsigned char *)dma_buffer;
	shm->submission_chunk = 1;
	if (!js_audio_init(rate, (int)(intptr_t)dma_buffer, DMA_SAMPLES,
			(int)(intptr_t)&audio_read_cursor, (int)(intptr_t)&audio_submit_seq))
		return false;
	snd_inited = 1;
	return true;
}

void SNDDMA_Pause(void)  { SNDDMA_SetPaused(1); }
void SNDDMA_Resume(void) { SNDDMA_SetPaused(0); }

int SNDDMA_GetDMAPos(void) {
	return snd_inited ? audio_read_cursor : 0;
}

void SNDDMA_Submit(void) {
	if (snd_inited)
		audio_submit_seq++;
}

void SNDDMA_Shutdown(void) {
	if (!snd_inited)
		return;
	js_audio_shutdown();
	snd_inited = 0;
	snd_paused = 0;
}
