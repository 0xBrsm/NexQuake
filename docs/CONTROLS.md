# Controls

NexQuake supports three input methods: mouse + keyboard, touch (mobile), and gamepad. The active input method is detected automatically and the in-game Options menu updates its labels to match.

## Mouse and Keyboard

Standard Quake mouse and keyboard input. Pointer lock is acquired on click and released when the settings overlay is opened. Mouse sensitivity is set via the global `sensitivity` cvar (also `m_yaw`, `m_pitch`).

## Touch Input

Touch controls activate automatically on coarse-pointer (touch) screens with no setup required.

### Layout

| Zone / Element | Location | Purpose |
|----------------|----------|---------|
| Joystick zone | Left 40% of the screen | Virtual thumbstick — drag from any starting point to move. Re-centers on release. |
| Look zone | Right 60% of the screen | Swipe to turn and look. |
| Touch slots 1–9 | Edge buttons (configurable positions) | Bindable action buttons. |
| Back button (`Q`) | Top-left (fixed) | Tap: menu back / escape. Long-press (400 ms): open console. |
| Gear button | Top-right (fixed) | Opens the [settings panel](UI.md). |

Slots default to common actions (fire, jump, weapon cycle, team chat, rocket launcher, and lightning gun). Each button shows an SVG glyph matching its bound command. The overlay auto-hides after 2.5 seconds of inactivity and reappears on the next touch.

### Touch Text Entry

When Quake enters a text mode on touch (console input, `say`/`messagemode`, or a menu text field), a text bar appears at the top:

- Tap the bar to focus and use the mobile keyboard; press enter/send to submit to the game.
- Tap outside the bar to hide it. `Esc`/back also dismisses it (and cancels message entry).
- Console text stays synced with the engine buffer while the bar is focused.

### Flip Mode

`touch_flip 1` mirrors the layout horizontally for left-handed play — the joystick moves to the right side and the look zone to the left. Set it in the console or `config.cfg`.

### Repositioning Slots

Touch slots can be dragged to any position on the screen. Layout is saved to `localStorage` (`nexquake.touch.layout.v1`) and restored on reload. To reset to the default, clear `localStorage` or delete the key.

### Touch Buttons

Nine slots (`touch1`–`touch9`) can be rebound with the standard `bind` command:

```
bind touch1 +attack
bind touch2 +jump
bind touch3 impulse 10
```

Default bindings:

| Slot | Default | Action |
|------|---------|--------|
| `touch1` | `+jump` | Jump |
| `touch2` | `impulse 12` | Previous weapon |
| `touch3` | `+attack` | Fire |
| `touch4` | `impulse 10` | Next weapon |
| `touch5` | `messagemode2` | Team chat |
| `touch6` | — | Unbound |
| `touch7` | — | Unbound |
| `touch8` | `impulse 7` | Rocket launcher |
| `touch9` | `impulse 8` | Lightning gun |

### Touch Tap Zones

Quick taps (within `touch_tap_ms` ms and `touch_tap_px` px of movement) on the left zone fire `touch_tap1` and on the right zone fire `touch_tap2`. These are bindable the same way:

```
bind touch_tap1 +attack
bind touch_tap2 +jump
```

### Touch Cvars

| Cvar | Default | Description |
|------|---------|-------------|
| `touch_sensitivity` | `3` | Look sensitivity for swipe-look. Equivalent of `sensitivity` for touch. Archived (saved in `config.cfg`). |
| `touch_lookspring` | `0` | If `1`, look returns to center when swipe is released. |
| `touch_lookstrafe` | `0` | If `1`, horizontal swipe strafes instead of turning. |
| `touch_invertpitch` | `0` | If `1`, inverts vertical look direction for swipe. |
| `touch_flip` | `0` | If `1`, mirrors the touch layout horizontally (left-handed mode). |
| `touch_tap_ms` | `220` | Maximum contact duration in milliseconds for a look-zone touch to register as a tap (`touch_tap1`/`touch_tap2`) rather than a swipe. |
| `touch_tap_px` | `40` | Maximum finger movement in pixels for a look-zone touch to register as a tap rather than a swipe. |

## Gamepad Input

