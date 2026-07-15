#!/usr/bin/env bash
# Build the fixter-collector OCB distribution binary into ./_build/.
# Downloads the pinned ocb release if not already present.
set -euo pipefail
cd "$(dirname "$0")/.."

OCB_VERSION="0.156.0"

case "$(uname -m)" in
  x86_64)        ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "build.sh: unsupported architecture $(uname -m); ocb ships amd64/arm64 only" >&2; exit 1 ;;
esac

# The release tag contains a slash, which must be URL-encoded as %2F or curl 404s.
OCB_URL="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/cmd%2Fbuilder%2Fv${OCB_VERSION}/ocb_${OCB_VERSION}_linux_${ARCH}"

if [ ! -x ./ocb ]; then
  curl --proto '=https' --tlsv1.2 -fL -o ocb "$OCB_URL"
  chmod +x ocb
fi

# --skip-strict-versioning=false is a weak guard (it misses a component pinned a
# minor back); the real guard is the `components` assertion in CI.
./ocb --config builder-config.yaml --skip-strict-versioning=false
