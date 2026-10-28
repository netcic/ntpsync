#!/usr/bin/env bash
# build.sh - 一键交叉编译局域网 NTP 授时系统全平台二进制。
#
# 用法：
#   bash scripts/build.sh            # 编译全矩阵，版本默认 1.10.28
#   bash scripts/build.sh 1.10.29    # 指定版本号
#
# 产出（dist/，文件名一律带版本号）：
#   Sntpserver-v<ver>-<os>-<arch>[.exe]   服务端（含内嵌 Web UI）
#   SntpClient-v<ver>-<os>-<arch>[.exe]   客户端
#
# 为什么只产出带版本号的文件：同一个目录里既放带版本号又不带版本号的副本时，
# 现场无法判断「哪个是这次交付的」，也容易把上一版当成这一版发出去。
# 需要固定路径的部署脚本自行重命名即可。
#
# 依赖：本机已装 Go 工具链（可用 PATH 指定）。开发机有外网时安装 Go 即可；
#       部署机无外网，靠 Go 静态二进制天然离线分发。

set -euo pipefail

cd "$(dirname "$0")/.."   # 回到项目根目录

VERSION="${1:-1.10.28}"
TARGETS=(
  "windows amd64"
  "windows arm64"
  "linux   amd64"
  "linux   arm64"
)

# 探测 Go 命令（优先环境变量 GO，其次 PATH 里的 go）。
GO="${GO:-go}"
if ! command -v "$GO" >/dev/null 2>&1; then
  echo "错误：找不到 Go 工具链，请设置 PATH 或 GO 环境变量指向 go 可执行文件。" >&2
  exit 1
fi

# 允许传入 CGO_ENABLED=0 保证纯静态；此处强制关闭以消除 libc 依赖。
export CGO_ENABLED=0
# 默认走国内代理（goproxy.cn），首次构建需拉取 x/sys 依赖；部署机无外网
# 时依赖已在本地模块缓存，可直接编译。可用环境变量覆盖。
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

mkdir -p dist

echo "=== 版本 $VERSION ==="
"$GO" version

# 源码目录（cmd/<dir>）到发布名（对外软件名）的映射。
# 目录名沿用 ntpsyncd / ntpsync：Go 包路径与内部标识不随商品名变，改的只是交付物。
publish_name() {
  case "$1" in
    server) echo "Sntpserver" ;;
    client) echo "SntpClient" ;;
  esac
}

cmd_dir() {
  case "$1" in
    server) echo "ntpsyncd" ;;
    client) echo "ntpsync" ;;
  esac
}

for entry in "${TARGETS[@]}"; do
  read -r os arch <<< "$entry"
  for role in server client; do
    name="$(publish_name "$role")"
    dir="$(cmd_dir "$role")"
    out="dist/${name}-v${VERSION}-${os}-${arch}"
    [ "$os" = "windows" ] && out="${out}.exe"

    echo "编译 ${name} ${os}/${arch} -> ${out}"
    GOOS="$os" GOARCH="$arch" "$GO" build -trimpath -ldflags "-s -w" \
      -o "$out" "./cmd/${dir}"

    [ -f "$out" ] || { echo "错误：未生成 $out" >&2; exit 1; }
  done
done

echo ""
echo "=== 构建完成，产物清单 ==="
ls -lh dist/
