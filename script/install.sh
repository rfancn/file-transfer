#!/bin/sh
set -eu

# ============================================================
# ft-client 安装器
# 只安装 ft-client 和客户端配置（先执行 make build）。
# 可用 FT_SRC_DIR 指定源码目录，FT_SERVER_URL 跳过交互。
# 需要 root 权限。
# ============================================================

SRC_DIR="${FT_SRC_DIR:-$(cd "$(dirname "$0")/.." && pwd)}"
CLIENT_BIN="$SRC_DIR/bin/ft-client"
CLIENT_CONF="$SRC_DIR/config/client.example.toml"
PREFIX="${PREFIX:-/usr/local}"
CONF_DIR="${FT_CONF_DIR:-/etc/file-transfer}"
CONF_FILE="$CONF_DIR/config.toml"
FT_SERVER_URL="${FT_SERVER_URL:-}"

error() {
  echo "❌ $*" >&2
}

[ "$(id -u)" -eq 0 ] || { error "请用 root 运行"; exit 1; }
command -v install >/dev/null 2>&1 || { error "缺少 install 命令"; exit 1; }
command -v awk >/dev/null 2>&1 || { error "缺少 awk 命令"; exit 1; }
[ -f "$CLIENT_BIN" ] || { error "未找到 $CLIENT_BIN，请先在项目根目录执行 make build"; exit 1; }
[ -f "$CLIENT_CONF" ] || { error "未找到 $CLIENT_CONF"; exit 1; }

# 已有配置时先回显，确认后才覆盖。
if [ -f "$CONF_FILE" ]; then
  echo "已存在配置文件: $CONF_FILE"
  echo "----------------------------------------"
  cat "$CONF_FILE"
  echo "----------------------------------------"
  printf "是否覆盖该配置？[y/N] "
  if ! IFS= read -r answer; then
    answer=""
  fi
  case "$answer" in
    y|Y|yes|YES) ;;
    *)
      echo "已取消，未做任何修改。"
      exit 0
      ;;
  esac
  backup="$CONF_FILE.bak.$(date +%Y%m%d%H%M%S)"
  cp -p "$CONF_FILE" "$backup"
  echo "已备份旧配置: $backup"
fi

# 收集需要写入配置的服务端地址。
if [ -z "$FT_SERVER_URL" ]; then
  printf "服务端地址（如 http://192.168.20.10:8080）: "
  if ! IFS= read -r FT_SERVER_URL; then
    error "未提供服务端地址"
    exit 1
  fi
fi

case "$FT_SERVER_URL" in
  https://*)
    error "客户端与服务端在同一局域网，请使用 http://"
    exit 1
    ;;
  http://?*) ;;
  *://*)
    error "只支持 http:// 地址"
    exit 1
    ;;
  *) FT_SERVER_URL="http://$FT_SERVER_URL" ;;
esac

mkdir -p "$PREFIX/bin" "$CONF_DIR"
install -m 0755 "$CLIENT_BIN" "$PREFIX/bin/ft-client"
chown root:root "$PREFIX/bin/ft-client"
echo "✅ 已安装 ft-client: $PREFIX/bin/ft-client"

tmp="$CONF_FILE.tmp.$$"
awk -v url="$FT_SERVER_URL" '/^[[:space:]]*url[[:space:]]*=/ { print "  url = \"" url "\""; next } { print }' "$CLIENT_CONF" > "$tmp"
chmod 0644 "$tmp"
mv "$tmp" "$CONF_FILE"
chown root:root "$CONF_FILE"
echo "✅ 已写入配置: $CONF_FILE（服务端 $FT_SERVER_URL）"

"$PREFIX/bin/ft-client" --version >/dev/null
echo "✅ ft-client 可执行"

echo "=========================================="
echo "安装完成。下一步：运行 ft-client"
echo "首次使用会引导登录或注册；注册用户名使用姓名全拼。"
echo "=========================================="