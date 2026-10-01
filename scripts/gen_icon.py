#!/usr/bin/env python3
"""生成 Gopherite 内嵌默认服务器图标 (64x64 PNG)。

纯标准库实现(zlib + struct 手写 PNG), 无 PIL 依赖, 可在任何环境复现:
    python3 scripts/gen_icon.py server/assets/server-icon.png
"""
import struct, sys, zlib

SIZE = 64

# 背景基调: 深玄武岩色阶(随机抖动产生石材质感)
BG = [(0x23, 0x26, 0x2C), (0x2B, 0x2F, 0x36), (0x33, 0x38, 0x40)]
BORDER = (0x16, 0x18, 0x1C)
HILIT = (0x4A, 0x51, 0x5C)

# Gopherite 主色: 地鼠合金琥珀
FG = (0xE8, 0xB8, 0x4B)
FG_DARK = (0xC2, 0x93, 0x2E)
FG_HILIT = (0xFF, 0xD9, 0x7A)

# 9x? 像素字模 "G" (手绘): 1=深色描边 2=主体, 4x 放大居中绘制
GLYPH = [
    " 111111 ",
    "11    21",
    "1     21",
    "1     21",
    "1 22222",
    "1 2   1",
    "1 2 221",
    "1    21",
    "21  221",
    " 1112  ",
]

def pixel(x, y):
    # 边框
    if x == 0 or y == 0 or x == SIZE - 1 or y == SIZE - 1:
        return BORDER
    # 背景石材质感: 对角渐变 + 伪随机抖动
    t = (x + y) / (SIZE * 2)
    base = BG[int(t * 2.99)]
    jitter = ((x * 73856093) ^ (y * 19349663)) % 9
    r, g, b = base
    d = jitter - 4
    if (x * x + y * y) % 17 == 0:  # 少量高光颗粒
        r, g, b = HILIT
    return (max(0, min(255, r + d)), max(0, min(255, g + d)), max(0, min(255, b + d)))

def draw_glyph(pix):
    gh, gw = len(GLYPH), len(GLYPH[0])
    scale = 4
    ox = (SIZE - gw * scale) // 2
    oy = (SIZE - gh * scale) // 2
    for gy, row in enumerate(GLYPH):
        for gx, ch in enumerate(row):
            if ch == " ":
                continue
            for dy in range(scale):
                for dx in range(scale):
                    x, y = ox + gx * scale + dx, oy + gy * scale + dy
                    # 主体 + 左上高光/右下阴影, 形成微立体
                    if ch == "2":
                        shade = FG_HILIT if (dx < 2 and dy < 2) else (FG_DARK if (dx > 2 and dy > 2) else FG)
                        pix[y][x] = shade
                    else:
                        pix[y][x] = (0x0D, 0x0E, 0x11)

def build_png(path):
    pix = [[pixel(x, y) for x in range(SIZE)] for y in range(SIZE)]
    draw_glyph(pix)
    raw = b""
    for row in pix:
        raw += b"\x00" + bytes(c for px in row for c in px)  # filter 0 + RGB

    def chunk(tag, data):
        c = tag + data
        return struct.pack(">I", len(data)) + c + struct.pack(">I", zlib.crc32(c) & 0xFFFFFFFF)

    ihdr = struct.pack(">IIBBBBB", SIZE, SIZE, 8, 2, 0, 0, 0)  # 8bit RGB
    png = (b"\x89PNG\r\n\x1a\n"
           + chunk(b"IHDR", ihdr)
           + chunk(b"IDAT", zlib.compress(raw, 9))
           + chunk(b"IEND", b""))
    with open(path, "wb") as f:
        f.write(png)
    print(f"已生成 {path} ({len(png)} bytes)")

if __name__ == "__main__":
    build_png(sys.argv[1] if len(sys.argv) > 1 else "server-icon.png")
