#!/usr/bin/env bash
#
# Build nqwasm client from upstream sources + our overlays.
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# shellcheck disable=SC1091
source "${ROOT}/build/platform.sh"

nq_platform_resolve

OUT_DIR="${OUT_DIR:-${ROOT}/build/tmp/bin/nqwasm}"
CLIENT_BUILD_DIR="${CLIENT_BUILD_DIR:-${ROOT}/build/tmp/client}"

mkdir -p "${OUT_DIR}"

make_jobs="${NQ_MAKE_JOBS:-}"
if [[ -z "${make_jobs}" ]]; then
  if command -v nproc >/dev/null 2>&1; then
    make_jobs="$(nproc)"
  else
    make_jobs="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)"
  fi
fi

if ! [[ "${make_jobs}" =~ ^[0-9]+$ ]] || [[ "${make_jobs}" -lt 1 ]]; then
  echo "error: NQ_MAKE_JOBS must be a positive integer (got '${make_jobs}')" >&2
  exit 2
fi

OUT_DIR="${CLIENT_BUILD_DIR}" "${ROOT}/build/prepare-upstream.sh" client

pushd "${CLIENT_BUILD_DIR}" >/dev/null
make -j "${make_jobs}" -f Makefile.emscripten
popd >/dev/null

cp -f "${CLIENT_BUILD_DIR}/"{index.html,shell.css,favicon.svg,manifest.webmanifest,pwa-icon.svg,index.js,index.wasm} "${OUT_DIR}/"
for icon_name in nq-icon-512.png nq-icon-192.png nq-touch-icon-180.png; do
  if [[ -f "${CLIENT_BUILD_DIR}/${icon_name}" ]]; then
    cp -f "${CLIENT_BUILD_DIR}/${icon_name}" "${OUT_DIR}/"
  fi
done
if [[ -f "${CLIENT_BUILD_DIR}/index.data" ]]; then
  cp -f "${CLIENT_BUILD_DIR}/index.data" "${OUT_DIR}/"
fi

echo "Built client: ${OUT_DIR}"
