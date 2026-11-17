#!/bin/sh
# ft-clean：供 cron 调用的非交互清理脚本。
#
# 凭据文件 /etc/file-transfer/ft-admin.env（0600），内容：
#   ADMIN_USER=admin
#   ADMIN_PASSWORD=...
#
# 服务器地址从 /etc/file-transfer/ft-client.toml 的 [server] url 读取。
#
# 示例：
#   ft-clean.sh inbound --days 3
#   ft-clean.sh inbound --dry-run
set -eu

env_file="/etc/file-transfer/ft-admin.env"
if [ -r "$env_file" ]; then
	. "$env_file"
fi

if [ -z "${ADMIN_USER:-}" ] || [ -z "${ADMIN_PASSWORD:-}" ]; then
	echo "ft-clean: 缺少 ADMIN_USER / ADMIN_PASSWORD（$env_file）" >&2
	exit 1
fi

admin_bin="/usr/local/bin/ft-admin"
admin_config="/etc/file-transfer/ft-client.toml"
password_file=$(mktemp)
cleanup() { rm -f "$password_file"; }
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM
chmod 600 "$password_file"
printf '%s' "$ADMIN_PASSWORD" > "$password_file"
"$admin_bin" --config "$admin_config" --admin-user "$ADMIN_USER" --admin-password-file "$password_file" clean "$@" --yes