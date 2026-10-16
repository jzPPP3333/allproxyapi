#!/usr/bin/env bash
# Cross-platform build for AllProxyAPI.
#
#   ./build.sh                 # every target
#   ./build.sh linux/amd64     # one or more explicit targets
#
# Output lands in dist/ as a .tar.gz per target (plus a ZIP for Windows).

set -euo pipefail

APP="allproxyapi"
PKG="./cmd/proxy"
DIST="dist"

# Version metadata baked into the binary. A git checkout supplies the revision;
# anything else falls back to a placeholder so the build never fails here.
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}"

# os/arch pairs. arm64 covers Apple Silicon and ARM servers; 386 is omitted
# because a 32-bit address space is not useful for a long-lived proxy.
DEFAULT_TARGETS=(
  "windows/amd64"
  "windows/arm64"
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
)

if [ "$#" -gt 0 ]; then
  TARGETS=("$@")
else
  TARGETS=("${DEFAULT_TARGETS[@]}")
fi

rm -rf "${DIST}"
mkdir -p "${DIST}"

echo "AllProxyAPI ${VERSION} (${COMMIT}) built ${BUILD_DATE}"
echo

for target in "${TARGETS[@]}"; do
  GOOS="${target%%/*}"
  GOARCH="${target##*/}"

  name="${APP}-${GOOS}-${GOARCH}"
  [ "${GOOS}" = "windows" ] && name="${name}.exe"

  echo "==> ${GOOS}/${GOARCH}"

  # CGO off keeps the binary static and cross-compilable without a C toolchain.
  CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -trimpath -ldflags "${LDFLAGS}" -o "${DIST}/${name}" "${PKG}"

  # Ship the example config and the license alongside each binary.
  stage="$(mktemp -d)"
  cp "${DIST}/${name}" "${stage}/"
  cp data/config.example.yaml "${stage}/"
  cp README.md LICENSE "${stage}/" 2>/dev/null || true

  # Windows conventionally ships a .zip, but tar.gz is universally readable and
  # is the fallback when zip is not installed.
  if [ "${GOOS}" = "windows" ] && command -v zip >/dev/null 2>&1; then
    (cd "${stage}" && zip -q "${OLDPWD}/${DIST}/${APP}-${GOOS}-${GOARCH}.zip" ./*)
  else
    tar -czf "${DIST}/${APP}-${GOOS}-${GOARCH}.tar.gz" -C "${stage}" .
  fi
  rm -rf "${stage}"

  ls -lh "${DIST}/" | grep "${GOOS}-${GOARCH}" || true
done

echo
echo "Artifacts in ${DIST}/:"
ls -1 "${DIST}"
