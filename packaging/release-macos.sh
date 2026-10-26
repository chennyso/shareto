#!/usr/bin/env bash
set -euo pipefail

go test -race -count=1 ./...
go vet ./...
if [ "${RELEASE_REF_TYPE:-}" = tag ]; then
    package_version="${RELEASE_VERSION#v}"
else
    package_version="$(make -s print-version)"
fi
make macos VERSION="${package_version}"
VERSION="${package_version}" bash packaging/test-macos.sh
