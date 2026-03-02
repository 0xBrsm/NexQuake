// in_wasm.c -- WASM/Emscripten input (keyboard, mouse, touch, gamepad)
//
// Follows the WinQuake paradigm: platform input lives in in_<platform>.c,
// separate from the video driver. Implements the standard Quake IN_*
// interface plus C-native touch and gamepad support so the JS layer can
// stay thin.

#include "quakedef.h"
#include <emscripten.h>
#include <emscripten/html5.h>
#include <string.h>
#include <math.h>

// ---------------------------------------------------------------------------
// External engine symbols
// ---------------------------------------------------------------------------
extern int m_state;
extern void SNDDMA_Pause(void);
extern void SNDDMA_Resume(void);

// menu.c local enum value for m_quit
#define MSTATE_QUIT 12

// Bind slots mapped onto stock Quake JOY/AUX keycodes.
#ifndef K_JOY_A
#define K_JOY_A				K_JOY1
#define K_JOY_B				K_JOY2
#define K_JOY_X				K_JOY3
#define K_JOY_Y				K_JOY4
#define K_JOY_LB			K_AUX1
#define K_JOY_RB			K_AUX2
#define K_JOY_LT			K_AUX3
#define K_JOY_RT			K_AUX4
#define K_JOY_BACK			K_AUX5
#define K_JOY_START			K_AUX6
#define K_JOY_DPAD_UP		K_AUX7
#define K_JOY_DPAD_DOWN		K_AUX8
#define K_JOY_DPAD_LEFT		K_AUX9
#define K_JOY_DPAD_RIGHT	K_AUX10
#define K_JOY_LS			K_AUX11
#define K_JOY_RS			K_AUX12
#endif

#ifndef K_TOUCH1
#define K_TOUCH1 K_AUX13
#define K_TOUCH2 K_AUX14
#define K_TOUCH3 K_AUX15
#define K_TOUCH4 K_AUX16
#define K_TOUCH5 K_AUX17
#define K_TOUCH6 K_AUX18
#define K_TOUCH7 K_AUX19
#define K_TOUCH8 K_AUX20
#endif

#ifndef K_TOUCH_TAP1
#define K_TOUCH_TAP1 K_AUX21
#define K_TOUCH_TAP2 K_AUX22
#endif

// ---------------------------------------------------------------------------
// Tunables / shared constants
// ---------------------------------------------------------------------------
#define TOUCH_SLOT_COUNT      8
#define TOUCH_MENU_BUTTON_COUNT 1
#define TOUCH_BUTTON_COUNT    (TOUCH_SLOT_COUNT + TOUCH_MENU_BUTTON_COUNT)
#define TOUCH_MENU_BACK_BUTTON TOUCH_SLOT_COUNT
#define MAX_TOUCH_POINTS      10
#define TOUCH_MOVE_RADIUS     60.0f
#define TOUCH_MOVE_ZONE_SPLIT 0.40f
#define TOUCH_MENU_ZONE_SPLIT 0.50f
#define JOY_MENU_NAV_THRESH   0.35f

#define JOY_DEAD_ZONE             0.20f
#define JOY_TRIGGER_THRESH        0.5f
#define DEFAULT_SENSITIVITY       3.0f
#define DEFAULT_M_YAW             0.022f
#define JOY_TARGET_TURN_RATE_DPS  180.0f
#define TOUCH_TARGET_SWIPE_TURN   180.0f
#define TOUCH_TARGET_SWIPE_FRAC   0.50f
#define JOY_LOOK_UNITS_PER_SECOND (JOY_TARGET_TURN_RATE_DPS / (DEFAULT_SENSITIVITY * DEFAULT_M_YAW))

#define CLAMP_PITCH(a) do { if ((a) > 80) (a) = 80; else if ((a) < -70) (a) = -70; } while(0)

// ---------------------------------------------------------------------------
// Mouse state
// ---------------------------------------------------------------------------
static qboolean mouse_avail;
static float    mouse_x, mouse_y;
static qboolean pointer_locked;
static double   ptrlock_lost_at;

// ---------------------------------------------------------------------------
// Touch state
// ---------------------------------------------------------------------------
static qboolean touch_active; // true when touch/gamepad device detected
static float    touch_look_x, touch_look_y;
static float    joy_look_x, joy_look_y;

static struct {
	long identifier;
	float lastX, lastY;
	float startX, startY;
	double startMs;
	qboolean active;
} touch_look_points[MAX_TOUCH_POINTS];

static struct {
	long identifier;
	float originX, originY;
	float startX, startY;
	double startMs;
	float axisX, axisY;
	qboolean active;
} touch_move;

static struct {
	long identifier;
	qboolean active;
} touch_buttons[TOUCH_BUTTON_COUNT];

static const int touch_slot_keys[TOUCH_SLOT_COUNT] = {
	K_TOUCH1, K_TOUCH2, K_TOUCH3, K_TOUCH4,
	K_TOUCH5, K_TOUCH6, K_TOUCH7, K_TOUCH8
};

static qboolean fullscreen_requested;
static qboolean touch_flip_latched;

// ---------------------------------------------------------------------------
// Gamepad state
// ---------------------------------------------------------------------------
// Standard gamepad button indices (W3C "standard" mapping)
#define JOY_BTN_A            0
#define JOY_BTN_B            1
#define JOY_BTN_X            2
#define JOY_BTN_Y            3
#define JOY_BTN_LB           4
#define JOY_BTN_RB           5
#define JOY_BTN_LT           6
#define JOY_BTN_RT           7
#define JOY_BTN_BACK         8
#define JOY_BTN_START        9
#define JOY_BTN_LS          10
#define JOY_BTN_RS          11
#define JOY_BTN_DPAD_UP     12
#define JOY_BTN_DPAD_DOWN   13
#define JOY_BTN_DPAD_LEFT   14
#define JOY_BTN_DPAD_RIGHT  15
#define JOY_MAX_BUTTONS     17

