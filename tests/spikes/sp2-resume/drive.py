#!/usr/bin/env python3
"""Runs an interactive Claude Code command in a pty for spike SP2 (#155), the way lea starts an
agent (`claude --resume <id> [prompt]`): answers the folder trust dialog, waits, then /exit.
Usage: drive.py <dir> <seconds> <screen file> claude args...
Prints the screen text without escape sequences."""
import os
import pty
import re
import select
import sys
import time

d, dur, screen, argv = sys.argv[1], float(sys.argv[2]), sys.argv[3], sys.argv[4:]
pid, fd = pty.fork()
if pid == 0:
    os.chdir(d)
    os.execvp(argv[0], argv)
buf, trusted, t0 = b"", False, time.time()
while time.time() - t0 < dur:
    r, _, _ = select.select([fd], [], [], 0.5)
    if r:
        try:
            buf += os.read(fd, 65536)
        except OSError:
            break
    if not trusted and b"Entertoconfirm" in re.sub(rb"\x1b\[[0-9;?<>]*[a-zA-Z~]|\s", b"", buf):
        time.sleep(1)
        os.write(fd, b"\x1b[B")  # "Yes, I trust this folder" is the second choice
        time.sleep(0.5)
        os.write(fd, b"\r")
        trusted = True
for ch in "/exit":
    os.write(fd, ch.encode())
    time.sleep(0.02)
os.write(fd, b"\r")
time.sleep(4)
try:
    os.kill(pid, 9)
except ProcessLookupError:
    pass
open(screen, "wb").write(buf)
text = re.sub(r"\x1b\[[0-9;?<>]*[a-zA-Z~]|\x1b\][^\x07]*\x07|\x1b[()][AB0]|\x1b[78=>]", "",
              buf.decode("utf8", "replace"))
print(text[-1500:])
