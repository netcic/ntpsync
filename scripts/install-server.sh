#!/usr/bin/env bash
# install-server.sh - 局域网 NTP 授时系统 服务端静默部署（Linux / 国产 OS）
#
# 用法（root 执行）:
#   bash install-server.sh [--listen 0.0.0.0:123] [--web 8080] [--upstream ntp.ntsc.ac.cn] [--upstream-interval 1小时] [--no-start]
#
# 行为:
#   1. 安装 Sntpserver 到 /usr/local/bin
#   2. 写 systemd unit（Sntpserver.service，常驻授时 + Web 管理界面）并 enable
#   3. 放行防火墙（firewalld / ufw 二选一，存在才配）
#   4. 启动服务
#
# 说明: Linux 无系统托盘，仅以常驻守护进程运行；
#       Web 管理界面默认启用，浏览器访问 http://127.0.0.1:8080。
#
# 适配: systemd 系（CentOS/RHEL/UOS/麒麟/Ubuntu 等）；无 systemd 的旧版见 README 兜底说明。
set -euo pipefail

LISTEN="0.0.0.0:123"
WEB="8080"
UPSTREAM=""
UPSTREAM_INTERVAL="1小时"
NO_START=0

while [ $# -gt 0 ]; do
  case "$1" in
    --listen)  LISTEN="$2"; shift 2 ;;
    --web)     WEB="$2"; shift 2 ;;
    --upstream) UPSTREAM="$2"; shift 2 ;;
    --upstream-interval) UPSTREAM_INTERVAL="$2"; shift 2 ;;
    --no-start) NO_START=1; shift ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

[ "$(id -u)" -eq 0 ] || { echo "请以 root 运行本脚本。" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_SRC="${SCRIPT_DIR}/Sntpserver"
BIN_DST="/usr/local/bin/Sntpserver"

[ -f "$BIN_SRC" ] || { echo "未找到 $BIN_SRC，请将 Sntpserver 与本脚本放同一目录。" >&2; exit 1; }

echo "[1/4] 安装二进制 -> $BIN_DST"
install -m 0755 "$BIN_SRC" "$BIN_DST"

# 组装 ExecStart 参数
RUN_ARGS="run -l $LISTEN"
[ -n "$WEB" ] && RUN_ARGS="$RUN_ARGS -web 127.0.0.1:$WEB"
[ -n "$UPSTREAM" ] && RUN_ARGS="$RUN_ARGS -upstream $UPSTREAM -upstream-interval $UPSTREAM_INTERVAL"

echo "[2/4] 写 systemd unit -> /etc/systemd/system/Sntpserver.service"
cat > /etc/systemd/system/Sntpserver.service <<EOF
[Unit]
Description=Sntpserver
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=$BIN_DST $RUN_ARGS
Restart=on-failure
RestartSec=5
# 校时/授时所需能力
AmbientCapabilities=CAP_SYS_TIME CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_SYS_TIME CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

echo "[3/4] 放行防火墙（如存在）"
if command -v firewall-cmd >/dev/null 2>&1; then
  firewall-cmd --permanent --add-service=ntp >/dev/null 2>&1 || true
  firewall-cmd --reload >/dev/null 2>&1 || true
fi
if command -v ufw >/dev/null 2>&1; then
  ufw allow 123/udp >/dev/null 2>&1 || true
fi

echo "[4/4] 启用并启动服务"
systemctl daemon-reload
systemctl enable Sntpserver >/dev/null
if [ "$NO_START" -eq 0 ]; then
  systemctl restart Sntpserver
  sleep 1
  systemctl --no-pager --full status Sntpserver || true
else
  echo "已安装但未启动（--no-start），手动启动: systemctl start Sntpserver"
fi

echo ""
echo "[完成] 服务端已部署。"
echo "  监听: $LISTEN"
[ -n "$UPSTREAM" ] && echo "  上游时间源: $UPSTREAM（每 $UPSTREAM_INTERVAL 同步）"
[ -n "$WEB" ] && echo "  Web 界面: http://127.0.0.1:$WEB"
echo "  管理: systemctl {start|stop|restart|status} Sntpserver"

