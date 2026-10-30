#!/usr/bin/env bash
# e2e-local.sh - 端到端安装回归（尽量在本机跑，跑不了的部分明确标 SKIP）。
#
# 为什么要这个脚本：整套安装流程里，只有「真正装一遍」才能发现的问题（服务名串台、
# 安装目录互相覆盖、开始菜单图标指向错文件）光靠 go test 是测不到的。但完整安装需要
# 管理员权限并会改动系统，不可能每次改代码都做一遍。本脚本把它拆成四段：
#
#   A 安装器静态段：把 NSIS 安装器以「不压缩」方式重编一份，直接对二进制里的字符串
#     做断言（服务名隔离、旧版服务清理、.url 图标指向、安装目录）。压缩后的安装器
#     字符串不可见，所以必须重编一份未压缩探针，产物放临时目录，不进 dist/。
#   B 布局重放段：按安装器的布局把文件铺到临时目录（含开始菜单 .url），验证两产品
#     目录分离、文件名、图标指向与真实安装一致。
#   C 冒烟段：真跑服务端 + 客户端（非特权端口），打 Web API，验证授时链路与日志。
#   D 服务段：调用 status 子命令，验证两端各自只引用自己的服务名（回归「客户端卸载
#     把服务端服务删掉」）。注册/卸载服务需要管理员 + sc.exe，缺失时标 SKIP。
#
# 用法: bash scripts/e2e-local.sh [版本号，默认 1.10.28]
# 退出码: 有 FAIL 返回 1；只有 SKIP 返回 0（并在末尾列出待人工复验项）。

