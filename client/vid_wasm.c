// vid_wasm.c -- WebGL2 video driver
#include "quakedef.h"
#include "d_local.h"
#include <emscripten.h>
#include <emscripten/html5.h>
#include <GLES3/gl3.h>
#include <math.h>
#include <string.h>
#include <stdlib.h>

unsigned short d_8to16table[256];
int VGA_width, VGA_height, VGA_rowbytes;
byte *VGA_pagebase;
extern viddef_t vid;

//engine mins/maxes. The software edge rasterizer's screen-x fixed point was
//widened 12.20 -> 14.18 (patches/r_{main,draw,edge}.c.patch) so render width
//can exceed the old 2047 ceiling without overflowing the active-edge-table
//sort; 18 fraction bits is still far more sub-pixel precision than needed.
//3840 covers 4K-wide / super-ultrawide at full native resolution.
//
//!!! Do NOT raise these without re-checking three independent, unrelated
//ceilings these values sit just under — each fails silently (no compile
//error), and the failure modes are a hang, corruption, or a black screen:
//  - VID_MAX_WIDTH <= ~8191: the 14.18 edge screen-x fixed point above.
//    Past it, R_EdgeDrawing's active-edge sort cycles -> main-thread hang.
//  - VID_MAX_WIDTH <= 4096: the resolve texture is uploaded as one GL
//    texture; 4096 is the floor for WebGL2 MAX_TEXTURE_SIZE. Past it the
//    upload silently fails on low-end GPUs -> black 3D view.
//  - VID_MAX_HEIGHT <= MAXHEIGHT (1024, patches/r_shared.h.patch): height
//    indexes fixed [MAXHEIGHT]-sized scratch (e.g. D_WarpScreen rowptr[]).
//    This one has ZERO margin today (1024 == 1024) -> any bump overruns.
//Width also drives MAXWIDTH in r_shared.h.patch (D_WarpScreen column[], sin
//tables); keep that patch's MAXWIDTH >= VID_MAX_WIDTH.
#define VID_MIN_WIDTH  320
#define VID_MIN_HEIGHT 200
#define VID_MAX_WIDTH  3840
#define VID_MAX_HEIGHT 1024

#define VID_ASPECT_RATIO (4.0 / 3.0)
#define VID_DEFAULT_MODE "1"
#define VID_NUM_MODES 6

// Live re-mode tuning (VID_Update): ignore sub-pixel jitter, snap orientation
// flips almost immediately, debounce gradual drags so they re-mode once settled.
#define NATIVE_REMODE_EPS         2      // px; change below this is noise
#define NATIVE_REMODE_FLIP_DELAY  0.03   // s; portrait<->landscape flip
#define NATIVE_REMODE_DRAG_DELAY  0.20   // s; continuous resize


static int clamp_int(int v, int lo, int hi) { return v < lo ? lo : v > hi ? hi : v; }

EM_JS(void, js_update_canvas_ar, (double ar), {
	var c = document.getElementById('canvas');
	if (c && ar > 0) c.style.setProperty('--nq-ar', ar);
});

EM_JS(int, js_viewport_dim, (int use_width), {
	var key = use_width ? 'nqStartupMonitorWidth' : 'nqStartupMonitorHeight';
	try { var v = (Module && Module[key]) | 0; if (v > 0) return v; } catch(e) {}
	try {
		var dpr = window.devicePixelRatio || 1;
		var vp  = window.visualViewport;
		var px  = use_width ? (vp ? vp.width  : window.innerWidth  || (screen && screen.width)  || 0)
		                    : (vp ? vp.height : window.innerHeight || (screen && screen.height) || 0);
		return px > 0 ? Math.max(1, Math.round(px * dpr)) : 0;
	} catch(e2) { return 0; }
});

static double startup_viewport_aspect(void)
{
	int w = js_viewport_dim(1), h = js_viewport_dim(0);
	if (w <= 0 || h <= 0) return VID_ASPECT_RATIO;
	return w > h ? (double)w / h : (double)h / w;
}

// True live window aspect (w/h, tracks resizes) — uses the same innerWidth/
// innerHeight source that 50-ui.js feeds the canvas CSS vars, so the render
// aspect and the CSS box stay in lockstep (no residual letterbox).
EM_JS(double, js_live_viewport_aspect, (), {
	try {
		var w = window.innerWidth  || (window.visualViewport && window.visualViewport.width)  || (screen && screen.width)  || 0;
		var h = window.innerHeight || (window.visualViewport && window.visualViewport.height) || (screen && screen.height) || 0;
		if (w > 0 && h > 0) return w / h;
	} catch (e) {}
	return 0.0;
});

