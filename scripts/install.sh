#!/bin/sh
# Install the local-ci release binary on macOS or Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/DiversioTeam/local-ci-runner/main/scripts/install.sh | sh
#
# Environment:
#   LOCAL_CI_VERSION       version to install, without the leading "v" (default: latest release)
#   LOCAL_CI_INSTALL_DIR   directory to install into (default: $HOME/.local/bin)
set -eu

REPO="DiversioTeam/local-ci-runner"
INSTALL_DIR="${LOCAL_CI_INSTALL_DIR:-${HOME}/.local/bin}"
VERSION="${LOCAL_CI_VERSION:-}"

fail() {
  echo "local-ci install: $1" >&2
  exit 1
}

require() {
  command -v "$1" >/dev/null 2>&1 || fail "'$1' is required but not installed"
}

# Downloads $1 to $2, preferring curl and falling back to wget.
download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2" || fail "failed to download $1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1" || fail "failed to download $1"
  else
    fail "either 'curl' or 'wget' is required but neither is installed"
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "either 'sha256sum' or 'shasum' is required to verify the download"
  fi
}

case "$(uname -s)" in
  Darwin) OS="darwin" ;;
  Linux)  OS="linux" ;;
  *) fail "unsupported operating system: $(uname -s); only macOS and Linux have release builds" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64)  ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
  *) fail "unsupported architecture: $(uname -m); only amd64 and arm64 have release builds" ;;
esac

require tar

if [ -z "$VERSION" ]; then
  TMP_RELEASE="$(mktemp)"
  # shellcheck disable=SC2064
  trap "rm -f '$TMP_RELEASE'" EXIT
  download "https://api.github.com/repos/${REPO}/releases/latest" "$TMP_RELEASE"
  VERSION="$(sed -n 's/.*"tag_name"[ ]*:[ ]*"v\{0,1\}\([^"]*\)".*/\1/p' "$TMP_RELEASE" | head -n 1)"
  rm -f "$TMP_RELEASE"
  trap - EXIT
  [ -n "$VERSION" ] || fail "could not determine the latest release; set LOCAL_CI_VERSION to install a specific version"
fi

VERSION="${VERSION#v}"
ARCHIVE="local-ci_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"

WORK_DIR="$(mktemp -d)"
# shellcheck disable=SC2064
trap "rm -rf '$WORK_DIR'" EXIT

echo "local-ci install: downloading v${VERSION} for ${OS}/${ARCH}"
download "${BASE_URL}/${ARCHIVE}" "${WORK_DIR}/${ARCHIVE}"
download "${BASE_URL}/checksums.txt" "${WORK_DIR}/checksums.txt"

EXPECTED_SHA="$(awk -v archive="$ARCHIVE" '$2 == archive {print $1}' "${WORK_DIR}/checksums.txt")"
[ -n "$EXPECTED_SHA" ] || fail "no checksum published for ${ARCHIVE}"
ACTUAL_SHA="$(sha256_of "${WORK_DIR}/${ARCHIVE}")"
if [ "$EXPECTED_SHA" != "$ACTUAL_SHA" ]; then
  fail "checksum mismatch for ${ARCHIVE}: expected ${EXPECTED_SHA}, got ${ACTUAL_SHA}"
fi

# --no-same-owner: as root, tar would otherwise restore the build machine's
# uid/gid onto the installed binary instead of leaving it owned by root.
tar -xzf "${WORK_DIR}/${ARCHIVE}" -C "$WORK_DIR" --no-same-owner \
  || fail "failed to extract ${ARCHIVE}"
[ -f "${WORK_DIR}/local-ci" ] || fail "${ARCHIVE} did not contain a local-ci binary"

mkdir -p "$INSTALL_DIR" || fail "could not create ${INSTALL_DIR}"
chmod 755 "${WORK_DIR}/local-ci"
# Replacing by rename keeps the install atomic and survives a running binary.
mv -f "${WORK_DIR}/local-ci" "${INSTALL_DIR}/local-ci" || fail "could not install into ${INSTALL_DIR}"

echo "local-ci install: installed v${VERSION} to ${INSTALL_DIR}/local-ci"

case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo "local-ci install: ${INSTALL_DIR} is not on your PATH; add this to your shell profile:"
    echo ""
    echo "    export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac
