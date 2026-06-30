#!/usr/bin/env bash
#
# Prepare an upstream WinQuake source tree for builds.
#
# This repo does not vendor the upstream Quake sources. This script fetches
# WinQuake from upstream via a temporary sparse clone, caches the plain source
# tree under build/tmp/WinQuake/, and applies our server overlays/patches into
# a disposable working tree.
#
# By default, community-sourced vanilla Quake bugfix patches (buffer overflows,
# crashes, etc.) from src/bugfix/ are applied before any build-specific patches.
# Set BUGFIX=0 to disable.
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
UPSTREAM_REF="${UPSTREAM_REF:-HEAD}"
FETCH_ONLY="${FETCH_ONLY:-0}"

server_bits="${SERVER_BITS:-auto}"
if [[ "${server_bits}" == "auto" ]]; then
  server_bits="$(nq_server_bits_detect)"
fi

mkdir -p "${UPSTREAM_QUAKE_DIR}" "$(dirname "${OUT_DIR}")"

remove_legacy_upstream_git() {
  local legacy_git_dir="${UPSTREAM_QUAKE_DIR}/.git"
  local legacy_origin=""

  if [[ ! -d "${legacy_git_dir}" ]]; then
    return 0
  fi

  legacy_origin="$(git -C "${UPSTREAM_QUAKE_DIR}" config --get remote.origin.url 2>/dev/null || true)"
  if [[ "${legacy_origin}" != "${UPSTREAM_REPO}" ]]; then
    echo "warning: leaving existing git metadata in ${UPSTREAM_QUAKE_DIR} (origin: ${legacy_origin:-unknown})" >&2
    return 0
  fi

  echo "Removing legacy upstream git metadata from ${UPSTREAM_QUAKE_DIR} ..."
  rm -rf "${legacy_git_dir}"
}

fetch_upstream_winquake() {
  local fetch_root checkout_dir

  fetch_root="$(mktemp -d)"
  checkout_dir="${fetch_root}/quake"

  cleanup_fetch_root() {
    rm -rf "${fetch_root}"
  }
  trap cleanup_fetch_root RETURN

  git clone --depth 1 --filter=blob:none --sparse "${UPSTREAM_REPO}" "${checkout_dir}"
  git -C "${checkout_dir}" sparse-checkout set WinQuake

  if [[ "${UPSTREAM_REF}" != "HEAD" ]]; then
    git -C "${checkout_dir}" fetch --depth 1 origin "${UPSTREAM_REF}"
    git -C "${checkout_dir}" checkout --force FETCH_HEAD
  fi

  rm -rf "${UPSTREAM_WINQUAKE_DIR}"
  mkdir -p "${UPSTREAM_QUAKE_DIR}"
  cp -r "${checkout_dir}/WinQuake" "${UPSTREAM_WINQUAKE_DIR}"
}

refresh_upstream=0
if [[ ! -d "${UPSTREAM_WINQUAKE_DIR}" ]]; then
  refresh_upstream=1
fi
if [[ "${UPSTREAM_REF}" != "HEAD" ]]; then
  refresh_upstream=1
fi

remove_legacy_upstream_git

if [[ "${refresh_upstream}" == "1" ]]; then
  fetch_upstream_winquake
fi

if [[ "${FETCH_ONLY}" == "1" ]]; then
  echo "Upstream source ready at ${UPSTREAM_WINQUAKE_DIR}"
  exit 0
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

normalize_nq_version() {
  local value="${1:-}"
  value="$(printf '%s' "${value}" | tr -d '[:space:]')"
  printf '%s' "${value}"
}

resolved_nq_version=""
resolved_nq_version_source=""

resolve_nq_version() {
  local candidate=""
  resolved_nq_version=""
  resolved_nq_version_source=""

  candidate="$(normalize_nq_version "${NQ_VERSION:-}")"
  if [[ -n "${candidate}" ]]; then
    resolved_nq_version="${candidate}"
    resolved_nq_version_source="env"
    return 0
  fi

  if command -v git >/dev/null 2>&1; then
    candidate="$(normalize_nq_version "$(git -C "${ROOT}/.." describe --tags --always --dirty 2>/dev/null || true)")"
    if [[ -n "${candidate}" ]]; then
      resolved_nq_version="${candidate}"
      resolved_nq_version_source="git-root"
      return 0
    fi
    candidate="$(normalize_nq_version "$(git -C "${ROOT}" describe --tags --always --dirty 2>/dev/null || true)")"
    if [[ -n "${candidate}" ]]; then
      resolved_nq_version="${candidate}"
      resolved_nq_version_source="git-src"
      return 0
    fi
  fi

  resolved_nq_version="unknown"
  resolved_nq_version_source="fallback"
  return 0
}

