#!/usr/bin/env python3
"""Stub MCP server (stdio) for spike SP1 (#154): one tool, `wait`, that answers
{"status":"pending","ticket":42} as structuredContent and as its text block."""
import json
import sys


def send(o):
    sys.stdout.write(json.dumps(o) + "\n")
    sys.stdout.flush()


for line in sys.stdin:
    m = json.loads(line)
    mid, meth = m.get("id"), m.get("method")
    if meth == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "protocolVersion": m["params"].get("protocolVersion", "2025-06-18"),
            "capabilities": {"tools": {}}, "serverInfo": {"name": "stub", "version": "0"}}})
    elif meth == "tools/list":
        send({"jsonrpc": "2.0", "id": mid, "result": {"tools": [{
            "name": "wait", "description": "Request approval; returns status",
            "inputSchema": {"type": "object", "properties": {}}}]}})
    elif meth == "tools/call":
        sc = {"status": "pending", "ticket": 42}
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "content": [{"type": "text", "text": json.dumps(sc)}], "structuredContent": sc}})
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
