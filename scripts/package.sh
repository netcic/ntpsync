#!/usr/bin/env bash
# package.sh - 把编译产物打包成 deb / rpm（Linux / 国产 OS 分发包）。
#
# 用法（在 Linux 机上运行）:
#   bash scripts/package.sh                 # 自动打包 dist/ 下所有 linux 二进制（默认 1.10.28）
#   bash scripts/package.sh 1.10.29         # 指定版本号
#   bash scripts/package.sh --dry-run       # 只生成 staging 树与元数据，不调用 dpkg-deb/rpmbuild
#   CLIENT_SERVER=192.168.1.10:123 bash scripts/package.sh 1.10.28   # 指定客户端校时目标
#
# 前置: 已运行 scripts/build.sh 产出 dist/Sntp*-v<ver>-linux-*。
# 依赖: dpkg-deb（deb 系）与 rpmbuild（rpm 系）。缺工具会跳过对应格式并提示；
#       --dry-run 不依赖这两者，可在任意装了 bash 的机器上生成待校验目录树，
#       配合 scripts/check-stage.sh 做离线校验。
#
# 产出（dist/，包名统一小写——dpkg 不接受大写包名；可执行文件与 systemd 单元保留
# 产品名 Sntpserver / SntpClient，与服务名一致）:
#   服务端: sntpserver_<ver>_<arch>.deb / sntpserver-<ver>-1.<arch>.rpm
#   客户端: sntpclient_<ver>_<arch>.deb / sntpclient-<ver>-1.<arch>.rpm
#
# 说明: 每个包安装后即含 systemd unit 与开机自启。配置文件不随包分发——首次在
#       Web 参数页保存时由程序自行创建（config.Save 会先 MkdirAll）。
#       客户端包默认校时目标 127.0.0.1:123，可用 CLIENT_SERVER 环境变量覆盖。
set -euo pipefail

cd "$(dirname "$0")/.."

# ---- 参数解析 ---------------------------------------------------------------
DRY_RUN=0
VERSION=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) VERSION="$arg" ;;
  esac
done
VERSION="${VERSION:-1.10.28}"

CLIENT_SERVER="${CLIENT_SERVER:-127.0.0.1:123}"
CLIENT_INTERVAL="${CLIENT_INTERVAL:-15分}"
CLIENT_STEP="${CLIENT_STEP:-5秒}"

ARCHS=(amd64 arm64)

# 检查是否有任何 linux 二进制
if ! ls dist/Sntp*-v*-linux-* >/dev/null 2>&1; then
  echo "未找到 linux 二进制，请先运行 scripts/build.sh。" >&2
  exit 1
fi

have_deb=0; have_rpm=0
if [ "$DRY_RUN" -eq 0 ]; then
  command -v dpkg-deb >/dev/null 2>&1 && have_deb=1
  command -v rpmbuild  >/dev/null 2>&1 && have_rpm=1

  [ "$have_deb" -eq 0 ] && [ "$have_rpm" -eq 0 ] && {
    echo "本机既无 dpkg-deb 也无 rpmbuild，无法生成 deb/rpm。" >&2
    echo "请在有打包工具的 Linux 机（Debian/Ubuntu 装 dpkg，红帽/麒麟装 rpm-build）运行本脚本，" >&2
    echo "或加 --dry-run 只生成待校验目录树（配合 scripts/check-stage.sh）。" >&2
    exit 1
  }
fi

echo "=== 打包目标 ==="
echo "版本: $VERSION"
echo "客户端校时目标: $CLIENT_SERVER  间隔: $CLIENT_INTERVAL  护栏: $CLIENT_STEP"
if [ "$DRY_RUN" -eq 1 ]; then
  echo "模式: dry-run（只生成 staging 树，不产出 deb/rpm）"
else
  echo "可用工具: deb=$have_deb rpm=$have_rpm"
fi
echo ""

