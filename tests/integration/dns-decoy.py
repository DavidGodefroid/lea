# A decoy DNS server for tests/integration/isolation.sh (#98): answers every query on port 53, UDP
# and TCP (an A record of 192.0.2.53 for type A, an empty answer otherwise), and prints
# "query <name> <type>" for each one, so the test can tell whether a query from inside lea reached it.
# The address to listen on is the first argument (default 0.0.0.0).
import socket
import struct
import sys
import threading

ADDR = sys.argv[1] if len(sys.argv) > 1 else "0.0.0.0"


def question(q):
    i, labels = 12, []
    while q[i]:
        labels.append(q[i + 1:i + 1 + q[i]].decode("ascii", "replace"))
        i += 1 + q[i]
    return ".".join(labels), struct.unpack(">H", q[i + 1:i + 3])[0], i + 5


def answer(q):
    name, qtype, end = question(q)
    print("query", name, qtype, flush=True)
    r = q[:2] + b"\x81\x80" + q[4:6] + (b"\x00\x01" if qtype == 1 else b"\x00\x00") + b"\x00\x00\x00\x00" + q[12:end]
    if qtype == 1:
        r += b"\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x3c\x00\x04" + bytes([192, 0, 2, 53])
    return r


def serve_tcp(s):
    while True:
        c, _ = s.accept()
        try:
            c.settimeout(5)
            n, q = struct.unpack(">H", c.recv(2))[0], b""
            while len(q) < n:
                b = c.recv(n - len(q))
                if not b:
                    break
                q += b
            r = answer(q)
            c.sendall(struct.pack(">H", len(r)) + r)
        except (OSError, IndexError, struct.error):
            pass
        finally:
            c.close()


u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
u.bind((ADDR, 53))
t = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
t.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
t.bind((ADDR, 53))
t.listen(16)
threading.Thread(target=serve_tcp, args=(t,), daemon=True).start()
print("decoy-dns ready", flush=True)
while True:
    q, a = u.recvfrom(4096)
    try:
        u.sendto(answer(q), a)
    except (OSError, IndexError, struct.error):
        pass
