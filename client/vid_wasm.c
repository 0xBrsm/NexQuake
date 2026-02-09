// vid_wasm.c -- WebGL2 video + input
#include "quakedef.h"
#include "d_local.h"
#include <emscripten.h>
#include <emscripten/html5.h>
#include <GLES3/gl3.h>
#include <string.h>
#include <stdlib.h>

unsigned short d_8to16table[256];
int VGA_width, VGA_height, VGA_rowbytes;
byte *VGA_pagebase;
extern viddef_t vid;
extern int m_state;
extern void SNDDMA_Pause(void);
extern void SNDDMA_Resume(void);

#define BASEWIDTH  800
#define BASEHEIGHT 525

static EMSCRIPTEN_WEBGL_CONTEXT_HANDLE gl_ctx;
static GLuint prog, vao, fb_tex, pal_tex;
static byte *pixels;
static uint32_t pal_rgba[256];
static qboolean mouse_avail;
static float mouse_x, mouse_y;
static qboolean pointer_locked;
static double ptrlock_lost_at;

// DOM keyCode -> Quake key (browser handles numlock translation for numpad)
static const uint8_t keymap[256] = {
	[8]=K_BACKSPACE, [9]=K_TAB, [13]=K_ENTER, [27]=K_ESCAPE, [32]=K_SPACE,
	[16]=K_SHIFT, [17]=K_CTRL, [18]=K_ALT, [19]=K_PAUSE,
	[33]=K_PGUP, [34]=K_PGDN, [35]=K_END, [36]=K_HOME,
	[37]=K_LEFTARROW, [38]=K_UPARROW, [39]=K_RIGHTARROW, [40]=K_DOWNARROW,
	[45]=K_INS, [46]=K_DEL,
	[48]='0',[49]='1',[50]='2',[51]='3',[52]='4',
	[53]='5',[54]='6',[55]='7',[56]='8',[57]='9',
	[65]='a',[66]='b',[67]='c',[68]='d',[69]='e',[70]='f',
	[71]='g',[72]='h',[73]='i',[74]='j',[75]='k',[76]='l',
	[77]='m',[78]='n',[79]='o',[80]='p',[81]='q',[82]='r',
	[83]='s',[84]='t',[85]='u',[86]='v',[87]='w',[88]='x',
	[89]='y',[90]='z',
	[93]='`', // APPLICATION/context menu -> console
	[96]='0',[97]='1',[98]='2',[99]='3',[100]='4',
	[101]='5',[102]='6',[103]='7',[104]='8',[105]='9',
	[106]='*',[107]='+',[109]='-',[110]='.',[111]='/',
	[112]=K_F1,[113]=K_F2,[114]=K_F3,[115]=K_F4,[116]=K_F5,[117]=K_F6,
	[118]=K_F7,[119]=K_F8,[120]=K_F9,[121]=K_F10,[122]=K_F11,[123]=K_F12,
	[186]=';',[187]='=',[188]=',',[189]='-',[190]='.',[191]='/',
	[192]='`',[219]='[',[220]='\\',[221]=']',[222]='\''
};

// Fullscreen triangle from gl_VertexID (no VBO), palette lookup in fragment
static const char *vs_src =
	"#version 300 es\n"
	"out vec2 uv;\n"
	"void main(){\n"
	"  float x=float((gl_VertexID&1)<<2)-1.0,y=float((gl_VertexID&2)<<1)-1.0;\n"
	"  uv=vec2((x+1.0)*0.5,1.0-(y+1.0)*0.5);\n"
	"  gl_Position=vec4(x,y,0,1);\n"
	"}\n";

static const char *fs_src =
	"#version 300 es\n"
	"precision mediump float;\n"
	"in vec2 uv;\n"
	"out vec4 o;\n"
	"uniform sampler2D framebuf,pal;\n"
	"void main(){\n"
	"  o=texelFetch(pal,ivec2(int(texture(framebuf,uv).r*255.0+0.5),0),0);\n"
	"}\n";

