# NexQuake Attributions

This file records upstream sources that NexQuake derives from or incorporates.

## Core Engine Lineage

- **id Software Quake GPL release**
  - Repository: https://github.com/id-Software/Quake
  - Reference commit: `bf4ac424ce75`
  - Upstream path used: `WinQuake/`
  - Notes: Base Quake engine source used by build/staging scripts.

- **Original WASM port (Gregory Maynard-Hoare)**
  - Repository: https://github.com/GMH-Code/Quake-WASM
  - Notes: Historical WASM/SDL port lineage used by downstream forks.

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

## File-Level Guidance

- `client/net_websocket.c`: derivative net driver glue for NexQuake.
- `client/net_ws_transport.c`: derivative transport/callback/queue logic.
- `client/cmd_rcon.c`: NexQuake-focused auth/token command layer; still part of the derivative websocket module lineage.
- `client/net_websocket.h`: derivative public net driver interface.

## License

NexQuake code remains distributed under `GPL-2.0-or-later`, consistent with upstream Quake GPL lineage.