// Persist the selected mode index to localStorage and read it back at boot, so
// VID_Init can start directly in the user's last mode instead of the compiled
// default — avoiding the visible default->config jump on launch. We store the
// mode *index* (not dimensions); geometry is always recomputed for the current
// window, so launching in a different orientation than you exited still fits.
EM_JS(void, js_persist_vid_mode, (int n), {
	try { localStorage.setItem("nqVidMode", n); } catch (e) {}
});

EM_JS(int, js_startup_vid_mode, (), {
	try {
		var v = localStorage.getItem("nqVidMode");
		if (v !== null && v !== "") return parseInt(v, 10) | 0;
	} catch (e) {}
	return -1;
});

static double live_viewport_aspect(void)
{
	double a = js_live_viewport_aspect();
	return a > 0.0 ? a : startup_viewport_aspect();
}

typedef struct { int width, height; char desc[32]; } vid_mode_t;
static vid_mode_t modelist[VID_NUM_MODES];
static int        vid_nummodes;
static cvar_t     vid_mode = {"vid_mode", VID_DEFAULT_MODE, true};
static int        startup_vid_mode;
static int        vid_modenum = -1;
static int        vid_cursor = 0;        // menu row: 0 = Mode, 1 = Detail
static int        vid_fixedmodes = 0;    // count of Classic (4:3) modes
static int        disp_w, disp_h;        // canvas drawing-buffer size (window px)
static int        vid_hunkmark;          // hunk high-mark for the per-mode video block
static double     native_resize_at;
static int        native_resize_pending, native_pending_w, native_pending_h;

extern void M_Menu_Options_f(void);
extern void M_Print(int cx, int cy, char *str);
extern void M_DrawCharacter(int cx, int line, int num);
extern void M_DrawPic(int x, int y, qpic_t *pic);
extern qpic_t *Draw_CachePic(char *path);
int VID_NumModes(void);
char *VID_GetModeDescription(int n);

// Decode a mode index: 0..vid_fixedmodes-1 are Classic, the rest Native; the
// Detail tier (0..2) is the offset within either group.
static int mode_is_native(int m) { return m >= vid_fixedmodes; }
static int mode_tier(int m)      { return mode_is_native(m) ? m - vid_fixedmodes : m; }

// Detail tiers expressed as render height (Low / Medium / High), shared by both
// Modes: Classic renders them 4:3, Native renders them at the window aspect, so
// toggling Mode at a tier keeps the same height — only the width changes.
static const int   detail_height[3] = { 240, 480, 960 };
static const char *detail_name[3]   = { "Low", "Medium", "High" };

// Scale v into [lo,hi], moving its partner by the same factor so the pair's
// aspect is preserved. (v's min and max are mutually exclusive, so one call
// per axis covers both bounds.)
static void fit_axis(double *v, double *partner, double lo, double hi)
{
	double s = *v < lo ? lo / *v : (*v > hi ? hi / *v : 1.0);
	*v *= s; *partner *= s;
}

// Native (window-matching) dimensions: anchor on the Detail height, derive
// width from the window aspect, and fit both axes (aspect-preserving) into the
// engine envelope. At very wide aspects the width saturates and the height is
// trimmed so the image still fills the window edge-to-edge without bars.
static void native_dims(int tier, double aspect, int *w, int *h)
{
	double hh = detail_height[clamp_int(tier, 0, 2)];
	double ww;

	if (aspect <= 0.0) aspect = VID_ASPECT_RATIO;
	ww = hh * aspect;
	fit_axis(&ww, &hh, VID_MIN_WIDTH,  VID_MAX_WIDTH);
	fit_axis(&hh, &ww, VID_MIN_HEIGHT, VID_MAX_HEIGHT);
	*w = clamp_int((int)(ww + 0.5), VID_MIN_WIDTH,  VID_MAX_WIDTH);
	*h = clamp_int((int)(hh + 0.5), VID_MIN_HEIGHT, VID_MAX_HEIGHT);
}

static void set_mode_entry(int idx, int w, int h)
{
	modelist[idx].width  = w;
	modelist[idx].height = h;
	snprintf(modelist[idx].desc, sizeof(modelist[idx].desc), "%dx%d", w, h);
}