Gamepad input uses the browser Gamepad API with the W3C standard mapping. Connecting a gamepad is enough — no browser extension is needed on modern browsers. The right stick controls look; the left stick moves. Buttons are dispatched as Quake key events and can be rebound with `bind`.

Gamepad input is detected when any gamepad axis or button is used. Once detected, Options menu labels switch to gamepad equivalents.

### Gamepad Key Names

Use these names in `bind` commands:

| Key name | Standard controller button |
|----------|---------------------------|
| `joy_a` | A (bottom face button) |
| `joy_b` | B (right face button) |
| `joy_x` | X (left face button) |
| `joy_y` | Y (top face button) |
| `joy_lb` | Left bumper (LB / L1) |
| `joy_rb` | Right bumper (RB / R1) |
| `joy_lt` | Left trigger (LT / L2) |
| `joy_rt` | Right trigger (RT / R2) |
| `joy_back` | Back / Select / View |
| `joy_start` | Start / Menu |
| `joy_dpad_up` | D-pad up |
| `joy_dpad_down` | D-pad down |
| `joy_dpad_left` | D-pad left |
| `joy_dpad_right` | D-pad right |
| `joy_ls` | Left stick click (L3) |
| `joy_rs` | Right stick click (R3) |

Example bindings:

```
bind joy_a +jump
bind joy_rt +attack
bind joy_lb impulse 12
bind joy_rb impulse 10
bind joy_start togglemenu
```

### Gamepad Cvars

| Cvar | Default | Description |
|------|---------|-------------|
| `joy_sensitivity` | `3` | Look sensitivity for right-stick look. Archived. |
| `joy_lookspring` | `0` | If `1`, look returns to center when the stick is released. |
| `joy_lookstrafe` | `0` | If `1`, right stick X axis strafes instead of turning. |
| `joy_invertpitch` | `0` | If `1`, inverts vertical look direction for stick. |

### Shared Analog Cvar

| Cvar | Default | Description |
|------|---------|-------------|
| `analog_speed` | `400` | Maximum look turn rate in degrees per second for both touch and gamepad analog inputs. |

## Video Modes

`vid_mode` selects the render resolution. Set it in the console or Options → Video Modes menu.

```
vid_mode 0
```

The mode list is split into two sections:

**Classic Modes** — fixed 4:3 aspect ratio, independent of window size:

| `vid_mode` | Resolution | Notes |
|-----------|------------|-------|
| `0` | 320×240 | Low-res Quake look. |
| `1` | 640×480 | Default. Classic res. |
| `2` | 1280×960 | Full-resolution 4:3. |

**Fullscreen Modes** — match the current browser viewport aspect ratio, scaled at 25%, 50%, and 100% of the maximum render dimension. Exact resolutions depend on the viewport at startup.

FOV scales automatically when switching modes to preserve the vertical field of view. Switching from 4:3 to a widescreen mode widens the horizontal view; the vertical play area stays the same.

A widescreen warning appears in the Video Modes menu when the selected mode hides the weapon model (standard Quake behavior for FOV above 90).

## All New Cvars at a Glance

| Cvar | Default | Archived | Category |
|------|---------|----------|---------|
| `touch_sensitivity` | `3` | yes | Touch look |
| `touch_lookspring` | `0` | yes | Touch look |
| `touch_lookstrafe` | `0` | yes | Touch look |
| `touch_invertpitch` | `0` | yes | Touch look |
| `touch_flip` | `0` | no | Touch layout |
| `touch_tap_ms` | `220` | no | Touch tap |
| `touch_tap_px` | `20` | no | Touch tap |
| `joy_sensitivity` | `3` | yes | Gamepad look |
| `joy_lookspring` | `0` | yes | Gamepad look |
| `joy_lookstrafe` | `0` | yes | Gamepad look |
| `joy_invertpitch` | `0` | yes | Gamepad look |
| `analog_speed` | `400` | no | Analog shared |
| `vid_mode` | `0` | yes | Video |

Archived cvars are written to `config.cfg` automatically and restored on next load. Non-archived cvars reset to their defaults on each startup unless set explicitly in `autoexec.cfg`.