// Parallel table: W3C button index → Quake key (0 = unmapped)
static const int s_joy_key_map[JOY_MAX_BUTTONS] = {
	K_JOY_A, K_JOY_B, K_JOY_X, K_JOY_Y,
	K_JOY_LB, K_JOY_RB, K_JOY_LT, K_JOY_RT,
	K_JOY_BACK, K_JOY_START, K_JOY_LS, K_JOY_RS,
	K_JOY_DPAD_UP, K_JOY_DPAD_DOWN, K_JOY_DPAD_LEFT, K_JOY_DPAD_RIGHT,
	0
};

static qboolean joy_button_state[JOY_MAX_BUTTONS];
static qboolean joy_connected;
static float    joy_move_x, joy_move_y;

// ---------------------------------------------------------------------------
// Virtual control emission + menu translation
// ---------------------------------------------------------------------------
#define CTRL_TOUCH_SLOT_BASE  0
#define CTRL_TOUCH_SLOT_COUNT TOUCH_BUTTON_COUNT
#define CTRL_JOY_BTN_BASE    (CTRL_TOUCH_SLOT_BASE + CTRL_TOUCH_SLOT_COUNT)
#define CTRL_JOY_BTN_COUNT    JOY_MAX_BUTTONS
#define CTRL_JOY_NAV_BASE    (CTRL_JOY_BTN_BASE + CTRL_JOY_BTN_COUNT)
#define CTRL_JOY_NAV_COUNT    4
#define CTRL_TOTAL           (CTRL_JOY_NAV_BASE + CTRL_JOY_NAV_COUNT)

static qboolean vctrl_down[CTRL_TOTAL];
static int      vctrl_emitted_key[CTRL_TOTAL];
static qboolean menu_mode_latched;

void js_set_touch_menu_mode(int active);
static void touch_clear_move(void);
static void touch_cancel_all(void);

static qboolean is_menu_virtual_key(int key)
{
	switch (key)
	{
	case K_ENTER:
	case K_ESCAPE:
	case K_SPACE:
	case K_UPARROW:
	case K_DOWNARROW:
	case K_LEFTARROW:
	case K_RIGHTARROW:
	case 'y':
		return true;
	default:
		return false;
	}
}

static int touch_button_gameplay_key(int slot)
{
	if ((unsigned)slot < (unsigned)TOUCH_SLOT_COUNT)
		return touch_slot_keys[slot];
	return 0;
}

static qboolean menu_accepts_yesno(void)
{
	return key_dest == key_menu && (m_state == MSTATE_QUIT || key_count < 0);
}

static int menu_accept_key(void)
{
	return menu_accepts_yesno() ? 'y' : K_ENTER;
}

static int menu_key_for_gameplay_key(int gameplay_key)
{
	switch (gameplay_key)
	{
	case K_JOY_DPAD_UP:    return K_UPARROW;
	case K_JOY_DPAD_DOWN:  return K_DOWNARROW;
	case K_JOY_DPAD_LEFT:  return K_LEFTARROW;
	case K_JOY_DPAD_RIGHT: return K_RIGHTARROW;
	case K_JOY_A: return menu_accept_key();
	case K_JOY_B: return K_ESCAPE;
	case K_JOY_X: return K_SPACE;
	default:       return 0;
	}
}

static int translate_virtual_key(int gameplay_key, int menu_key)
{
	int mapped;

	if (key_dest != key_menu)
		return gameplay_key;
	if (menu_key)
		return menu_key;
	mapped = menu_key_for_gameplay_key(gameplay_key);
	return mapped ? mapped : gameplay_key;
}

static void emit_virtual_control(int control_id, int gameplay_key, int menu_key, qboolean down)
{
	int emitted;

	if (control_id < 0 || control_id >= CTRL_TOTAL)
		return;
	if (down == vctrl_down[control_id])
		return;

	if (down)
	{
		emitted = translate_virtual_key(gameplay_key, menu_key);
		vctrl_down[control_id] = true;
		vctrl_emitted_key[control_id] = emitted;
		if (emitted)
			Key_Event(emitted, true);
		return;
	}

	emitted = vctrl_emitted_key[control_id];
	vctrl_down[control_id] = false;
	vctrl_emitted_key[control_id] = 0;
	if (emitted)
		Key_Event(emitted, false);
}

static void release_menu_virtual_controls(void)
{
	int i;

	for (i = 0; i < CTRL_TOTAL; i++)
	{
		if (!vctrl_down[i] || !is_menu_virtual_key(vctrl_emitted_key[i]))
			continue;

		Key_Event(vctrl_emitted_key[i], false);
		vctrl_down[i] = false;
		vctrl_emitted_key[i] = 0;
	}
}

static void sync_menu_mode_transition(void)
{
	qboolean in_menu = (key_dest == key_menu);

	if (in_menu == menu_mode_latched)
		return;

	if (in_menu)
		touch_cancel_all();
	release_menu_virtual_controls();
	menu_mode_latched = in_menu;
	js_set_touch_menu_mode(in_menu);
}

static void update_menu_nav(int nav_base, float thresh, float nx, float ny)
{
	qboolean in_menu = (key_dest == key_menu);

	emit_virtual_control(nav_base + 0, 0, K_UPARROW,    in_menu && ny < -thresh);
	emit_virtual_control(nav_base + 1, 0, K_DOWNARROW,  in_menu && ny >  thresh);
	emit_virtual_control(nav_base + 2, 0, K_LEFTARROW,  in_menu && nx < -thresh);
	emit_virtual_control(nav_base + 3, 0, K_RIGHTARROW, in_menu && nx >  thresh);
}

