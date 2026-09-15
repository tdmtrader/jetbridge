#!/usr/bin/env python3
"""Run the bounded HTTP schema fixture and actual reference/Codex clients.

Only synthetic loopback grants are used. Existing Codex login is used by Codex
itself; this script never reads credentials or modifies user MCP configuration.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

HERE = Path(__file__).resolve().parent


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


def verify_results(out, results):
    for result in results:
        calls = result["completed_mcp_calls"]
        expected = ["pipelines_list"] if result["case"].startswith("pruned") else ["pipelines_list", "pipeline_get"]
        actual = [call["arguments"]["request"]["operation"] for call in calls]
        if result["exit_code"] or actual != expected:
            raise RuntimeError("unexpected Codex calls in " + result["case"])
        blocked = result["case"] == "mixed-default-policy"
        for call in calls:
            if call["server"] != "schema_probe" or call["tool"] != "pipeline":
                raise RuntimeError("non-fixture call")
            if blocked:
                if not call.get("error") or "requires approval" not in call["error"].get("message", ""):
                    raise RuntimeError("expected conservative mixed-group approval denial")
            elif call.get("error") or call.get("result", {}).get("isError") or not call.get("result"):
                raise RuntimeError("expected successful fixture read")
    server_events = [json.loads(line) for line in (out / "server-events.jsonl").read_text().splitlines()]
    dispatches = [e for e in server_events if e["event"] == "dispatch"]
    # Five valid reference calls plus five valid Codex calls. Rejected schemas
    # and client-blocked calls must never reach a handler; no mutation is requested.
    if len(dispatches) != 10 or any(e["arguments"]["request"]["operation"] == "pipeline_pause" for e in dispatches):
        raise RuntimeError("unexpected handler dispatches")
    write_json(out / "verification.json", {"passed": True, "reference_valid_calls": 5,
               "reference_invalid_calls": 7, "codex_valid_calls": 5, "codex_approval_denials": 2,
               "mutation_calls": 0, "protocols_observed": sorted({e["protocol"] for e in server_events if e.get("protocol")})})


def run_codex(endpoint, out, mode, approved):
    case = mode + ("-approved" if approved else "-default-policy")
    dest = out / case
    dest.mkdir()
    instructions = """You are checking an isolated synthetic MCP fixture. Use only its tools.
Do not inspect local files, run shell commands, browse, edit code, or invoke other agents.
Call only read operations; do not call pipeline_pause. Do not bypass denied approvals.
Report only actual tool results and availability or approval failures. This is fixture data.
"""
    (dest / "instructions.md").write_text(instructions)
    task = "List pipelines in team main, then read pipeline payments in team main. Use each operation only once. If the second operation is absent or denied, explain that and stop."
    (dest / "prompt.txt").write_text(task + "\n")
    settings = {
        "model_reasoning_effort": "low",
        "model_instructions_file": str(dest / "instructions.md"),
        "web_search": "disabled", "approval_policy": "never", "project_doc_max_bytes": 0,
        "mcp_servers.schema_probe.url": endpoint,
        "mcp_servers.schema_probe.bearer_token_env_var": "JB_SCHEMA_FIXTURE_GRANT",
        "mcp_servers.schema_probe.required": True,
        "mcp_servers.schema_probe.startup_timeout_sec": 20,
        "mcp_servers.schema_probe.tool_timeout_sec": 20,
        "suppress_unstable_features_warning": True,
    }
    for feature in ("shell_tool", "apps", "plugins", "remote_plugin", "hooks", "multi_agent", "memories", "skill_search",
                    "browser_use", "computer_use", "image_generation", "workspace_dependencies", "sleep_tool"):
        settings["features." + feature] = False
    settings["features.skip_host_skill_discovery"] = True
    if approved:
        # Explicit fixture-only preapproval lets us separate union acceptance
        # from the host's conservative group-wide approval identity.
        settings["mcp_servers.schema_probe.tools.pipeline.approval_mode"] = "approve"
    with tempfile.TemporaryDirectory(prefix="jb-schema-codex-") as cwd:
        cmd = ["codex", "exec", "--ignore-user-config", "--ephemeral", "--skip-git-repo-check",
               "--sandbox", "read-only", "--cd", cwd, "--json", "--color", "never",
               "--model", "gpt-6-astra", "--output-last-message", str(dest / "final.txt")]
        for key, value in settings.items():
            cmd += ["-c", key + "=" + json.dumps(value)]
        cmd += ["-"]
        write_json(dest / "command.json", cmd)
        env = dict(os.environ, JB_SCHEMA_FIXTURE_GRANT="fixture-only-" + mode)
        started = time.monotonic()
        with (dest / "events.jsonl").open("w") as events, (dest / "stderr.txt").open("w") as errors:
            process = subprocess.run(cmd, input=task, text=True, env=env, stdout=events, stderr=errors, timeout=120)
        stream = [json.loads(line) for line in (dest / "events.jsonl").read_text().splitlines() if line.startswith("{")]
        calls = [x["item"] for x in stream if x.get("type") == "item.completed" and x.get("item", {}).get("type") == "mcp_tool_call"]
        result = {"case": case, "exit_code": process.returncode, "seconds": round(time.monotonic() - started, 3),
                  "completed_mcp_calls": calls,
                  "turns": [x for x in stream if x.get("type") in ("turn.completed", "turn.failed", "error")]}
        write_json(dest / "result.json", result)
        print(case, process.returncode, len(calls), "completed MCP call events", flush=True)
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    version = subprocess.run(["codex", "--version"], text=True, capture_output=True, check=True).stdout.strip()
    write_json(out / "manifest.json", {
        "started_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "codex": version, "model": "gpt-6-astra", "effort": "low", "sdk": "v1.6.1", "declared_server_profile": "2025-11-25; SDK backward-compatible negotiation",
        "transport": "stateless Streamable HTTP with JSON responses on numeric loopback",
        "auth": "synthetic fixture-only bearer labels; no OAuth enrollment proof",
        "schema_source": "atc/mcp.Operations, GroupInputSchema and GroupOutputSchema, fixture handlers only",
        "http_wrapper": "atc/api/mcpserver.NewHTTPHandler",
        "source_sha256": {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(HERE.glob("*")) if p.is_file()},
    })
    with (out / "server.stderr.txt").open("w") as stderr:
        server = subprocess.Popen([str(args.binary), "--out", str(out / "server-events.jsonl")], text=True, stdout=subprocess.PIPE, stderr=stderr)
        try:
            endpoint = server.stdout.readline().strip()
            if not endpoint.startswith("http://127.0.0.1:"):
                raise RuntimeError("fixture did not return numeric loopback endpoint")
            (out / "endpoint.txt").write_text(endpoint + "\n")
            subprocess.run([str(args.binary), "--mode", "reference-check", "--endpoint", endpoint, "--out", str(out / "reference.json")], check=True)
            results = [run_codex(endpoint, out, mode, approved) for mode, approved in
                       (("read", False), ("pruned", False), ("mixed", False), ("mixed", True))]
            write_json(out / "codex-results.json", results)
            verify_results(out, results)
            print("all compatibility observations verified", flush=True)
        finally:
            server.terminate()
            server.wait(timeout=10)


if __name__ == "__main__":
    main()
