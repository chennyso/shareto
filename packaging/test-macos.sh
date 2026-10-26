#!/usr/bin/env bash
set -euo pipefail

repository_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
package_version="${VERSION:-$(make -s -C "${repository_dir}" print-version)}"
mkdir -p "${repository_dir}/work"
verification_dir="$(mktemp -d "${repository_dir}/work/macos-package-test.XXXXXX")"
trap 'rm -rf "${verification_dir}"' EXIT

tar -xzf "${repository_dir}/dist/shareto_${package_version}_darwin_universal.tar.gz" -C "${verification_dir}"
bash "${verification_dir}/shareto/install.sh" "${verification_dir}/installation"
installed_binary="${verification_dir}/installation/bin/shareto"
"${installed_binary}" version
/usr/bin/lipo "${installed_binary}" -verify_arch arm64 x86_64
/usr/bin/codesign --verify --strict "${installed_binary}"

/usr/sbin/pkgutil --expand-full "${repository_dir}/dist/shareto_${package_version}_darwin_universal.pkg" "${verification_dir}/package"
"${verification_dir}/package/shareto-component.pkg/Payload/usr/local/bin/shareto" version
cd "${repository_dir}/dist"
shasum -a 256 -c sha256sums-macos.txt