// ---------------------------------------------------------------------------
// Input cvars
// ---------------------------------------------------------------------------
static cvar_t analog_speed      = {"analog_speed",      "400"};
static cvar_t touch_flip        = {"touch_flip",        "0"};
static cvar_t touch_tap_ms      = {"touch_tap_ms",      "220"};
static cvar_t touch_tap_px      = {"touch_tap_px",      "20"};
static cvar_t joy_sensitivity   = {"joy_sensitivity",   "3", true};
static cvar_t touch_sensitivity = {"touch_sensitivity", "3", true};
static cvar_t joy_invertpitch   = {"joy_invertpitch",   "0", true};
static cvar_t touch_invertpitch = {"touch_invertpitch", "0", true};
static cvar_t joy_lookspring    = {"joy_lookspring",    "0", true};
static cvar_t touch_lookspring  = {"touch_lookspring",  "0", true};
static cvar_t joy_lookstrafe    = {"joy_lookstrafe",    "0", true};
static cvar_t touch_lookstrafe  = {"touch_lookstrafe",  "0", true};

// ---------------------------------------------------------------------------
// DOM keyCode -> Quake key mapping
// ---------------------------------------------------------------------------
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

// ---------------------------------------------------------------------------
// EM_JS helpers
// ---------------------------------------------------------------------------
EM_JS(void, js_request_pointerlock, (), {
	var p = document.getElementById('canvas').requestPointerLock();
	if (p && p.catch) p.catch(function(){});
});

EM_JS(int, js_overlay_modal_open, (), {
	return Module.nqOverlayModalOpen ? 1 : 0;
});

EM_JS(int, js_touch_active, (), {
	return Module.nqTouchActive ? 1 : 0;
});

EM_JS(int, js_touch_drag_active, (), {
	return Module.nqTouchDragActive ? 1 : 0;
});

EM_JS(int, js_touch_controls_visible, (), {
	return Module.nqTouchControlsVisible ? 1 : 0;
});

EM_JS(void, js_set_touch_menu_mode, (int active), {
	Module.nqTouchMenuMode = !!active;
});

EM_JS(void, js_set_touch_flip, (int active), {
	Module.nqTouchFlip = !!active;
});

// Virtual joystick visual — positioned from C, rendered as two DOM elements
EM_JS(void, js_joy_show, (float cx, float cy), {
	var r = document.getElementById('nq-joy-ring');
	var k = document.getElementById('nq-joy-knob');
	var rw, rh, kw, kh;
	if (!r || !k) return;
	rw = r.offsetWidth || 120;
	rh = r.offsetHeight || rw;
	kw = k.offsetWidth || 60;
	kh = k.offsetHeight || kw;
	r.style.display = 'block';
	k.style.display = 'block';
	r.style.left = (cx - rw * 0.5) + 'px';
	r.style.top  = (cy - rh * 0.5) + 'px';
	k.style.left = (cx - kw * 0.5) + 'px';
	k.style.top  = (cy - kh * 0.5) + 'px';
});

EM_JS(void, js_joy_move, (float kx, float ky), {
	var k = document.getElementById('nq-joy-knob');
	var kw, kh;
	if (!k) return;
	kw = k.offsetWidth || 60;
	kh = k.offsetHeight || kw;
	k.style.left = (kx - kw * 0.5) + 'px';
	k.style.top = (ky - kh * 0.5) + 'px';
});

EM_JS(void, js_joy_hide, (), {
	var r = document.getElementById('nq-joy-ring');
	var k = document.getElementById('nq-joy-knob');
	if (r) r.style.display = 'none';
	if (k) k.style.display = 'none';
});

// Hit-test the action-button list. Returns element index, or -1.
EM_JS(int, js_hit_test_buttons, (float x, float y), {
	var ids = Module.nqTouchButtonElementIds || [];
	var i, el, rect;
	for (i = 0; i < ids.length; i++) {
		el = document.getElementById(ids[i]);
		if (!el || !el.getClientRects || el.getClientRects().length === 0) continue;
		rect = el.getBoundingClientRect();
		if (x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom)
			return i;
	}
	return -1;
});

// Add or remove the 'active' CSS class on an action button element.
EM_JS(void, js_highlight_button, (int index, int active), {
	var ids = Module.nqTouchButtonElementIds || [];
	if (index < 0 || index >= ids.length) return;
	var el = document.getElementById(ids[index]);
	if (!el) return;
	if (active) el.classList.add('active');
	else el.classList.remove('active');
});

EM_JS(int, js_touch_zone_for_point, (float x, float split, int flip), {
	var canvas = document.getElementById('canvas');
	var localX;
	if (!canvas) return 1;
	var rect = canvas.getBoundingClientRect();
	localX = x - rect.left;
	if (flip)
		localX = rect.width - localX;
	return (localX > rect.width * split) ? 1 : 0;
});

EM_JS(float, js_touch_look_zone_width, (float split), {
	var canvas = document.getElementById('canvas');
	if (!canvas) return 0;
	var rect = canvas.getBoundingClientRect();
	return rect.width * (1.0 - split);
});

// Notify JS that the C touch layer is active (for overlay visibility sync)
EM_JS(void, js_set_touch_active, (int active), {
	Module.nqTouchActive = !!active;
});