// Modes 0-2: Classic 4:3 (fixed). Modes 3-5: Native (recomputed live on resize).
// Both share the detail_height ladder, so mode N and N+3 have the same height.
static void build_modelist(void)
{
	double aspect = live_viewport_aspect();
	int w, h, j;

	startup_vid_mode = Q_atoi(vid_mode.string);

	for (j = 0; j < 3; j++) {
		h = detail_height[j];
		w = (int)(h * VID_ASPECT_RATIO + 0.5);
		set_mode_entry(j, w, h);
	}
	vid_fixedmodes = 3;

	for (j = 0; j < 3; j++) {
		native_dims(j, aspect, &w, &h);
		set_mode_entry(3 + j, w, h);
	}
	vid_nummodes = 6;
}

static void VID_DescribeModes_f(void)
{
	int i;
	for (i = 0; i < vid_nummodes; i++)
		Con_Printf("%2d: %s%s\n", i, modelist[i].desc, i == vid_modenum ? "  *" : "");
}

// Apply a Mode (native?) + Detail (tier) selection. Native dimensions are
// recomputed from the live window aspect so the choice takes effect at the
// right size immediately.
static void vid_apply(int native, int tier)
{
	int m = (native ? vid_fixedmodes : 0) + clamp_int(tier, 0, 2);
	if (native) {
		int w, h;
		native_dims(tier, live_viewport_aspect(), &w, &h);
		set_mode_entry(m, w, h);
	}
	VID_SetMode(m, NULL);
}

// Video menu layout (8x8 text-grid coords). Resolution sits a blank row below
// Detail to set the read-only readout apart from the two adjustable rows.
#define VID_MENU_LABEL_X    56
#define VID_MENU_VALUE_X   152
#define VID_MENU_CURSOR_X  136
#define VID_MENU_MODE_Y     56
#define VID_MENU_DETAIL_Y   64
#define VID_MENU_RES_Y      80

static void VID_MenuDraw(void)
{
	qpic_t *p;
	int cur = vid_modenum >= 0 ? vid_modenum : 0;
	int is_native = mode_is_native(cur);
	int tier = mode_tier(cur);
	int rw, rh;
	char buf[32];

	p = Draw_CachePic("gfx/vidmodes.lmp");
	M_DrawPic((320 - p->width) / 2, 4, p);

	M_Print(VID_MENU_LABEL_X, VID_MENU_MODE_Y, "Mode");
	M_Print(VID_MENU_VALUE_X, VID_MENU_MODE_Y, is_native ? "Native" : "Classic (4:3)");

	M_Print(VID_MENU_LABEL_X, VID_MENU_DETAIL_Y, "Detail");
	M_Print(VID_MENU_VALUE_X, VID_MENU_DETAIL_Y, (char *)detail_name[tier]);

	// Resolution is read-only. For Native it tracks the live window.
	if (is_native)
		native_dims(tier, live_viewport_aspect(), &rw, &rh);
	else {
		rw = modelist[cur].width; rh = modelist[cur].height;
	}
	M_Print(VID_MENU_LABEL_X, VID_MENU_RES_Y, "Resolution");
	snprintf(buf, sizeof(buf), "%d x %d", rw, rh);
	M_Print(VID_MENU_VALUE_X, VID_MENU_RES_Y, buf);

	M_DrawCharacter(VID_MENU_CURSOR_X, vid_cursor == 0 ? VID_MENU_MODE_Y : VID_MENU_DETAIL_Y,
	                12 + ((int)(realtime * 4) & 1));
}

// Advance the highlighted row's value, wrapping (Quake menus cycle).
static void vid_adjust(int dir)
{
	int cur = vid_modenum >= 0 ? vid_modenum : 0;
	int is_native = mode_is_native(cur);
	int tier = mode_tier(cur);

	if (vid_cursor == 0)
		vid_apply(!is_native, tier);                 // Mode: toggle Classic <-> Native
	else
		vid_apply(is_native, (tier + dir + 3) % 3);  // Detail: cycle Low/Med/High
}

static void VID_MenuKey(int key)
{
	switch (key) {
	case K_ESCAPE:
		S_LocalSound("misc/menu1.wav");
		M_Menu_Options_f();
		break;
	case K_UPARROW:
	case K_DOWNARROW:
		S_LocalSound("misc/menu1.wav");
		vid_cursor ^= 1;
		break;
	case K_LEFTARROW:
		S_LocalSound("misc/menu3.wav");
		vid_adjust(-1);
		break;
	case K_RIGHTARROW:
	case K_ENTER:
		S_LocalSound("misc/menu3.wav");
		vid_adjust(1);
		break;
	default:
		break;
	}
}

