#!/bin/sh
# ft-clean：供 cron 调用的非交互清理脚本。
#
# 必填环境变量（或写入 FT_ADMIN_ENV 指向的文件）：
#   FT_ADMIN_USER / FT_ADMIN_PASSWORD
# 可选：
#   FT_SERVER_URL       服务器地址，无需 -config
#   FT_ADMIN_CONFIG     配置文件路径（与 ft-client 共用 config.toml）
#   FT_ADMIN_BIN        ft-admin 路径，默认 /usr/local/bin/ft-admin
#   FT_ADMIN_ENV        环境文件，默认 /etc/file-transfer/ft-admin.env
#
# 示例：
#   ft-clean.sh inbound --days 3
#   ft-clean.sh inbound --dry-run
set -eu

env_file="${FT_ADMIN_ENV:-/etc/file-transfer/ft-admin.env}"
if [ -r "$env_file" ]; then
	. "$env_file"
fi

if [ -z "${FT_ADMIN_USER:-}" ] || [ -z "${FT_ADMIN_PASSWORD:-}" ]; then
	echo "ft-clean: 缺少 FT_ADMIN_USER / FT_ADMIN_PASSWORD" >&2
	exit 1
fi

ft_admin="${FT_ADMIN_BIN:-/usr/local/bin/ft-admin}"
if [ -n "${FT_ADMIN_CONFIG:-}" ]; then
	exec "$ft_admin" -config "$FT_ADMIN_CONFIG" clean "$@" --yes
fi
exec "$ft_admin" clean "$@" --yes