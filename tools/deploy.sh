#!/usr/bin/env bash
# Send out/syncps5.elf to the ELF loader and, unless --no-install is given,
# append a copy that the launcher saves as /data/syncps5/syncps5.elf (used to
# relaunch on restart and by autoloaders). Prints what the launcher reports.
#   tools/deploy.sh [--no-install] [file.elf]
set -euo pipefail
source "$(dirname "$0")/env.sh"

install=1
if [ "${1:-}" = "--no-install" ]; then install=0; shift; fi
ELF="${1:-$SYNCPS5_ROOT/out/syncps5.elf}"
[ -f "$ELF" ] || { echo "no $ELF; run tools/build.sh"; exit 1; }

{
  cat "$ELF"
  if [ "$install" = 1 ]; then
    printf 'SYNCPS5INSTALL01'
    python3 -c 'import struct,sys,os; sys.stdout.buffer.write(struct.pack("<Q", os.path.getsize(sys.argv[1])))' "$ELF"
    cat "$ELF"
  fi
  sleep 20
} | timeout 30 nc "$PS5_HOST" "$PS5_PORT" || true