// Request fullscreen + landscape lock (one-shot, called on first touch)
EM_JS(void, js_request_fullscreen, (), {
	if (Module && typeof Module.nqRequestFullscreen === 'function') {
		try {
			var request = Module.nqRequestFullscreen();
			if (request && request.catch) request.catch(function(){});
			return;
		} catch (e0) {}
	}
	if (document.fullscreenElement || document.webkitFullscreenElement) return;
	try {
		var el = document.documentElement;
		var rfs = el.requestFullscreen || el.webkitRequestFullscreen;
		if (rfs) rfs.call(el).catch(function(){});
		var orient = screen.orientation;
		if (orient && orient.lock) orient.lock('landscape').catch(function(){});
	} catch(e) {}
});

EM_JS(int, js_touch_on_overlay_ui, (float x, float y), {
	var el = document.elementFromPoint(x, y);
	return (el && el.closest('#nq-overlay-toggle, #nq-overlay-panel, #nq-editor, #nq-touch-menu-m2')) ? 1 : 0;
});

// ---------------------------------------------------------------------------
// Keyboard callbacks
// ---------------------------------------------------------------------------
static EM_BOOL on_key(int type, const EmscriptenKeyboardEvent *e, void *ud)
{
	int k;

	if (e->keyCode == 27 && emscripten_get_now() - ptrlock_lost_at < 50)
		return 1;

	k = e->keyCode < 256 ? keymap[e->keyCode] : 0;
	if (!k)
		return 0;

	Key_Event(k, type == EMSCRIPTEN_EVENT_KEYDOWN);
	return e->keyCode != 116 && e->keyCode != 123; // let F5/F12 through
}

// ---------------------------------------------------------------------------
// Mouse callbacks
// ---------------------------------------------------------------------------
static EM_BOOL on_mouse_move(int t, const EmscriptenMouseEvent *e, void *ud)
{
	if (js_overlay_modal_open() || !mouse_avail || !pointer_locked)
		return 0;

	mouse_x += e->movementX;
	mouse_y += e->movementY;
	return 1;
}

static EM_BOOL on_mouse_btn(int type, const EmscriptenMouseEvent *e, void *ud)
{
	static const int bmap[] = { K_MOUSE1, K_MOUSE3, K_MOUSE2 };
	qboolean down;

	if (js_overlay_modal_open() || !mouse_avail)
		return 0;

	down = (type == EMSCRIPTEN_EVENT_MOUSEDOWN);
	if (down && !pointer_locked && !touch_active)
	{
		js_request_pointerlock();
		if (key_dest == key_menu)
		{
			m_state = 0;
			key_dest = key_game;
			return 1;
		}
	}

	if (e->button > 2)
		return 0;

	Key_Event(bmap[e->button], down);
	return 1;
}

static void emit_wheel_key(int dir)
{
	int key;

	key = (dir < 0) ? K_MWHEELUP : K_MWHEELDOWN;
	Key_Event(key, 1);
	Key_Event(key, 0);
}

static EM_BOOL on_wheel(int t, const EmscriptenWheelEvent *e, void *ud)
{
	static float accum;
	static int accum_dir;
	static double last_emit_ms;
	static int last_emit_dir;
	const float pixel_notch_cutoff = 35.0f;
	const double notch_cooldown_ms = 30.0;
	const float pixel_tick = 60.0f;
	float dy;
	int dir;
	double now_ms;
	qboolean large_pixel_step;
	qboolean non_pixel_mode;

	if (js_overlay_modal_open() || !mouse_avail) return 0;

	dy = (float)e->deltaY;
	if (dy == 0.0f) return 1;
	dir = (dy < 0.0f) ? -1 : 1;
	now_ms = emscripten_get_now();
	large_pixel_step = (dy <= -pixel_notch_cutoff || dy >= pixel_notch_cutoff);
	non_pixel_mode = (e->deltaMode != DOM_DELTA_PIXEL);

	if (non_pixel_mode || large_pixel_step)
	{
		if (large_pixel_step && dir == last_emit_dir && (now_ms - last_emit_ms) < notch_cooldown_ms)
			return 1;
		emit_wheel_key(dir);
		last_emit_ms = now_ms;
		last_emit_dir = dir;
		accum = 0.0f;
		accum_dir = 0;
		return 1;
	}

	if (accum_dir && dir != accum_dir)
		accum = 0.0f;
	accum_dir = dir;
	accum += dy / pixel_tick;

	while (accum <= -1.0f)
	{
		emit_wheel_key(-1);
		accum += 1.0f;
	}
	while (accum >= 1.0f)
	{
		emit_wheel_key(1);
		accum -= 1.0f;
	}

	return 1;
}

// ---------------------------------------------------------------------------
// Pointer lock / visibility callbacks
// ---------------------------------------------------------------------------
static EM_BOOL on_ptrlock(int t, const EmscriptenPointerlockChangeEvent *e, void *ud)
{
	pointer_locked = e->isActive;
	if (!e->isActive)
	{
		ptrlock_lost_at = emscripten_get_now();
		if (!touch_active && key_dest == key_game)
		{
			Key_Event(K_ESCAPE, 1);
			Key_Event(K_ESCAPE, 0);
		}
	}
	return 1;
}

static EM_BOOL on_visibility(int t, const EmscriptenVisibilityChangeEvent *e, void *ud)
{
	if (e->hidden)
	{
		mouse_x = mouse_y = 0;
		SNDDMA_Pause();
		emscripten_set_main_loop_timing(EM_TIMING_SETTIMEOUT, 100);
	}
	else
	{
		EmscriptenPointerlockChangeEvent pe;
		emscripten_set_main_loop_timing(EM_TIMING_RAF, 0);
		SNDDMA_Resume();
		if (emscripten_get_pointerlock_status(&pe) == EMSCRIPTEN_RESULT_SUCCESS)
			pointer_locked = pe.isActive;
	}
	return 1;
}

