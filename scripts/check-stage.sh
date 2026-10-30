#!/usr/bin/env bash
# check-stage.sh - 离线校验 Linux 打包 staging 树（deb 元数据 + rpm spec + systemd unit）。
#
# 为什么需要它：deb/rpm 只有在装了 dpkg-deb / rpmbuild 的机器上才能真正打出来，
# 而打包脚本里最容易出错的恰恰是那几段纯文本元数据（control 字段、spec 的 %files、
# unit 的 ExecStart）。本脚本把这些「文本正确性」单独拎出来，在没有打包工具的机器上
# 也能验，配合 package.sh --dry-run 使用。
#
# 用法:
#   bash scripts/package.sh --dry-run 1.10.28   # 先生成 dist/pkgstage/
#   bash scripts/check-stage.sh [版本号]        # 再校验，默认 1.10.28
#
# 校验范围: 文件清单 / 可执行位 / ELF 架构 / 版本一致性 / 包名规范 / control 字段 /
#           maintainer 脚本服务名 / spec 字段与 %files / systemd unit 的 ExecStart 参数
#           是否真的被 Go 程序支持（交叉校验 cmd/*/main.go 里注册的 flag）。
# 退出码: 全部通过 0，任一失败 1。

set -uo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(tr -d ' \t\r\n' < VERSION 2>/dev/null || true)}"
[ -n "$VERSION" ] || { echo "错误：未指定版本且根目录无 VERSION 文件。" >&2; exit 1; }

PASS=0; FAIL=0; SKIP=0
ok()   { PASS=$((PASS+1)); printf '  \033[32mok\033[0m   %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }
skip() { SKIP=$((SKIP+1)); printf '  \033[33mSKIP\033[0m %s\n' "$1"; }
chk()  { if [ "$2" = "1" ]; then ok "$1"; else bad "$1"; fi; }

# Windows 文件系统没有 POSIX 权限位，chmod 不生效，可执行位一类断言只能跳过——
# 不算通过也不算失败，单独统计，避免拿本机结果冒充 Linux 结论。
FS_POSIX=1
case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*|Windows_NT) FS_POSIX=0;; esac
permchk() { if [ "$FS_POSIX" -eq 1 ]; then chk "$1" "$2"; else skip "$1（Windows 无 POSIX 权限位，需在 Linux 上复验）"; fi; }

# 目录名 -> 角色元数据。与 package.sh 的 pkg_meta 保持一致（改那里时同步这里）。
role_bin()   { [ "$1" = "sntpserver" ] && echo "Sntpserver" || echo "SntpClient"; }
other_bin()  { [ "$1" = "sntpserver" ] && echo "SntpClient" || echo "Sntpserver"; }
main_src()   { [ "$1" = "sntpserver" ] && echo "cmd/ntpsyncd/main.go" || echo "cmd/ntpsync/main.go"; }
# v1.0.x 遗留服务名：升级时必须清掉，否则新旧服务抢 UDP 123。
legacy_name(){ [ "$1" = "sntpserver" ] && echo "ntpsyncd" || echo "ntpsync"; }

# 从 Go 源码里提取某个 FlagSet 注册的 flag 名集合。
# 形参 $2 是子命令对应的 FlagSet 变量名（如 run / daemon）；留空取全文件并集。
# 之所以按 FlagSet 分别取：同一个 flag 在 run 里有、在 install 里未必有，
# 混在一起会把「某子命令根本不支持这个参数」这类错误放过。
registered_flags() {
  local src="$1" fs="${2:-}"
  local pat='\.(String|Int|Uint|Bool|Duration)\("[^"]+"'
  [ -n "$fs" ] && pat="\\b${fs}${pat}"
  grep -oE "$pat" "$src" 2>/dev/null | sed -E 's/.*\("([^"]+)".*/\1/' | sort -u
}

# 读 ELF 的 e_machine 字段（偏移 18，2 字节小端）。非 ELF 文件返回空。
elf_machine() {
  local f="$1"
  [ -f "$f" ] || return 0
  # 前 4 字节必须是 7f 45 4c 46
  [ "$(head -c 4 "$f" | od -An -tx1 | tr -d ' \n')" = "7f454c46" ] || return 0
  od -An -tu1 -j18 -N2 "$f" | awk '{print $1 + $2*256}'
}
elf_class() { head -c 5 "$f" 2>/dev/null | od -An -tu1 -j4 -N1 | tr -d ' \n'; }

