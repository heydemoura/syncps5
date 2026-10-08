#!/usr/bin/env bash
# Install the payload into Payload Manager (pldmgr) over FTP, as
# /data/pldmgr/payloads/syncps5/syncps5.elf, where pldmgr lists it and its
# autoloader can find it. Needs an FTP server on the console (e.g.
# ps5-payload-dev/ftpsrv on port 2121, or zftpd).
#   tools/install-pldmgr.sh [--run] [file.elf]
#     --run   also start it through pldmgr (port 8084)
set -euo pipefail
source "$(dirname "$0")/env.sh"
FTP_PORT="${PS5_FTP_PORT:-2121}"
PLDMGR_PORT="${PLDMGR_PORT:-8084}"
DEST_DIR=/data/pldmgr/payloads/syncps5
DEST="$DEST_DIR/syncps5.elf"

run=0
if [ "${1:-}" = "--run" ]; then run=1; shift; fi
ELF="${1:-$SYNCPS5_ROOT/out/syncps5.elf}"
[ -f "$ELF" ] || { echo "no $ELF; run tools/build.sh"; exit 1; }

ftp="ftp://$PS5_HOST:$FTP_PORT"
echo ">> uploading $(basename "$ELF") to $DEST"
curl -sS --ftp-create-dirs -T "$ELF" "$ftp$DEST_DIR/syncps5.elf.part"
# Replace the old copy. Some FTP servers report errors for commands that
# worked, so only the final listing decides whether this succeeded.
curl -s -o /dev/null "$ftp/" -Q "DELE $DEST" 2>/dev/null || true
curl -s -o /dev/null "$ftp/" -Q "RNFR $DEST_DIR/syncps5.elf.part" -Q "RNTO $DEST" 2>/dev/null || true
size="$(curl -sS "$ftp$DEST_DIR/" | awk '$NF=="syncps5.elf"{print $5}')"
[ "$size" = "$(stat -c %s "$ELF")" ] || { echo "upload size mismatch: ${size:-missing}"; exit 1; }
echo ">> installed ($size bytes)"

if [ "$run" = 1 ]; then
  echo ">> starting it through pldmgr"
  curl -sS "http://$PS5_HOST:$PLDMGR_PORT/loadpayload:syncps5.elf"; echo
fi
