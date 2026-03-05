# Build System

Scripts that compile the WASM client and dedicated server from the upstream id Software Quake source.

## Files

| File | Purpose |
|------|---------|
| `Makefile` | Primary orchestrator. Top-level build targets for client, server, and upstream preparation. |
| `build-client.sh` | WASM client build. Prepares the upstream source, applies client patches and overlays, runs `make -j <jobs> -f Makefile.emscripten`. Produces `index.html`, `shell.css`, `favicon.svg`, `manifest.webmanifest`, `nq-icon-192.png`, `nq-icon-512.png`, `nq-touch-icon-180.png`, `index.js`, `index.wasm`, and optional `index.data`. |
| `build-server.sh` | Dedicated server build. Prepares the upstream source, applies server patches, compiles with GCC, and runs `make -j <jobs>` for parallel object builds. Produces `nqserver`. Handles platform detection and 32/64-bit selection. |
| `prepare-upstream.sh` | Upstream fetch and build-tree staging. Uses a temporary sparse clone of `id-Software/Quake` to refresh `tmp/WinQuake/`, applies overlays/patches, and resolves client version metadata (`NQ_VERSION` -> `git describe` -> `unknown`). Set `FETCH_ONLY=1` to only ensure checkout exists (used by CI prefetch). |
| `platform.sh` | Platform detection. Sets `PLATFORM` environment variable from Docker-style platform strings (linux/amd64, linux/arm64, linux/arm/v7, linux/386). Used by Dockerfiles and build scripts. |

## How It Works

```
1. prepare-upstream.sh
   └── temporary sparse clone of id-Software/Quake -> tmp/WinQuake/ (canonical cache, never modified)

2. build-client.sh
   ├── cp tmp/WinQuake/ -> tmp/client/ (working copy)
   ├── apply src/client/patches/*.patch
   ├── cp src/client/*.c, *.h, *.js + shell assets (`shell.html`, `favicon.svg`, `manifest.webmanifest`, `nq-icon-*.png`, `nq-touch-icon-180.png`)
   ├── bundle `shell-nq.css`, `shell-loader.css`, `shell-ui.css`, `shell-touch.css` -> `shell.css`
   ├── stage seed cfgs from src/etc/ into tmp/client/seed/<base-game>/
   └── emcc (Emscripten) -> output files

3. build-server.sh
   ├── cp tmp/WinQuake/ -> tmp/server/ (working copy)
   ├── apply src/server/*.patch
   ├── cat src/server/sys_linux_stub.c >> sys_linux.c
   └── gcc -> nqserver binary
```

## Workspace

All build intermediates go under `build/tmp/` (relative to `src/`, i.e. `src/build/tmp/` from the repo root). This directory is gitignored.

```
build/tmp/
  WinQuake/       Canonical upstream checkout (shared, never modified)
  client/         Client working copy (disposable)
  server/         Server working copy (disposable)
  bin/            Build outputs (nqwasm/, nqserver)
```

Clean the workspace:

```bash
rm -rf src/build/tmp/client src/build/tmp/server src/build/tmp/bin    # clean working copies
rm -rf src/build/tmp/WinQuake                                          # force re-checkout
rm -rf src/build/tmp/.git                                              # remove legacy nested repo from older workspaces
```

## Parallelism Knobs

- `NQ_MAKE_JOBS`: Number of parallel `make` jobs for client/server builds. Defaults to detected CPU count.
- `NQ_GO_BUILD_P`: Number of parallel Go package builds (`go build -p`) for Nexus builds. Defaults to detected CPU count.

## Version Knobs

- `NQ_VERSION`: Explicit client version string injected into WASM build metadata.
- `NQ_REQUIRE_VERSION`: Set to `1` to require explicit version metadata (`NQ_VERSION`). Git-derived fallback is rejected.
