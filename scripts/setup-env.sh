#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ENV_EXAMPLE="$ROOT_DIR/.env.example"
ENV_FILE="$ROOT_DIR/.env"

if [ ! -f "$ENV_EXAMPLE" ]; then
  echo "Error: $ENV_EXAMPLE tidak ditemukan"
  exit 1
fi

if [ -f "$ENV_FILE" ]; then
  echo "Lewati: .env sudah ada di $ENV_FILE"
  echo "Tip: edit manual jika ingin mengganti nilai"
  exit 0
fi

cp "$ENV_EXAMPLE" "$ENV_FILE"
chmod 600 "$ENV_FILE"
echo "Berhasil membuat .env dari .env.example"
echo "Langkah selanjutnya:"
echo "1) Edit $ENV_FILE dan isi nilai secret yang benar"
echo "2) Jalankan: . ./scripts/export-env.sh"
