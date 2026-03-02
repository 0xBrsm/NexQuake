// cd_wasm.c -- browser-backed digital music for Quake track numbers
#include "quakedef.h"
#include <emscripten/emscripten.h>

extern	cvar_t	bgmvolume;

#define CDAUDIO_SRC_MAX		512

static qboolean	initialized = false;
static qboolean	enabled = true;
static qboolean	playing = false;
static qboolean	wasPlaying = false;
static float	cdvolume = -1.0f;

// Called by JS when a track finishes or errors out naturally.
EMSCRIPTEN_KEEPALIVE void CDAudio_OnTrackEnded(void)
{
	playing = false;
	wasPlaying = false;
}

// js_cd_init: create the audio element, wire all event handlers, attach helpers
// to the state object, and expose the overlay query API on Module.
EM_JS(int, js_cd_init, (), {
	var CD_DIR = (typeof nqGetCdDir === 'function' ? nqGetCdDir() : '/cd/').replace(/\\/+$/, "");

	function getTrackNum(name) {
		name = String(name || "").toLowerCase().replace(/\\.(?:ogg|mp3)$/, "");
		var m = name.match(/^#?(\\d+)/) || name.match(/(\\d+)$/);
		var n = m ? (Number(m[1]) | 0) : 0;
		return n > 0 ? n : 0;
	}

	function resolveLocalPath(track) {
		var st = nqSafeStat(CD_DIR);
		if (!st || !FS.isDir(st.mode)) return "";
		var q = [CD_DIR], qi = 0;
		for (; qi < q.length; qi++) {
			var dir = q[qi];
			var entries = nqSafeReadDir(dir).slice().sort(function(a, b) { return a.localeCompare(b); });
			for (var i = 0; i < entries.length; i++) {
				var name = entries[i];
				if (name === '.' || name === '..') continue;
				var path = dir + '/' + name;
				var s = nqSafeStat(path);
				if (!s) continue;
				if (FS.isFile(s.mode)) {
					if (getTrackNum(name) === track) return path;
				} else if (FS.isDir(s.mode)) {
					q.push(path);
				}
			}
		}
		return "";
	}

	function loadRemoteManifest() {
		var raw;
		try { raw = Module.nexquakeCdRemoteManifest || []; } catch(e) { return []; }
		if (!Array.isArray(raw)) return [];
		var out = [];
		raw.forEach(function(entry) {
			var p = String(entry && entry.path || "").trim();
			var u = String(entry && entry.url || "").trim();
			if (p && u) out.push({ path: p, url: u });
		});
		return out;
	}

	function resolveTrack(track) {
		var path = resolveLocalPath(track);
		if (path) {
			try {
				var bytes = FS.readFile(path);
				var ext = path.toLowerCase().slice(path.lastIndexOf('.') + 1);
				var mime = ext === 'mp3' ? 'audio/mpeg' : 'audio/ogg';
				return { path: path, url: URL.createObjectURL(new Blob([bytes], { type: mime })) };
			} catch(e) {}
		}
		var entries = loadRemoteManifest();
		for (var i = 0; i < entries.length; i++) {
			var e = entries[i];
			if (getTrackNum(String(e.path || "").split(/[\\\\/]/).pop()) === track)
				return { path: CD_DIR + '/' + e.path, url: e.url };
		}
		return null;
	}

	var rqf = typeof requestAnimationFrame === 'function'
		? requestAnimationFrame.bind(window)
		: function(fn) { return setTimeout(function() { fn(Date.now()); }, 16); };
	var cqf = typeof cancelAnimationFrame === 'function'
		? cancelAnimationFrame.bind(window) : clearTimeout;

	function notify() {
		if (typeof Module.nqOverlayOnCdStateChange === 'function')
			try { Module.nqOverlayOnCdStateChange(); } catch(e) {}
	}

	function revokeBlob(url) {
		if (url && url.indexOf('blob:') === 0)
			try { URL.revokeObjectURL(url); } catch(e) {}
	}

	function fadeToSilence(s, done) {
		var startVol = Number(s.audio.volume);
		if (s.audio.paused || !Number.isFinite(startVol) || startVol <= 0.001) {
			if (done) done();
			return;
		}
		var startAt = typeof performance !== 'undefined' ? performance.now() : Date.now();
		var durationMs = 48;
		function step(now) {
			var t = Math.min(1, Math.max(0, (Number(now) - startAt) / durationMs));
			try { s.audio.volume = startVol * (1 - t); } catch(e) {}
			if (t >= 1) { s.fadeToken = 0; if (done) done(); return; }
			s.fadeToken = rqf(step);
		}
		s.fadeToken = rqf(step);
	}

	var s = {
		audio: document.createElement('audio'),
		status: 'stopped',
		sourcePath: "",
		blobURL: "",
		targetVolume: 1,
		fadeToken: 0,
		resolveTrack: resolveTrack,
		loadRemoteManifest: loadRemoteManifest,
		getTrackNum: getTrackNum,
		notify: notify,
		revokeBlob: revokeBlob,
		fadeToSilence: fadeToSilence,
		cancelFade: function() { if (s.fadeToken) { cqf(s.fadeToken); s.fadeToken = 0; } },
		rqf: rqf,
		cqf: cqf
	};

	s.audio.preload = 'auto';
	s.audio.onplaying = function() { s.status = 'playing'; notify(); };
	s.audio.onended = s.audio.onerror = function() {
		s.cancelFade();
		revokeBlob(s.blobURL);
		s.blobURL = "";
		s.status = 'stopped';
		s.sourcePath = "";
		try { s.audio.volume = s.targetVolume; } catch(e) {}
		_CDAudio_OnTrackEnded();
		notify();
	};

	function resumeOnInteraction() {
		if (s.status === 'loading' && s.audio.src)
			try { s.audio.play(); } catch(e) {}
	}
	document.addEventListener('click', resumeOnInteraction);
	document.addEventListener('keydown', resumeOnInteraction);
	s.cleanup = function() {
		document.removeEventListener('click', resumeOnInteraction);
		document.removeEventListener('keydown', resumeOnInteraction);
	};

	Module._nq_cdaudio = s;

	// Overlay query API
	Module.nqCdGetPlaybackState = function() { return s.status; };
	Module.nqCdGetSource = function() { return s.sourcePath || ""; };
	Module.nqCdGetTrackNumberFromPath = function(path) {
		return s.getTrackNum(String(path || "").split(/[\\\\/]/).pop());
	};
	Module.nqCdGetRemoteTracks = function() {
		return s.loadRemoteManifest().map(function(e) { return String(e.path || ""); }).filter(Boolean);
	};

	notify();
	return 1;
});

EM_JS(int, js_cd_play, (int track, int looping), {
	var s = Module._nq_cdaudio;
	if (!s) return 0;
	var entry = s.resolveTrack(track);
	if (!entry) return 0;

	// Same track already active: adjust loop, resume if paused
	if (s.sourcePath && s.sourcePath === entry.path &&
	    (s.status === 'playing' || s.status === 'paused' || s.status === 'loading')) {
		if (entry.url !== s.blobURL) s.revokeBlob(entry.url);
		try { s.audio.loop = !!looping; } catch(e) {}
		if (s.status === 'paused') {
			s.cancelFade();
			try { s.audio.volume = s.targetVolume; } catch(e2) {}
			s.status = 'loading';
			s.notify();
			try { s.audio.play(); } catch(e3) {}
		} else {
			s.notify();
		}
		return 1;
	}

	// Switch to new track: stop current immediately
	s.cancelFade();
	try { s.audio.pause(); s.audio.currentTime = 0; } catch(e) {}
	s.revokeBlob(s.blobURL);
	s.sourcePath = entry.path;
	s.status = 'loading';
	s.blobURL = entry.url.indexOf('blob:') === 0 ? entry.url : "";
	try {
		s.audio.loop = !!looping;
		if (s.audio.src !== entry.url) s.audio.src = entry.url;
	} catch(e4) {}
	s.notify();
	try { s.audio.play(); } catch(e5) {}
	return 1;
});

EM_JS(void, js_cd_stop, (), {
	var s = Module._nq_cdaudio;
	if (!s) return;
	s.cancelFade();
	s.status = 'stopped';
	s.sourcePath = "";
	s.fadeToSilence(s, function() {
		try { s.audio.pause(); s.audio.currentTime = 0; } catch(e) {}
		s.revokeBlob(s.blobURL);
		s.blobURL = "";
		try { s.audio.volume = s.targetVolume; } catch(e2) {}
		s.notify();
	});
	s.notify();
});

EM_JS(void, js_cd_pause, (), {
	var s = Module._nq_cdaudio;
	if (!s || s.status !== 'playing') return;
	s.cancelFade();
	s.status = 'paused';
	s.notify();
	s.fadeToSilence(s, function() {
		try { s.audio.pause(); s.audio.volume = s.targetVolume; } catch(e) {}
	});
});

EM_JS(void, js_cd_resume, (), {
	var s = Module._nq_cdaudio;
	if (!s || s.status !== 'paused') return;
	s.cancelFade();
	try { s.audio.volume = s.targetVolume; } catch(e) {}
	s.status = 'loading';
	s.notify();
	try { s.audio.play(); } catch(e2) {}
});

EM_JS(void, js_cd_set_volume, (float volume), {
	var s = Module._nq_cdaudio;
	if (!s) return;
	var v = Math.min(1, Math.max(0, Number(volume)));
	s.targetVolume = v;
	try { s.audio.volume = v; } catch(e) {}
});

EM_JS(void, js_cd_get_source, (char *out, int outlen), {
	if (!out || outlen <= 0) return;
	var source = Module._nq_cdaudio ? String(Module._nq_cdaudio.sourcePath || "") : "";
	stringToUTF8(source, out, outlen);
});

EM_JS(void, js_cd_shutdown, (), {
	var s = Module._nq_cdaudio;
	if (!s) return;
	s.cancelFade();
	if (s.cleanup) s.cleanup();
	try { s.audio.pause(); } catch(e) {}
	s.revokeBlob(s.blobURL);
	s.audio.onplaying = s.audio.onended = s.audio.onerror = null;
	try { s.audio.removeAttribute('src'); s.audio.load(); } catch(e2) {}
	s.status = 'stopped';
	s.sourcePath = "";
	s.blobURL = "";
	Module._nq_cdaudio = null;
	if (typeof Module.nqOverlayOnCdStateChange === 'function')
		try { Module.nqOverlayOnCdStateChange(); } catch(e3) {}
});

static void CDAudio_UpdateVolume(void)
{
	float	newVolume;

	newVolume = bgmvolume.value;
	if (newVolume < 0.0f) newVolume = 0.0f;
	if (newVolume > 1.0f) newVolume = 1.0f;
	if (newVolume == cdvolume)
		return;

	cdvolume = newVolume;
	js_cd_set_volume(cdvolume);
}

void CDAudio_Play(byte track, qboolean looping)
{
	if (!initialized || !enabled)
		return;

	if (!track)
	{
		Con_DPrintf("CDAudio: Bad track number %u.\n", track);
		return;
	}

	CDAudio_UpdateVolume();

	if (!js_cd_play((int)track, looping ? 1 : 0))
	{
		Con_DPrintf("CDAudio: no .ogg/.mp3 file matches track %u\n", track);
		playing = false;
		wasPlaying = false;
		return;
	}

	playing = true;
	wasPlaying = false;
}

void CDAudio_Stop(void)
{
	if (!initialized || !enabled)
		return;

	if (!playing && !wasPlaying)
		return;

	js_cd_stop();
	playing = false;
	wasPlaying = false;
}

void CDAudio_Pause(void)
{
	if (!initialized || !enabled || !playing)
		return;

	js_cd_pause();
	wasPlaying = playing;
	playing = false;
}

void CDAudio_Resume(void)
{
	int	track;

	if (!initialized || !enabled)
		return;

	if (wasPlaying)
	{
		js_cd_resume();
		playing = true;
		wasPlaying = false;
		return;
	}

	track = cl.looptrack ? cl.looptrack : cl.cdtrack;
	if ((cls.demoplayback || cls.demorecording) && cls.forcetrack != -1)
		track = cls.forcetrack;
	if (track <= 0 || track > 255)
		return;

	CDAudio_Play((byte)track, true);
}

static void CD_f(void)
{
	char	*command;
	int		track;
	qboolean looping;
	char	source[CDAUDIO_SRC_MAX];

	if (Cmd_Argc() < 2)
		return;

	command = Cmd_Argv(1);

	if (Q_strcasecmp(command, "on") == 0)
	{
		enabled = true;
		return;
	}

	if (Q_strcasecmp(command, "off") == 0)
	{
		CDAudio_Stop();
		enabled = false;
		return;
	}

	if (Q_strcasecmp(command, "play") == 0 || Q_strcasecmp(command, "loop") == 0)
	{
		if (Cmd_Argc() < 3)
		{
			Con_Printf("cd %s <track number>\n", command);
			return;
		}
		track = Q_atoi(Cmd_Argv(2));
		if (track <= 0 || track > 255)
		{
			Con_Printf("CDAudio: Bad track number %d.\n", track);
			return;
		}
		looping = (Q_strcasecmp(command, "loop") == 0);
		CDAudio_Play((byte)track, looping);
		return;
	}

	if (Q_strcasecmp(command, "stop") == 0)
	{
		CDAudio_Stop();
		return;
	}

	if (Q_strcasecmp(command, "pause") == 0)
	{
		CDAudio_Pause();
		return;
	}

	if (Q_strcasecmp(command, "resume") == 0)
	{
		CDAudio_Resume();
		return;
	}

	if (Q_strcasecmp(command, "info") == 0)
	{
		source[0] = 0;
		js_cd_get_source(source, sizeof(source));
		Con_Printf("%s\n", source[0] ? COM_SkipPath(source) : "<none>");
		return;
	}
}

void CDAudio_Update(void)
{
	if (!initialized || !enabled)
		return;

	CDAudio_UpdateVolume();
}

int CDAudio_Init(void)
{
	if (cls.state == ca_dedicated)
		return -1;

	if (COM_CheckParm("-nocdaudio"))
		return -1;

	if (!js_cd_init())
	{
		Con_Printf("CDAudio_Init: browser audio initialization failed\n");
		return -1;
	}

	Cmd_AddCommand("cd", CD_f);

	initialized = true;
	enabled = true;
	playing = false;
	wasPlaying = false;
	cdvolume = -1.0f;
	CDAudio_UpdateVolume();

	Con_Printf("CD Audio (WASM) Initialized\n");
	return 0;
}

void CDAudio_Shutdown(void)
{
	if (!initialized)
		return;

	CDAudio_Stop();
	js_cd_shutdown();
	initialized = false;
}