static GLuint compile_shader(GLenum type, const char *src) {
	GLuint s = glCreateShader(type);
	glShaderSource(s, 1, &src, NULL);
	glCompileShader(s);
	GLint ok; glGetShaderiv(s, GL_COMPILE_STATUS, &ok);
	if (!ok) { char log[512]; glGetShaderInfoLog(s, 512, NULL, log); Sys_Error("Shader: %s", log); }
	return s;
}

static GLuint mk_tex(GLenum ifmt, int w, int h, GLenum fmt) {
	GLuint t; glGenTextures(1, &t); glBindTexture(GL_TEXTURE_2D, t);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
	glTexImage2D(GL_TEXTURE_2D, 0, ifmt, w, h, 0, fmt, GL_UNSIGNED_BYTE, NULL);
	return t;
}

static void init_gl(int w, int h) {
	EmscriptenWebGLContextAttributes a;
	emscripten_webgl_init_context_attributes(&a);
	a.majorVersion = 2; a.alpha = a.antialias = a.depth = a.stencil = 0;
	gl_ctx = emscripten_webgl_create_context("#canvas", &a);
	if (gl_ctx <= 0) Sys_Error("VID: WebGL2 failed (%d)", gl_ctx);
	emscripten_webgl_make_context_current(gl_ctx);
	emscripten_set_canvas_element_size("#canvas", w, h);
	// Framebuffer is GL_R8; width may be non-4-byte aligned on custom modes.
	glPixelStorei(GL_UNPACK_ALIGNMENT, 1);

	GLuint vs = compile_shader(GL_VERTEX_SHADER, vs_src);
	GLuint fs = compile_shader(GL_FRAGMENT_SHADER, fs_src);
	prog = glCreateProgram();
	glAttachShader(prog, vs); glAttachShader(prog, fs);
	glLinkProgram(prog);
	GLint ok; glGetProgramiv(prog, GL_LINK_STATUS, &ok);
	if (!ok) { char log[512]; glGetProgramInfoLog(prog, 512, NULL, log); Sys_Error("Link: %s", log); }
	glDeleteShader(vs); glDeleteShader(fs);

	glGenVertexArrays(1, &vao);
	fb_tex = mk_tex(GL_R8, w, h, GL_RED);
	pal_tex = mk_tex(GL_RGBA8, 256, 1, GL_RGBA);
	glUseProgram(prog);
	glUniform1i(glGetUniformLocation(prog, "framebuf"), 0);
	glUniform1i(glGetUniformLocation(prog, "pal"), 1);
}

// Pointer lock with caught promise rejection (avoids SecurityError on post-Escape cooldown)
EM_JS(void, js_request_pointerlock, (), {
	var p = document.getElementById('canvas').requestPointerLock();
	if (p && p.catch) p.catch(function(){});
});

// Input callbacks
static EM_BOOL on_key(int type, const EmscriptenKeyboardEvent *e, void *ud) {
	if (e->keyCode == 27 && emscripten_get_now() - ptrlock_lost_at < 50) return 1;
	int k = e->keyCode < 256 ? keymap[e->keyCode] : 0;
	if (!k) return 0;
	Key_Event(k, type == EMSCRIPTEN_EVENT_KEYDOWN);
	return e->keyCode != 116 && e->keyCode != 123; // let F5/F12 through
}

static EM_BOOL on_mouse_move(int t, const EmscriptenMouseEvent *e, void *ud) {
	if (!mouse_avail || !pointer_locked) return 0;
	mouse_x += e->movementX * 2; mouse_y += e->movementY * 2;
	return 1;
}

static EM_BOOL on_mouse_btn(int type, const EmscriptenMouseEvent *e, void *ud) {
	if (!mouse_avail) return 0;
	qboolean down = (type == EMSCRIPTEN_EVENT_MOUSEDOWN);
	if (down && !pointer_locked) {
		js_request_pointerlock();
		if (key_dest == key_menu) { m_state = 0; key_dest = key_game; return 1; }
	}
	static const int bmap[] = { K_MOUSE1, K_MOUSE3, K_MOUSE2 };
	if (e->button > 2) return 0;
	Key_Event(bmap[e->button], down);
	return 1;
}

