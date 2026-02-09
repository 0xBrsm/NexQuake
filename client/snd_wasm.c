// snd_wasm.c -- WebAudio sound (no SDL)
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

EM_JS(int, js_audio_init, (int rate, int buf, int samples, int cursor), {
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
		node.onaudioprocess = function(e) {
			var L = e.outputBuffer.getChannelData(0), R = e.outputBuffer.getChannelData(1);
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
		document.addEventListener('click', r);
		document.addEventListener('keydown', r);
		Module._nq_audio = {ctx: ctx, node: node, resume: r};
		return 1;
	} catch(e) { console.warn("js_audio_init failed:", e); return 0; }
});

EM_JS(void, js_audio_shutdown, (), {
	try {
		if (Module._nq_audio) {
			document.removeEventListener('click', Module._nq_audio.resume);
			document.removeEventListener('keydown', Module._nq_audio.resume);
			Module._nq_audio.node.onaudioprocess = null;
			Module._nq_audio.node.disconnect();
			Module._nq_audio.ctx.close();
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
	shm = &the_shm;
	shm->splitbuffer = 0; shm->samplebits = 16; shm->speed = rate;
	shm->channels = 2; shm->samples = DMA_SAMPLES;
	shm->samplepos = 0; shm->buffer = (unsigned char *)dma_buffer;
	shm->submission_chunk = 1;
	if (!js_audio_init(rate, (int)(intptr_t)dma_buffer, DMA_SAMPLES, (int)(intptr_t)&audio_read_cursor))
		return false;
	snd_inited = 1;
	return true;
}

void SNDDMA_Pause(void) {
	if (!snd_inited) return;
	snd_blocked++;
	js_audio_pause();
}

void SNDDMA_Resume(void) {
	if (!snd_inited) return;
	memset(dma_buffer, 0, sizeof(dma_buffer));
	audio_read_cursor = shm->samplepos;
	js_audio_unpause();
	snd_blocked--;
}

int SNDDMA_GetDMAPos(void) { return snd_inited ? audio_read_cursor : 0; }
void SNDDMA_Submit(void) {}
void SNDDMA_Shutdown(void) { if (snd_inited) { js_audio_shutdown(); snd_inited = 0; } }
