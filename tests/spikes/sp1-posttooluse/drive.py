#!/usr/bin/env python3
"""Drives an interactive Claude Code session in a pty: types the prompt once the TUI is up,
waits, then /exit. The raw screen goes to screen.log. The folder must already be trusted."""
import os
import pty
import select
import sys
import time

d, dur, prompt = sys.argv[1], float(sys.argv[2]), sys.argv[3]
pid, fd = pty.fork()
if pid == 0:
    os.chdir(d)
    os.execvp("claude", ["claude", "--mcp-config", "mcp.json", "--strict-mcp-config",
                         "--settings", "settings.json", "--allowedTools", "mcp__stub__wait"])
out, buf, typed, t0 = open(d + "/screen.log", "wb"), b"", False, time.time()


def typ(s):
    for ch in s:
        os.write(fd, ch.encode())
        time.sleep(0.02)


while time.time() - t0 < dur:
    r, _, _ = select.select([fd], [], [], 0.5)
    if r:
        try:
            chunk = os.read(fd, 65536)
        except OSError:
            break
        out.write(chunk)
        buf += chunk
    if not typed and b"shift+tab" in buf:
        time.sleep(2)
        typ(prompt)
        os.write(fd, b"\r")
        typed = True
typ("/exit")
os.write(fd, b"\r")
time.sleep(4)
os.kill(pid, 9)
