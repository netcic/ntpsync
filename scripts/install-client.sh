#!/usr/bin/env bash
# install-client.sh - 局域网 NTP 授时系统 客户端静默部署（Linux / 国产 OS）
#
# 用法（root 执行）:
#   bash install-client.sh --server 127.0.0.1:123 [--interval 15分] [--step 5秒] [--web 8090] [--no-start]
#
# 行为:
#   1. 安装 SntpClient 到 /usr/local/bin
#   2. 写 systemd unit（常驻 daemon，周期校时 + 本地 Web 管理界面）
#   3. 启用并启动
#
# 说明: 客户端以常驻守护进程周期校时（区别于旧的 oneshot timer），
#       并内置本地 Web 界面（默认 http://127.0.0.1:8090）查看校时状态。
#       Linux 无系统托盘，仅以常驻守护进程运行。
set -euo pipefail

SERVER=""
INTERVAL="15分"
STEP="5秒"
WEB="8090"
NO_START=0

while [ $# -gt 0 ]; do
  case "$1" in
    --server)   SERVER="$2"; shift 2 ;;
    --interval) INTERVAL="$2"; shift 2 ;;
    --step)     STEP="$2"; shift 2 ;;
    --web)      WEB="$2"; shift 2 ;;
    --no-start) NO_START=1; shift ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

[ -n "$SERVER" ] || { echo "缺少 --server，例如: --server 127.0.0.1:123" >&2; exit 2; }
[ "$(id -u)" -eq 0 ] || { echo "请以 root 运行本脚本。" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_SRC="${SCRIPT_DIR}/SntpClient"
BIN_DST="/usr/local/bin/SntpClient"

[ -f "$BIN_SRC" ] || { echo "未找到 $BIN_SRC，请将 SntpClient 与本脚本放同一目录。" >&2; exit 1; }

echo "[1/3] 安装二进制 -> $BIN_DST"
install -m 0755 "$BIN_SRC" "$BIN_DST"

# 组装 ExecStart 参数（常驻 daemon + 可选 Web 界面）
RUN_ARGS="daemon -s $SERVER -interval $INTERVAL -step $STEP"
[ -n "$WEB" ] && RUN_ARGS="$RUN_ARGS -web 127.0.0.1:$WEB"

echo "[2/3] 配置 systemd 常驻校时服务"
cat > /etc/systemd/system/SntpClient.service <<EOF
[Unit]
Description=局域网 NTP 客户端校时守护
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=$BIN_DST $RUN_ARGS
Restart=on-failure
RestartSec=5
# 校时所需能力
AmbientCapabilities=CAP_SYS_TIME
CapabilityBoundingSet=CAP_SYS_TIME

[Install]
WantedBy=multi-user.target
EOF

echo "[3/3] 启用并启动服务"
systemctl daemon-reload
systemctl enable SntpClient >/dev/null
if [ "$NO_START" -eq 0 ]; then
  systemctl restart SntpClient
  sleep 1
  systemctl --no-pager --full status SntpClient || true
else
  echo "已安装但未启动（--no-start），手动启动: systemctl start SntpClient"
fi

echo ""
echo "[完成] 客户端已部署。"
echo "  服务端: $SERVER  间隔: $INTERVAL  护栏: $STEP"
[ -n "$WEB" ] && echo "  本地 Web 界面: http://127.0.0.1:$WEB"
echo "  管理: systemctl {start|stop|restart|status} SntpClient"