# ---------------------------------------------------------------------------
# 角色元数据的唯一来源：改产品名或默认参数只需改这里。
# bash 没有返回值，用全局变量输出，避免每个调用点重复写同样四五个变量。
# ---------------------------------------------------------------------------
M_BIN=""; M_PKG=""; M_DISPLAY=""; M_DESC=""; M_EXECSTART=""
pkg_meta() {
  local role="$1"
  if [ "$role" = "server" ]; then
    M_BIN="Sntpserver"; M_PKG="sntpserver"
    M_DISPLAY="Sntpserver - 局域网 NTP 授时服务"
    M_DESC="以本机硬件时钟为权威时间源，向局域网设备提供 NTP 授时。"
    M_EXECSTART="/usr/local/bin/Sntpserver run -l 0.0.0.0:123 -web 127.0.0.1:8080"
  else
    M_BIN="SntpClient"; M_PKG="sntpclient"
    M_DISPLAY="SntpClient - 局域网 NTP 校时客户端"
    M_DESC="开机自启，周期性向局域网 NTP 服务端校准本机时钟。"
    M_EXECSTART="/usr/local/bin/SntpClient daemon -s ${CLIENT_SERVER} -interval ${CLIENT_INTERVAL} -step ${CLIENT_STEP} -web 127.0.0.1:8090"
  fi
}

# ---------------------------------------------------------------------------
# 生成一个包的 staging 目录（含二进制 + systemd unit）
# 参数: $1=角色(server|client)  $2=架构(amd64|arm64)  $3=staging根
# ---------------------------------------------------------------------------
make_stage() {
  local role="$1" arch="$2" root="$3"
  pkg_meta "$role"
  local bin="$M_BIN" src="dist/${M_BIN}-v${VERSION}-linux-${arch}"

  [ -f "$src" ] || { echo "  [跳过] 缺少 $src"; return 1; }

  mkdir -p "$root/usr/local/bin" "$root/usr/lib/systemd/system"
  cp "$src" "$root/usr/local/bin/$bin"
  chmod 0755 "$root/usr/local/bin/$bin"

  cat > "$root/usr/lib/systemd/system/${bin}.service" <<EOF
[Unit]
Description=$M_DISPLAY
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=$M_EXECSTART
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_SYS_TIME CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_SYS_TIME CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF
  return 0
}

# ---------------------------------------------------------------------------
# 遍历角色 × 架构，生成 deb / rpm
# ---------------------------------------------------------------------------
# staging 目录刻意放在仓库内的相对路径：某些受限环境（容器/沙箱）不允许删除
# /tmp 下的绝对路径，相对路径既能正常清理，也不会在中断时丢在系统临时区。
STAGE=""
cleanup() { [ -n "$STAGE" ] && [ -d "$STAGE" ] && rm -rf "$STAGE"; return 0; }
trap cleanup EXIT

for role in server client; do
  pkg_meta "$role"
  bin="$M_BIN"; pkg="$M_PKG"

  for arch in "${ARCHS[@]}"; do
    src="dist/${bin}-v${VERSION}-linux-${arch}"
    [ -f "$src" ] || { echo "[跳过] 无 ${src}"; continue; }

    STAGE=".pkgstage.$$"
    rm -rf "$STAGE"; mkdir -p "$STAGE"
    pkg_root="$STAGE/pkg"

    if ! make_stage "$role" "$arch" "$pkg_root"; then
      rm -rf "$STAGE"; STAGE=""; continue
    fi
    pkg_meta "$role"   # make_stage 内会重设元数据，这里取回本轮角色的值

    # rpm 架构名映射（麒麟/欧拉用 aarch64）
    rpm_arch="$arch"
    [ "$arch" = "arm64" ] && rpm_arch="aarch64"

    # ---- deb ----
    deb_root="$STAGE/deb"
    mkdir -p "$deb_root/DEBIAN"
    cp -r "$pkg_root/." "$deb_root/"
    cat > "$deb_root/DEBIAN/control" <<EOF
