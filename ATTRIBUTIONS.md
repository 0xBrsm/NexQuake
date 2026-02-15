# NexQuake Attributions

This file records upstream sources that NexQuake derives from or incorporates.

## Core Engine Lineage

- **id Software Quake GPL release**
  - Repository: https://github.com/id-Software/Quake
  - Reference commit: `bf4ac424ce75`
  - Upstream path used: `WinQuake/`
  - Notes: Base Quake engine source used by build/staging scripts.

## WebSocket Networking Lineage

- **initialed85 Quake-WASM fork**
  - Repository: https://github.com/initialed85/Quake-WASM
  - Reference commit: `2bac461a6cc8`
  - Upstream paths referenced:
    - `WinQuake/net_websocket.c`
    - `WinQuake/net_websocket.h`
  - Notes: Primary ancestry for NexQuake websocket net driver integration.

- **initialed85 Quake fork**
  - Repository: https://github.com/initialed85/Quake
  - Reference commit: `7640d6b58f91`
  - Upstream paths referenced:
    - `WinQuake/websockets/websockets.c`
    - `WinQuake/websockets/websockets.h`
  - Notes: Secondary websocket transport design/reference lineage.

## LZH Decompression Lineage

- **koron-go/lha**
  - Repository: https://github.com/koron-go/lha
  - Notes: LZH decoding logic in `nexus/quake106/quake106.go` is derived from this library. Heavily modified and optimized for Quake 1.06 resource extraction. MIT licensed.

## Quickstart Game Data

- **id Software Quake 1.06 Shareware**
  - Source: `quake106.zip` from public archives
  - Notes: Extracted at runtime by `nexus/quake106/` with SHA256 verification at every stage. id Software's shareware license permits redistribution of the original, unmodified archive only. The extraction pipeline enforces this by rejecting any archive or resource that doesn't match known-good hashes.

- **LibreQuake PAK1** (`lq-pak1.zip`)
  - Repository: https://github.com/lavenderdotpet/LibreQuake
  - Notes: Open-source PAK1 prepared for NexQuake. Fileset matches retail PAK1 exactly; sounds resampled to match PAK0 shareware quality. Art assets under BSD-3-Clause (LibreQuake contributors). `pop.lmp` under GPL-2.0 (derived from id Software Quake source via [pop.lmp generator](https://github.com/lavenderdotpet/pop.lmp_generator)).

## UI Typography

- **DpQuake font**
  - Creator metadata embedded in original TTF: `Dead Pete [deadpete@iname.com - http://deadpete.tripod.com]`
  - Notes: NexQuake embeds this font directly in `client/shell/shell.css` via `@font-face` data URI for the UI branding treatment.

## Acknowledgements

The following projects provided inspiration and reference during NexQuake's development but are not direct code ancestors of the current codebase:

- **Gregory Maynard-Hoare** ([GMH-Code/Quake-WASM](https://github.com/GMH-Code/Quake-WASM)) -- Original WASM/SDL port of Quake. NexQuake's platform layer was rewritten from scratch using direct Emscripten APIs (no SDL), but the GMH port demonstrated the viability of Quake-in-a-browser and influenced downstream forks that NexQuake's WebSocket networking derives from.

## File-Level Guidance

- `client/net_ws_transport.c`: derivative websocket transport (Emscripten websocket lifecycle + frame queues).
- `client/net_ws_vnet.c`: derivative net driver + virtual LAN address veneer for NexQuake.
- `client/cmd_rcon.c`: NexQuake-focused auth/token command layer; still part of the derivative websocket module lineage.
- `client/net_ws_transport.h`: public transport interface.
- `client/net_ws_vnet.h`: public net driver interface.

## License

NexQuake code remains distributed under `GPL-2.0-or-later`, consistent with upstream Quake GPL lineage.