static EM_BOOL on_wheel(int t, const EmscriptenWheelEvent *e, void *ud) {
	if (!mouse_avail) return 0;
	int k = e->deltaY < 0 ? K_MWHEELUP : e->deltaY > 0 ? K_MWHEELDOWN : 0;
	if (!k) return 0;
	Key_Event(k, 1); Key_Event(k, 0);
	return 1;
}

static EM_BOOL on_ptrlock(int t, const EmscriptenPointerlockChangeEvent *e, void *ud) {
	pointer_locked = e->isActive;
	if (!e->isActive) {
		ptrlock_lost_at = emscripten_get_now();
		if (key_dest == key_game) { Key_Event(K_ESCAPE, 1); Key_Event(K_ESCAPE, 0); }
	}
	return 1;
}

static EM_BOOL on_visibility(int t, const EmscriptenVisibilityChangeEvent *e, void *ud) {
	if (e->hidden) {
		mouse_x = mouse_y = 0;
		SNDDMA_Pause();
		emscripten_set_main_loop_timing(EM_TIMING_SETTIMEOUT, 100);
	} else {
		emscripten_set_main_loop_timing(EM_TIMING_RAF, 0);
		SNDDMA_Resume();
		EmscriptenPointerlockChangeEvent pe;
		if (emscripten_get_pointerlock_status(&pe) == EMSCRIPTEN_RESULT_SUCCESS)
			pointer_locked = pe.isActive;
	}
	return 1;
}

static void init_input(void) {
	emscripten_set_keydown_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_key);
	emscripten_set_keyup_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_key);
	emscripten_set_mousemove_callback("#canvas", 0, 1, on_mouse_move);
	emscripten_set_mousedown_callback("#canvas", 0, 1, on_mouse_btn);
	emscripten_set_mouseup_callback("#canvas", 0, 1, on_mouse_btn);
	emscripten_set_wheel_callback("#canvas", 0, 1, on_wheel);
	emscripten_set_pointerlockchange_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_ptrlock);
	emscripten_set_visibilitychange_callback(0, 1, on_visibility);
}

// Video interface
void VID_SetPalette(unsigned char *palette) {
	if (!palette) return;
	for (int i = 0; i < 256; i++)
		pal_rgba[i] = palette[i*3] | (palette[i*3+1] << 8) | (palette[i*3+2] << 16) | 0xFF000000;
	glBindTexture(GL_TEXTURE_2D, pal_tex);
	glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, 256, 1, GL_RGBA, GL_UNSIGNED_BYTE, pal_rgba);
}

void VID_ShiftPalette(unsigned char *p) { VID_SetPalette(p); }

void VID_Init(unsigned char *palette) {
	int pnum, chunk, cachesize;
	byte *cache;

	vid.width = BASEWIDTH; vid.height = BASEHEIGHT;
	vid.maxwarpwidth = WARP_WIDTH; vid.maxwarpheight = WARP_HEIGHT;
	if ((pnum = COM_CheckParm("-width"))) {
		if (pnum >= com_argc - 1) Sys_Error("VID: -width <width>\n");
		vid.width = Q_atoi(com_argv[pnum+1]);
		vid.height = vid.width * 3 / 4;
	}
	if ((pnum = COM_CheckParm("-height"))) {
		if (pnum >= com_argc - 1) Sys_Error("VID: -height <height>\n");
		vid.height = Q_atoi(com_argv[pnum+1]);
	}
	if ((pnum = COM_CheckParm("-winsize"))) {
		if (pnum >= com_argc - 2) Sys_Error("VID: -winsize <width> <height>\n");
		vid.width = Q_atoi(com_argv[pnum+1]);
		vid.height = Q_atoi(com_argv[pnum+2]);
		if (!vid.width || !vid.height) Sys_Error("VID: Bad window width/height\n");
	}
	if (vid.width < 320) vid.width = 320;
	if (vid.height < 200) vid.height = 200;
	vid.conwidth = vid.width; vid.conheight = vid.height;

	init_gl(vid.width, vid.height);
	VGA_width = vid.width; VGA_height = vid.height;
	pixels = malloc(VGA_width * VGA_height);
	if (!pixels) Sys_Error("VID: Not enough memory for framebuffer\n");

	VGA_pagebase = vid.buffer = vid.conbuffer = pixels;
	VGA_rowbytes = vid.rowbytes = vid.conrowbytes = VGA_width;
	vid.direct = 0;
	vid.aspect = ((float)vid.height / (float)vid.width) * (320.0 / 240.0);
	vid.numpages = 1;
	vid.colormap = host_colormap;
	vid.fullbright = 256 - LittleLong(*((int *)vid.colormap + 2048));

	chunk = vid.width * vid.height * sizeof(*d_pzbuffer);
	cachesize = D_SurfaceCacheForRes(vid.width, vid.height);
	d_pzbuffer = Hunk_HighAllocName(chunk + cachesize, "video");
	if (!d_pzbuffer) Sys_Error("VID: Not enough memory for video mode\n");
	cache = (byte *)d_pzbuffer + chunk;
	D_InitCaches(cache, cachesize);

	VID_SetPalette(palette);
	init_input();
}