// ---------------------------------------------------------------------------
// Touch helpers
// ---------------------------------------------------------------------------
static int touch_find_look_point(long identifier)
{
	int i;

	for (i = 0; i < MAX_TOUCH_POINTS; i++)
		if (touch_look_points[i].active && touch_look_points[i].identifier == identifier)
			return i;
	return -1;
}

static qboolean touch_add_look_point(long identifier, float px, float py)
{
	int j;

	if (touch_find_look_point(identifier) >= 0)
		return true;

	for (j = 0; j < MAX_TOUCH_POINTS; j++)
	{
		if (touch_look_points[j].active)
			continue;
		touch_look_points[j].active = true;
		touch_look_points[j].identifier = identifier;
		touch_look_points[j].lastX = px;
		touch_look_points[j].lastY = py;
		touch_look_points[j].startX = px;
		touch_look_points[j].startY = py;
		touch_look_points[j].startMs = emscripten_get_now();
		return true;
	}

	return false;
}

static void touch_move_axes_for_point(float px, float py, float *nx, float *ny)
{
	float dx, dy, dist;

	dx = px - touch_move.originX;
	dy = py - touch_move.originY;
	dist = sqrtf(dx * dx + dy * dy);
	if (dist < TOUCH_MOVE_RADIUS)
		dist = TOUCH_MOVE_RADIUS;
	*nx = dx / dist;
	*ny = dy / dist;
}

static void touch_clear_move(void)
{
	touch_move.active = false;
	touch_move.identifier = -1;
	touch_move.startX = touch_move.startY = 0.0f;
	touch_move.startMs = 0.0;
	touch_move.axisX = touch_move.axisY = 0.0f;
	js_joy_hide();
}

static int touch_find_button(long identifier)
{
	int slot;

	for (slot = 0; slot < TOUCH_BUTTON_COUNT; slot++)
		if (touch_buttons[slot].active && touch_buttons[slot].identifier == identifier)
			return slot;
	return -1;
}

static void touch_release_buttons(void)
{
	int slot;

	for (slot = 0; slot < TOUCH_BUTTON_COUNT; slot++)
	{
		if (!touch_buttons[slot].active)
			continue;
		touch_buttons[slot].active = false;
		emit_virtual_control(CTRL_TOUCH_SLOT_BASE + slot, touch_button_gameplay_key(slot), 0, false);
		js_highlight_button(slot, 0);
	}
}

static void touch_cancel_all(void)
{
	touch_release_buttons();
	touch_clear_move();
	memset(touch_look_points, 0, sizeof(touch_look_points));
}

static qboolean touch_event_blocked(void)
{
	if (!js_touch_controls_visible())
	{
		touch_cancel_all();
		return true;
	}

	if (js_overlay_modal_open())
		return true;

	if (!js_touch_drag_active())
		return false;

	touch_cancel_all();
	return true;
}

static float touch_look_units_per_pixel(void)
{
	float w = js_touch_look_zone_width(TOUCH_MOVE_ZONE_SPLIT);
	if (w <= 1.0f)
		return 0.0f;
	return TOUCH_TARGET_SWIPE_TURN / (w * TOUCH_TARGET_SWIPE_FRAC * DEFAULT_SENSITIVITY * DEFAULT_M_YAW);
}

static qboolean touch_is_tap(float startX, float startY, double startMs, float endX, float endY, double max_ms, float max_px)
{
	float dx, dy, max_dist_sq;

	if (emscripten_get_now() - startMs > max_ms)
		return false;
	dx = endX - startX;
	dy = endY - startY;
	max_dist_sq = max_px * max_px;
	return (dx * dx + dy * dy) <= max_dist_sq;
}

static void touch_try_zone_tap(int zone, float startX, float startY, double startMs, float endX, float endY)
{
	double tap_ms;
	float tap_px;
	int key;

	if (zone != 0 && zone != 1)
		return;

	tap_ms = touch_tap_ms.value;
	tap_px = touch_tap_px.value;
	if (tap_ms < 1.0)
		tap_ms = 1.0;
	if (tap_px < 1.0f)
		tap_px = 1.0f;

	if (!touch_is_tap(startX, startY, startMs, endX, endY, tap_ms, tap_px))
		return;

	if (key_dest == key_menu)
		key = (zone == 0) ? K_ESCAPE : menu_accept_key();
	else
		key = (zone == 0) ? K_TOUCH_TAP1 : K_TOUCH_TAP2;

	Key_Event(key, 1);
	Key_Event(key, 0);
}

static float touch_zone_split(qboolean in_menu)
{
	return in_menu ? TOUCH_MENU_ZONE_SPLIT : TOUCH_MOVE_ZONE_SPLIT;
}

static int touch_zone_for_point(float x, float split)
{
	return js_touch_zone_for_point(x, split, touch_flip.value != 0.0f);
}

static void touch_sync_flip_mode(void)
{
	qboolean flip = (touch_flip.value != 0.0f);

	if (flip == touch_flip_latched)
		return;
	touch_flip_latched = flip;
	js_set_touch_flip(flip);
}

