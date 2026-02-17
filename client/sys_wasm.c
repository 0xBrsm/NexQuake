// sys_wasm.c -- WASM system layer (Emscripten)
#include <stdlib.h>
#include <sys/time.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <errno.h>
#include <sys/stat.h>
#include <emscripten.h>
#include "quakedef.h"

#ifndef NEXQUAKE_VERSION
#define NEXQUAKE_VERSION "unknown"
#endif

qboolean isDedicated;

static double time, oldtime, newtime;
static qboolean quit_requested, bootstrap_ready, main_loop_started, canvas_visible;
void main_loop(void);

// Stubs: no-ops on WASM (engine calls these but they have no meaning here)
void Sys_MakeCodeWriteable(unsigned long startaddr, unsigned long length) {}
char *Sys_ConsoleInput(void) { return 0; }
#if !id386
void Sys_LowFPPrecision(void) {}
void Sys_HighFPPrecision(void) {}
#endif

void Sys_Printf(char *fmt, ...) {
	va_list a; char t[1024];
	va_start(a, fmt); vsnprintf(t, sizeof(t), fmt, a); va_end(a);
	fputs(t, stdout);
}

void Sys_Quit(void) { quit_requested = true; }

void Sys_Error(char *error, ...) {
	va_list a; char s[1024];
	va_start(a, error); vsnprintf(s, sizeof(s), error, a); va_end(a);
	fprintf(stdout, "Error: %s\n", s);
	Host_Shutdown(); exit(1);
}

// File I/O
#define MAX_HANDLES 10
static FILE *sys_handles[MAX_HANDLES];

static FILE *get_handle(int handle) {
	if (handle <= 0 || handle >= MAX_HANDLES)
		return NULL;
	return sys_handles[handle];
}

static int findhandle(void) {
	for (int i = 1; i < MAX_HANDLES; i++)
		if (!sys_handles[i]) return i;
	Sys_Error("out of handles");
	return -1;
}

static int Qfilelength(FILE *f) {
	int pos = ftell(f);
	fseek(f, 0, SEEK_END);
	int end = ftell(f);
	fseek(f, pos, SEEK_SET);
	return end;
}

int Sys_FileOpenRead(char *path, int *hndl) {
	int i = findhandle();
	FILE *f = fopen(path, "rb");
	if (!f) { *hndl = -1; return -1; }
	sys_handles[i] = f; *hndl = i;
	return Qfilelength(f);
}

int Sys_FileOpenWrite(char *path) {
	int i = findhandle();
	FILE *f = fopen(path, "wb");
	if (!f) Sys_Error("Error opening %s: %s", path, strerror(errno));
	sys_handles[i] = f;
	return i;
}

void Sys_FileClose(int handle) {
	FILE *f = get_handle(handle);
	if (!f)
		return;
	fclose(f);
	sys_handles[handle] = NULL;
}

void Sys_FileSeek(int handle, int position) {
	FILE *f = get_handle(handle);
	if (f)
		fseek(f, position, SEEK_SET);
}

int Sys_FileRead(int handle, void *dst, int count) {
	int size = 0;
	FILE *f = get_handle(handle);
	if (f) {
		char *data = dst;
		while (count > 0) {
			size_t done = fread(data, 1, count, f);
			if (!done) break;
			data += done; count -= (int)done; size += (int)done;
		}
	}
	return size;
}

int Sys_FileWrite(int handle, void *src, int count) {
	int size = 0;
	FILE *f = get_handle(handle);
	if (f) {
		char *data = src;
		while (count > 0) {
			size_t done = fwrite(data, 1, count, f);
			if (!done) break;
			data += done; count -= (int)done; size += (int)done;
		}
	}
	return size;
}

int Sys_FileTime(char *path) {
	FILE *f = fopen(path, "rb");
	if (f) { fclose(f); return 1; }
	return -1;
}

void Sys_mkdir(char *path) { mkdir(path, 0777); }