void VID_Shutdown(void) {
	free(pixels); pixels = NULL;
	glDeleteTextures(1, &fb_tex); glDeleteTextures(1, &pal_tex);
	glDeleteProgram(prog); glDeleteVertexArrays(1, &vao);
	if (gl_ctx > 0) { emscripten_webgl_destroy_context(gl_ctx); gl_ctx = 0; }
}

void VID_Update(vrect_t *rects) {
	glActiveTexture(GL_TEXTURE0); glBindTexture(GL_TEXTURE_2D, fb_tex);
	glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, VGA_width, VGA_height, GL_RED, GL_UNSIGNED_BYTE, pixels);
	glActiveTexture(GL_TEXTURE1); glBindTexture(GL_TEXTURE_2D, pal_tex);
	glViewport(0, 0, VGA_width, VGA_height);
	glUseProgram(prog); glBindVertexArray(vao);
	glDrawArrays(GL_TRIANGLES, 0, 3);
}

void D_BeginDirectRect(int x, int y, byte *pbitmap, int width, int height) {
	if (!pixels || !pbitmap || width <= 0 || height <= 0) return;
	if (x < 0) x = VGA_width + x - 1;
	if (x < 0 || y < 0 || x >= VGA_width || y >= VGA_height) return;
	if (x + width > VGA_width) width = VGA_width - x;
	if (y + height > VGA_height) height = VGA_height - y;
	if (width <= 0 || height <= 0) return;
	byte *dst = pixels + y * VGA_width + x;
	while (height--) { memcpy(dst, pbitmap, width); dst += VGA_width; pbitmap += width; }
}

void D_EndDirectRect(int x, int y, int width, int height) {}

// Input interface
void Sys_SendKeyEvents(void) {}
void IN_Init(void) { if (!COM_CheckParm("-nomouse")) mouse_avail = 1; }
void IN_Shutdown(void) { mouse_avail = 0; }
void IN_Commands(void) {}

void IN_Move(usercmd_t *cmd) {
	if (!mouse_avail) return;
	mouse_x *= sensitivity.value; mouse_y *= sensitivity.value;
	if ((in_strafe.state & 1) || lookstrafe.value)
		cmd->sidemove += m_side.value * mouse_x;
	else
		cl.viewangles[YAW] -= m_yaw.value * mouse_x;
	V_StopPitchDrift();
	if (!(in_strafe.state & 1)) {
		cl.viewangles[PITCH] += m_pitch.value * mouse_y;
		if (cl.viewangles[PITCH] > 80) cl.viewangles[PITCH] = 80;
		if (cl.viewangles[PITCH] < -70) cl.viewangles[PITCH] = -70;
	} else {
		if (noclip_anglehack) cmd->upmove -= m_forward.value * mouse_y;
		else cmd->forwardmove -= m_forward.value * mouse_y;
	}
	mouse_x = mouse_y = 0;
}
