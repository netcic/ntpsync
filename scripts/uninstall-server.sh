#!/usr/bin/env bash
# uninstall-server.sh - 局域网 NTP 授时系统 服务端卸载（Linux / 国产 OS）
#
# 用法（root 执行）:
#   bash uninstall-server.sh
#
# 行为（与 install-server.sh 一一对应清理）:
#   1. 停止并禁用 systemd 服务 Sntpserver
#   2. 删除 systemd unit /etc/systemd/system/Sntpserver.service
#   3. 删除二进制 /usr/local/bin/Sntpserver
#   4. 撤销防火墙放行（firewalld / ufw，存在才处理）
#
# 说明: 脚本对「服务不存在」「文件已删」「防火墙规则不存在」均容错，重复执行无副作用。
set -uo pipefail

[ "$(id -u)" -eq 0 ] || { echo "请以 root 运行本脚本。" >&2; exit 1; }

SVC="Sntpserver"
UNIT="/etc/systemd/system/${SVC}.service"
BIN="/usr/local/bin/${SVC}"

echo "[1/4] 停止并禁用服务 ${SVC} ..."
if systemctl list-unit-files "${SVC}.service" >/dev/null 2>&1 || [ -f "$UNIT" ]; then
  systemctl stop "${SVC}" >/dev/null 2>&1 || true
  systemctl disable "${SVC}" >/dev/null 2>&1 || true
else
  echo "  服务 ${SVC} 未安装，跳过。"
fi

echo "[2/4] 删除 systemd unit ..."
if [ -f "$UNIT" ]; then
  rm -f "$UNIT"
  systemctl daemon-reload
  echo "  已删除 $UNIT"
else
  echo "  $UNIT 不存在，跳过。"
fi

echo "[3/4] 删除二进制 ..."
if [ -f "$BIN" ]; then
  rm -f "$BIN"
  echo "  已删除 $BIN"
else
  echo "  $BIN 不存在，跳过。"
fi

echo "[4/4] 撤销防火墙放行（如存在）..."
if command -v firewall-cmd >/dev/null 2>&1; then
  firewall-cmd --permanent --remove-service=ntp >/dev/null 2>&1 || true
  firewall-cmd --reload >/dev/null 2>&1 || true
fi
if command -v ufw >/dev/null 2>&1; then
  ufw delete allow 123/udp >/dev/null 2>&1 || true
fi

echo ""
echo "[完成] 服务端已卸载。"