// ---------------------------------------------------------------------------
// Touch callbacks (C-native via Emscripten touch API)
// ---------------------------------------------------------------------------
static EM_BOOL on_touchstart(int type, const EmscriptenTouchEvent *e, void *ud)
{
	int i;
	qboolean handled;
	qboolean in_menu;
	float zone_split;

	if (touch_event_blocked())
		return 0;

	handled = false;
	in_menu = (key_dest == key_menu);
	zone_split = touch_zone_split(in_menu);

	if (!fullscreen_requested)
	{
		fullscreen_requested = true;
		js_request_fullscreen();
	}

	for (i = 0; i < e->numTouches; i++)
	{
		const EmscriptenTouchPoint *tp;
		float px, py;
		int slot;
		int zone;

		tp = &e->touches[i];
		if (!tp->isChanged)
			continue;

		px = (float)tp->clientX;
		py = (float)tp->clientY;

		if (js_touch_on_overlay_ui(px, py))
			continue;

		zone = touch_zone_for_point(px, zone_split);
		slot = js_hit_test_buttons(px, py);
		if (slot >= 0 && slot < TOUCH_BUTTON_COUNT && !touch_buttons[slot].active)
		{
			if (in_menu && slot < TOUCH_SLOT_COUNT)
				continue;
			touch_buttons[slot].active = true;
			touch_buttons[slot].identifier = tp->identifier;
			js_highlight_button(slot, 1);
			if (slot == TOUCH_MENU_BACK_BUTTON)
				Cbuf_AddText("togglemenu\n");
			else
				emit_virtual_control(CTRL_TOUCH_SLOT_BASE + slot, touch_button_gameplay_key(slot), 0, true);
			if (slot < TOUCH_SLOT_COUNT && !in_menu && zone == 1)
				touch_add_look_point(tp->identifier, px, py);
			handled = true;
			continue;
		}

		if (zone == 0 && !touch_move.active)
		{
			touch_move.active = true;
			touch_move.identifier = tp->identifier;
			touch_move.originX = px;
			touch_move.originY = py;
			touch_move.startX = px;
			touch_move.startY = py;
			touch_move.startMs = emscripten_get_now();
			touch_move.axisX = touch_move.axisY = 0.0f;
			js_joy_show(touch_move.originX, touch_move.originY);
			handled = true;
			continue;
		}

		if (zone == 1 && touch_add_look_point(tp->identifier, px, py))
			handled = true;
	}

	return handled ? 1 : 0;
}

static EM_BOOL on_touchmove(int type, const EmscriptenTouchEvent *e, void *ud)
{
	int i;
	float look_scale;
	qboolean handled;
	qboolean in_menu;

	if (touch_event_blocked())
		return 0;

	look_scale = touch_look_units_per_pixel();
	handled = false;
	in_menu = (key_dest == key_menu);

	for (i = 0; i < e->numTouches; i++)
	{
		const EmscriptenTouchPoint *tp;
		float px, py;
		int look_slot;

		tp = &e->touches[i];
		if (!tp->isChanged)
			continue;

		px = (float)tp->clientX;
		py = (float)tp->clientY;

		if (touch_move.active && tp->identifier == touch_move.identifier)
		{
			touch_move_axes_for_point(px, py, &touch_move.axisX, &touch_move.axisY);
			js_joy_move(touch_move.originX + touch_move.axisX * TOUCH_MOVE_RADIUS, touch_move.originY + touch_move.axisY * TOUCH_MOVE_RADIUS);
			handled = true;
			continue;
		}

		look_slot = touch_find_look_point(tp->identifier);
		if (look_slot < 0)
			continue;
		if (in_menu)
		{
			touch_look_points[look_slot].lastX = px;
			touch_look_points[look_slot].lastY = py;
			handled = true;
			continue;
		}

		touch_look_x += (px - touch_look_points[look_slot].lastX) * look_scale;
		touch_look_y += (py - touch_look_points[look_slot].lastY) * look_scale;
		touch_look_points[look_slot].lastX = px;
		touch_look_points[look_slot].lastY = py;
		handled = true;
	}

	return handled ? 1 : 0;
}

static EM_BOOL on_touchend(int type, const EmscriptenTouchEvent *e, void *ud)
{
	int i;
	qboolean handled;
	float zone_split;

	if (js_touch_drag_active())
	{
		touch_cancel_all();
		return 0;
	}

	handled = false;
	zone_split = touch_zone_split(key_dest == key_menu);

	for (i = 0; i < e->numTouches; i++)
	{
		const EmscriptenTouchPoint *tp;
		float px, py;
		int look_slot;
		int slot;

		tp = &e->touches[i];
		if (!tp->isChanged)
			continue;

		px = (float)tp->clientX;
		py = (float)tp->clientY;

		slot = touch_find_button(tp->identifier);
		if (slot >= 0)
		{
			touch_buttons[slot].active = false;
			js_highlight_button(slot, 0);
			emit_virtual_control(CTRL_TOUCH_SLOT_BASE + slot, touch_button_gameplay_key(slot), 0, false);
			look_slot = touch_find_look_point(tp->identifier);
			if (look_slot >= 0)
				touch_look_points[look_slot].active = false;
			handled = true;
			continue;
		}

		if (touch_move.active && tp->identifier == touch_move.identifier)
		{
			touch_try_zone_tap(0, touch_move.startX, touch_move.startY, touch_move.startMs, px, py);
			touch_clear_move();
			handled = true;
			continue;
		}

		look_slot = touch_find_look_point(tp->identifier);
		if (look_slot < 0)
			continue;

		touch_try_zone_tap(touch_zone_for_point(touch_look_points[look_slot].startX, zone_split),
			touch_look_points[look_slot].startX, touch_look_points[look_slot].startY,
			touch_look_points[look_slot].startMs, px, py);
		touch_look_points[look_slot].active = false;
		handled = true;
	}

	return handled ? 1 : 0;
}

