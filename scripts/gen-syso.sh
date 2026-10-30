#!/usr/bin/env bash
# gen-syso.sh - 由 VERSION 文件重新生成 Windows 版本资源（resource_windows_*.syso）。
#
# 为什么需要它：exe 的「文件属性 → 版本/产品名称」来自编译时链入的 .syso，
# 以前改版本要手工改 assets/versioninfo.json 再手工敲 goversioninfo，漏一步
# 就会出现「文件名是 v1.10.29、属性里还是 1.10.28」这种对不上的情况。
# 现在版本号只在 VERSION 文件里写一次，本脚本负责同步 JSON 并重生成 syso。
#
# 用法: bash scripts/gen-syso.sh [版本号，默认读 VERSION]
# 依赖: goversioninfo（go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest）
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(tr -d ' \t\r\n' < VERSION 2>/dev/null || true)}"
[ -n "$VERSION" ] || { echo "错误：未指定版本且根目录无 VERSION 文件。" >&2; exit 1; }

GVI="${GOVERSIONINFO:-goversioninfo}"
if ! command -v "$GVI" >/dev/null 2>&1; then
  echo "未找到 goversioninfo，只同步 versioninfo.json 的字段，不生成 syso。" >&2
  echo "安装: go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest" >&2
  GVI=""
fi

# ---- 1. 把版本号同步进 versioninfo.json（Major/Minor/Patch 与两个字符串字段）----
IFS='.' read -r vmaj vmin vpat <<< "$VERSION"
: "${vpat:=0}"
VI="assets/versioninfo.json"
[ -f "$VI" ] || { echo "错误：缺少 $VI" >&2; exit 1; }

sed -i.bak -E \
  -e "s/(\"Major\": )[0-9]+/\1${vmaj:-0}/g" \
  -e "s/(\"Minor\": )[0-9]+/\1${vmin:-0}/g" \
  -e "s/(\"Patch\": )[0-9]+/\1${vpat:-0}/g" \
  -e "s/(\"(File|Product)Version\": \")[0-9]+(\.[0-9]+)*/\1${VERSION}/g" \
  "$VI"
rm -f "${VI}.bak"
echo "已同步 $VI -> $VERSION"

# ---- 2. 逐个产品生成四个架构的 syso ----
[ -z "$GVI" ] && exit 0

gen() { # 参数: $1=cmd 目录  $2=产品名  $3=描述
  local dir="$1" prod="$2" desc="$3"
  ( cd "cmd/$dir"
    "$GVI" -platform-specific \
      -icon "../../assets/app.ico" \
      -product-name "$prod" \
      -internal-name "$prod" \
      -original-name "${prod}.exe" \
      -description "$desc" \
      -file-version "$VERSION" \
      -product-version "$VERSION" \
      "../../$VI" >/dev/null
  )
  echo "已生成 cmd/$dir/resource_windows_*.syso ($prod $VERSION)"
}

gen ntpsyncd Sntpserver "局域网 NTP 授时系统 - 服务端"
gen ntpsync  SntpClient "局域网 NTP 授时系统 - 客户端"

echo ""
echo "=== 完成，版本 $VERSION ==="
ls -l cmd/ntpsyncd/resource_windows_amd64.syso cmd/ntpsync/resource_windows_amd64.syso
