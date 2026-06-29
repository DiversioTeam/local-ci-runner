#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <version> <dist-dir> <output-path>" >&2
  exit 2
fi

VERSION="$1"
DIST_DIR="$2"
OUTPUT_PATH="$3"
TAG="v${VERSION}"
REPO="DiversioTeam/local-ci-runner"

sha_for() {
  local platform="$1"
  local archive="$DIST_DIR/local-ci_${VERSION}_${platform}.tar.gz"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$archive" | awk '{print $1}'
    return
  fi
  shasum -a 256 "$archive" | awk '{print $1}'
}

DARWIN_AMD64_SHA="$(sha_for darwin_amd64)"
DARWIN_ARM64_SHA="$(sha_for darwin_arm64)"
LINUX_AMD64_SHA="$(sha_for linux_amd64)"
LINUX_ARM64_SHA="$(sha_for linux_arm64)"

cat > "$OUTPUT_PATH" <<EOF
class LocalCi < Formula
  desc "Shared local CI runner for repo-owned verification steps"
  homepage "https://github.com/${REPO}"
  version "${VERSION}"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/${REPO}/releases/download/${TAG}/local-ci_${VERSION}_darwin_arm64.tar.gz"
      sha256 "${DARWIN_ARM64_SHA}"
    else
      url "https://github.com/${REPO}/releases/download/${TAG}/local-ci_${VERSION}_darwin_amd64.tar.gz"
      sha256 "${DARWIN_AMD64_SHA}"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/${REPO}/releases/download/${TAG}/local-ci_${VERSION}_linux_arm64.tar.gz"
      sha256 "${LINUX_ARM64_SHA}"
    else
      url "https://github.com/${REPO}/releases/download/${TAG}/local-ci_${VERSION}_linux_amd64.tar.gz"
      sha256 "${LINUX_AMD64_SHA}"
    end
  end

  def install
    bin.install "local-ci"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/local-ci version")
  end
end
EOF
