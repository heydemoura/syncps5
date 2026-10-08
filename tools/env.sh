# Source this file: toolchain locations for syncps5. Every value can be
# overridden from the environment.
SYNCPS5_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export SYNCPS5_ROOT
export TOOLCHAIN_DIR="${TOOLCHAIN_DIR:-$SYNCPS5_ROOT/toolchain}"
export PS5_PAYLOAD_SDK="${PS5_PAYLOAD_SDK:-$TOOLCHAIN_DIR/ps5-payload-sdk}"
export PS5_GOROOT="${PS5_GOROOT:-$TOOLCHAIN_DIR/go-ps5}"
if [ -z "${LLVM_CONFIG:-}" ] && [ -x "$TOOLCHAIN_DIR/llvm/bin/llvm-config" ]; then
  export LLVM_CONFIG="$TOOLCHAIN_DIR/llvm/bin/llvm-config"
fi
export PS5_HOST="${PS5_HOST:-192.168.0.221}"
export PS5_PORT="${PS5_PORT:-9021}"
export SYNCPS5_LOG_PORT="${SYNCPS5_LOG_PORT:-8385}"
