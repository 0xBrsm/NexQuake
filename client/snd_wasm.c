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
extern int desired_speed;
extern int snd_blocked;

// Shared flag: when non-zero the JS audio callback outputs silence and does
// not advance the read cursor.  This prevents stale-buffer audio loops during
// blocking operations (synchronous XHR, emscripten_sleep, map loads, etc.)
// and avoids the audible pop/delay on startup by keeping the callback idle
// until the mixer has had a chance to fill the buffer.
static int audio_muted;

EM_JS(int, js_audio_init, (int rate, int buf, int samples, int cursor, int mute_flag), {
	try {
		var AC = window.AudioContext || window.webkitAudioContext;
		if (!AC) {
			console.warn("AudioContext unavailable");
			return 0;
		}
		var ctx;
		try { ctx = new AC({sampleRate: rate}); }
		catch (e) { ctx = new AC(); }
		if (!ctx.createScriptProcessor) {
			console.warn("createScriptProcessor unavailable");
			return 0;
		}
		var node = ctx.createScriptProcessor(512, 0, 2);
		var mask = samples - 1, base = buf >> 1, ca = cursor >> 2;
		var ma = mute_flag >> 2;
		node.onaudioprocess = function(e) {
			var L = e.outputBuffer.getChannelData(0);
			var R = e.outputBuffer.getChannelData(1);
			if (HEAP32[ma]) {
				// Muted: output silence, don't advance the read cursor.
				L.fill(0); R.fill(0);
				return;
			}
			var p = HEAP32[ca];
			for (var i = 0; i < L.length; i++) {
				var q = p & mask;
				L[i] = HEAP16[base + q] / 32768.0;
				R[i] = HEAP16[base + q + 1] / 32768.0;
				p += 2;
			}
			HEAP32[ca] = p & mask;
		};
		node.connect(ctx.destination);
		var r = function() { if (ctx.state === 'suspended') ctx.resume(); };
		var canvas = document.getElementById('canvas');
		var targets = [document];
		if (canvas) targets.push(canvas);
		var events = ['click', 'keydown', 'mousedown', 'touchstart'];
		for (var t = 0; t < targets.length; t++)
			for (var ev = 0; ev < events.length; ev++)
				targets[t].addEventListener(events[ev], r, true);
		Module._nq_audio = {ctx: ctx, node: node, resume: r, targets: targets, events: events};
		return 1;
	} catch(e) { console.warn("js_audio_init failed:", e); return 0; }
});

EM_JS(void, js_audio_shutdown, (), {
	try {
		if (Module._nq_audio) {
			var a = Module._nq_audio;
			for (var t = 0; t < a.targets.length; t++)
				for (var ev = 0; ev < a.events.length; ev++)
					a.targets[t].removeEventListener(a.events[ev], a.resume, true);
			a.node.onaudioprocess = null;
			a.node.disconnect();
			a.ctx.close();
			Module._nq_audio = null;
		}
	} catch(e) { console.warn("js_audio_shutdown failed:", e); }
});

EM_JS(void, js_audio_pause, (), {
	if (Module._nq_audio) { Module._nq_audio.node.disconnect(); Module._nq_audio.ctx.suspend(); }
});

EM_JS(void, js_audio_unpause, (), {
	if (Module._nq_audio) { Module._nq_audio.node.connect(Module._nq_audio.ctx.destination); Module._nq_audio.ctx.resume(); }
});

qboolean SNDDMA_Init(void) {
	if (snd_inited) return true;
	int rate = desired_speed ? desired_speed : 11025;
	memset(dma_buffer, 0, sizeof(dma_buffer));
	audio_read_cursor = 0;
	audio_muted = 1;
	shm = &the_shm;
	shm->splitbuffer = 0; shm->samplebits = 16; shm->speed = rate;
	shm->channels = 2; shm->samples = DMA_SAMPLES;
	shm->samplepos = 0; shm->buffer = (unsigned char *)dma_buffer;
	shm->submission_chunk = 1;
	if (!js_audio_init(rate, (int)(intptr_t)dma_buffer, DMA_SAMPLES,
			(int)(intptr_t)&audio_read_cursor, (int)(intptr_t)&audio_muted))
		return false;
	snd_inited = 1;
	return true;
}

void SNDDMA_Pause(void) {
	if (!snd_inited) return;
	snd_blocked++;
	audio_muted = 1;
	js_audio_pause();
}

void SNDDMA_Resume(void) {
	if (!snd_inited) return;
	memset(dma_buffer, 0, sizeof(dma_buffer));
	audio_read_cursor = shm->samplepos;
	js_audio_unpause();
	snd_blocked--;
	if (snd_blocked == 0)
		audio_muted = 0;
}

int SNDDMA_GetDMAPos(void) { return snd_inited ? audio_read_cursor : 0; }

// Mute the audio callback without touching snd_blocked or the AudioContext.
// Used to silence output during blocking waits (prefetch, sync XHR) where the
// mixer cannot run.  SNDDMA_Submit() will automatically unmute once the mixer
// resumes producing data.
void SNDDMA_MuteTransfer(void) { audio_muted = 1; }

void SNDDMA_Submit(void) {
	// First submit after init: unmute so the JS callback begins reading.
	// This ensures the mixer has filled at least one buffer before playback.
	if (audio_muted && !snd_blocked)
		audio_muted = 0;
}

void SNDDMA_Shutdown(void) {
	if (snd_inited) {
		audio_muted = 1;
		js_audio_shutdown();
		snd_inited = 0;
	}
}
