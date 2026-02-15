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

OUT_DIR="${CLIENT_BUILD_DIR}" "${ROOT}/build/prepare-upstream.sh" client

pushd "${CLIENT_BUILD_DIR}" >/dev/null
make -f Makefile.emscripten
popd >/dev/null

cp -f "${CLIENT_BUILD_DIR}/index.html" "${OUT_DIR}/"
cp -f "${CLIENT_BUILD_DIR}/shell.css" "${OUT_DIR}/"
cp -f "${CLIENT_BUILD_DIR}/index.js" "${OUT_DIR}/"
cp -f "${CLIENT_BUILD_DIR}/index.wasm" "${OUT_DIR}/"
if [[ -f "${CLIENT_BUILD_DIR}/index.data" ]]; then
  cp -f "${CLIENT_BUILD_DIR}/index.data" "${OUT_DIR}/"
fi

echo "Built client: ${OUT_DIR}"
