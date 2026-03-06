#!/usr/bin/env bash
#
# Generate the packaged PWA PNG icons from the square source SVG.
#
set -euo pipefail

src_svg="${1:-}"
out_dir="${2:-}"

if [[ -z "${src_svg}" || -z "${out_dir}" ]]; then
  echo "usage: $0 <source-svg> <out-dir>" >&2
  exit 2
fi

if [[ ! -f "${src_svg}" ]]; then
  echo "missing PWA icon source: ${src_svg}" >&2
  exit 1
fi

mkdir -p "${out_dir}"
mode_file="${out_dir}/.nq-pwa-icon-mode"

targets=(
  "nq-icon-192.png:192"
  "nq-icon-512.png:512"
  "nq-touch-icon-180.png:180"
)

if [[ "${NQ_DISABLE_RSVG:-0}" != "1" ]] && command -v rsvg-convert >/dev/null 2>&1; then
  echo "Generating PWA icons from ${src_svg} ..."
  for target in "${targets[@]}"; do
    name="${target%%:*}"
    size="${target##*:}"
    rsvg-convert --format=png --width="${size}" --height="${size}" --output="${out_dir}/${name}" "${src_svg}"
  done
  printf 'raster\n' > "${mode_file}"
  exit 0
fi

echo "warning: rsvg-convert not found; falling back to SVG PWA icon only" >&2
printf 'svg\n' > "${mode_file}"
