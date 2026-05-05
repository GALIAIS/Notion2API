#!/bin/sh
set -eu

CONFIG_PATH="${NOTION2API_CONFIG_PATH:-/app/data/config.json}"
DEFAULT_CONFIG_PATH="${NOTION2API_DEFAULT_CONFIG_PATH:-/app/config/config.default.json}"

if [ ! -f "$CONFIG_PATH" ] && [ -f "$DEFAULT_CONFIG_PATH" ]; then
  mkdir -p "$(dirname "$CONFIG_PATH")"
  cp "$DEFAULT_CONFIG_PATH" "$CONFIG_PATH"
  echo "[entrypoint] $CONFIG_PATH not found; copied default template from $DEFAULT_CONFIG_PATH" >&2
fi

exec "$@"
