#!/usr/bin/env bash
# Set up everything needed to build syncps5 under ./toolchain:
#   - ps5-payload-sdk (prebuilt release)
#   - Go 1.27.1 with patches/go1.27.1-ps5.patch applied
#   - an LLVM overlay directory (clang + ld.lld side by side), needed when
#     clang and lld live in different prefixes (e.g. Homebrew)
# Requires: clang/lld >= 18, curl, unzip, tar, git.
set -euo pipefail
source "$(dirname "$0")/env.sh"

GO_VERSION=go1.27.1
SDK_URL=https://github.com/ps5-payload-dev/sdk/releases/download/v0.43/ps5-payload-sdk.zip

mkdir -p "$TOOLCHAIN_DIR"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

if [ ! -x "$PS5_PAYLOAD_SDK/bin/prospero-clang" ]; then
  echo ">> ps5-payload-sdk"
  curl -fsSL -o "$tmp/sdk.zip" "$SDK_URL"
  unzip -q -o "$tmp/sdk.zip" -d "$(dirname "$PS5_PAYLOAD_SDK")"
fi

if [ ! -x "$PS5_GOROOT/bin/go" ] || ! grep -q ps5 "$PS5_GOROOT/src/syscall/ps5_freebsd_amd64.go" 2>/dev/null; then
  echo ">> $GO_VERSION + PS5 patch"
  curl -fsSL -o "$tmp/go.tgz" "https://go.dev/dl/$GO_VERSION.linux-amd64.tar.gz"
  rm -rf "$PS5_GOROOT"; mkdir -p "$tmp/go"
  tar -C "$tmp/go" -xzf "$tmp/go.tgz"
  mv "$tmp/go/go" "$PS5_GOROOT"
  (cd "$PS5_GOROOT" && git apply -p1 "$SYNCPS5_ROOT/patches/go1.27.1-ps5.patch")
  cp "$PS5_GOROOT/bin/go" "$PS5_GOROOT/bin/go-bootstrap"
  (cd "$PS5_GOROOT" && GOTOOLCHAIN=local GOROOT="$PS5_GOROOT" ./bin/go-bootstrap install cmd/go cmd/link)
fi

# LLVM overlay: the SDK looks for ld.lld next to clang (llvm-config --bindir).
find_tool() { for c in "$@"; do command -v "$c" 2>/dev/null && return 0; done; return 1; }
LLVM_CFG_REAL="$(find_tool llvm-config-23 llvm-config-22 llvm-config-21 llvm-config-20 llvm-config-19 llvm-config-18 llvm-config)" || { echo "llvm-config not found"; exit 1; }
LLD="$(find_tool ld.lld "$( (brew --prefix lld 2>/dev/null || true) )/bin/ld.lld")" || { echo "ld.lld not found"; exit 1; }
LLVM_BIN="$("$LLVM_CFG_REAL" --bindir)"
O="$TOOLCHAIN_DIR/llvm/bin"
rm -rf "$TOOLCHAIN_DIR/llvm"; mkdir -p "$O"
for f in "$LLVM_BIN"/*; do ln -s "$f" "$O/"; done
ln -sf "$LLD" "$O/ld.lld"
rm -f "$O/llvm-config"
cat > "$O/llvm-config" <<EOS
#!/usr/bin/env bash
if [ "\$1" = "--bindir" ]; then echo "$O"; exit 0; fi
exec "$LLVM_CFG_REAL" "\$@"
EOS
chmod +x "$O/llvm-config"

echo ">> toolchain ready in $TOOLCHAIN_DIR"
"$PS5_GOROOT/bin/go" version
