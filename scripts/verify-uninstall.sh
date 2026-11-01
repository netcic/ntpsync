#!/usr/bin/env bash
# verify-uninstall.sh - 卸载残留核查（Linux / 国产 OS），只读脚本。
#
# 用法:
#   bash scripts/verify-uninstall.sh            # 同时检查服务端与客户端
#   bash scripts/verify-uninstall.sh server     # 只查服务端
#   bash scripts/verify-uninstall.sh client
#
# 检查范围严格限定在本程序自己的痕迹，不碰系统或第三方数据：
#   · systemd unit 与二进制（含 1.0.x 的 ntpsyncd / ntpsync、1.11.x 的 Sntpserver）
#   · /etc/ntpsync 下属于该角色的配置与授时统计（含 .tmp）
#   · 防火墙放行（123/udp、Web 端口的 TCP；无 firewall-cmd/ufw 时跳过）
#
# 退出码: 0 = 无残留；1 = 有残留。
# 说明: 防火墙与 systemd 状态最好以 root 运行才看得全；非 root 时相关项标记 SKIP。
set -uo pipefail

ROLE="${1:-both}"
case "$ROLE" in
  server|client|both) ;;
  *) echo "用法: bash $0 [server|client|both]" >&2; exit 2 ;;
esac

# 默认 /etc/ntpsync；可用环境变量指到别处，方便自测脚本本身或非标准安装
CFG_DIR="${NTPSYNC_CFG_DIR:-/etc/ntpsync}"
PASS=0; FAIL=0; SKIP=0
ok()   { PASS=$((PASS+1)); printf '  [\033[32mPASS\033[0m] %s  %s\n' "$1" "$2"; }
bad()  { FAIL=$((FAIL+1)); printf '  [\033[31mFAIL\033[0m] %s  %s%s\n' "$1" "$2" "${3:+  — $3}"; }
skip() { SKIP=$((SKIP+1)); printf '  [\033[33mSKIP\033[0m] %s  %s%s\n' "$1" "$2" "${3:+  — $3}"; }
keep() { printf '  [\033[90mKEEP\033[0m] %s  %s  — %s\n' "$1" "$2" "$3"; }

# 断言「不存在」
absent() { # $1=项目 $2=路径 $3=说明
  if [ -e "$2" ]; then bad "$1" "$2" "${3:-仍存在}"; else ok "$1" "$2"; fi
}

check_role() {
  local role="$1"
  local svc bin legacy_svcs legacy_bins cfg_files
  if [ "$role" = "server" ]; then
    svc="SntpServer"; bin="/usr/local/bin/SntpServer"
    legacy_svcs=("Sntpserver" "ntpsyncd")
    legacy_bins=("/usr/local/bin/Sntpserver" "/usr/local/bin/ntpsyncd")
    cfg_files=("${CFG_DIR}/ntpsyncd.json" "${CFG_DIR}/SntpServer-clients.json")
    web_ports=(8080 123)
  else
    svc="SntpClient"; bin="/usr/local/bin/SntpClient"
    legacy_svcs=("ntpsync")
    legacy_bins=("/usr/local/bin/ntpsync")
    cfg_files=("${CFG_DIR}/ntpsync.json")
    web_ports=(8090)
  fi

  echo ""
  echo "--- ${role}（${svc}）---"

  # 1. 服务与二进制
  for name in "$svc" "${legacy_svcs[@]}"; do
    if [ -f "/etc/systemd/system/${name}.service" ] || [ -f "/lib/systemd/system/${name}.service" ]; then
      bad "systemd unit" "${name}.service" "unit 文件仍存在"
    else
      ok "systemd unit" "${name}.service"
    fi
    if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files "${name}.service" >/dev/null 2>&1; then
      if systemctl is-enabled "${name}" >/dev/null 2>&1 || systemctl is-active "${name}" >/dev/null 2>&1; then
        bad "服务状态" "${name}" "仍处于启用/运行状态"
      else
        ok "服务状态" "${name}"
      fi
    else
      skip "服务状态" "${name}" "无 systemctl 或无权限"
    fi
  done
  absent "二进制" "$bin"
  for b in "${legacy_bins[@]}"; do absent "历史二进制" "$b"; done

  # 2. 配置与授时统计
  for f in "${cfg_files[@]}"; do
    absent "配置文件" "$f"
    absent "配置临时文件" "${f}.tmp"
  done
  if [ -d "$CFG_DIR" ]; then
    local left
    left="$(ls -A "$CFG_DIR" 2>/dev/null | tr '\n' ' ')"
    if [ -z "$left" ]; then
      bad "数据目录" "$CFG_DIR" "目录已空却仍存在（卸载应删除）"
    else
      keep "数据目录" "$CFG_DIR" "非空，剩余：${left}（属于同机另一个产品或用户自行放置，未删除）"
    fi
  else
    ok "数据目录" "$CFG_DIR 已删除"
  fi

  # 3. 防火墙：只查本程序放行过的端口，规则名不可枚举（ufw/firewalld 都以端口记账）
  if command -v firewall-cmd >/dev/null 2>&1; then
    local ports
    ports="$(firewall-cmd --list-ports 2>/dev/null || true)"
    [ -z "$ports" ] && ports="$(firewall-cmd --permanent --list-ports 2>/dev/null || true)"
    if [ -z "$ports" ]; then
      skip "防火墙放行" "firewalld" "读不到端口列表（可能需 root）"
    else
      local leftovers=""
      for p in 123/udp "${web_ports[@]/%/\/tcp}"; do
        case " $ports " in *" $p "*) leftovers="$leftovers $p" ;; esac
      done
      # 123/udp 是服务端专属，查客户端时不该算在它头上
      if [ "$role" = "client" ]; then leftovers="${leftovers// 123\/udp/}"; fi
      if [ -n "$leftovers" ]; then
        bad "防火墙放行" "firewalld" "仍放行：$leftovers"
      else
        ok "防火墙放行" "firewalld"
      fi
    fi
  elif command -v ufw >/dev/null 2>&1; then
    local st leftovers=""
    st="$(ufw status 2>/dev/null || true)"
    if [ -z "$st" ] || echo "$st" | grep -qi "inactive"; then
      ok "防火墙放行" "ufw（未启用）"
    else
      for p in 123/udp "${web_ports[@]/%/\/tcp}"; do
        case "$st" in *"$p"*) leftovers="$leftovers $p" ;; esac
      done
      if [ "$role" = "client" ]; then leftovers="${leftovers// 123\/udp/}"; fi
      if [ -n "$leftovers" ]; then
        bad "防火墙放行" "ufw" "仍放行：$leftovers"
      else
        ok "防火墙放行" "ufw"
      fi
    fi
  else
    skip "防火墙放行" "firewalld/ufw" "本机没有可识别的防火墙工具"
  fi
}

echo "=== 卸载残留核查（只读）==="
[ "$(id -u)" -eq 0 ] || echo "提示：非 root 运行，部分系统状态可能读不到。"

if [ "$ROLE" = "both" ]; then
  check_role server
  check_role client
else
  check_role "$ROLE"
fi

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '=== 发现 %d 项残留（通过 %d，跳过 %d）===\n' "$FAIL" "$PASS" "$SKIP"
  exit 1
fi
printf '=== 未发现残留（通过 %d，跳过 %d）===\n' "$PASS" "$SKIP"
[ "$SKIP" -gt 0 ] && echo "（跳过项建议以 root 重跑，或用 scripts/uninstall-*.sh 复核）"
exit 0