int   VID_NumModes(void)            { return vid_nummodes; }
char *VID_GetModeDescription(int n) { return (n >= 0 && n < vid_nummodes) ? modelist[n].desc : NULL; }

static EMSCRIPTEN_WEBGL_CONTEXT_HANDLE gl_ctx;
static GLuint prog, blit_prog, vao, fb_tex, pal_tex, resolve_fbo, resolve_tex;
static byte *pixels;
static uint32_t pal_rgba[256];

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

static const char *blit_fs_src =
	"#version 300 es\n"
	"precision mediump float;\n"
	"in vec2 uv;\n"
	"out vec4 o;\n"
	"uniform sampler2D framebuf;\n"
	"void main(){o=texture(framebuf,vec2(uv.x,1.0-uv.y));}\n";

static GLuint compile_shader(GLenum type, const char *src) {
	GLuint s = glCreateShader(type);
	glShaderSource(s, 1, &src, NULL);
	glCompileShader(s);
	GLint ok; glGetShaderiv(s, GL_COMPILE_STATUS, &ok);
	if (!ok) { char log[512]; glGetShaderInfoLog(s, 512, NULL, log); Sys_Error("Shader: %s", log); }
	return s;
}

static GLuint link_program(GLuint vs, GLuint fs) {
	GLuint p = glCreateProgram();
	glAttachShader(p, vs); glAttachShader(p, fs);
	glLinkProgram(p);
	GLint ok; glGetProgramiv(p, GL_LINK_STATUS, &ok);
	if (!ok) { char log[512]; glGetProgramInfoLog(p, 512, NULL, log); Sys_Error("Link: %s", log); }
	return p;
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

static void init_gl(void) {
	EmscriptenWebGLContextAttributes a;
	emscripten_webgl_init_context_attributes(&a);
	a.majorVersion = 2; a.alpha = a.antialias = a.depth = a.stencil = 0;
	gl_ctx = emscripten_webgl_create_context("#canvas", &a);
	if (gl_ctx <= 0) Sys_Error("VID: WebGL2 failed (%d)", gl_ctx);
	emscripten_webgl_make_context_current(gl_ctx);
	// Framebuffer is GL_R8; width may be non-4-byte aligned on custom modes.
	glPixelStorei(GL_UNPACK_ALIGNMENT, 1);

	GLuint vs = compile_shader(GL_VERTEX_SHADER, vs_src);
	GLuint fs = compile_shader(GL_FRAGMENT_SHADER, fs_src);
	prog = link_program(vs, fs); glDeleteShader(fs);
	GLuint bfs = compile_shader(GL_FRAGMENT_SHADER, blit_fs_src);
	blit_prog = link_program(vs, bfs); glDeleteShader(vs); glDeleteShader(bfs);

	glGenVertexArrays(1, &vao);
	pal_tex = mk_tex(GL_RGBA8, 256, 1, GL_RGBA);
	glGenFramebuffers(1, &resolve_fbo);
	glUseProgram(prog);
	glUniform1i(glGetUniformLocation(prog, "framebuf"), 0);
	glUniform1i(glGetUniformLocation(prog, "pal"), 1);
	glUseProgram(blit_prog);
	glUniform1i(glGetUniformLocation(blit_prog, "framebuf"), 0);
}

int VID_SetMode(int modenum, unsigned char *palette)
{
	if (modenum < 0 || modenum >= vid_nummodes)
		return 0;

	int w = modelist[modenum].width, h = modelist[modenum].height;

	free(pixels);
	pixels = malloc(w * h);
	if (!pixels) Sys_Error("VID_SetMode: not enough memory\n");

	if (fb_tex)      glDeleteTextures(1, &fb_tex);
	if (resolve_tex) glDeleteTextures(1, &resolve_tex);
	fb_tex      = mk_tex(GL_R8,    w, h, GL_RED);
	resolve_tex = mk_tex(GL_RGBA8, w, h, GL_RGBA);
	// Plain LINEAR, no mip chain: the canvas backbuffer is sized to the render
	// resolution, so Pass 2 presents 1:1 and never minifies. The texture is only
	// ever sampled from level 0; no per-frame glGenerateMipmap is needed.
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
	glBindFramebuffer(GL_FRAMEBUFFER, resolve_fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, resolve_tex, 0);
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	emscripten_set_canvas_element_size("#canvas", w, h);
	js_update_canvas_ar((double)w / h);
	disp_w = disp_h = 0;

	VGA_width = vid.width = vid.conwidth = w;
	VGA_height = vid.height = vid.conheight = h;
	VGA_pagebase = vid.buffer = vid.conbuffer = pixels;
	VGA_rowbytes = vid.rowbytes = vid.conrowbytes = w;

	// Reallocate the z-buffer + surface cache for this mode's resolution. (Stock
	// maps only, so the per-mode hunk churn from live re-modes stays bounded.)
	if (d_pzbuffer) { D_FlushCaches(); Hunk_FreeToHighMark(vid_hunkmark); }
	vid_hunkmark = Hunk_HighMark();
	int chunk = w * h * sizeof(*d_pzbuffer), cachesize = D_SurfaceCacheForRes(w, h);
	d_pzbuffer = Hunk_HighAllocName(chunk + cachesize, "video");
	if (!d_pzbuffer) Sys_Error("VID_SetMode: not enough memory for video mode\n");
	D_InitCaches((byte *)d_pzbuffer + chunk, cachesize);

	VID_SetPalette(palette);
	Cvar_Set("vid_mode", va("%d", modenum));
	if (modenum != vid_modenum)
		js_persist_vid_mode(modenum);  // mirror to localStorage for next boot
	vid_modenum = modenum;
	vid.recalc_refdef = 1;
	return 1;
}

void VID_SetPalette(unsigned char *palette) {
	if (!palette) return;
	for (int i = 0; i < 256; i++)
		pal_rgba[i] = palette[i*3] | (palette[i*3+1] << 8) | (palette[i*3+2] << 16) | 0xFF000000;
	glBindTexture(GL_TEXTURE_2D, pal_tex);
	glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, 256, 1, GL_RGBA, GL_UNSIGNED_BYTE, pal_rgba);
}

