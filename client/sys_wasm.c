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
static qboolean text_input_latched, console_text_latched, message_text_latched;
qboolean menu_text_editing;
qboolean console_text_editing;
void main_loop(void);

EM_JS(void, js_syncfs, (), {
	if (typeof FS !== 'undefined')
		try { FS.syncfs(false, function(err) { if (err) console.warn('syncfs:', err); }); } catch(e) {}
});

EM_JS(void, js_on_quit, (), {
	_js_syncfs();
	if (Module.nqOverlayRefreshVFS) try { Module.nqOverlayRefreshVFS(); } catch(e) {}
	if (Module.nqShowReloadScreen) try { Module.nqShowReloadScreen(); } catch(e2) {}
});

EM_JS(void, js_on_bootstrap_ready, (), {
	if (Module.nqOnBootstrapReady)
		try { Module.nqOnBootstrapReady(); } catch(e) {}
});

EM_JS(void, js_hide_console, (), {
	if (typeof Module.hideConsole === 'function') Module.hideConsole();
});

EM_JS(void, js_request_text_entry, (), {
	var ctx = Module.nqOverlayCtx;
	if (!ctx || typeof ctx.requestTextEntry !== 'function')
		return;
	try {
		ctx.requestTextEntry();
	} catch (e) {
		console.warn('requestTextEntry failed:', e);
	}
});

EM_JS(void, js_close_text_entry, (), {
	var ctx = Module.nqOverlayCtx;
	if (ctx && typeof ctx.closeTextEntry === 'function')
		ctx.closeTextEntry();
});

EM_JS(void, js_set_console_text_entry, (int active), {
	Module.nqConsoleTextEntryOpen = !!active;
	if (Module.nqOverlayCtx && typeof Module.nqOverlayCtx.syncTextEntryMode === 'function')
		Module.nqOverlayCtx.syncTextEntryMode();
});

EM_JS(void, js_set_message_text_entry, (int active), {
	Module.nqMessageTextEntryOpen = !!active;
	if (Module.nqOverlayCtx && typeof Module.nqOverlayCtx.syncTextEntryMode === 'function')
		Module.nqOverlayCtx.syncTextEntryMode();
});

EM_JS(void, js_sync_menu_text_entry, (), {
	if (Module.nqOverlayCtx && typeof Module.nqOverlayCtx.syncTextEntryValueFromGame === 'function')
		Module.nqOverlayCtx.syncTextEntryValueFromGame();
});

EM_JS(int, js_text_entry_open, (), {
	return Module.nqTextEntryOpen ? 1 : 0;
});

EM_JS(void, js_register_unload_handlers, (), {
	var fired = false;
	function handler() {
		if (fired) return;
		fired = true;
		try {
			if (typeof Module.ccall === 'function')
				Module.ccall('NQWasm_OnPageUnload', 'void', [], []);
		} catch(e) { console.warn('NQWasm_OnPageUnload failed:', e); }
	}
	window.addEventListener('pagehide', handler);
	window.addEventListener('beforeunload', handler);
});

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
	js_register_unload_handlers();
	Con_Printf("NexQuake WebAssembly - %s\n", NEXQUAKE_VERSION);
	oldtime = Sys_FloatTime() - 0.1;
	bootstrap_ready = false;
	main_loop_started = false;
	canvas_visible = false;
	text_input_latched = false;
	console_text_latched = false;
	message_text_latched = false;
	menu_text_editing = false;
	console_text_editing = false;
#ifdef WASM_BENCHMARK
	emscripten_set_main_loop_timing(EM_TIMING_SETIMMEDIATE, 0);
#endif
	emscripten_set_main_loop(main_loop, 0, 0);
}

static qboolean text_entry_was_dismissed(void)
{
	return text_input_latched && !js_text_entry_open();
}

