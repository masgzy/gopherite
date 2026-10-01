#!/usr/bin/env python3
"""Gopherite 冒烟测试客户端: 完整实现 握手→状态请求→Ping-Pong (纯标准库)。"""
import json, socket, struct, sys

def write_varint(v):
    out = bytearray()
    while True:
        if v & ~0x7F == 0:
            out.append(v)
            return bytes(out)
        out.append((v & 0x7F) | 0x80)
        v >>= 7

def read_varint(sock):
    n, result = 0, 0
    while True:
        b = sock.recv(1)[0]
        result |= (b & 0x7F) << (7 * n)
        if not b & 0x80:
            return result
        n += 1

def frame(payload):
    return write_varint(len(payload)) + payload

def read_frame(sock):
    length = read_varint(sock)
    data = b""
    while len(data) < length:
        data += sock.recv(length - len(data))
    return data

host, port = sys.argv[1], int(sys.argv[2])

s = socket.create_connection((host, port), timeout=5)

# 1. 握手: [id=0x00][protocol=776][addr][port][next_state=1]
hs = write_varint(0x00) + write_varint(776) + write_varint(len("127.0.0.1")) + b"127.0.0.1" + struct.pack(">H", port) + write_varint(1)
s.sendall(frame(hs))

# 2. 状态请求: [id=0x00]
s.sendall(frame(write_varint(0x00)))

# 3. 读状态响应: [id=0x00][json]
resp = read_frame(s)
pid = read_varint_from = None
buf = read_frame  # 占位
# 手动解析: 包 ID VarInt
def parse_varint(data):
    result, n = 0, 0
    for i, b in enumerate(data):
        result |= (b & 0x7F) << (7 * i)
        if not b & 0x80:
            return result, i + 1
    raise ValueError("varint 越界")

pid, off = parse_varint(resp)
jlen, off2 = parse_varint(resp[off:])
status = json.loads(resp[off + off2: off + off2 + jlen])

# 4. Ping: [id=0x01][uint64 时间戳]
ts = 0x1122334455667788
s.sendall(frame(write_varint(0x01) + struct.pack(">Q", ts)))
pong = read_frame(s)
ppid, poff = parse_varint(pong)
pts = struct.unpack(">Q", pong[poff:poff + 8])[0]

print("── Gopherite 冒烟测试 ──")
print("状态响应包 ID :", hex(pid))
print("版本          :", json.dumps(status.get("version"), ensure_ascii=False))
print("玩家          :", json.dumps(status.get("players"), ensure_ascii=False))
print("MOTD          :", repr(status["description"]["text"]))
print("图标          :", (status.get("favicon") or "")[:40] + "...")
print("enforcesSecureChat:", status.get("enforcesSecureChat"))
print("Pong 包 ID    :", hex(ppid), "| 时间戳回显:", hex(pts), "| 一致:", pts == ts)
print("JSON 总长     :", jlen, "bytes")
