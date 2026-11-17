#!/bin/sh
set -eu

config="/etc/file-transfer/ft-server.toml"
mkdir -p /app/secrets "$(dirname "$config")"
if [ ! -f "$config" ]; then
	cp /app/ft-server.toml.default "$config"
fi

exec /app/ft-server -c "$config"
