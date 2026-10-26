#!/usr/bin/env bash
set -euo pipefail

if [ "$(uname -s)" != Darwin ]; then
    echo "This installer requires macOS." >&2
    exit 1
fi
if [ "$#" -gt 1 ]; then
    echo "Usage: bash install.sh [installation-prefix]" >&2
    exit 1
fi

source_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
installation_prefix="${1:-${HOME}/.local}"
mkdir -p "${installation_prefix}/bin"
staged_binary="$(mktemp "${installation_prefix}/bin/.shareto-install.XXXXXX")"
trap 'rm -f "${staged_binary}"' EXIT
install -m 0755 "${source_dir}/shareto" "${staged_binary}"
mv -f "${staged_binary}" "${installation_prefix}/bin/shareto"
echo "Installed ${installation_prefix}/bin/shareto"
echo "Add ${installation_prefix}/bin to PATH, then run: shareto service install"