void main_loop(void) {
	qboolean in_con, in_msg, want_text;

	if (quit_requested) {
		quit_requested = false;
		if (cls.state == ca_connected) CL_Disconnect();
		Host_Shutdown();
		main_loop_started = false;
		canvas_visible = false;
		js_on_quit();
		emscripten_cancel_main_loop();
		return;
	}
	if (!bootstrap_ready) {
		bootstrap_ready = true;
		js_on_bootstrap_ready();
	}
	if (!main_loop_started)
		return;
	if (!canvas_visible) {
		canvas_visible = true;
		js_hide_console();
	}
	newtime = Sys_FloatTime();
	time = newtime - oldtime;
	if (time > sys_ticrate.value * 2) oldtime = newtime;
	else oldtime += time;
	Host_Frame(time);

	// --- text input state machine (touch keyboard on mobile) ---
	// Cancel touch-triggered editing modes when the game state or DOM
	// text entry no longer supports them.
	if (menu_text_editing && (!M_TextInputActive() || text_entry_was_dismissed()))
		menu_text_editing = false;
	in_con = key_dest == key_console || (key_dest == key_game && con_forcedup);
	if (console_text_editing && (!in_con || text_entry_was_dismissed()))
		console_text_editing = false;

	// Determine whether we need the text entry bar open.
	in_msg = (key_dest == key_message);
	want_text = in_msg || console_text_editing || menu_text_editing;

	// Notify JS of sub-mode changes.
	if (in_con != console_text_latched)
		js_set_console_text_entry(in_con);
	if (in_msg != message_text_latched)
		js_set_message_text_entry(in_msg);

	// Open/close the text entry bar on edges.
	if (want_text && !text_input_latched)
		js_request_text_entry();
	else if (!want_text && text_input_latched)
		js_close_text_entry();

	// Keep menu text field in sync while editing.
	if (menu_text_editing)
		js_sync_menu_text_entry();

	console_text_latched = in_con;
	message_text_latched = in_msg;
	text_input_latched = want_text;
}

// Exported JS hooks (browser only).
EMSCRIPTEN_KEEPALIVE void NQWasm_ExecCommand(const char *cmd)
{
	if (!cmd || !cmd[0])
		return;
	Cbuf_AddText((char *)cmd);
	Cbuf_AddText("\n");
}

EMSCRIPTEN_KEEPALIVE void NQWasm_StartMainLoop(void)
{
	main_loop_started = true;
}

EMSCRIPTEN_KEEPALIVE void NQWasm_OnPageUnload(void)
{
	if (cls.state == ca_connected)
		CL_Disconnect();
	Host_Shutdown();
	js_syncfs();
}

EMSCRIPTEN_KEEPALIVE const char *NQWasm_GetKeyBinding(int key)
{
	if (key < 0 || key >= 256 || !keybindings[key])
		return "";
	return keybindings[key];
}

EMSCRIPTEN_KEEPALIVE void NQWasm_TextInputKey(int key)
{
	if (key < 1 || key > 127)
		return;
	if (!(key_dest == key_message || key_dest == key_console || (key_dest == key_game && con_forcedup) || M_TextInputActive()))
		return;
	Key_Event(key, true);
	Key_Event(key, false);
}

EMSCRIPTEN_KEEPALIVE const char *NQWasm_GetTextInputValue(void)
{
	if (!M_TextInputActive())
		return "";
	return M_TextInputValue();
}

EMSCRIPTEN_KEEPALIVE int NQWasm_GetVideoWidth(void)
{
	return vid.width;
}

EMSCRIPTEN_KEEPALIVE int NQWasm_GetConnectedServerListenPort(void)
{
	int listen_port;
	int driver;

	if (cls.state != ca_connected || !cls.netcon)
		return 0;
	driver = cls.netcon->driver;
	if (driver < 0 || driver >= net_numdrivers)
		return 0;

	// Join code is only valid for remote Datagram transport (WebSocket/UDP tunnel),
	// not local loopback ("connect local"/single-player).
	if (Q_strcmp(net_drivers[driver].name, "Datagram") != 0)
		return 0;

	// NexQuake WS virtual server address encoding:
	// 13.37.<listen-port-high-byte>.<listen-port-low-byte>
	if ((byte)cls.netcon->addr.sa_data[2] != 13 ||
		(byte)cls.netcon->addr.sa_data[3] != 37)
		return 0;

	listen_port = (((int)(byte)cls.netcon->addr.sa_data[4]) << 8) |
		(int)(byte)cls.netcon->addr.sa_data[5];
	if (listen_port < 1 || listen_port > 65535)
		return 0;

	return listen_port;
}
