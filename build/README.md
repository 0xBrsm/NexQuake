# Build System

Scripts that compile the WASM client and dedicated server from the upstream id Software Quake source.

## Files

| File | Purpose |
|------|---------|
| `Makefile` | **Primary orchestrator.** Top-level build targets for client, server, and upstream preparation. |
| `build-client.sh` | **WASM client build.** Prepares the upstream source, applies client patches and overlays, runs `make -f Makefile.emscripten`. Produces `index.html`, `index.js`, `index.wasm`. |
| `build-server.sh` | **Dedicated server build.** Prepares the upstream source, applies server patches, compiles with GCC. Produces `nqserver`. Handles platform detection and 32/64-bit selection. |
| `prepare-upstream.sh` | **Upstream checkout.** Sparse-clones `id-Software/Quake` into `tmp/WinQuake/`. Idempotent -- skips if already present. |
| `platform.sh` | **Platform detection.** Sets `PLATFORM` environment variable from Docker-style platform strings (linux/amd64, linux/arm64, linux/arm/v7, linux/386). Used by Dockerfiles and build scripts. |

## How It Works

```
1. prepare-upstream.sh
   └── git sparse-checkout id-Software/Quake -> tmp/WinQuake/ (pristine, never modified)

2. build-client.sh
   ├── cp tmp/WinQuake/ -> tmp/client/ (working copy)
   ├── apply src/client/*.patch
   ├── cp src/client/*.c, *.h, *.js, Makefile.emscripten, shell.html
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
  WinQuake/       Pristine upstream checkout (shared, never modified)
  client/         Client working copy (disposable)
  server/         Server working copy (disposable)
  bin/            Build outputs (nqwasm/, nqserver)
```

Clean the workspace:

```bash
rm -rf src/build/tmp/client src/build/tmp/server src/build/tmp/bin    # clean working copies
rm -rf src/build/tmp/WinQuake                                          # force re-checkout
```

## Important Rules

- **Never modify `build/tmp/WinQuake/`** directly. Always work on copies.
- **Never commit `build/tmp/`**. It's in `.gitignore`.
- Changes to the upstream source are made via `.patch` files in `src/client/` or `src/server/`, not by editing the checkout.