double Sys_FloatTime(void) {
	struct timeval tp;
	static int secbase;
	gettimeofday(&tp, NULL);
	if (!secbase) { secbase = tp.tv_sec; return tp.tv_usec / 1000000.0; }
	return (tp.tv_sec - secbase) + tp.tv_usec / 1000000.0;
}

byte *Sys_ZoneBase(int *size) {
	*size = 0xc00000;
	return malloc(*size);
}

int main(int c, char **v) {
	quakeparms_t parms;
	int pnum;

	COM_InitArgv(c, v);
	parms.argc = com_argc;
	parms.argv = com_argv;
	parms.memsize = 32 * 1024 * 1024;
	if ((pnum = COM_CheckParm("-mem"))) {
		if (pnum >= com_argc - 1) {
			fprintf(stderr, "Error: -mem requires a size in MB\n");
			return 1;
		}
		parms.memsize = Q_atoi(com_argv[pnum + 1]) * 1024 * 1024;
	}
	if ((pnum = COM_CheckParm("-heapsize"))) {
		if (pnum >= com_argc - 1) {
			fprintf(stderr, "Error: -heapsize requires a size in KB\n");
			return 1;
		}
		parms.memsize = Q_atoi(com_argv[pnum + 1]) * 1024;
	}
	parms.membase = malloc(parms.memsize);
	if (!parms.membase) {
		fprintf(stderr, "Error: unable to allocate %d bytes for game memory\n", parms.memsize);
		return 1;
	}
	parms.basedir = ".";
	parms.cachedir = NULL;

	Host_Init(&parms);
	Con_Printf("NexQuake WebAssembly v%s\n", NEXQUAKE_VERSION);
	oldtime = Sys_FloatTime() - 0.1;
	bootstrap_ready = false;
	main_loop_started = false;
	canvas_visible = false;
#ifdef WASM_BENCHMARK
	emscripten_set_main_loop_timing(EM_TIMING_SETIMMEDIATE, 0);
#endif
	emscripten_set_main_loop(main_loop, 0, 0);
}

void main_loop(void) {
	if (quit_requested) {
		quit_requested = false;
		if (cls.state == ca_connected) CL_Disconnect();
		Host_Shutdown();
		main_loop_started = false;
		canvas_visible = false;
		EM_ASM({
			try {
				if (Module.nqPersistUserFiles) Module.nqPersistUserFiles();
				if (Module.nqOverlayRefreshVFS) Module.nqOverlayRefreshVFS();
			} catch(e) { console.warn('quit cleanup failed:', e); }
		});
		EM_ASM({
			try {
				if (Module.nqShowReloadScreen)
					Module.nqShowReloadScreen();
			} catch(e) {}
		});
		emscripten_cancel_main_loop();
		return;
	}
	if (!bootstrap_ready) {
		bootstrap_ready = true;
		EM_ASM({
			try {
				if (Module.nqOnBootstrapReady)
					Module.nqOnBootstrapReady();
			} catch(e) {}
		});
	}
	if (!main_loop_started)
		return;
	if (!canvas_visible) {
		canvas_visible = true;
		EM_ASM( if (typeof Module.hideConsole === 'function') Module.hideConsole(); );
	}
	newtime = Sys_FloatTime();
	time = newtime - oldtime;
	if (time > sys_ticrate.value * 2) oldtime = newtime;
	else oldtime += time;
	Host_Frame(time);
}

// Exported JS hooks (browser only).
EMSCRIPTEN_KEEPALIVE void NexQuake_ExecCommand(const char *cmd)
{
	if (!cmd || !cmd[0])
		return;
	Cbuf_AddText((char *)cmd);
	Cbuf_AddText("\n");
}

EMSCRIPTEN_KEEPALIVE void NexQuake_StartMainLoop(void)
{
	main_loop_started = true;
}

EMSCRIPTEN_KEEPALIVE void NexQuake_OnPageHide(void)
{
	if (cls.state == ca_connected)
		CL_Disconnect();
	Host_Shutdown();
}

EMSCRIPTEN_KEEPALIVE void NexQuake_VFSReady(void)
{
	// No-op for the browser; headless builds define this in sys_node.c.
}
