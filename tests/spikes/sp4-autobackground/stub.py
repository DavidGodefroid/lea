#!/usr/bin/env python3
"""Stub MCP server (stdio) for spike SP4 (#157): one tool, `approve`, that waits WAIT seconds
(default 180, a TTY approval) and then answers "ticket 42 approved". Logs to calls.log."""
import json
import os
import sys
import threading
import time

WAIT = float(os.environ.get("WAIT", "180"))
LOG = os.path.join(os.environ.get("SP4_DIR", os.path.dirname(os.path.abspath(__file__))), "calls.log")
lock = threading.Lock()


def log(s):
    with open(LOG, "a") as f:
        f.write(time.strftime("%H:%M:%S ") + s + "\n")


def send(o):
    with lock:
        sys.stdout.write(json.dumps(o) + "\n")
        sys.stdout.flush()


def call(mid):
    time.sleep(WAIT)
    log("answering call %s" % mid)
    send({"jsonrpc": "2.0", "id": mid, "result": {
        "content": [{"type": "text", "text": "ticket 42 approved"}]}})


for line in sys.stdin:
    m = json.loads(line)
    mid, meth = m.get("id"), m.get("method")
    log("%s %s" % (meth, json.dumps(m.get("params"))[:300]))
    if meth == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "protocolVersion": m["params"].get("protocolVersion", "2025-06-18"),
            "capabilities": {"tools": {}}, "serverInfo": {"name": "stub", "version": "0"}}})
    elif meth == "tools/list":
        send({"jsonrpc": "2.0", "id": mid, "result": {"tools": [{
            "name": "approve", "description": "Asks a human to approve ticket 42; can take minutes",
            "inputSchema": {"type": "object", "properties": {}}}]}})
    elif meth == "tools/call":
        threading.Thread(target=call, args=(mid,), daemon=True).start()
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "method not found"}})
