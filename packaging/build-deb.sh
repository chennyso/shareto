#!/usr/bin/env bash
set -euo pipefail

VERSION="${VERSION:-3.0.0}"
ARCH="${ARCH:-amd64}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${ROOT_DIR}/dist"
PKG_DIR="${DIST_DIR}/deb-pkg"

if [ ! -f "${DIST_DIR}/shareto" ]; then
    echo "Error: binary ${DIST_DIR}/shareto does not exist. Run 'make build' first."
    exit 1
fi

rm -rf "${PKG_DIR}"
mkdir -p "${PKG_DIR}/usr/bin"
mkdir -p "${PKG_DIR}/usr/lib/systemd/user"
mkdir -p "${PKG_DIR}/DEBIAN"

# Copy binary
install -m 0755 "${DIST_DIR}/shareto" "${PKG_DIR}/usr/bin/shareto"

# Copy systemd service
install -m 0644 "${ROOT_DIR}/packaging/shareto.service" "${PKG_DIR}/usr/lib/systemd/user/shareto.service"

# Copy postinst script
install -m 0755 "${ROOT_DIR}/packaging/postinst" "${PKG_DIR}/DEBIAN/postinst"

# Generate control file
cat <<EOF > "${PKG_DIR}/DEBIAN/control"
Package: shareto
Version: ${VERSION}
Section: utils
Priority: optional
Architecture: ${ARCH}
Depends: systemd
Maintainer: shareto developers <support@shareto.local>
Description: Safe and minimal large file distribution tool for Linux.
 A hardened, low-overhead daemon and CLI for sharing large files via HTTP(S) with
 atomic link quotas, subnet-locked sessions, Argon2id auth, and rate limiting.
EOF

DEB_NAME="shareto_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "${PKG_DIR}" "${DIST_DIR}/${DEB_NAME}"

echo "Successfully built ${DIST_DIR}/${DEB_NAME}"
rm -rf "${PKG_DIR}"
