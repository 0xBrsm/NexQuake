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

//engine mins/maxes
#define VID_MIN_WIDTH  320
#define VID_MIN_HEIGHT 200
#define VID_MAX_WIDTH  1280
#define VID_MAX_HEIGHT 1024
//
#define VID_ASPECT_RATIO (4.0 / 3.0)
#define VID_DEFAULT_MODE "1"
#define VID_NUM_MODES 6
#define VID_ROW_SIZE 3


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

typedef struct { int width, height; char desc[32]; } vid_mode_t;
static vid_mode_t modelist[VID_NUM_MODES];
static int        vid_nummodes;
static cvar_t     vid_mode = {"vid_mode", VID_DEFAULT_MODE, true};
static int        startup_vid_mode;
static int        vid_modenum = -1;
static int        vid_hunkmark = 0;
static int        vid_line = 0;
static int        vid_wmodes = 0;
static int        vid_fixedmodes = 0;

extern void M_Menu_Options_f(void);
extern void M_Print(int cx, int cy, char *str);
extern void M_PrintWhite(int cx, int cy, char *str);
extern void M_DrawCharacter(int cx, int line, int num);
extern void M_DrawPic(int x, int y, qpic_t *pic);
extern qpic_t *Draw_CachePic(char *path);
int VID_NumModes(void);
char *VID_GetModeDescription(int n);

#define MAX_COLUMN_SIZE 5
#define MODE_AREA_HEIGHT (MAX_COLUMN_SIZE + 6)

static qboolean mode_is_widescreen(int modenum)
{
	if (modenum < 0 || modenum >= vid_nummodes)
		return false;
	return modelist[modenum].width * 3 > modelist[modenum].height * 4;
}

static void update_mode_fov(int old_w, int old_h, int new_w, int new_h)
{
	cvar_t *fovvar;
	double old_aspect, new_aspect, old_fov, old_half, new_fov;

	if (new_w <= 0 || new_h <= 0)
		return;

	fovvar = Cvar_FindVar("fov");
	if (!fovvar)
		return;

	old_aspect = (old_w > 0 && old_h > 0)
		? (double)old_w / old_h
		: VID_ASPECT_RATIO;
	new_aspect = (double)new_w / new_h;
	if (old_aspect <= 0.0 || new_aspect <= 0.0 || fabs(new_aspect - old_aspect) < 0.0001)
		return;

	old_fov = fovvar->value;
	old_half = old_fov * (M_PI / 360.0);
	new_fov = atan(tan(old_half) * (new_aspect / old_aspect)) * (360.0 / M_PI);
	if (fabs(new_fov - old_fov) > 0.01)
		Cvar_SetValue("fov", (float)new_fov);
}

// Compute (w,h) from one anchored dimension; anchor_w true = width anchor.
static void mode_size(double aspect, qboolean anchor_w, int anchor, int *w, int *h)
{
	if (anchor_w) {
		*w = clamp_int(anchor, VID_MIN_WIDTH,  VID_MAX_WIDTH);
		*h = clamp_int((int)(*w / aspect + 0.5), VID_MIN_HEIGHT, VID_MAX_HEIGHT);
	} else {
		*h = clamp_int(anchor, VID_MIN_HEIGHT, VID_MAX_HEIGHT);
		*w = clamp_int((int)(*h * aspect + 0.5), VID_MIN_WIDTH,  VID_MAX_WIDTH);
	}
}

static void append_mode(int w, int h)
{
	int i;

	if (vid_nummodes >= VID_NUM_MODES) return;
	for (i = 0; i < vid_nummodes; i++) {
		if (modelist[i].width == w && modelist[i].height == h)
			return;
	}
	modelist[vid_nummodes].width  = w;
	modelist[vid_nummodes].height = h;
	sprintf(modelist[vid_nummodes].desc, "%dx%d", w, h);
	vid_nummodes++;
}

static void build_modelist(void)
{
	static const double scales[3]    = {0.25, 0.5, 1.0};
	const double hardcap = (double)VID_MAX_WIDTH / VID_MAX_HEIGHT;
	double aspects[2] = {VID_ASPECT_RATIO, startup_viewport_aspect()};
	int w, h, i, j;

	vid_nummodes = 0;
	vid_fixedmodes = 0;
	startup_vid_mode = Q_atoi(vid_mode.string);

	for (i = 0; i < 2; i++) {
		qboolean wa = (aspects[i] >= hardcap);
		int base = wa ? VID_MAX_WIDTH : VID_MAX_HEIGHT;
		for (j = 0; j < 3; j++) {
			mode_size(aspects[i], wa, (int)(base * scales[j] + 0.5), &w, &h);
			append_mode(w, h);
		}
		if (i == 0)
			vid_fixedmodes = vid_nummodes;
	}
}

