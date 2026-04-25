# Attributions

This file records upstream sources that NexQuake derives from or incorporates.

## Core Engine Lineage

- **id Software Quake GPL release**
  - Repository: https://github.com/id-Software/Quake
  - Reference commit: `bf4ac424ce75`
  - Upstream path used: `WinQuake/`
  - Notes: Base Quake engine source used by build/staging scripts.

## WebSocket Networking Lineage

- **initialed85 WebSocket / WASM Quake projects**
  - Repositories:
    - https://github.com/initialed85/quake-websocket-proxy (initial seed for what became Nexus)
    - https://github.com/initialed85/Quake-WASM (reference commit `2bac461a6cc8`)
    - https://github.com/initialed85/Quake (reference commit `7640d6b58f91`)
  - Upstream paths referenced:
    - `WinQuake/net_websocket.c`
    - `WinQuake/net_websocket.h`
    - `WinQuake/websockets/websockets.c`
    - `WinQuake/websockets/websockets.h`
  - Notes: Primary ancestry for NexQuake websocket net driver integration. The `quake-websocket-proxy` repo was the original proof-of-concept that seeded this project; the two Quake forks provided the websocket transport code that NexQuake's networking derives from.

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
  - Source: `https://dl.dafont.com/dl/?f=quake`
  - Notes: NexQuake embeds a compact subset directly in `client/shell/shell-nq.css` for the runtime shell and ships the full upstream TTF in `site/assets/fonts/` for the documentation site. The upstream `dpquake.txt` redistribution notice is included alongside the staged site font.

## Acknowledgements

The following projects provided inspiration and reference during NexQuake's development but are not direct code ancestors of the current codebase:

- **Gregory Maynard-Hoare** ([GMH-Code/Quake-WASM](https://github.com/GMH-Code/Quake-WASM)). Original WASM/SDL port of Quake. NexQuake's platform layer was rewritten from scratch using direct Emscripten APIs (no SDL), but the GMH port demonstrated the viability of Quake-in-a-browser and influenced downstream forks that NexQuake's WebSocket networking derives from.

## License

NexQuake code remains distributed under `GPL-2.0-or-later`, consistent with upstream Quake GPL lineage.