# --- Upstream bugfix patches (opt-out via BUGFIX=0) --------------------------
# These fix well-documented vanilla WinQuake bugs (buffer overflows, crashes,
# etc.) and are applied to the canonical source *before* any build-specific
# patches.  They are safe for both server and client builds.
BUGFIX="${BUGFIX:-1}"
if [[ "${BUGFIX}" == "1" ]]; then
  echo "Applying upstream bugfix patches ..."
  for patch_file in "${ROOT}/bugfix/"*.patch; do
    [[ -f "${patch_file}" ]] || continue
    apply_patch "${patch_file}"
  done
fi

if [[ "${kind}" == "server" ]]; then
  echo "Applying server overlays + patches ..."
  cp "${ROOT}/server/Makefile.dedicated" "${OUT_DIR}/"

  apply_patch "${ROOT}/server/sv_main.c.patch"
  apply_patch "${ROOT}/server/net_udp.c.patch"
  apply_patch "${ROOT}/server/host.c.patch"
  apply_patch "${ROOT}/server/pr_cmds.c.patch"

  if [[ "${server_bits}" == "64" ]]; then
    echo "Applying 64-bit portability patches ..."
    # net_dgrm.c.64bit.patch replaces the BAN_TEST hand-rolled POSIX structs
    # with standard headers — same fix as bugfix/net_dgrm.c.patch.
    # Skip when BUGFIX=1 to avoid a "reversed patch" error.
    if [[ "${BUGFIX}" != "1" ]]; then
      apply_patch "${ROOT}/bugfix/64bit/net_dgrm.c.64bit.patch"
    fi
    apply_patch "${ROOT}/bugfix/64bit/pr_cmds.c.64bit.patch"
    apply_patch "${ROOT}/bugfix/64bit/host_cmd.c.64bit.patch"
    apply_patch "${ROOT}/bugfix/64bit/sv_main.c.64bit.patch"
  fi

  if ! grep -q "Stub functions for headless NetQuake server" "${OUT_DIR}/sys_linux.c" 2>/dev/null; then
    cat "${ROOT}/server/sys_linux_stub.c" >> "${OUT_DIR}/sys_linux.c"
  fi
fi