void VID_ShiftPalette(unsigned char *p) { VID_SetPalette(p); }

void VID_Init(unsigned char *palette) {
	vid.maxwarpwidth = WARP_WIDTH; vid.maxwarpheight = WARP_HEIGHT;
	vid.direct = 0;
	vid.aspect = 1.0f;
	vid.numpages = 1;
	vid.colormap = host_colormap;
	vid.fullbright = 256 - LittleLong(*((int *)vid.colormap + 2048));

	build_modelist();
	Cvar_RegisterVariable(&vid_mode);
	Cmd_AddCommand("vid_describemodes", VID_DescribeModes_f);
	vid_menudrawfn = VID_MenuDraw;
	vid_menukeyfn = VID_MenuKey;
	init_gl();
	// Prefer the last mode the user picked (persisted), so boot starts in it
	// rather than the compiled default and then jumping when config.cfg loads.
	{
		int sm = js_startup_vid_mode();
		if (sm >= 0 && sm < vid_nummodes) startup_vid_mode = sm;
	}
	VID_SetMode(clamp_int(startup_vid_mode, 0, vid_nummodes - 1), palette);
}

void VID_Shutdown(void) {
	free(pixels); pixels = NULL;
	glDeleteFramebuffers(1, &resolve_fbo);
	glDeleteTextures(1, &fb_tex); glDeleteTextures(1, &pal_tex); glDeleteTextures(1, &resolve_tex);
	glDeleteProgram(prog); glDeleteProgram(blit_prog); glDeleteVertexArrays(1, &vao);
	if (gl_ctx > 0) { emscripten_webgl_destroy_context(gl_ctx); gl_ctx = 0; }
}