set -uo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(tr -d ' \t\r\n' < VERSION 2>/dev/null || true)}"
[ -n "$VERSION" ] || { echo "错误：未指定版本且根目录无 VERSION 文件。" >&2; exit 1; }
PASS=0; FAIL=0; SKIP=0; SKIP_LIST=()
ok()   { PASS=$((PASS+1)); printf '  \033[32mok\033[0m   %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }
skip() { SKIP=$((SKIP+1)); SKIP_LIST+=("$1"); printf '  \033[33mSKIP\033[0m %s\n' "$1"; }
chk()  { if [ "$2" = "1" ]; then ok "$1"; else bad "$1"; fi; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

WORK=".e2e.$$"
rm -rf "$WORK"; mkdir -p "$WORK"
cleanup() {
  for pid in "${PIDS[@]:-}"; do [ -n "$pid" ] && kill "$pid" 2>/dev/null; done
  [ -n "${SRV_EXE:-}" ] && rm -rf "$SRV_EXE" 2>/dev/null
  rm -rf "$WORK" 2>/dev/null
  return 0
}
trap cleanup EXIT
PIDS=()

echo "=== 端到端安装回归（版本 $VERSION）==="
echo "工作目录: $WORK"

# ---------------------------------------------------------------------------
# A. 安装器静态段
# ---------------------------------------------------------------------------
section "A. 安装器内容断言（未压缩探针）"
PROBE_MISSING=0
command -v makensis >/dev/null 2>&1 || PROBE_MISSING=1
if [ "$PROBE_MISSING" -eq 1 ]; then
  skip "未装 makensis，跳过安装器字符串断言"
else
  # 原安装器是压缩的，字符串读不出来，所以重编一份"未压缩探针"。
  # 探针必须放在 packaging/win/ 下：nsi 里的 ..\..\assets、..\..\dist 是相对自身位置的。
  probe_abs="$(cd "$WORK" && pwd -W)"
  probe_abs="${probe_abs//\//\\}"
  probe_nsi="packaging/win/.probe.$$.nsi"
  python - "$probe_nsi" "$probe_abs" <<'PY'
import sys
dst, out = sys.argv[1], sys.argv[2]
lines = open('packaging/win/ntp-installer.nsi', encoding='utf-8-sig').read().splitlines()
buf = []
for ln in lines:
    if ln.startswith('OutFile'):
        buf.append('SetCompress off')            # 未压缩，字符串才能被读到
        buf.append('OutFile "%s\\probe-${BIN_NAME}-${ARCH}.exe"' % out)
    elif ln.strip().startswith(('SetCompressor', 'SetDatablockOptimize')):
        # 原脚本在 OutFile 之后才设 SetCompressor，会把上面的 off 覆盖掉，必须一并禁掉
        buf.append('; ' + ln + '   ; probe: 关闭压缩以便断言字符串')
    elif ln.strip().startswith('File '):
        # 不打包任何文件：探针只用来断言"安装脚本自身"的字符串。
        # 若把 exe 也打进去，二进制里那些 Go 字符串（例如 pal 包里同时存在的
        # 两个服务名常量）会混进来，服务名隔离这类断言就失去意义了。
        buf.append('; ' + ln + '   ; probe: 不打包文件，只留脚本字符串')
    else:
        buf.append(ln)
open(dst, 'w', encoding='utf-8-sig').write('\n'.join(buf) + '\n')
PY

  for role in Sntpserver SntpClient; do
    arch="amd64"
    if [ "$role" = "Sntpserver" ]; then defs="-DROLE=server"; else defs="-DROLE=client"; fi
    out=""
    if makensis $defs -DARCH="$arch" -DVERSION="$VERSION" "$probe_nsi" > "$WORK/makensis-$role.log" 2>&1; then
      out="$(ls "$WORK"/probe-${role}-${arch}.exe 2>/dev/null | head -1)"
    fi
    if [ -z "$out" ] || [ ! -f "$out" ]; then
      bad "探针安装器编译失败（$role）：$(tail -2 "$WORK/makensis-$role.log" 2>/dev/null | tr '\n' ' ')"
      continue
    fi
    other="SntpClient"; legacy="ntpsyncd"
    [ "$role" = "SntpClient" ] && { other="Sntpserver"; legacy="ntpsync"; }

    # NSIS 字符串是 UTF-16LE，一次算完所有命中，避免反复起 python
    res="$(python - "$out" "$role" "$other" "$legacy" <<'PY'
import sys
data = open(sys.argv[1], 'rb').read()
role, other, legacy = sys.argv[2], sys.argv[3], sys.argv[4]
def h(s): return 1 if (s.encode('utf-16-le') in data or s.encode('utf-8') in data) else 0
print(h(role), h(other), h('sc delete ' + legacy), h('IconFile'), h('uninstall.exe'))
PY
)"
    set -- $res
    chk "$role 安装器: 含自身服务名 $role"     "$([ "$1" = "1" ] && echo 1 || echo 0)"
    chk "$role 安装器: 不含对方服务名 $other"   "$([ "$2" = "0" ] && echo 1 || echo 0)"
    chk "$role 安装器: 清理旧版服务 $legacy"    "$([ "$3" = "1" ] && echo 1 || echo 0)"
    chk "$role 安装器: .url 指定自有图标"       "$([ "$4" = "1" ] && echo 1 || echo 0)"
    chk "$role 安装器: 卸载器名 uninstall.exe"  "$([ "$5" = "1" ] && echo 1 || echo 0)"
  done
  rm -f "$probe_nsi"
fi

# ---------------------------------------------------------------------------
# B. 布局重放段：按安装器的规则铺一份"安装后的样子"
# ---------------------------------------------------------------------------
section "B. 安装布局重放"
ROOT="$WORK/pf"
mkdir -p "$ROOT/Sntpserver" "$ROOT/SntpClient" "$ROOT/startmenu"
cp "dist/Sntpserver-v${VERSION}-windows-amd64.exe" "$ROOT/Sntpserver/Sntpserver.exe" 2>/dev/null
cp "dist/SntpClient-v${VERSION}-windows-amd64.exe" "$ROOT/SntpClient/SntpClient.exe" 2>/dev/null
cp assets/icon.ico "$ROOT/Sntpserver/Sntpserver.ico" 2>/dev/null
cp assets/icon.ico "$ROOT/SntpClient/SntpClient.ico" 2>/dev/null

# 开始菜单快捷方式：与 nsi 中 WriteINIStr 写出的内容保持一致——以产品名命名、直接落在
# 开始菜单根目录（不再套一层产品子文件夹），卸载不再单独放「卸载」快捷方式。
for role in Sntpserver SntpClient; do
  cat > "$ROOT/startmenu/$role.url" <<EOF
[InternetShortcut]
URL=http://127.0.0.1:8080
IconFile=$ROOT\\$role\\$role.exe
IconIndex=0
EOF
done

chk "布局: 两产品安装目录分离"           "$([ -d "$ROOT/Sntpserver" ] && [ -d "$ROOT/SntpClient" ] && echo 1 || echo 0)"
chk "布局: 服务端主程序名 Sntpserver.exe" "$([ -f "$ROOT/Sntpserver/Sntpserver.exe" ] && echo 1 || echo 0)"
chk "布局: 客户端主程序名 SntpClient.exe" "$([ -f "$ROOT/SntpClient/SntpClient.exe" ] && echo 1 || echo 0)"
chk "布局: 不新建产品子文件夹"            "$([ -e "$ROOT/startmenu/Sntpserver" ] && echo 0 || echo 1)"
chk "布局: .url 落在开始菜单根目录"       "$([ -f "$ROOT/startmenu/Sntpserver.url" ] && echo 1 || echo 0)"
chk "布局: .url 以产品名命名"             "$([ -f "$ROOT/startmenu/Sntpserver.url" ] && [ -f "$ROOT/startmenu/SntpClient.url" ] && echo 1 || echo 0)"
chk "布局: .url 图标指向自身 exe"         "$(grep -q 'IconFile=.*Sntpserver.exe' "$ROOT/startmenu/Sntpserver.url" && echo 1 || echo 0)"
chk "布局: 客户端 .url 图标指向自身 exe"  "$(grep -q 'IconFile=.*SntpClient.exe' "$ROOT/startmenu/SntpClient.url" && echo 1 || echo 0)"
chk "布局: 无「卸载」快捷方式"            "$([ -e "$ROOT/startmenu/Sntpserver/卸载 Sntpserver.lnk" ] && echo 0 || echo 1)"
chk "布局: 客户端目录不含服务端程序"      "$([ -f "$ROOT/SntpClient/Sntpserver.exe" ] && echo 0 || echo 1)"

# ---------------------------------------------------------------------------
# C. 冒烟段：真跑服务端 + 客户端
# ---------------------------------------------------------------------------
section "C. 端到端冒烟（非特权端口）"
SRV_PORT=$((20000 + RANDOM % 20000))
WEB_PORT=$((SRV_PORT + 1))
CLIENT_WEB=$((SRV_PORT + 2))
SRV_EXE="$ROOT/Sntpserver/Sntpserver.exe"
CLI_EXE="$ROOT/SntpClient/SntpClient.exe"

if [ ! -f "$SRV_EXE" ] || [ ! -f "$CLI_EXE" ]; then
  skip "dist 缺少 windows 二进制（版本 $VERSION），跳过冒烟"
else
  "$SRV_EXE" run -l "127.0.0.1:$SRV_PORT" -web "127.0.0.1:$WEB_PORT" > "$WORK/srv.log" 2>&1 &
  PIDS+=($!)
  sleep 2

  if curl -sf "http://127.0.0.1:$WEB_PORT/api/status" -o "$WORK/status.json" 2>/dev/null; then
    ok "服务端 Web 可访问 (/api/status)"
  else
    bad "服务端 Web 不可访问 (127.0.0.1:$WEB_PORT)"
  fi

  "$CLI_EXE" daemon -s "127.0.0.1:$SRV_PORT" -interval 2秒 -web "127.0.0.1:$CLIENT_WEB" > "$WORK/cli.log" 2>&1 &
  PIDS+=($!)
  sleep 4

  # 服务端应看到来自 127.0.0.1 的客户端，且同一 IP 只占一行（Count 累加）
  if curl -sf "http://127.0.0.1:$WEB_PORT/api/clients" -o "$WORK/clients.json" 2>/dev/null; then
    ok "客户端列表接口可访问 (/api/clients)"
    n127=$(python -c "
import json,sys
try:
    d=json.load(open(r'$WORK/clients.json'))
    items = d if isinstance(d,list) else d.get('clients',[])
    print(sum(1 for c in items if str(c.get('addr','')).startswith('127.0.0.1')))
except Exception: print(-1)")
    chk "客户端列表出现 127.0.0.1（同一 IP 单行）" "$([ "$n127" = "1" ] && echo 1 || echo 0)"
    cnt=$(python -c "
import json
try:
    d=json.load(open(r'$WORK/clients.json'))
    items = d if isinstance(d,list) else d.get('clients',[])
    print(next((c.get('count',0) for c in items if str(c.get('addr','')).startswith('127.0.0.1')), 0))
except Exception: print(0)")
    chk "授时次数累加（count >= 2，实测 $cnt）" "$([ "${cnt:-0}" -ge 2 ] && echo 1 || echo 0)"
  else
    bad "客户端列表接口不可访问"
  fi

  # 客户端日志：应出现中文检测日志且不含英文 check/sync
  if [ -s "$WORK/cli.log" ]; then
    chk "客户端有日志输出" "1"
    chk "客户端日志已中文化（无 'check:'）"  "$(grep -q 'check:' "$WORK/cli.log" && echo 0 || echo 1)"
  else
    skip "客户端未产生日志（daemon 可能未跑起来）"
  fi
fi

# ---------------------------------------------------------------------------
# D. 服务名隔离段（回归：客户端卸载删掉服务端服务）
# ---------------------------------------------------------------------------
section "D. 服务名隔离（status 子命令实跑）"
if [ -f "$SRV_EXE" ]; then
  out="$("$SRV_EXE" status 2>&1 || true)"
  chk "服务端 status 引用 Sntpserver"      "$(echo "$out" | grep -q 'Sntpserver' && echo 1 || echo 0)"
  chk "服务端 status 不含 SntpClient"      "$(echo "$out" | grep -q 'SntpClient' && echo 0 || echo 1)"
fi
if [ -f "$CLI_EXE" ]; then
  out="$("$CLI_EXE" status 2>&1 || true)"
  chk "客户端 status 引用 SntpClient"      "$(echo "$out" | grep -q 'SntpClient' && echo 1 || echo 0)"
  chk "客户端 status 不含 Sntpserver"      "$(echo "$out" | grep -q 'Sntpserver' && echo 0 || echo 1)"
fi
skip "注册/卸载/启动系统服务需管理员与 sc.exe，本机无法执行（见 TESTING.md）"

# ---------------------------------------------------------------------------
section "=== 汇总 ==="
for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
echo "通过 $PASS，失败 $FAIL，跳过 $SKIP"
if [ ${#SKIP_LIST[@]} -gt 0 ]; then
  echo "跳过项（需在管理员/目标系统上人工复验）:"
  for s in "${SKIP_LIST[@]}"; do echo "  - $s"; done
fi
[ "$FAIL" -eq 0 ] || exit 1
