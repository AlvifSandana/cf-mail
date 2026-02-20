#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ENV_FILE="$ROOT_DIR/.env"

if [ ! -f "$ENV_FILE" ]; then
  echo "Error: .env belum ada di $ENV_FILE"
  echo "Jalankan dulu: ./scripts/setup-env.sh"
  return 1 2>/dev/null || exit 1
fi

load_env_file() {
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      "" | \#*)
        continue
        ;;
    esac

    case "$line" in
      *=*)
        key=${line%%=*}
        value=${line#*=}
        ;;
      *)
        echo "Error: format .env tidak valid: $line"
        return 1
        ;;
    esac

    case "$key" in
      [A-Za-z_][A-Za-z0-9_]*)
        ;;
      *)
        echo "Error: nama variabel tidak valid: $key"
        return 1
        ;;
    esac

    case "$value" in
      \"*\")
        value=${value#\"}
        value=${value%\"}
        ;;
      "'"*"'")
        value=${value#"'"}
        value=${value%"'"}
        ;;
    esac

    case "$value" in
      *'`'* | *'$('* | *'${'* | *';'* | *'&&'* | *'|'*)
        echo "Error: value untuk $key mengandung karakter berbahaya"
        return 1
        ;;
    esac

    export "$key=$value"
  done < "$ENV_FILE"
}

load_env_file

: "${CF_API_TOKEN:?CF_API_TOKEN wajib diisi di .env}"
: "${IMAP_APP_PASSWORD:?IMAP_APP_PASSWORD wajib diisi di .env}"

echo "Environment variables loaded dari $ENV_FILE"
echo "Cek cepat:"
echo "- CF_API_TOKEN      : ${CF_API_TOKEN:+(set)}"
echo "- IMAP_APP_PASSWORD : ${IMAP_APP_PASSWORD:+(set)}"
echo ""
echo "Sekarang kamu bisa jalankan aplikasi (pakai config real):"
echo "go run ./cmd/tuiotp --config ./config.yml"