if [[ "${kind}" == "client" ]]; then
  echo "Applying client (WASM) overlays + patches ..."
  client_shell_src_dir="${ROOT}/client/shell"
  client_shell_css_parts=(
    "shell-nq.css"
    "shell-loader.css"
    "shell-ui.css"
    "shell-touch.css"
  )

  cp "${ROOT}/client/net_bsd.c" "${ROOT}/client/net_ws.c" "${ROOT}/client/net_wt.c" "${ROOT}/client/net_wasm.c" "${ROOT}/client/net_nqchan.c" "${ROOT}/client/net_slist.c" "${ROOT}/client/cmd_rcon.c" "${ROOT}/client/net_wasm.h" "${ROOT}/client/net_nqchan.h" "${OUT_DIR}/"
  cp "${ROOT}/client/sys_wasm.c" "${ROOT}/client/vid_wasm.c" "${ROOT}/client/in_wasm.c" "${ROOT}/client/snd_wasm.c" "${ROOT}/client/cd_wasm.c" "${OUT_DIR}/"
  cp "${ROOT}/client/com_gameswitch.c" "${ROOT}/client/com_gameswitch.h" "${ROOT}/client/cl_prefetch.c" "${ROOT}/client/cl_prefetch.h" "${OUT_DIR}/"
  cp "${ROOT}/client/cl_replay.c" "${ROOT}/client/cl_replay.h" "${OUT_DIR}/"
  cp "${ROOT}/client/Makefile.emscripten" "${client_shell_src_dir}/"{shell.html,favicon.svg,manifest.webmanifest,pwa-icon.svg} "${OUT_DIR}/"
  bash "${ROOT}/build/gen-pwa-icons.sh" "${client_shell_src_dir}/pwa-icon.svg" "${OUT_DIR}"
  pwa_icon_mode="$(tr -d '[:space:]' < "${OUT_DIR}/.nq-pwa-icon-mode" 2>/dev/null || true)"
  rm -f "${OUT_DIR}/.nq-pwa-icon-mode"
  if [[ "${pwa_icon_mode}" == "svg" ]]; then
    sed -i 's|<link rel="apple-touch-icon" sizes="180x180" href="nq-touch-icon-180.png">|<link rel="apple-touch-icon" type="image/svg+xml" sizes="any" href="pwa-icon.svg">|' "${OUT_DIR}/shell.html"
  fi

  : > "${OUT_DIR}/shell.css"
  for css_name in "${client_shell_css_parts[@]}"; do
    css_path="${client_shell_src_dir}/${css_name}"
    if [[ ! -f "${css_path}" ]]; then
      echo "missing shell stylesheet: ${css_path}" >&2
      exit 1
    fi
    printf '/* %s */\n' "${css_name}" >> "${OUT_DIR}/shell.css"
    cat "${css_path}" >> "${OUT_DIR}/shell.css"
    printf '\n' >> "${OUT_DIR}/shell.css"
  done
  sed -i '/href="shell-loader.css"/d;/href="shell-ui.css"/d;/href="shell-touch.css"/d' "${OUT_DIR}/shell.html"
  sed -i 's|<link rel="stylesheet" href="shell-nq.css">|<link rel="stylesheet" href="shell.css">|' "${OUT_DIR}/shell.html"

  resolve_nq_version
  client_version="${resolved_nq_version}"
  if [[ "${NQ_REQUIRE_VERSION:-0}" == "1" ]]; then
    case "${resolved_nq_version_source}" in
      env)
        ;;
      *)
        echo "failed to resolve client version from explicit metadata (set NQ_VERSION)" >&2
        exit 1
        ;;
    esac
  fi
  printf '%s\n' "${client_version}" > "${OUT_DIR}/VERSION"
  mkdir -p "${OUT_DIR}/shell"
  cp "${ROOT}/client/shell/"*.js "${OUT_DIR}/shell/"

  apply_patch "${ROOT}/client/patches/net.h.patch"
  apply_patch "${ROOT}/client/patches/common.h.patch"
  apply_patch "${ROOT}/client/patches/common.c.patch"
  apply_patch "${ROOT}/client/patches/host.c.patch"
  apply_patch "${ROOT}/client/patches/keys.c.patch"
  apply_patch "${ROOT}/client/patches/net_main.c.patch"
  apply_patch "${ROOT}/client/patches/menu.h.patch"
  apply_patch "${ROOT}/client/patches/menu.c.patch"
  apply_patch "${ROOT}/client/patches/net_dgrm.c.patch"
  apply_patch "${ROOT}/client/patches/cl_parse.c.patch"
  apply_patch "${ROOT}/client/patches/cl_demo.c.patch"
  apply_patch "${ROOT}/client/patches/cl_main.c.patch"
  apply_patch "${ROOT}/client/patches/screen.c.patch"
  apply_patch "${ROOT}/client/patches/snd_dma.c.patch"
  apply_patch "${ROOT}/client/patches/r_shared.h.patch"
  apply_patch "${ROOT}/client/patches/r_main.c.patch"
  apply_patch "${ROOT}/client/patches/r_draw.c.patch"
  apply_patch "${ROOT}/client/patches/r_edge.c.patch"
  apply_patch "${ROOT}/client/patches/d_modech.c.patch"
  apply_patch "${ROOT}/client/patches/d_part.c.patch"

  client_gamename="$(sed -n 's/^[[:space:]]*#define[[:space:]]*GAMENAME[[:space:]]*"\([^"]\+\)".*/\1/p' "${OUT_DIR}/quakedef.h" | head -n1)"
  if [[ -z "${client_gamename}" ]]; then
    echo "failed to resolve GAMENAME from ${OUT_DIR}/quakedef.h" >&2
    exit 1
  fi
  mkdir -p "${OUT_DIR}/seed/${client_gamename}"
  client_autoexec_src="${ROOT}/etc/client/autoexec.cfg"
  client_nexquake_src="${ROOT}/etc/client/nexquake.cfg"
  if [[ ! -f "${client_autoexec_src}" || ! -f "${client_nexquake_src}" ]]; then
    echo "missing client seed cfgs: ${client_autoexec_src}, ${client_nexquake_src}" >&2
    exit 1
  fi
  cp "${client_autoexec_src}" "${client_nexquake_src}" "${OUT_DIR}/seed/${client_gamename}/"
  client_remote_root_basename="nexusfs"

  client_gamename_escaped="$(printf '%s' "${client_gamename}" | sed -e 's/[\/&]/\\&/g')"
  sed -i "s/__NEXQUAKE_GAMENAME__/${client_gamename_escaped}/g" "${OUT_DIR}/shell.html"

  sed -i "s/__NEXQUAKE_VERSION__/${client_version}/g" "${OUT_DIR}/shell.html"
  client_remote_root_basename_escaped="$(printf '%s' "${client_remote_root_basename}" | sed -e 's/[\/&]/\\&/g')"

  mapfile -t client_pre_js_files < <(find "${OUT_DIR}/shell" -maxdepth 1 -type f -name '*.js' | sort)
  if [[ "${#client_pre_js_files[@]}" -eq 0 ]]; then
    echo "failed to assemble client pre-js bundle: no module files found under ${OUT_DIR}/shell" >&2
    exit 1
  fi
  cat "${client_pre_js_files[@]}" > "${OUT_DIR}/nq-pre.js"
  sed -i "s/__NEXQUAKE_GAMENAME__/${client_gamename_escaped}/g" "${OUT_DIR}/nq-pre.js"
  sed -i "s/__NEXQUAKE_REMOTE_ROOT_BASENAME__/${client_remote_root_basename_escaped}/g" "${OUT_DIR}/nq-pre.js"

  mkdir -p "${OUT_DIR}/${client_gamename}"
fi

echo "OK"