Package: $pkg
Version: $VERSION
Section: net
Priority: optional
Architecture: $arch
Maintainer: NTP Sync <admin@local>
Description: $M_DISPLAY
 $M_DESC
EOF
    cat > "$deb_root/DEBIAN/postinst" <<EOF
#!/bin/sh
set -e
systemctl daemon-reload >/dev/null 2>&1 || true
systemctl enable $bin >/dev/null 2>&1 || true
exit 0
EOF
    cat > "$deb_root/DEBIAN/prerm" <<EOF
#!/bin/sh
set -e
systemctl stop $bin >/dev/null 2>&1 || true
systemctl disable $bin >/dev/null 2>&1 || true
exit 0
EOF
    chmod 0755 "$deb_root/DEBIAN/postinst" "$deb_root/DEBIAN/prerm"

    # ---- rpm ----
    rpm_root="$STAGE/rpmbuild"
    mkdir -p "$rpm_root"/{BUILD,RPMS,SOURCES,SPECS,SRPMS}
    # 注意：rpmbuild 执行 %install 时的工作目录不是仓库根，脚本里的相对路径会失效，
    # 因此 spec 里出现的所有路径都必须先展开成绝对路径。
    repo_root="$(pwd)"
    pkg_root_abs="$repo_root/$pkg_root"
    cat > "$rpm_root/SPECS/${pkg}.spec" <<EOF
Name:           $pkg
Version:        $VERSION
Release:        1
Summary:        $M_DISPLAY
License:        MIT
BuildArch:      $rpm_arch

%description
$M_DESC

%install
mkdir -p %{buildroot}/usr/local/bin %{buildroot}/usr/lib/systemd/system
install -m 0755 $repo_root/$src %{buildroot}/usr/local/bin/$bin
cp $pkg_root_abs/usr/lib/systemd/system/${bin}.service %{buildroot}/usr/lib/systemd/system/

%post
systemctl daemon-reload || true
systemctl enable $bin || true

%preun
systemctl stop $bin || true
systemctl disable $bin || true

%files
/usr/local/bin/$bin
/usr/lib/systemd/system/${bin}.service
EOF

    if [ "$DRY_RUN" -eq 1 ]; then
      # 落地到 dist/pkgstage/ 供 scripts/check-stage.sh 离线校验
      out="dist/pkgstage/${pkg}_${VERSION}_${arch}"
      rm -rf "$out"; mkdir -p "$out"
      cp -r "$deb_root/." "$out/"
      mkdir -p "$out/SPECS"
      cp "$rpm_root/SPECS/${pkg}.spec" "$out/SPECS/${pkg}.spec"
      echo "[dry ] ${out}"
    else
      if [ "$have_deb" -eq 1 ]; then
        dpkg-deb --build "$deb_root" "dist/${pkg}_${VERSION}_${arch}.deb" >/dev/null
        echo "[deb ] ${pkg}_${VERSION}_${arch}.deb"
      fi
      if [ "$have_rpm" -eq 1 ]; then
        if rpmbuild --define "_topdir $repo_root/$rpm_root" -bb "$repo_root/$rpm_root/SPECS/${pkg}.spec" >/dev/null 2>&1; then
          cp "$rpm_root"/RPMS/*/*.rpm dist/ 2>/dev/null || true
          echo "[rpm ] ${pkg}-${VERSION}-1.${rpm_arch}.rpm"
        else
          echo "[rpm ] ${pkg} 构建失败（rpmbuild 返回非 0），已跳过" >&2
        fi
      fi
    fi

    rm -rf "$STAGE"; STAGE=""
  done
done

echo ""
if [ "$DRY_RUN" -eq 1 ]; then
  echo "=== dry-run 完成，下一步：bash scripts/check-stage.sh $VERSION ==="
  ls -d dist/pkgstage/* 2>/dev/null || true
else
  echo "=== 打包完成，dist/ 产物清单 ==="
  ls -lh dist/*.deb dist/*.rpm 2>/dev/null || true
fi