static void VID_DescribeModes_f(void)
{
	int i;
	for (i = 0; i < vid_nummodes; i++)
		Con_Printf("%2d: %s%s\n", i, modelist[i].desc, i == vid_modenum ? "  *" : "");
}

static void draw_mode_grid(int start, int count, int base_y)
{
	int i, col = 16, row = base_y;
	for (i = 0; i < count; i++) {
		char *desc = VID_GetModeDescription(start + i);
		if (start + i == vid_modenum)
			M_PrintWhite(col, row, desc);
		else
			M_Print(col, row, desc);
			col += 13 * 8;
		if ((i % VID_ROW_SIZE) == (VID_ROW_SIZE - 1)) {
			col = 16;
			row += 8;
		}
	}
}

static void grid_cursor_pos(int line, int fixed, int classic_y, int fs_y,
	int *cx, int *cy)
{
	int in_classic = (line < fixed);
	int local = in_classic ? line : line - fixed;
	*cx = 8 + (local % VID_ROW_SIZE) * 13 * 8;
	*cy = (in_classic ? classic_y : fs_y) + (local / VID_ROW_SIZE) * 8;
}

// Navigate a flat grid of `count` items in `cols` columns.
static int grid_nav(int line, int count, int cols, int key)
{
	int total_rows;
	switch (key) {
	case K_LEFTARROW:  // wrap left within row
		line = (line / cols) * cols + (line + cols - 1) % cols;
		if (line >= count)
			line = count - 1;
		break;
	case K_RIGHTARROW:  // wrap right within row
		line = (line / cols) * cols + (line + 1) % cols;
		if (line >= count)
			line = (line / cols) * cols;
		break;
	case K_UPARROW:  // wrap up to bottom-most row in same column
		line -= cols;
		if (line < 0) {
			total_rows = (count + cols - 1) / cols;
			line += total_rows * cols;
			while (line >= count)
				line -= cols;
		}
		break;
	case K_DOWNARROW:  // wrap down to top-most row in same column
		line += cols;
		if (line >= count) {
			total_rows = (count + cols - 1) / cols;
			line -= total_rows * cols;
			while (line < 0)
				line += cols;
		}
		break;
	}
	return line;
}

static void VID_MenuDraw(void)
{
	int fixed_modes, fullscreen_modes, fixed_rows;
	int classic_y, fs_label_y, fs_modes_y, cursor_x, cursor_y;
	qpic_t *p;

	if (vid_nummodes <= 0) {
		M_Print(16, 36, "No video modes available");
		M_Print(16, 52, "Esc to exit");
		return;
	}

	p = Draw_CachePic("gfx/vidmodes.lmp");
	M_DrawPic((320 - p->width) / 2, 4, p);

	vid_wmodes = vid_nummodes;
	fixed_modes = clamp_int(vid_fixedmodes, 0, vid_wmodes);
	fullscreen_modes = vid_wmodes - fixed_modes;
	fixed_rows = (fixed_modes + (VID_ROW_SIZE - 1)) / VID_ROW_SIZE;
	classic_y = 36 + 2 * 8;
	fs_label_y = 36 + (fixed_rows + 4) * 8;
	fs_modes_y = fs_label_y + 2 * 8;

	if (vid_line < 0 || vid_line >= vid_wmodes)
		vid_line = clamp_int(vid_modenum, 0, vid_wmodes - 1);

	M_Print(13 * 8, 36, "Classic Modes");
	draw_mode_grid(0, fixed_modes, classic_y);

	if (fullscreen_modes > 0) {
		M_Print(11 * 8, fs_label_y, "Fullscreen Modes");
		draw_mode_grid(fixed_modes, fullscreen_modes, fs_modes_y);
	}

	if (mode_is_widescreen(vid_line))
		M_Print(6 * 8, 36 + MODE_AREA_HEIGHT * 8 - 8, "Weapon hidden in widescreen");
	M_Print(9 * 8, 36 + MODE_AREA_HEIGHT * 8 + 8, "Press Enter to set mode");
	M_Print(15 * 8, 36 + MODE_AREA_HEIGHT * 8 + 8 * 2, "Esc to exit");

	grid_cursor_pos(vid_line, fixed_modes, classic_y, fs_modes_y, &cursor_x, &cursor_y);
	M_DrawCharacter(cursor_x, cursor_y, 12 + ((int)(realtime * 4) & 1));
}

