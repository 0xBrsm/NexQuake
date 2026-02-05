#!/usr/bin/env bash
#
# Build nqserver from upstream sources + our overlays.
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# shellcheck disable=SC1091
source "${ROOT}/build/platform.sh"

nq_platform_resolve

OUT="${OUT:-${ROOT}/build/tmp/bin/nqserver}"
SERVER_BUILD_DIR="${SERVER_BUILD_DIR:-${ROOT}/build/tmp/server}"

mkdir -p "$(dirname "${OUT}")"

OUT_DIR="${SERVER_BUILD_DIR}" "${ROOT}/build/prepare-upstream.sh" server

pushd "${SERVER_BUILD_DIR}" >/dev/null
make -f Makefile.dedicated \
  ${PLATFORM:+PLATFORM=${PLATFORM}} \
  ${SERVER_BITS:+BITS=${SERVER_BITS}} \
  ${SERVER_TARGET:+TARGET=${SERVER_TARGET}} \
  ${SERVER_STATIC:+STATIC=${SERVER_STATIC}}
popd >/dev/null

bin_path="$(
  ls -1 "${SERVER_BUILD_DIR}"/build-netquake-*/nqserver 2>/dev/null | head -n 1 || true
)"
if [[ -z "${bin_path}" ]]; then
  echo "error: nqserver binary not found under ${SERVER_BUILD_DIR}/build-netquake-*/nqserver" >&2
  exit 1
fi

cp -f "${bin_path}" "${OUT}"
chmod +x "${OUT}" || true
echo "Built server: ${OUT}"
