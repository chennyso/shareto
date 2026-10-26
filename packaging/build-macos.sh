#!/usr/bin/env bash
set -euo pipefail

if [ "$(uname -s)" != Darwin ]; then
    echo "macOS packages must be built on macOS with Apple's command line tools." >&2
    exit 1
fi

package_version="${VERSION:-3.0.0}"
repository_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
staging_dir="${repository_dir}/dist/macos/staging"
payload_dir="${staging_dir}/root"
archive_dir="${staging_dir}/archive/shareto"

rm -rf "${staging_dir}"
mkdir -p "${payload_dir}/usr/local/bin" "${payload_dir}/usr/local/share/doc/shareto" "${archive_dir}"
/usr/bin/lipo -create \
    "${repository_dir}/dist/macos/arm64/shareto" \
    "${repository_dir}/dist/macos/amd64/shareto" \
    -output "${payload_dir}/usr/local/bin/shareto"
/usr/bin/codesign --force --sign - "${payload_dir}/usr/local/bin/shareto"
/usr/bin/codesign --verify --strict "${payload_dir}/usr/local/bin/shareto"
/usr/bin/lipo "${payload_dir}/usr/local/bin/shareto" -verify_arch arm64 x86_64

install -m 0644 "${repository_dir}/README.md" "${payload_dir}/usr/local/share/doc/shareto/README.md"
install -m 0644 "${repository_dir}/docs/MACOS.md" "${payload_dir}/usr/local/share/doc/shareto/MACOS.md"
install -m 0644 "${repository_dir}/LICENSE" "${payload_dir}/usr/local/share/doc/shareto/LICENSE"

/usr/bin/pkgbuild \
    --root "${payload_dir}" \
    --identifier io.github.huadaox.shareto \
    --version "${package_version}" \
    --min-os-version 13.0 \
    --install-location / \
    --ownership recommended \
    "${staging_dir}/shareto-component.pkg"
/usr/bin/productbuild \
    --product "${repository_dir}/packaging/macos-requirements.plist" \
    --package "${staging_dir}/shareto-component.pkg" \
    --identifier io.github.huadaox.shareto \
    --version "${package_version}" \
    "${repository_dir}/dist/shareto_${package_version}_darwin_universal.pkg"

install -m 0755 "${payload_dir}/usr/local/bin/shareto" "${archive_dir}/shareto"
install -m 0755 "${repository_dir}/packaging/install-macos.sh" "${archive_dir}/install.sh"
install -m 0644 "${repository_dir}/docs/MACOS.md" "${archive_dir}/MACOS.md"
install -m 0644 "${repository_dir}/LICENSE" "${archive_dir}/LICENSE"
COPYFILE_DISABLE=1 tar -czf "${repository_dir}/dist/shareto_${package_version}_darwin_universal.tar.gz" \
    -C "${staging_dir}/archive" shareto

cd "${repository_dir}/dist"
shasum -a 256 "shareto_${package_version}_darwin_universal.pkg" \
    "shareto_${package_version}_darwin_universal.tar.gz" > sha256sums-macos.txt
