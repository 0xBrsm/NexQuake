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
extern int desired_speed;
extern int snd_blocked;

EM_JS(int, js_audio_init, (int rate, int buf, int samples, int cursor, int submit_seq_ptr), {
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
		var out = ctx.createGain();
		try { out.gain.value = 0.0; } catch (e0) {}
		var mask = samples - 1;
		var base = buf >> 1;
		var ca = cursor >> 2;
		var sa = submit_seq_ptr >> 2;
		var sampleFrames = samples >> 1;
		var staleFrameBudget = Math.min(sampleFrames, Math.max(512, Math.floor(rate * 0.35)));
		var lastSubmitSeq = HEAPU32[sa] >>> 0;
		var framesSinceSubmit = staleFrameBudget;
		var fadeStep = 1.0 / 256.0;
		var fadeGain = 0.0;

		function rampOutputIn() {
			var now = ctx.currentTime;
			try {
				out.gain.cancelScheduledValues(now);
				out.gain.setValueAtTime(0.0, now);
				out.gain.linearRampToValueAtTime(1.0, now + 0.02);
			} catch (e) {
				try { out.gain.value = 1.0; } catch (e2) {}
			}
		}

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

			if (framesSinceSubmit >= staleFrameBudget) {
				L.fill(0);
				R.fill(0);
				fadeGain = 0.0;
				return;
			}

			var p = HEAP32[ca];
			for (var i = 0; i < L.length; i++) {
				var q = p & mask;
				if (fadeGain < 1.0) {
					fadeGain += fadeStep;
					if (fadeGain > 1.0)
						fadeGain = 1.0;
				}
				L[i] = (HEAP16[base + q] / 32768.0) * fadeGain;
				R[i] = (HEAP16[base + q + 1] / 32768.0) * fadeGain;
				p += 2;
			}
			HEAP32[ca] = p & mask;
		};

		node.connect(out);
		out.connect(ctx.destination);
		rampOutputIn();

		var r = function() {
			if (ctx.state === 'suspended')
				ctx.resume();
		};
		var canvas = document.getElementById('canvas');
		var targets = [document];
		if (canvas) targets.push(canvas);
		var events = ['click', 'keydown', 'mousedown', 'touchstart'];
		for (var t = 0; t < targets.length; t++)
			for (var ev = 0; ev < events.length; ev++)
				targets[t].addEventListener(events[ev], r, true);

		Module._nq_audio = {
			ctx: ctx,
			node: node,
			out: out,
			resume: r,
			targets: targets,
			events: events,
			connected: true,
			rampOutputIn: rampOutputIn,
			fadeIn: function() { fadeGain = 0.0; }
		};
		return 1;
	} catch (e) {
		console.warn("js_audio_init failed:", e);
		return 0;
	}
});

EM_JS(void, js_audio_shutdown, (), {
	try {
		if (Module._nq_audio) {
			var a = Module._nq_audio;
			for (var t = 0; t < a.targets.length; t++)
				for (var ev = 0; ev < a.events.length; ev++)
					a.targets[t].removeEventListener(a.events[ev], a.resume, true);
			a.node.onaudioprocess = null;
			if (a.connected) {
				a.node.disconnect();
				a.connected = false;
			}
			a.out.disconnect();
			a.ctx.close();
			Module._nq_audio = null;
		}
	} catch (e) {
		console.warn("js_audio_shutdown failed:", e);
	}
});

EM_JS(void, js_audio_pause, (), {
	if (Module._nq_audio) {
		if (Module._nq_audio.connected) {
			Module._nq_audio.node.disconnect();
			Module._nq_audio.connected = false;
		}
		Module._nq_audio.ctx.suspend();
	}
});

EM_JS(void, js_audio_unpause, (), {
	if (Module._nq_audio) {
		Module._nq_audio.fadeIn();
		if (!Module._nq_audio.connected) {
			Module._nq_audio.node.connect(Module._nq_audio.out);
			Module._nq_audio.connected = true;
		}
		Module._nq_audio.rampOutputIn();
		Module._nq_audio.ctx.resume();
	}
});

qboolean SNDDMA_Init(void) {
	if (snd_inited)
		return true;

	int rate = desired_speed ? desired_speed : 11025;
	memset(dma_buffer, 0, sizeof(dma_buffer));
	audio_read_cursor = 0;
	audio_submit_seq = 0;
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

void SNDDMA_Pause(void) {
	if (!snd_inited)
		return;
	snd_blocked++;
	js_audio_pause();
}

void SNDDMA_Resume(void) {
	if (!snd_inited)
		return;
	memset(dma_buffer, 0, sizeof(dma_buffer));
	audio_read_cursor = shm->samplepos;
	js_audio_unpause();
	snd_blocked--;
}

int SNDDMA_GetDMAPos(void) {
	return snd_inited ? audio_read_cursor : 0;
}

void SNDDMA_Submit(void) {
	if (snd_inited)
		audio_submit_seq++;
}

void SNDDMA_Shutdown(void) {
	if (snd_inited) {
		js_audio_shutdown();
		snd_inited = 0;
	}
}