static void VID_MenuKey(int key)
{
	if (vid_wmodes <= 0)
		return;
	if (vid_line < 0 || vid_line >= vid_wmodes)
		vid_line = clamp_int(vid_modenum, 0, vid_wmodes - 1);

	switch (key) {
	case K_ESCAPE:
		S_LocalSound("misc/menu1.wav");
		M_Menu_Options_f();
		break;
	case K_LEFTARROW:
	case K_RIGHTARROW:
	case K_UPARROW:
	case K_DOWNARROW:
		S_LocalSound("misc/menu1.wav");
		vid_line = grid_nav(vid_line, vid_wmodes, VID_ROW_SIZE, key);
		break;
	case K_ENTER:
		S_LocalSound("misc/menu1.wav");
		VID_SetMode(vid_line, NULL);
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
static int disp_w, disp_h;

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
	int old_w = VGA_width, old_h = VGA_height;

	free(pixels);
	pixels = malloc(w * h);
	if (!pixels) Sys_Error("VID_SetMode: not enough memory\n");

	if (fb_tex)      glDeleteTextures(1, &fb_tex);
	if (resolve_tex) glDeleteTextures(1, &resolve_tex);
	fb_tex      = mk_tex(GL_R8,    w, h, GL_RED);
	resolve_tex = mk_tex(GL_RGBA8, w, h, GL_RGBA);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR_MIPMAP_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
	glBindFramebuffer(GL_FRAMEBUFFER, resolve_fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, resolve_tex, 0);
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	emscripten_set_canvas_element_size("#canvas", w, h);
	js_update_canvas_ar((double)w / h);
	disp_w = disp_h = 0;
	update_mode_fov(old_w, old_h, w, h);

	VGA_width = vid.width = vid.conwidth = w;
	VGA_height = vid.height = vid.conheight = h;
	VGA_pagebase = vid.buffer = vid.conbuffer = pixels;
	VGA_rowbytes = vid.rowbytes = vid.conrowbytes = w;

	if (d_pzbuffer) { D_FlushCaches(); Hunk_FreeToHighMark(vid_hunkmark); }
	vid_hunkmark = Hunk_HighMark();
	int chunk = w * h * sizeof(*d_pzbuffer), cachesize = D_SurfaceCacheForRes(w, h);
	d_pzbuffer = Hunk_HighAllocName(chunk + cachesize, "video");
	if (!d_pzbuffer) Sys_Error("VID_SetMode: not enough memory for video mode\n");
	D_InitCaches((byte *)d_pzbuffer + chunk, cachesize);

	VID_SetPalette(palette);
	Cvar_Set("vid_mode", va("%d", modenum));
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

	double css_w, css_h;
	emscripten_get_element_css_size("#canvas", &css_w, &css_h);
	double dpr = emscripten_get_device_pixel_ratio();
	int dw = (int)(css_w * dpr), dh = (int)(css_h * dpr);
	if (dw > 0 && dh > 0 && (dw != disp_w || dh != disp_h)) {
		disp_w = dw; disp_h = dh;
		emscripten_set_canvas_element_size("#canvas", disp_w, disp_h);
	}
	// Pass 1: palette resolve at game res into FBO
	glBindFramebuffer(GL_FRAMEBUFFER, resolve_fbo);
	glViewport(0, 0, VGA_width, VGA_height);
	glActiveTexture(GL_TEXTURE0); glBindTexture(GL_TEXTURE_2D, fb_tex);
	glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, VGA_width, VGA_height, GL_RED, GL_UNSIGNED_BYTE, pixels);
	glActiveTexture(GL_TEXTURE1); glBindTexture(GL_TEXTURE_2D, pal_tex);
	glUseProgram(prog); glBindVertexArray(vao);
	glDrawArrays(GL_TRIANGLES, 0, 3);
	// Pass 2: aspect-correct blit to display (letterbox / pillarbox)
	glActiveTexture(GL_TEXTURE0); glBindTexture(GL_TEXTURE_2D, resolve_tex);
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	glGenerateMipmap(GL_TEXTURE_2D);
	{
		int fw = disp_w ? disp_w : VGA_width, fh = disp_h ? disp_h : VGA_height;
		double gasp = (double)VGA_width / VGA_height;
		int bx = 0, by = 0, bw = fw, bh = fh;
		if ((double)fw / fh > gasp) { bw = (int)(fh * gasp + 0.5); bx = (fw - bw) / 2; }
		else                        { bh = (int)(fw / gasp + 0.5); by = (fh - bh) / 2; }
		glClear(GL_COLOR_BUFFER_BIT);
		glViewport(bx, by, bw, bh);
	}
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
