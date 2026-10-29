#!/usr/bin/env python3
"""Runs an interactive Claude Code session in a pty for spike SP4 (#157): answers the folder trust
dialog, types the prompt once the TUI is up, waits, then /exit.
Usage: drive.py <dir> <seconds> <screen file> <prompt> claude args...
Prints the screen text without escape sequences, with a timestamp every 10 s."""
import os
import pty
import re
import select
import sys
import time

d, dur, screen, prompt, argv = sys.argv[1], float(sys.argv[2]), sys.argv[3], sys.argv[4], sys.argv[5:]
ESC = rb"\x1b\[[0-9;?<>]*[a-zA-Z~]|\x1b\][^\x07]*\x07|\x1b[()][AB0]|\x1b[78=>]"
pid, fd = pty.fork()
if pid == 0:
    os.chdir(d)
    os.execvp(argv[0], argv)


def typ(s):
    for ch in s:
        os.write(fd, ch.encode())
        time.sleep(0.02)


out = open(screen, "wb")
buf, trusted, typed, t0, mark = b"", False, False, time.time(), 0.0
while time.time() - t0 < dur:
    r, _, _ = select.select([fd], [], [], 0.5)
    if r:
        try:
            chunk = os.read(fd, 65536)
        except OSError:
            break
        buf += chunk
        out.write(chunk)
    if time.time() - mark >= 10:
        mark = time.time()
        out.write(("\n[t+%ds]\n" % (mark - t0)).encode())
    out.flush()
    flat = re.sub(ESC + rb"|\s", b"", buf)
    if not trusted and b"Entertoconfirm" in flat and b"trust" in flat:
        time.sleep(1)
        os.write(fd, b"\x1b[B")  # "Yes, I trust this folder" is the second choice
        time.sleep(0.5)
        os.write(fd, b"\r")
        trusted = True
    if not typed and b"shift+tab" in buf:
        time.sleep(2)
        typ(prompt)
        os.write(fd, b"\r")
        typed = True
typ("/exit")
os.write(fd, b"\r")
time.sleep(4)
try:
    os.kill(pid, 9)
except ProcessLookupError:
    pass
