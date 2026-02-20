#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

CONFIG_PATH="${1:-$ROOT_DIR/config.yml}"
MODE="${2:-hybrid}"

if [ ! -f "$CONFIG_PATH" ]; then
  echo "Error: config file tidak ditemukan: $CONFIG_PATH"
  echo "Usage: ./scripts/migrate-config.sh [config-path] [hybrid|inline]"
  exit 1
fi

echo "Migrating $CONFIG_PATH (mode=$MODE)..."
(
  cd "$ROOT_DIR"
  go run ./cmd/config-migrator --config "$CONFIG_PATH" --mode "$MODE"
)
