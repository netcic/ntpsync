#!/usr/bin/env bash
# uninstall-client.sh - 局域网 NTP 授时系统 客户端卸载（Linux / 国产 OS）
#
# 用法（root 执行）:
#   bash uninstall-client.sh
#
# 行为（与 install-client.sh 一一对应清理）:
#   1. 停止并禁用 systemd 服务 SntpClient（连同 1.0.x 的 ntpsync）
#   2. 删除 systemd unit（及历史 unit）
#   3. 删除二进制（及历史二进制）
#   4. 撤销防火墙放行（Web 管理端口的 TCP；授时走 UDP，客户端不监听端口）
#   5. 删除本机配置（含原子写入留下的 .tmp），目录空了才删
#
# 只动本程序自己的东西：/etc/ntpsync 与客户端共用给服务端，目录里还有
# ntpsyncd.json / SntpServer-clients.json 时必须留着，所以用 rmdir（非空即失败）。
# 同理不碰 UDP 123 的放行——那是服务端的。
#
# 说明: 脚本对「服务不存在」「文件已删」均容错，重复执行无副作用。
set -uo pipefail

[ "$(id -u)" -eq 0 ] || { echo "请以 root 运行本脚本。" >&2; exit 1; }

SVC="SntpClient"
# 1.0.x 的客户端守护进程叫 ntpsync
LEGACY_SVCS=("ntpsync")
BIN="/usr/local/bin/${SVC}"
CFG_DIR="/etc/ntpsync"
# 客户端只写配置文件（授时统计只有服务端有）
CFG_FILES=("${CFG_DIR}/ntpsync.json")
WEB_PORTS=(8090)

echo "[1/5] 停止并禁用服务 ${SVC}（及历史服务）..."
for name in "$SVC" "${LEGACY_SVCS[@]}"; do
  if systemctl list-unit-files "${name}.service" >/dev/null 2>&1 || [ -f "/etc/systemd/system/${name}.service" ]; then
    systemctl stop "${name}" >/dev/null 2>&1 || true
    systemctl disable "${name}" >/dev/null 2>&1 || true
  else
    echo "  服务 ${name} 未安装，跳过。"
  fi
done

echo "[2/5] 删除 systemd unit ..."
removed_unit=0
for unit in "/etc/systemd/system/${SVC}.service" "/etc/systemd/system/ntpsync.service"; do
  if [ -f "$unit" ]; then
    rm -f "$unit"
    removed_unit=1
    echo "  已删除 $unit"
  fi
done
[ "$removed_unit" -eq 0 ] && echo "  没有遗留的 unit 文件，跳过。"
[ "$removed_unit" -eq 1 ] && systemctl daemon-reload

echo "[3/5] 删除二进制 ..."
removed_bin=0
for bin in "$BIN" "/usr/local/bin/ntpsync"; do
  if [ -f "$bin" ]; then
    rm -f "$bin"
    removed_bin=1
    echo "  已删除 $bin"
  fi
done
[ "$removed_bin" -eq 0 ] && echo "  ${BIN} 不存在，跳过。"

echo "[4/5] 撤销防火墙放行（Web 端口，如存在）..."
if command -v firewall-cmd >/dev/null 2>&1; then
  for p in "${WEB_PORTS[@]}"; do
    firewall-cmd --permanent --remove-port="${p}/tcp" >/dev/null 2>&1 || true
  done
  firewall-cmd --reload >/dev/null 2>&1 || true
elif command -v ufw >/dev/null 2>&1; then
  for p in "${WEB_PORTS[@]}"; do
    ufw delete allow "${p}/tcp" >/dev/null 2>&1 || true
  done
else
  echo "  未找到 firewall-cmd/ufw，如需清理请手动撤销 ${WEB_PORTS[*]}/tcp 的放行。"
fi

echo "[5/5] 删除配置文件 ..."
removed_cfg=0
for f in "${CFG_FILES[@]}"; do
  for path in "$f" "$f.tmp"; do
    if [ -f "$path" ]; then
      rm -f "$path"
      removed_cfg=1
      echo "  已删除 $path"
    fi
  done
done
[ "$removed_cfg" -eq 0 ] && echo "  ${CFG_DIR} 下没有本程序的配置文件，跳过。"
if [ -d "$CFG_DIR" ]; then
  if rmdir "$CFG_DIR" 2>/dev/null; then
    echo "  已删除空目录 $CFG_DIR"
  else
    echo "  $CFG_DIR 非空（可能还有服务端配置），保留"
  fi
fi

echo ""
echo "[完成] 客户端已卸载。"
