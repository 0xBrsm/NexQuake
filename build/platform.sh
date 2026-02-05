#!/usr/bin/env bash
#
# Shared platform mapping for build scripts.
#
# Supported PLATFORM values:
#   linux/arm/v7, linux/arm64, linux/386, linux/amd64
#
set -euo pipefail

nq_platform_normalize() {
  local p="${1:-}"
  case "${p}" in
    ""|linux/arm/v7|linux/arm64|linux/386|linux/amd64) printf "%s" "${p}" ;;
    *) echo "error: unsupported PLATFORM=${p} (expected linux/arm/v7|linux/arm64|linux/386|linux/amd64)" >&2; return 2 ;;
  esac
}

# Resolves PLATFORM into:
#   NEXUS_GOOS, NEXUS_GOARCH, NEXUS_GOARM (optional)
#   SERVER_BITS, SERVER_TARGET (optional)
#
# If the target vars are already set and conflict with PLATFORM, errors out.
nq_platform_resolve() {
  local platform
  platform="$(nq_platform_normalize "${PLATFORM:-}")"
  [[ -z "${platform}" ]] && return 0

  local goos="linux" goarch="" goarm=""
  local server_bits="" server_target=""

  case "${platform}" in
    linux/arm/v7)
      goarch="arm"
      goarm="7"
      server_bits="32"
      server_target="armhf"
      ;;
    linux/arm64)
      goarch="arm64"
      server_bits="64"
      ;;
    linux/386)
      goarch="386"
      server_bits="32"
      server_target="i386"
      ;;
    linux/amd64)
      goarch="amd64"
      server_bits="64"
      ;;
    "")
      return 0
      ;;
  esac

  if [[ -n "${NEXUS_GOOS:-}" && "${NEXUS_GOOS}" != "${goos}" ]]; then
    echo "error: PLATFORM=${platform} implies NEXUS_GOOS=${goos}, but NEXUS_GOOS=${NEXUS_GOOS} is set" >&2
    return 2
  fi
  if [[ -n "${NEXUS_GOARCH:-}" && "${NEXUS_GOARCH}" != "${goarch}" ]]; then
    echo "error: PLATFORM=${platform} implies NEXUS_GOARCH=${goarch}, but NEXUS_GOARCH=${NEXUS_GOARCH} is set" >&2
    return 2
  fi
  if [[ -n "${goarm}" ]]; then
    if [[ -n "${NEXUS_GOARM:-}" && "${NEXUS_GOARM}" != "${goarm}" ]]; then
      echo "error: PLATFORM=${platform} implies NEXUS_GOARM=${goarm}, but NEXUS_GOARM=${NEXUS_GOARM} is set" >&2
      return 2
    fi
  fi

  if [[ -n "${SERVER_BITS:-}" && "${SERVER_BITS}" != "${server_bits}" ]]; then
    echo "error: PLATFORM=${platform} implies SERVER_BITS=${server_bits}, but SERVER_BITS=${SERVER_BITS} is set" >&2
    return 2
  fi
  if [[ -n "${server_target}" ]]; then
    if [[ -n "${SERVER_TARGET:-}" && "${SERVER_TARGET}" != "${server_target}" ]]; then
      echo "error: PLATFORM=${platform} implies SERVER_TARGET=${server_target}, but SERVER_TARGET=${SERVER_TARGET} is set" >&2
      return 2
    fi
  fi

  export NEXUS_GOOS="${goos}"
  export NEXUS_GOARCH="${goarch}"
  if [[ -n "${goarm}" ]]; then
    export NEXUS_GOARM="${goarm}"
  fi

  export SERVER_BITS="${server_bits}"
  if [[ -n "${server_target}" ]]; then
    export SERVER_TARGET="${server_target}"
  fi
}

# Detects server bitness for auto builds.
#
# Outputs: "32" or "64"
#
# Uses:
# - SERVER_TARGET (optional hint): armhf|i386
# - CC (optional hint): compiler used to infer toolchain triple
nq_server_bits_detect() {
  local tool_cc="${CC:-cc}"
  local cc_machine
  cc_machine="$("${tool_cc}" -dumpmachine 2>/dev/null || echo unknown)"
  if [[ "${SERVER_TARGET:-}" == "armhf" || "${cc_machine}" == *gnueabihf* ]]; then
    printf "32"
  elif [[ "${SERVER_TARGET:-}" == "i386" || "${cc_machine}" =~ (^|-)i[3-6]86(-|$) ]]; then
    printf "32"
  else
    printf "64"
  fi
}
