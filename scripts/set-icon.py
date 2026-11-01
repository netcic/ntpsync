#!/usr/bin/env python3
"""set-icon.py - 一次性替换产品图标（exe / 安装包 / 网页三处）。

为什么需要它：图标在三个地方各存一份，漏一处就会出现「exe 是新图、网页还是旧图」：
  1. assets/app.ico             → exe 图标 + NSIS 安装包图标（gen-syso.sh 与 nsi 都读它）
  2. internal/web/favicon.ico   → 浏览器标签页
  3. internal/web/index.html    → 页头 logo（内联 base64 PNG，肉眼最容易被漏掉）

用法:
    python scripts/set-icon.py 新图标.ico
    python scripts/set-icon.py 新图标.ico --keep 16 32 48 64 128 256   # 自定义 favicon 档位
之后仍需重跑 `bash scripts/gen-syso.sh <版本>` 与 `bash scripts/build.sh` 才会进 exe。

依赖: Pillow（仅用于生成页头 PNG 与校验，ICO 组装本身是纯标准库）。
"""
import argparse
import base64
import io
import re
import shutil
import struct
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_SIZES = (16, 32, 48, 64, 128)


def read_entries(data):
    """拆出 ICO 目录项与各自的原始数据块（不做重采样，保住小尺寸的手工像素）。"""
    _, typ, count = struct.unpack("<HHH", data[:6])
    if typ != 1:
        raise SystemExit("不是有效的 .ico 文件（type=%d）" % typ)
    out = []
    for i in range(count):
        base = 6 + i * 16
        w, h, colors, res, planes, bpp, size, off = struct.unpack(
            "<BBBBHHII", data[base:base + 16]
        )
        out.append({
            "w": w or 256, "h": h or 256, "colors": colors, "res": res,
            "planes": planes, "bpp": bpp, "blob": data[off:off + size],
        })
    return out


def write_ico(path, items):
    header = struct.pack("<HHH", 0, 1, len(items))
    offset = 6 + 16 * len(items)
    dir_bytes, blobs = b"", b""
    for it in items:
        dir_bytes += struct.pack(
            "<BBBBHHII",
            0 if it["w"] >= 256 else it["w"],
            0 if it["h"] >= 256 else it["h"],
            it["colors"], it["res"], it["planes"] or 1, it["bpp"],
            len(it["blob"]), offset,
        )
        blobs += it["blob"]
        offset += len(it["blob"])
    path.write_bytes(header + dir_bytes + blobs)


def main():
    ap = argparse.ArgumentParser(description="替换 SntpServer/SntpClient 图标")
    ap.add_argument("source", help="源 .ico 路径")
    ap.add_argument("--keep", nargs="+", type=int, default=list(DEFAULT_SIZES),
                    help="favicon 保留的尺寸档（默认 16 32 48 64 128）")
    args = ap.parse_args()

    src = Path(args.source)
    if not src.is_file():
        raise SystemExit("找不到文件：%s" % src)
    entries = read_entries(src.read_bytes())
    have = sorted(e["w"] for e in entries)
    print("源 ICO 尺寸：%s（%d 字节）" % (have, src.stat().st_size))

    missing = [s for s in args.keep if s not in have]
    if missing:
        raise SystemExit("源 ICO 缺少 favicon 需要的尺寸：%s" % missing)

    # 1) exe / 安装包图标：原样拷贝，保留全部尺寸档
    shutil.copyfile(src, ROOT / "assets" / "app.ico")
    print("已替换 assets/app.ico")

    # 2) favicon：只挑小尺寸，控制体积
    picked = sorted((e for e in entries if e["w"] in args.keep), key=lambda e: e["w"])
    fav = ROOT / "internal" / "web" / "favicon.ico"
    write_ico(fav, picked)
    print("已重写 internal/web/favicon.ico -> %s（%d 字节）"
          % ([e["w"] for e in picked], fav.stat().st_size))

    # 3) 页头 logo：64px 帧转 PNG 内联
    try:
        from PIL import Image
    except ImportError:
        raise SystemExit("需要 Pillow 生成页头 PNG：pip install pillow")

    im = Image.open(src)
    im.size = (64, 64)
    buf = io.BytesIO()
    im.convert("RGBA").save(buf, format="PNG", optimize=True)
    b64 = base64.b64encode(buf.getvalue()).decode("ascii")

    html_path = ROOT / "internal" / "web" / "index.html"
    html = html_path.read_text(encoding="utf-8")
    new_html, n = re.subn(
        r'(id="brandIco"[^>]*src="data:image/png;base64,)[^"]*(")',
        lambda m: m.group(1) + b64 + m.group(2),
        html,
    )
    if n != 1:
        raise SystemExit("页头 logo 替换次数异常：%d（预期 1）" % n)
    html_path.write_text(new_html, encoding="utf-8", newline="")
    print("已更新 index.html 页头 logo（64x64，base64 %d 字符）" % len(b64))

    (ROOT / "assets" / "favicon.b64").write_text(b64, encoding="ascii")
    print("已更新 assets/favicon.b64")
    print("\n下一步：bash scripts/gen-syso.sh <版本> && bash scripts/build.sh")


if __name__ == "__main__":
    sys.exit(main())