echo "=== 校验 staging 树（版本 $VERSION）==="
[ -d dist/pkgstage ] || { echo "未找到 dist/pkgstage，请先运行: bash scripts/package.sh --dry-run $VERSION" >&2; exit 1; }

shopt -s nullglob
stages=(dist/pkgstage/*_${VERSION}_*)
[ ${#stages[@]} -eq 0 ] && { echo "dist/pkgstage 下没有版本 $VERSION 的目录" >&2; exit 1; }

for dir in "${stages[@]}"; do
  [ -d "$dir" ] || continue
  name="$(basename "$dir")"
  pkg="${name%%_*}"; arch="${name##*_}"
  bin="$(role_bin "$pkg")"; other="$(other_bin "$pkg")"; src="$(main_src "$pkg")"
  legacy="$(legacy_name "$pkg")"

  echo ""
  echo "--- $name (bin=$bin arch=$arch) ---"

  # 1. 文件清单
  chk "清单: usr/local/bin/$bin"            "$([ -f "$dir/usr/local/bin/$bin" ] && echo 1 || echo 0)"
  chk "清单: systemd unit"                  "$([ -f "$dir/usr/lib/systemd/system/$bin.service" ] && echo 1 || echo 0)"
  chk "清单: DEBIAN/control"                "$([ -f "$dir/DEBIAN/control" ] && echo 1 || echo 0)"
  chk "清单: DEBIAN/postinst + prerm"       "$([ -f "$dir/DEBIAN/postinst" ] && [ -f "$dir/DEBIAN/prerm" ] && echo 1 || echo 0)"
  chk "清单: SPECS/$pkg.spec"               "$([ -f "$dir/SPECS/$pkg.spec" ] && echo 1 || echo 0)"
  [ -f "$dir/DEBIAN/control" ] || { echo "  （control 缺失，跳过该包后续校验）"; continue; }

  # 2. 二进制：ELF + 架构 + 可执行位
  f="$dir/usr/local/bin/$bin"
  m="$(elf_machine "$f")"
  want_m=62; [ "$arch" = "arm64" ] && want_m=183
  chk "二进制是 ELF"                        "$([ -n "$m" ] && echo 1 || echo 0)"
  chk "二进制架构匹配 $arch (e_machine=$want_m, 实测=${m:-无})" "$([ "$m" = "$want_m" ] && echo 1 || echo 0)"
  permchk "二进制带可执行位"                "$([ -x "$f" ] && echo 1 || echo 0)"

  # 3. control 字段
  ctrl="$dir/DEBIAN/control"
  c_pkg="$(grep -m1 '^Package:' "$ctrl" | sed 's/^Package: *//')"
  c_ver="$(grep -m1 '^Version:' "$ctrl" | sed 's/^Version: *//')"
  c_arch="$(grep -m1 '^Architecture:' "$ctrl" | sed 's/^Architecture: *//')"
  chk "control: Package == $pkg"            "$([ "$c_pkg" = "$pkg" ] && echo 1 || echo 0)"
  chk "control: Version == $VERSION"        "$([ "$c_ver" = "$VERSION" ] && echo 1 || echo 0)"
  chk "control: Architecture == $arch"      "$([ "$c_arch" = "$arch" ] && echo 1 || echo 0)"
  for fld in Section Priority Maintainer Description; do
    chk "control: 含 $fld"                  "$(grep -q "^$fld:" "$ctrl" && echo 1 || echo 0)"
  done
  chk "control: 包名符合 dpkg 规范(小写)"    "$(echo "$c_pkg" | grep -qE '^[a-z0-9][a-z0-9+.-]*$' && echo 1 || echo 0)"
  chk "control: Description 续行以空格缩进"  "$(grep -qE '^ .+' "$ctrl" && echo 1 || echo 0)"

  # 4. maintainer 脚本：服务名正确且不碰对方（回归：客户端卸载删掉服务端服务）
  for ms in postinst prerm; do
    p="$dir/DEBIAN/$ms"
    [ -f "$p" ] || continue
    permchk "$ms: 可执行"                   "$([ -x "$p" ] && echo 1 || echo 0)"
    # 行尾常带重定向（>/dev/null 2>&1），不能用 $ 锚定，改用「服务名后为非标识符字符」
    chk "$ms: 操作 $bin"                    "$(grep -qE "systemctl (daemon-reload|enable|stop|disable) $bin([^A-Za-z0-9_-]|$)" "$p" && echo 1 || echo 0)"
    chk "$ms: 不含对方服务 $other"           "$(grep -q "$other" "$p" && echo 0 || echo 1)"
    # 升级路径：v1.0.x 的旧服务若不清，会和新服务抢同一个 UDP 123
    if [ "$ms" = "postinst" ]; then
      chk "$ms: 清理旧版服务 $legacy"        "$(grep -q "systemctl stop $legacy" "$p" && echo 1 || echo 0)"
      chk "$ms: 删除旧版 unit 文件"           "$(grep -q "/etc/systemd/system/$legacy.service" "$p" && echo 1 || echo 0)"
    fi
  done

  # 5. systemd unit
  unit="$dir/usr/lib/systemd/system/$bin.service"
  chk "unit: 含三个 section"                "$([ "$(grep -cE '^\[(Unit|Service|Install)\]$' "$unit")" -eq 3 ] && echo 1 || echo 0)"
  execstart="$(grep -m1 '^ExecStart=' "$unit" | sed 's/^ExecStart=//')"
  exe="$(echo "$execstart" | awk '{print $1}')"
  chk "unit: ExecStart 指向包内可执行文件"   "$([ "$exe" = "/usr/local/bin/$bin" ] && echo 1 || echo 0)"
  chk "unit: ExecStart 非空"                "$([ -n "$execstart" ] && echo 1 || echo 0)"

  #    交叉校验：ExecStart 里每个 -flag 都必须是该子命令真实注册的 flag
  subcmd="$(echo "$execstart" | awk '{print $2}')"
  flags="$(registered_flags "$src" "$subcmd")"
  [ -z "$flags" ] && flags="$(registered_flags "$src")"   # 取不到子命令集合时退回全量并集
  bad_flags=""
  for tok in $(echo "$execstart" | tr ' ' '\n' | grep '^-'); do
    echo "$flags" | grep -qx "${tok#-}" || bad_flags="$bad_flags $tok"
  done
  chk "unit: ExecStart 参数均被 $src 的 $subcmd 子命令支持${bad_flags:+（未知:$bad_flags）}" "$([ -z "$bad_flags" ] && echo 1 || echo 0)"

  # 6. rpm spec
  spec="$dir/SPECS/$pkg.spec"
  if [ -f "$spec" ]; then
    s_ver="$(grep -m1 '^Version:' "$spec" | sed 's/^Version: *//')"
    s_arch="$(grep -m1 '^BuildArch:' "$spec" | sed 's/^BuildArch: *//')"
    want_arch="$arch"; [ "$arch" = "arm64" ] && want_arch="aarch64"
    chk "spec: Version == $VERSION"         "$([ "$s_ver" = "$VERSION" ] && echo 1 || echo 0)"
    chk "spec: BuildArch == $want_arch"     "$([ "$s_arch" = "$want_arch" ] && echo 1 || echo 0)"
    for fld in Name Release Summary License; do
      chk "spec: 含 $fld"                   "$(grep -q "^$fld:" "$spec" && echo 1 || echo 0)"
    done
    # %files 段列出的路径，必须恰好是包内实际安装的两个文件
    files="$(sed -n '/^%files/,$p' "$spec" | tail -n +2 | grep -vE '^\s*$' | sed 's/^ *//')"
    want_files="/usr/local/bin/$bin
/usr/lib/systemd/system/$bin.service"
    chk "spec: %files 与包内文件一致"       "$([ "$files" = "$want_files" ] && echo 1 || echo 0)"
    chk "spec: %install 使用绝对路径"       "$(sed -n '/^%install/,/^%/p' "$spec" | grep -qE '(install|cp) [^$%/]*/\$' && echo 0 || echo 1)"
    chk "spec: 不含对方服务 $other"          "$(grep -q "$other" "$spec" && echo 0 || echo 1)"
    chk "spec: %post 清理旧版服务 $legacy"   "$(sed -n '/^%post/,/^%/p' "$spec" | grep -q "systemctl stop $legacy" && echo 1 || echo 0)"
  fi

  # 7. 版本与 dist 中二进制文件名一致
  chk "dist: 源二进制版本号为 $VERSION"     "$(ls dist/$bin-v$VERSION-linux-$arch >/dev/null 2>&1 && echo 1 || echo 0)"
done

echo ""
echo "=== 汇总: 通过 $PASS，失败 $FAIL，跳过 $SKIP ==="
[ "$SKIP" -gt 0 ] && echo "（跳过项源于本机环境限制，需在 Linux 上复验，不计为通过）"
[ "$FAIL" -eq 0 ] || exit 1