void VID_Update(vrect_t *rects) {
	if ((int)vid_mode.value != vid_modenum) {
		int req = (int)vid_mode.value;
		int m   = clamp_int(req, 0, vid_nummodes - 1);
		if (req != m)
			Con_Printf("vid_mode %d invalid (0-%d); using %d\n", req, vid_nummodes - 1, m);
		VID_SetMode(m, NULL);
		return;  // skip render this frame; renderer resets next frame
	}

	// Canvas backbuffer = render resolution, NOT the full window. The element's
	// CSS box (sized to the game aspect via --nq-ar) upscales it to fill the
	// window, so the browser composites only render-res pixels. Sizing the
	// backbuffer to full-window x devicePixelRatio made the *present* (the
	// browser compositing the WebGL canvas) -- not the render -- the frame's
	// bottleneck: a large ultrawide backbuffer can't be composited within the
	// 60Hz vsync budget, so every Native mode fell to the next vsync (~30fps)
	// regardless of Detail or render cost (which is ~1ms). Present cost now
	// tracks the Detail tier, exactly like render cost. Upscaling render->window
	// is a single bilinear step either way -- here the browser does it on
	// composite instead of the GL blit doing it into an oversized backbuffer.
	if (disp_w == 0) {
		// Fresh mode change: VID_SetMode already sized the canvas, so just adopt
		// the dims without a redundant emscripten_set_canvas_element_size call.
		disp_w = VGA_width; disp_h = VGA_height;
	} else if (VGA_width != disp_w || VGA_height != disp_h) {
		disp_w = VGA_width; disp_h = VGA_height;
		emscripten_set_canvas_element_size("#canvas", disp_w, disp_h);
	}
	// Native follows the window: recompute the target from the live window aspect
	// EVERY frame and re-mode (debounced) when it drifts. Deliberately not gated
	// on canvas-resize — on mobile, innerWidth/innerHeight lag the canvas reflow
	// after a rotation, so a one-shot check can read a stale (pre-rotation) aspect
	// and never re-correct. Re-checking each frame self-heals once the viewport
	// settles; the EM_JS aspect read is cheap. (disp_changed only resizes the GL
	// buffer above.)
	if (mode_is_native(vid_modenum) && disp_w > 0 && disp_h > 0) {
		int tw, th;
		native_dims(mode_tier(vid_modenum), live_viewport_aspect(), &tw, &th);
		if (abs(tw - VGA_width) > NATIVE_REMODE_EPS || abs(th - VGA_height) > NATIVE_REMODE_EPS) {
			if (!native_resize_pending ||
			    abs(tw - native_pending_w) > NATIVE_REMODE_EPS || abs(th - native_pending_h) > NATIVE_REMODE_EPS) {
				// Snap orientation flips (rotation) immediately; debounce drags.
				int flip = (VGA_width >= VGA_height) != (tw >= th);
				native_resize_pending = 1;
				native_pending_w = tw; native_pending_h = th;
				native_resize_at = realtime + (flip ? NATIVE_REMODE_FLIP_DELAY : NATIVE_REMODE_DRAG_DELAY);
			} else if (realtime >= native_resize_at) {
				native_resize_pending = 0;
				set_mode_entry(vid_modenum, tw, th);
				VID_SetMode(vid_modenum, NULL);
				return;  // skip render this frame; renderer resets next frame
			}
		} else {
			native_resize_pending = 0;
		}
	} else {
		native_resize_pending = 0;
	}
	// Pass 1: palette resolve at game res into FBO
	glBindFramebuffer(GL_FRAMEBUFFER, resolve_fbo);
	glViewport(0, 0, VGA_width, VGA_height);
	glActiveTexture(GL_TEXTURE0); glBindTexture(GL_TEXTURE_2D, fb_tex);
	glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, VGA_width, VGA_height, GL_RED, GL_UNSIGNED_BYTE, pixels);
	glActiveTexture(GL_TEXTURE1); glBindTexture(GL_TEXTURE_2D, pal_tex);
	glUseProgram(prog); glBindVertexArray(vao);
	glDrawArrays(GL_TRIANGLES, 0, 3);
	// Pass 2: present the resolved image 1:1. The canvas backbuffer is sized to
	// the render resolution (see above), so the viewport is always the full
	// (0,0,VGA_width,VGA_height) -- the browser handles any letterbox/pillarbox
	// via CSS (--nq-ar) when upscaling the canvas to fill the window. The blit
	// never minifies, so the mip filter stays LINEAR (set in VID_SetMode) and no
	// per-frame glGenerateMipmap is needed.
	glActiveTexture(GL_TEXTURE0); glBindTexture(GL_TEXTURE_2D, resolve_tex);
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	glClear(GL_COLOR_BUFFER_BIT);
	glViewport(0, 0, VGA_width, VGA_height);
	glUseProgram(blit_prog);
	glDrawArrays(GL_TRIANGLES, 0, 3);
}

void D_BeginDirectRect(int x, int y, byte *pbitmap, int width, int height) {
	if (!pixels || !pbitmap || width <= 0 || height <= 0) return;
	if (x < 0) x = VGA_width + x - 1;
	if (x < 0 || y < 0 || x >= VGA_width || y >= VGA_height) return;
	if (x + width > VGA_width) width = VGA_width - x;
	if (y + height > VGA_height) height = VGA_height - y;
	byte *dst = pixels + y * VGA_width + x;
	while (height--) { memcpy(dst, pbitmap, width); dst += VGA_width; pbitmap += width; }
}

void D_EndDirectRect(int x, int y, int width, int height) {}