// ---------------------------------------------------------------------------
// Gamepad polling (C-native via Emscripten Gamepad API)
// ---------------------------------------------------------------------------
static float joy_apply_deadzone(float val)
{
	float av = fabsf(val);
	if (av < JOY_DEAD_ZONE)
		return 0.0f;
	return (val < 0.0f ? -1.0f : 1.0f) * (av - JOY_DEAD_ZONE) / (1.0f - JOY_DEAD_ZONE);
}

static void joy_handle_button(int index, qboolean pressed)
{
	if (index < 0 || index >= JOY_MAX_BUTTONS)
		return;
	if (pressed == joy_button_state[index])
		return;

	joy_button_state[index] = pressed;
	emit_virtual_control(CTRL_JOY_BTN_BASE + index, s_joy_key_map[index], 0, pressed);
}

static void joy_disconnect_cleanup(void)
{
	int b;

	for (b = 0; b < JOY_MAX_BUTTONS; b++)
	{
		if (!joy_button_state[b])
			continue;
		joy_button_state[b] = false;
		emit_virtual_control(CTRL_JOY_BTN_BASE + b, s_joy_key_map[b], 0, false);
	}

	joy_move_x = joy_move_y = 0.0f;
	update_menu_nav(CTRL_JOY_NAV_BASE, JOY_MENU_NAV_THRESH, 0.0f, 0.0f);
}

static void IN_PollGamepads(void)
{
	EmscriptenGamepadEvent gp;
	int i, b, num;

	num = emscripten_get_num_gamepads();
	for (i = 0; i < num; i++)
	{
		if (emscripten_get_gamepad_status(i, &gp) != EMSCRIPTEN_RESULT_SUCCESS || !gp.connected)
			continue;

		if (!touch_active)
		{
			touch_active = true;
			js_set_touch_active(1);
		}

		for (b = 0; b < gp.numButtons && b < JOY_MAX_BUTTONS; b++)
		{
			if (b == JOY_BTN_LT || b == JOY_BTN_RT)
				joy_handle_button(b, gp.analogButton[b] > JOY_TRIGGER_THRESH);
			else
				joy_handle_button(b, gp.digitalButton[b]);
		}

		if (gp.numAxes >= 2)
		{
			joy_move_x = joy_apply_deadzone(gp.axis[0]);
			joy_move_y = joy_apply_deadzone(gp.axis[1]);
		}
		else
		{
			joy_move_x = joy_move_y = 0.0f;
		}
		update_menu_nav(CTRL_JOY_NAV_BASE, JOY_MENU_NAV_THRESH, joy_move_x, joy_move_y);

		if (gp.numAxes >= 4)
		{
			float rx, ry;
			rx = joy_apply_deadzone(gp.axis[2]);
			ry = joy_apply_deadzone(gp.axis[3]);
			if (rx != 0.0f || ry != 0.0f)
			{
				joy_look_x += rx * JOY_LOOK_UNITS_PER_SECOND * (float)host_frametime;
				joy_look_y += ry * JOY_LOOK_UNITS_PER_SECOND * (float)host_frametime;
			}
		}

		joy_connected = true;
		return; // only use first connected gamepad
	}

	if (joy_connected)
		joy_disconnect_cleanup();
	joy_connected = false;
}

#define INPUT_PROFILE_PICK(mouse, touch, joy) (joy_connected ? (joy) : (touch_active ? (touch) : (mouse)))

static void IN_ApplyAnalogLook(usercmd_t *cmd, float x, float y, cvar_t *sens, cvar_t *strafe, cvar_t *invert)
{
	float pitch = fabsf(m_pitch.value);

	if (x == 0.0f && y == 0.0f)
		return;

	x *= sens->value;
	y *= sens->value;

	if ((in_strafe.state & 1) || strafe->value)
		cmd->sidemove += m_side.value * x;
	else
		cl.viewangles[YAW] -= m_yaw.value * x;

	V_StopPitchDrift();
	cl.viewangles[PITCH] += (invert->value != 0.0f ? -pitch : pitch) * y;
	CLAMP_PITCH(cl.viewangles[PITCH]);
}

// ---------------------------------------------------------------------------
// Input-profile cvar/label dispatch (used by options menu)
// ---------------------------------------------------------------------------
cvar_t *IN_SensitivityCvar(void)
{
	return INPUT_PROFILE_PICK(&sensitivity, &touch_sensitivity, &joy_sensitivity);
}

char *IN_SensitivityLabel(void)
{
	return INPUT_PROFILE_PICK("           Mouse Speed", "           Touch Speed", "       Joystick Speed");
}

cvar_t *IN_LookspringCvar(void)
{
	return INPUT_PROFILE_PICK(&lookspring, &touch_lookspring, &joy_lookspring);
}

char *IN_LookspringLabel(void)
{
	return INPUT_PROFILE_PICK("            Lookspring", "        Touch Lookspring", "     Joystick Lookspring");
}

cvar_t *IN_LookstrafeCvar(void)
{
	return INPUT_PROFILE_PICK(&lookstrafe, &touch_lookstrafe, &joy_lookstrafe);
}

char *IN_LookstrafeLabel(void)
{
	return INPUT_PROFILE_PICK("            Lookstrafe", "        Touch Lookstrafe", "     Joystick Lookstrafe");
}

qboolean IN_InvertPitchEnabled(void)
{
	cvar_t *invert = INPUT_PROFILE_PICK(NULL, &touch_invertpitch, &joy_invertpitch);
	return invert ? invert->value != 0.0f : m_pitch.value < 0.0f;
}

void IN_ToggleInvertPitch(void)
{
	cvar_t *invert = INPUT_PROFILE_PICK(NULL, &touch_invertpitch, &joy_invertpitch);

	if (invert)
		Cvar_SetValue (invert->name, !invert->value);
	else
		Cvar_SetValue ("m_pitch", -m_pitch.value);
}

