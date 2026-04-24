# User Interface

This guide explains how to use the NexQuake shell overlay (the gear button in the top-right corner) for local files, config editing, and CD audio controls.

## Open and Close

**Mouse / keyboard:** Click the gear icon (`NexQuake Settings`) in the top-right corner. Close the overlay by clicking the gear again, pressing `Esc`, or clicking back into the game canvas. When the overlay opens, pointer lock is released so you can use the mouse normally.

**Touch devices:** The gear button is fixed in the top-right corner of the screen (always visible). Tap it to open or close the settings panel. See [Controls](CONTROLS.md#touch-input) for the full touch layout and button configuration.

**Touch text entry:** When Quake asks for text (console, chat, or a menu field), a text bar appears at the top on touch devices. Tap it to focus, type on the system keyboard, and press enter/send to submit. Tap outside the bar to hide it; `Esc` (back button) cancels in-progress text.

## Overlay Layout

| Area | Purpose |
|------|---------|
| Game tabs (`GAME`) | Switch between game directories (`id1`, mods). |
| `Game Files` list | Shows local user files for the selected game directory. |
| Config scope toggle (globe icon) | Switch between shared cfg files and per-mod cfg files. |
| CD controls row | Open CD track list, play/pause, stop, and CD on/off. |
| Upload button | Upload files to the current mode (game files or CD tracks). |
| Join code | Displays the current server's join code when connected. See [Join Code](#join-code) below. |
| Close button (✕) | Closes the panel. Equivalent to clicking the gear again or pressing `Esc`. |

To open or collapse the directory sidebar (tabs drawer), click the current directory name beside `Game Files` at the top of the panel (for example `id1` or `ctf`). This toggle is only available in game-file mode (not in CD mode).

## Game File Mode

In normal mode (not CD track view), the file list shows user-managed files under your browser storage.

Supported upload file types:

- `.cfg`
- `.sav`
- `.dem`
- `.pcx`
- `.pak`

Available actions:

| Action | How |
|------|---------|
| Edit | Double-click a `.cfg` file to open the editor, then use `Save` or `Cancel`. |
| Download | Click the download button beside a file. |
| Delete | Click the delete button and confirm. |
| Move | Drag a file from the list and drop it onto another game tab. |

Notes:

- Existing files prompt for overwrite before upload.
- All writes (upload/edit/delete/move) are synced to browser storage.

## Local Files Override Server Files

For the same path, local user files override server-provided files. If both exist, the client uses your local copy.

Example:
- Server provides `/ctf/config.cfg`
- You upload `/ctf/config.cfg`
- The uploaded local file is the one Quake loads

## Config Scope Toggle

The globe button controls cfg scope:

- **Global cfgs enabled**: shared cfg files across mods.
- **Global cfgs disabled**: per-mod cfg files.

The selection is saved in browser storage and reused on next load.

| Mode | Behavior | Choose This When |
|------|----------|------------------|
| Global cfgs enabled | `config.cfg`/`autoexec.cfg` are treated as one shared setup across mods. | You want one consistent control/video/HUD setup everywhere. |
| Global cfgs disabled (per-mod) | Each mod keeps its own cfg state and can diverge. | Different mods need different binds, sensitivity, aliases, or HUD/cvar setups. |

Practical tradeoff:
- Global mode is simpler and predictable for everyday play.
- Per-mod mode keeps cfgs siloed and runs `quake.rc` on every server join.

## CD Mode and Native Quake CD Commands

Click the eject button to switch into CD track view (`/cd/`):

- Local user CD tracks are listed first.
- Server-provided tracks are shown under `Server CD tracks`.
- Local tracks with the same track number override server tracks.

Supported CD upload file types:

- `.ogg`
- `.mp3`

The CD controls are a UI for Quake's native `cd` console commands, not a separate audio control system. Quake's native BGM system plays tracks based on the map; each map specifies a track number 2-11. Track 1 was the data track (install files) on the original Quake CD.

CD controls:

| Button | Native Command |
|------|----------------|
| Eject | UI-only toggle for CD track view (no `cd` command). |
| Play/Pause | `cd resume` or `cd pause` (state-dependent). |
| Stop | `cd stop`. |
| Power | `cd on` / `cd off`. |

Track interaction:

- Click a track row (or its play/pause button) to run `cd loop <track-number>`, or pause/resume if that track is already active.
- Track filenames must include a track number at the start or end (for example `02-theme.ogg` or `track02.mp3`).

Because the overlay sends native Quake commands to the game, console-driven `cd` commands and overlay state stay aligned.

## Upload and Status Messages

Upload progress and status appear in the overlay footer:

- Progress messages during file reads and sync.
- Info messages on success.
- Warning/error messages for invalid extension, sync failures, or command failures.

## Join Code

When connected to a server, a **join code** appears in the overlay footer. The code is the server's port number — the same value you would use with `connect <port>` at the console.

Share the code with other players so they can join the same server without browsing the server list or being directed to a different instance. The join code is hidden when not connected to a game server.

## Persistence

Overlay-managed user files and settings are stored in browser persistent storage (IndexedDB via Emscripten FS sync), so they survive page reloads.
