#!/usr/bin/env bash
# Build the fixter-collector OCB distribution binary into ./_build/.
# Downloads the pinned ocb release if not already present.
#
# TWO architectures are in play and they are not the same thing:
#   OCB_ARCH    - the machine running this script. Picks which ocb binary to download.
#   TARGET_ARCH - what the collector is compiled FOR. Defaults to the host; set it
#                 to cross-compile:  TARGET_ARCH=amd64 ./scripts/build.sh
#
# Conflating the two is what shipped an arm64-only image (customer hit
# "exec format error" on amd64 nodes, 2026-08-19). ocb execs `go build` with
# os.Environ() inherited, so GOARCH here is authoritative; builder-config.yaml
# leaves cgo_enabled unset, so ocb forces CGO_ENABLED=0 and the cross-compile is
# exact — no cross toolchain, no emulation.
set -euo pipefail
cd "$(dirname "$0")/.."

OCB_VERSION="0.156.0"

case "$(uname -m)" in
  x86_64)        OCB_ARCH=amd64 ;;
  aarch64|arm64) OCB_ARCH=arm64 ;;
  *) echo "build.sh: unsupported build host $(uname -m); ocb ships amd64/arm64 only" >&2; exit 1 ;;
esac

TARGET_ARCH="${TARGET_ARCH:-$OCB_ARCH}"

# The release tag contains a slash, which must be URL-encoded as %2F or curl 404s.
OCB_URL="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/cmd%2Fbuilder%2Fv${OCB_VERSION}/ocb_${OCB_VERSION}_linux_${OCB_ARCH}"

if [ ! -x ./ocb ]; then
  curl --proto '=https' --tlsv1.2 -fL -o ocb "$OCB_URL"
  chmod +x ocb
fi

# Clear the convenience symlink first: `go build -o` FOLLOWS symlinks, so a stale
# link left by a previous pass would make this pass overwrite the other arch's
# binary in place.
rm -f _build/fixter-collector

# --skip-strict-versioning=false is a weak guard (it misses a component pinned a
# minor back); the real guard is the `components` assertion in CI.
GOOS=linux GOARCH="$TARGET_ARCH" ./ocb --config builder-config.yaml --skip-strict-versioning=false

# builder-config.yaml pins output_path to ./_build, so every pass writes the same
# filename. Park each build under its own arch dir so a second pass cannot clobber
# the first. Generated sources stay in _build/ and are reused, so the second arch
# only pays for compilation.
mkdir -p "_build/linux_${TARGET_ARCH}"
mv -f "_build/fixter-collector" "_build/linux_${TARGET_ARCH}/fixter-collector"

# Restore _build/fixter-collector as "the binary for this machine" whenever a host
# build exists — validate.sh, smoke.sh and the CI component assertions all expect
# that path, and they must keep working no matter which arch was built last.
if [ -x "_build/linux_${OCB_ARCH}/fixter-collector" ]; then
  ln -sfn "linux_${OCB_ARCH}/fixter-collector" "_build/fixter-collector"
fi
