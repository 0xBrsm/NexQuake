#!/usr/bin/env bash
#
# Prepare an upstream WinQuake source tree for builds.
#
# This repo does not vendor the upstream Quake sources. This script fetches
# them (sparse-checkout WinQuake/) and applies our server overlays/patches
# into a temporary working tree.
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# shellcheck disable=SC1091
source "${ROOT}/build/platform.sh"

nq_platform_resolve

kind="${1:-server}"
case "${kind}" in
  server|client) ;;
  *)
    echo "usage: $0 {server|client}" >&2
    exit 2
    ;;
esac

OUT_DIR="${OUT_DIR:-${ROOT}/build/tmp/${kind}}"
UPSTREAM_QUAKE_DIR="${UPSTREAM_QUAKE_DIR:-${ROOT}/build/tmp}"
UPSTREAM_WINQUAKE_DIR="${UPSTREAM_WINQUAKE_DIR:-${UPSTREAM_QUAKE_DIR}/WinQuake}"
UPSTREAM_REPO="${UPSTREAM_REPO:-https://github.com/id-Software/Quake.git}"
UPSTREAM_REF="${UPSTREAM_REF:-master}"

server_bits="${SERVER_BITS:-auto}"
if [[ "${server_bits}" == "auto" ]]; then
  server_bits="$(nq_server_bits_detect)"
fi

mkdir -p "${UPSTREAM_QUAKE_DIR}" "$(dirname "${OUT_DIR}")"

if [[ ! -d "${UPSTREAM_WINQUAKE_DIR}" ]]; then
  mkdir -p "${UPSTREAM_QUAKE_DIR}"
  if [[ ! -d "${UPSTREAM_QUAKE_DIR}/.git" ]]; then
    git -C "${UPSTREAM_QUAKE_DIR}" init
    git -C "${UPSTREAM_QUAKE_DIR}" remote add origin "${UPSTREAM_REPO}"
    git -C "${UPSTREAM_QUAKE_DIR}" config core.sparseCheckout true
    echo "WinQuake/" > "${UPSTREAM_QUAKE_DIR}/.git/info/sparse-checkout"
  fi
  git -C "${UPSTREAM_QUAKE_DIR}" fetch --depth 1 origin "${UPSTREAM_REF}"
  git -C "${UPSTREAM_QUAKE_DIR}" checkout --force FETCH_HEAD
fi

echo "Preparing upstream source for ${kind} build at ${OUT_DIR} ..."
rm -rf "${OUT_DIR}"
mkdir -p "${OUT_DIR}"
cp -r "${UPSTREAM_WINQUAKE_DIR}/." "${OUT_DIR}/"

apply_patch() {
  local patch_path="$1"
  echo "  patch: $(basename "${patch_path}")"
  patch -p0 -d "${OUT_DIR}" < "${patch_path}"
}

if [[ "${kind}" == "server" ]]; then
  echo "Applying server overlays + patches ..."
  cp "${ROOT}/server/Makefile.dedicated" "${OUT_DIR}/"

  apply_patch "${ROOT}/server/net_udp.c.patch"
  apply_patch "${ROOT}/server/common.c.patch"
  apply_patch "${ROOT}/server/net_main.c.patch"
  apply_patch "${ROOT}/server/sv_main.c.patch"

  if [[ "${server_bits}" == "64" ]]; then
    echo "Applying server 64-bit patches ..."
    apply_patch "${ROOT}/server/64bit/net_dgrm.c.64bit.patch"
    apply_patch "${ROOT}/server/64bit/pr_cmds.c.64bit.patch"
    apply_patch "${ROOT}/server/64bit/host_cmd.c.64bit.patch"
    apply_patch "${ROOT}/server/64bit/sv_main.c.64bit.patch"
  fi

  if ! grep -q "Stub functions for headless NetQuake server" "${OUT_DIR}/sys_linux.c" 2>/dev/null; then
    cat "${ROOT}/server/sys_linux_stub.c" >> "${OUT_DIR}/sys_linux.c"
  fi
fi

if [[ "${kind}" == "client" ]]; then
  echo "Applying client (WASM) overlays + patches ..."
  cp "${ROOT}/client/net_bsd.c" "${ROOT}/client/net_websocket.c" "${ROOT}/client/net_ws_transport.c" "${ROOT}/client/cmd_rcon.c" "${ROOT}/client/cmd_rcon_token.js" "${ROOT}/client/net_websocket.h" "${OUT_DIR}/"
  cp "${ROOT}/client/snd_sdl.c" "${ROOT}/client/sys_sdl.c" "${ROOT}/client/vid_sdl.c" "${OUT_DIR}/"
  cp "${ROOT}/client/Makefile.emscripten" "${ROOT}/client/shell.html" "${OUT_DIR}/"

  apply_patch "${ROOT}/client/net.h.patch"
  apply_patch "${ROOT}/client/net_main.c.patch"
  apply_patch "${ROOT}/client/net_dgrm.c.patch"
  apply_patch "${ROOT}/client/chase.c.patch"
  apply_patch "${ROOT}/client/cl_parse.c.patch"

  mkdir -p "${OUT_DIR}/id1"
fi

echo "OK"