char *IN_InvertPitchLabel(void)
{
	return INPUT_PROFILE_PICK("          Invert Mouse", "          Invert Touch", "       Invert Joystick");
}

// ---------------------------------------------------------------------------
// Init / register callbacks
// ---------------------------------------------------------------------------
static void init_input(void)
{
	touch_active = js_touch_active();
	menu_mode_latched = (key_dest == key_menu);
	touch_flip_latched = false;
	js_set_touch_menu_mode(menu_mode_latched);
	touch_sync_flip_mode();

	// Keyboard
	emscripten_set_keydown_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_key);
	emscripten_set_keyup_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_key);

	// Mouse
	emscripten_set_mousemove_callback("#canvas", 0, 1, on_mouse_move);
	emscripten_set_mousedown_callback("#canvas", 0, 1, on_mouse_btn);
	emscripten_set_mouseup_callback("#canvas", 0, 1, on_mouse_btn);
	emscripten_set_wheel_callback("#canvas", 0, 1, on_wheel);

	// Pointer lock & visibility
	emscripten_set_pointerlockchange_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_ptrlock);
	emscripten_set_visibilitychange_callback(0, 1, on_visibility);

	// Touch (C-native)
	emscripten_set_touchstart_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_touchstart);
	emscripten_set_touchmove_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_touchmove);
	emscripten_set_touchend_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_touchend);
	emscripten_set_touchcancel_callback(EMSCRIPTEN_EVENT_TARGET_DOCUMENT, 0, 1, on_touchend);

	// Gamepad: register sample callback so emscripten_get_gamepad_status works
	emscripten_sample_gamepad_data();
}

// ---------------------------------------------------------------------------
// Standard Quake IN_* interface
// ---------------------------------------------------------------------------
void Sys_SendKeyEvents(void)
{
	emscripten_sleep(0);
}

void IN_Init(void)
{
	if (!COM_CheckParm("-nomouse"))
		mouse_avail = 1;

	Cvar_RegisterVariable(&analog_speed);
	Cvar_RegisterVariable(&touch_flip);
	Cvar_RegisterVariable(&touch_tap_ms);
	Cvar_RegisterVariable(&touch_tap_px);
	Cvar_RegisterVariable(&joy_sensitivity);
	Cvar_RegisterVariable(&touch_sensitivity);
	Cvar_RegisterVariable(&joy_invertpitch);
	Cvar_RegisterVariable(&touch_invertpitch);
	Cvar_RegisterVariable(&joy_lookspring);
	Cvar_RegisterVariable(&touch_lookspring);
	Cvar_RegisterVariable(&joy_lookstrafe);
	Cvar_RegisterVariable(&touch_lookstrafe);

	init_input();
}

void IN_Shutdown(void)
{
	mouse_avail = 0;
	touch_cancel_all();
	joy_disconnect_cleanup();
	release_menu_virtual_controls();
	js_set_touch_menu_mode(0);
	js_set_touch_flip(0);
}

void IN_Commands(void)
{
	sync_menu_mode_transition();
	touch_sync_flip_mode();
	emscripten_sample_gamepad_data();
	IN_PollGamepads();
	// Touch joystick drives menu nav when no gamepad is connected.
	// joy_connected is authoritative after IN_PollGamepads runs.
	if (!joy_connected)
		update_menu_nav(CTRL_JOY_NAV_BASE, JOY_MENU_NAV_THRESH, touch_move.axisX, touch_move.axisY);
}

void IN_Move(usercmd_t *cmd)
{
	float mx, my;
	float jx, jy, tx, ty;

	jx = joy_look_x;   jy = joy_look_y;   joy_look_x   = joy_look_y   = 0;
	tx = touch_look_x; ty = touch_look_y; touch_look_x = touch_look_y = 0;
	mx = my = 0.0f;
	if (mouse_avail)
	{
		mx = mouse_x;
		my = mouse_y;
	}

	IN_ApplyAnalogLook(cmd, jx, jy, &joy_sensitivity, &joy_lookstrafe, &joy_invertpitch);
	IN_ApplyAnalogLook(cmd, tx, ty, &touch_sensitivity, &touch_lookstrafe, &touch_invertpitch);

	if (mx != 0.0f || my != 0.0f)
	{
		mx *= sensitivity.value;
		my *= sensitivity.value;

		if ((in_strafe.state & 1) || lookstrafe.value)
			cmd->sidemove += m_side.value * mx;
		else
			cl.viewangles[YAW] -= m_yaw.value * mx;

		V_StopPitchDrift();
		if (!(in_strafe.state & 1))
		{
			cl.viewangles[PITCH] += m_pitch.value * my;
			CLAMP_PITCH(cl.viewangles[PITCH]);
		}
		else
		{
			if (noclip_anglehack)
				cmd->upmove -= m_forward.value * my;
			else
				cmd->forwardmove -= m_forward.value * my;
		}
	}

	if (mx == 0.0f && my == 0.0f && jx == 0.0f && jy == 0.0f && tx == 0.0f && ty == 0.0f &&
		key_dest == key_game && IN_LookspringCvar()->value == 0.0f)
	{
		// Match WinQuake behavior: no look input should not re-center unless lookspring is enabled.
		V_StopPitchDrift();
	}

	if (key_dest != key_menu)
	{
		float spd = analog_speed.value;
		cmd->forwardmove -= (touch_move.axisY + joy_move_y) * spd;
		cmd->sidemove    += (touch_move.axisX + joy_move_x) * spd;
	}

	mouse_x = mouse_y = 0;
}
