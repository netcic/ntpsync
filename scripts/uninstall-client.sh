#!/usr/bin/env bash
# uninstall-client.sh - 局域网 NTP 授时系统 客户端卸载（Linux / 国产 OS）
#
# 用法（root 执行）:
#   bash uninstall-client.sh
#
# 行为（与 install-client.sh 一一对应清理）:
#   1. 停止并禁用 systemd 服务 SntpClient
#   2. 删除 systemd unit /etc/systemd/system/SntpClient.service
#   3. 删除二进制 /usr/local/bin/SntpClient
#
# 说明: 脚本对「服务不存在」「文件已删」均容错，重复执行无副作用。
set -uo pipefail

[ "$(id -u)" -eq 0 ] || { echo "请以 root 运行本脚本。" >&2; exit 1; }

SVC="SntpClient"
UNIT="/etc/systemd/system/${SVC}.service"
BIN="/usr/local/bin/${SVC}"

echo "[1/3] 停止并禁用服务 ${SVC} ..."
if systemctl list-unit-files "${SVC}.service" >/dev/null 2>&1 || [ -f "$UNIT" ]; then
  systemctl stop "${SVC}" >/dev/null 2>&1 || true
  systemctl disable "${SVC}" >/dev/null 2>&1 || true
else
  echo "  服务 ${SVC} 未安装，跳过。"
fi

echo "[2/3] 删除 systemd unit ..."
if [ -f "$UNIT" ]; then
  rm -f "$UNIT"
  systemctl daemon-reload
  echo "  已删除 $UNIT"
else
  echo "  $UNIT 不存在，跳过。"
fi

echo "[3/3] 删除二进制 ..."
if [ -f "$BIN" ]; then
  rm -f "$BIN"
  echo "  已删除 $BIN"
else
  echo "  $BIN 不存在，跳过。"
fi

echo ""
echo "[完成] 客户端已卸载。"
