#!/usr/bin/env bash
# uninstall-server.sh - 局域网 NTP 授时系统 服务端卸载（Linux / 国产 OS）
#
# 用法（root 执行）:
#   bash uninstall-server.sh
#
# 行为（与 install-server.sh 一一对应清理）:
#   1. 停止并禁用 systemd 服务 SntpServer
#      （连同 1.11.x 的 Sntpserver、1.0.x 的 ntpsyncd）
#   2. 删除 systemd unit（及历史 unit）
#   3. 删除二进制（及历史二进制）
#   4. 撤销防火墙放行（UDP 123 与 Web 管理端口的 TCP）
#   5. 删除本机配置与授时统计（含原子写入留下的 .tmp），目录空了才删
#
# 只动本程序自己的东西：/etc/ntpsync 与服务端共用给客户端，目录里还有
# ntpsync.json（客户端配置）时必须留着，所以用 rmdir（非空即失败）而不是 rm -rf。
#
# 说明: 脚本对「服务不存在」「文件已删」「防火墙规则不存在」均容错，重复执行无副作用。
set -uo pipefail

[ "$(id -u)" -eq 0 ] || { echo "请以 root 运行本脚本。" >&2; exit 1; }

SVC="SntpServer"
# 历史服务名：1.11.x 的 Sntpserver（Linux 的 unit 文件名区分大小写，老 unit 会继续
# 开机自启并占着 UDP 123）、1.0.x 的 ntpsyncd
LEGACY_SVCS=("Sntpserver" "ntpsyncd")
BIN="/usr/local/bin/${SVC}"
CFG_DIR="/etc/ntpsync"
# 服务端自己写过的文件：配置文件 + 授时统计（internal/config 的 ServerStatsFileName），
# 各自还可能有原子写入留下的 .tmp
CFG_FILES=("${CFG_DIR}/ntpsyncd.json" "${CFG_DIR}/SntpServer-clients.json")

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
for unit in "/etc/systemd/system/${SVC}.service" \
            "/etc/systemd/system/Sntpserver.service" \
            "/etc/systemd/system/ntpsyncd.service"; do
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
for bin in "$BIN" "/usr/local/bin/Sntpserver" "/usr/local/bin/ntpsyncd"; do
  if [ -f "$bin" ]; then
    rm -f "$bin"
    removed_bin=1
    echo "  已删除 $bin"
  fi
done
[ "$removed_bin" -eq 0 ] && echo "  ${BIN} 不存在，跳过。"

echo "[4/5] 撤销防火墙放行（如存在）..."
# 8080 是当前 Web 端口；123/tcp 是 1.11.x「Web 端口跟随授时端口」那版留下的
WEB_PORTS=(8080 123)
if command -v firewall-cmd >/dev/null 2>&1; then
  firewall-cmd --permanent --remove-service=ntp >/dev/null 2>&1 || true
  firewall-cmd --permanent --remove-port=123/udp >/dev/null 2>&1 || true
  for p in "${WEB_PORTS[@]}"; do
    firewall-cmd --permanent --remove-port="${p}/tcp" >/dev/null 2>&1 || true
  done
  firewall-cmd --reload >/dev/null 2>&1 || true
elif command -v ufw >/dev/null 2>&1; then
  ufw delete allow 123/udp >/dev/null 2>&1 || true
  for p in "${WEB_PORTS[@]}"; do
    ufw delete allow "${p}/tcp" >/dev/null 2>&1 || true
  done
else
  echo "  未找到 firewall-cmd/ufw，如需清理请手动撤销 123/udp 与 ${WEB_PORTS[*]}/tcp 的放行。"
fi

echo "[5/5] 删除配置与授时统计 ..."
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
# 目录与客户端共用：非空说明还有 ntpsync.json 之类，留着
if [ -d "$CFG_DIR" ]; then
  if rmdir "$CFG_DIR" 2>/dev/null; then
    echo "  已删除空目录 $CFG_DIR"
  else
    echo "  $CFG_DIR 非空（可能还有客户端配置），保留"
  fi
fi

echo ""
echo "[完成] 服务端已卸载。"
