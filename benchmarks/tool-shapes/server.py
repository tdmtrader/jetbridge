#!/usr/bin/env python3
"""Minimal newline-delimited stdio MCP fixture. Never runs real JetBridge actions."""
import argparse
import json
import sys
from pathlib import Path
from scenarios import CASES
from shapes import Surface, SHAPES
from world import World


def serve(shape, case, state_file, max_calls=40):
    world = World(CASES[case])
    surface = Surface(shape, world)
    listed = []

    def persist():
        payload = {"state": world.state, "surface_events": surface.events, "catalog": surface.tools,
                   "catalog_listed": listed, "principal": world.principal}
        tmp = state_file.with_suffix(".tmp")
        tmp.write_text(json.dumps(payload, indent=2) + "\n")
        tmp.replace(state_file)

    persist()
    for line in sys.stdin:
        req = json.loads(line)
        method, params = req.get("method"), req.get("params", {})
        if "id" not in req:
            continue
        if method == "initialize":
            result = {"protocolVersion": "2025-11-25", "capabilities": {"tools": {}},
                      "serverInfo": {"name": "jetbridge-benchmark", "version": "1"}, "instructions": surface.instructions()}
        elif method == "tools/list":
            listed.append([t["name"] for t in surface.tools]); persist()
            result = {"tools": surface.tools}
        elif method == "tools/call":
            if len(surface.events) >= max_calls:
                response = {"ok": False, "error": {"code": "CALL_LIMIT", "message": "Benchmark tool-call limit reached. Finish with current evidence."}}
                surface.events.append({"tool": params.get("name"), "arguments": params.get("arguments", {}), "response": response})
            else:
                response = surface.call(params["name"], params.get("arguments", {}))
            persist()
            result = {"content": [{"type": "text", "text": json.dumps(response, separators=(",", ":"))}], "isError": not response["ok"]}
        elif method == "ping":
            result = {}
        elif method in ("resources/list", "resources/templates/list", "prompts/list"):
            result = {{"resources/list": "resources", "resources/templates/list": "resourceTemplates", "prompts/list": "prompts"}[method]: []}
        else:
            print(json.dumps({"jsonrpc": "2.0", "id": req["id"], "error": {"code": -32601, "message": "Method not supported by fixture"}}), flush=True)
            continue
        print(json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": result}), flush=True)


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--shape", choices=SHAPES, required=True)
    p.add_argument("--case", choices=list(CASES), required=True)
    p.add_argument("--state-file", type=Path, required=True)
    p.add_argument("--max-calls", type=int, default=40)
    a = p.parse_args()
    serve(a.shape, a.case, a.state_file, a.max_calls)
