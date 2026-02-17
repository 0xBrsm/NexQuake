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

EM_JS(int, js_cd_init, (), {
	if (typeof Module === 'undefined' || typeof Module.nqCdInit !== 'function')
		return 0;
	try { return Module.nqCdInit() ? 1 : 0; }
	catch (e) { console.warn('nqCdInit failed:', e); return 0; }
});

EM_JS(void, js_cd_shutdown, (), {
	if (typeof Module === 'undefined' || typeof Module.nqCdShutdown !== 'function')
		return;
	try { Module.nqCdShutdown(); }
	catch (e) { console.warn('nqCdShutdown failed:', e); }
});

EM_JS(void, js_cd_set_volume, (float volume), {
	if (typeof Module === 'undefined' || typeof Module.nqCdSetVolume !== 'function')
		return;
	try { Module.nqCdSetVolume(volume); }
	catch (e) { console.warn('nqCdSetVolume failed:', e); }
});

EM_JS(int, js_cd_play, (int track, int looping), {
	if (typeof Module === 'undefined' || typeof Module.nqCdPlay !== 'function')
		return 0;
	try { return Module.nqCdPlay(track, looping) ? 1 : 0; }
	catch (e) { console.warn('nqCdPlay failed:', e); return 0; }
});

EM_JS(void, js_cd_stop, (), {
	if (typeof Module === 'undefined' || typeof Module.nqCdStop !== 'function')
		return;
	try { Module.nqCdStop(); }
	catch (e) { console.warn('nqCdStop failed:', e); }
});

EM_JS(void, js_cd_pause, (), {
	if (typeof Module === 'undefined' || typeof Module.nqCdPause !== 'function')
		return;
	try { Module.nqCdPause(); }
	catch (e) { console.warn('nqCdPause failed:', e); }
});

EM_JS(void, js_cd_resume, (), {
	if (typeof Module === 'undefined' || typeof Module.nqCdResume !== 'function')
		return;
	try { Module.nqCdResume(); }
	catch (e) { console.warn('nqCdResume failed:', e); }
});

EM_JS(void, js_cd_get_source, (char *out, int outlen), {
	if (!out || outlen <= 0)
		return;
	var source = '';
	if (typeof Module !== 'undefined' && typeof Module.nqCdGetSource === 'function') {
		try { source = String(Module.nqCdGetSource() || ''); }
		catch (e) { source = ''; }
	}
	stringToUTF8(source, out, outlen);
});

static float CDAudio_ClampVolume(float volume)
{
	if (volume < 0.0f)
		return 0.0f;
	if (volume > 1.0f)
		return 1.0f;
	return volume;
}

static void CDAudio_UpdateVolume(void)
{
	float	newVolume;

	newVolume = CDAudio_ClampVolume(bgmvolume.value);
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
