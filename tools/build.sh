#!/usr/bin/env bash
# Build out/syncps5.elf: Syncthing (patched Go, GOOS=freebsd, PIE) embedded in
# the C launcher (ps5-payload-sdk).
set -euo pipefail
source "$(dirname "$0")/env.sh"
cd "$SYNCPS5_ROOT"

: "${MAXPROCS:=4}"
B="$SYNCPS5_ROOT/build"
OUT="$SYNCPS5_ROOT/out"
ST="$B/syncthing"
mkdir -p "$B" "$OUT"

[ -f third_party/syncthing/go.mod ] || git submodule update --init third_party/syncthing
ST_VERSION="$(git -C third_party/syncthing describe --tags --always)"
SYNCPS5_VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

echo ">> preparing Syncthing $ST_VERSION tree"
rsync -a --delete --exclude .git third_party/syncthing/ "$ST/"
for p in patches/syncthing/*.patch; do
  # --git-dir=/dev/null: the build tree lives inside this repository, and a
  # plain "git apply" there would silently skip the files.
  (cd "$ST" && git --git-dir=/dev/null apply -p1 "$SYNCPS5_ROOT/$p")
done
grep -q "ps5ExitHook(int(status))" "$ST/cmd/syncthing/main.go" || { echo "syncthing patches did not apply"; exit 1; }
cp ps5/*.go "$ST/cmd/syncthing/"

export GOROOT="$PS5_GOROOT" GOTOOLCHAIN=local GOFLAGS=-mod=mod
export GOCACHE="$B/gocache" GOMODCACHE="${GOMODCACHE:-$B/gomodcache}" GOTMPDIR="$B/gotmp"
mkdir -p "$GOTMPDIR"
GO="$PS5_GOROOT/bin/go"

echo ">> adding PS5 dependencies"
(cd "$ST" && "$GO" get golang.org/x/crypto/x509roots/fallback@v0.0.0-20261005185213-c3db4df58582 >/dev/null)

echo ">> generating GUI assets"
(cd "$ST" && "$GO" generate github.com/syncthing/syncthing/lib/api/auto)

echo ">> building syncthing for the PS5"
LDFLAGS="-s -w -buildid="
LDFLAGS+=" -X github.com/syncthing/syncthing/lib/build.Version=$ST_VERSION"
LDFLAGS+=" -X github.com/syncthing/syncthing/lib/build.Stamp=$(git -C third_party/syncthing log -1 --format=%ct)"
LDFLAGS+=" -X github.com/syncthing/syncthing/lib/build.User=syncps5"
LDFLAGS+=" -X github.com/syncthing/syncthing/lib/build.Host=ps5"
LDFLAGS+=" -X github.com/syncthing/syncthing/lib/build.Tags=ps5,noupgrade"
(cd "$ST" && GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 "$GO" build -buildmode=pie -trimpath \
  -tags ps5,noupgrade -ldflags "$LDFLAGS" -o "$B/syncthing.bin" ./cmd/syncthing)

echo ">> building launcher"
"$PS5_PAYLOAD_SDK/bin/prospero-clang" -O2 -Wall \
  -DGO_IMAGE="\"$B/syncthing.bin\"" \
  -DGO_MAXPROCS="\"$MAXPROCS\"" \
  -DSYNCPS5_VERSION="\"$SYNCPS5_VERSION (syncthing $ST_VERSION)\"" \
  ${EXTRA_CFLAGS:-} \
  -o "$OUT/syncps5.elf" launcher/main.c launcher/goload.c launcher/report.c

ls -la "$OUT/syncps5.elf"
