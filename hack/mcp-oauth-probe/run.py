#!/usr/bin/env python3
"""Real Codex OAuth against the isolated Dex/PG fixture; never prints credentials."""
import argparse
import datetime
import hashlib
import json
import queue
import re
import shutil
import socket
import subprocess
import tempfile
import threading
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent


def save(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


class Pipe:
    def __init__(self, command, cwd):
        # Child diagnostics can contain OAuth URLs; discard instead of saving raw logs.
        self.proc = subprocess.Popen(command, cwd=cwd, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=subprocess.DEVNULL, text=True, bufsize=1)
        self.queue = queue.Queue()
        self.serial = 0
        self.notifications = []
        def read():
            for line in self.proc.stdout:
                try:
                    self.queue.put(json.loads(line))
                except json.JSONDecodeError:
                    pass
            self.queue.put(None)
        threading.Thread(target=read, daemon=True).start()

    def write(self, value):
        self.proc.stdin.write(json.dumps(value) + "\n")
        self.proc.stdin.flush()

    def receive(self, timeout=120):
        try:
            value = self.queue.get(timeout=timeout)
        except queue.Empty:
            raise RuntimeError("probe subprocess response timed out") from None
        if value is None:
            raise RuntimeError("probe subprocess ended without a response")
        return value

    def control(self, action, **kwargs):
        self.write({"action": action, **kwargs})
        result = self.receive()
        if not result.get("ok"):
            raise RuntimeError("fixture control failed for " + action)
        return result

    def rpc(self, method, params, allow_error=False):
        self.serial += 1
        request_id = self.serial
        self.write({"id": request_id, "method": method, "params": params})
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            response = self.receive(max(1, deadline-time.monotonic()))
            if response.get("method") == "mcpServer/oauthLogin/completed":
                # This notification has no authorization URL or credential.
                p = response["params"]
                self.notifications.append({"name": p["name"], "success": p["success"], "has_error": bool(p.get("error"))})
            if response.get("id") == request_id:
                if "error" in response and not allow_error:
                    raise RuntimeError("Codex RPC failed for " + method + " (code " + str(response["error"].get("code")) + ")")
                return response
        raise RuntimeError("Codex RPC timed out for " + method)

    def close(self):
        if self.proc.poll() is not None:
            return
        self.proc.stdin.close()
        try:
            self.proc.wait(timeout=8)
        except subprocess.TimeoutExpired:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait()


def settings(name, endpoint, callback, port, existing):
    values = {"mcp_oauth_credentials_store": "file", "web_search": "disabled", "approval_policy": "never",
              "project_doc_max_bytes": 0, "suppress_unstable_features_warning": True,
              f"mcp_servers.{name}.url": endpoint, f"mcp_servers.{name}.oauth_resource": endpoint,
              f"mcp_servers.{name}.oauth.client_id": name,
              f"mcp_servers.{name}.oauth.callback_url": callback,
              f"mcp_servers.{name}.oauth.callback_port": port,
              f"mcp_servers.{name}.enabled": True, f"mcp_servers.{name}.required": True,
              f"mcp_servers.{name}.startup_timeout_sec": 20,
              f"mcp_servers.{name}.tool_timeout_sec": 20,
              f"mcp_servers.{name}.scopes": ["read", "offline_access"],
              f"mcp_servers.{name}.tools.pipeline.approval_mode": "approve"}
    for other, transport in existing.items():
        if not re.fullmatch(r"[A-Za-z0-9_-]+", other):
            raise RuntimeError("Unexpected existing MCP name; refusing incomplete isolation")
        values[f"mcp_servers.{other}.enabled"] = False
        if transport == "stdio":
            values[f"mcp_servers.{other}.command"] = "/usr/bin/false"
    for feature in ("shell_tool", "apps", "plugins", "remote_plugin", "hooks", "multi_agent", "memories", "skill_search",
                    "browser_use", "computer_use", "image_generation", "workspace_dependencies", "sleep_tool"):
        values["features."+feature] = False
    values["features.skip_host_skill_discovery"] = True
    result = []
    for key, value in values.items():
        result += ["-c", key + "=" + json.dumps(value)]
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    root = args.out.resolve()
    root.mkdir(parents=True, exist_ok=False)
    (root/"source").mkdir()
    sources = [HERE/"run.py", HERE/"main.go", REPO/"atc/worker/jetbridge/brine/steps/mcp_oauth_probe.go"]
    source_hashes = {}
    for path in sources:
        shutil.copyfile(path, root/"source"/path.name)
        source_hashes[str(path.relative_to(REPO))] = hashlib.sha256(path.read_bytes()).hexdigest()
    binary = args.binary.resolve()
    compiled_sources = [*sorted((REPO/"atc/mcp").glob("*.go")),
                        *sorted((REPO/"skymarshal/mcpauth").glob("*.go")),
                        REPO/"atc/api/mcpserver/server.go",
                        *[REPO/"atc/worker/jetbridge/brine/steps"/name for name in
                          ("auth_fixture.go","auth_sessions.go","mcp_auth.go","mcp_oauth_probe.go","resources.go")]]
    def hashes():
        return {str(p.relative_to(REPO)):hashlib.sha256(p.read_bytes()).hexdigest() for p in compiled_sources}
    compiled_hashes=hashes()
    build=subprocess.run(["go","build","-buildvcs=false","-o",str(binary),str(HERE/"main.go")],
                         cwd=REPO/"atc/worker/jetbridge/brine",capture_output=True,text=True)
    if build.returncode or hashes()!=compiled_hashes:
        raise SystemExit("Probe build failed or inputs changed during build; no client login attempted")
    codex = shutil.which("codex")
    version = subprocess.run([codex,"--version"],capture_output=True,text=True,check=True).stdout.strip()
    # The CLI inventory may contain inline configured headers. Consume only names;
    # never persist, print, or reuse transports, credentials or header values.
    listing = subprocess.run([codex,"mcp","list","--json"],capture_output=True,text=True,check=True)
    existing = {x["name"]:x["transport"]["type"] for x in json.loads(listing.stdout)}
    del listing
    with socket.socket() as sock:
        sock.bind(("127.0.0.1",0))
        port = sock.getsockname()[1]
    callback = f"http://127.0.0.1:{port}/callback"
    name = "jetbridge-oauth-probe-"+str(port)
    fixture = app = None
    flags = None
    evidence = {"codex_version": version, "started_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                "client_id": name, "exact_callback": callback, "model_inference": False,
                "source_sha256": source_hashes, "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                "compiled_input_sha256": compiled_hashes,
                "existing_mcp_servers_disabled": len(existing), "steps": []}
    failure = None
    try:
        fixture = Pipe([str(binary)], REPO)
        endpoint = fixture.control("start",client_id=name,callback=callback)["endpoint"]
        evidence["endpoint"] = endpoint
        flags = settings(name,endpoint,callback,port,existing)
        # Verify all other configured servers are disabled before starting any MCP connection.
        inventory = subprocess.run([codex,"mcp","list","--json",*flags],capture_output=True,text=True,check=True)
        enabled = [x["name"] for x in json.loads(inventory.stdout) if x["enabled"]]
        del inventory
        if enabled != [name]:
            raise RuntimeError("Codex MCP isolation check failed")
        evidence["only_fixture_enabled"] = True
        with tempfile.TemporaryDirectory(prefix="jb-codex-oauth-work-") as cwd:
            def start_app():
                process = Pipe([codex,"app-server","--stdio",*flags],cwd)
                process.rpc("initialize",{"clientInfo":{"name":"jetbridge-oauth-probe","version":"1"},"capabilities":{"experimentalApi":True}})
                process.write({"method":"initialized","params":{}})
                return process
            app = start_app()

            def login(scopes):
                result = app.rpc("mcpServer/oauth/login",{"name":name,"scopes":scopes+["offline_access"],"timeoutSecs":60})["result"]
                fixture.control("approve",url=result["authorizationUrl"],scopes=scopes)
                del result
                # Request ordering lets the login completion notification be recorded without polling a model.
                deadline = time.monotonic()+20
                while time.monotonic()<deadline:
                    app.rpc("mcpServerStatus/list",{"detail":"toolsAndAuthOnly"})
                    if app.notifications and app.notifications[-1]["success"]:
                        break
                    time.sleep(0.2)
                else:
                    raise RuntimeError("Codex OAuth completion was not observed")
                evidence["steps"].append({"step":"login","selected_scopes":scopes,"success":True})
                print("Completed fixture OAuth login:", ", ".join(scopes), flush=True)

            def thread():
                result=app.rpc("thread/start",{"ephemeral":True,"cwd":cwd,"approvalPolicy":"never","sandbox":"read-only",
                    "baseInstructions":"This ephemeral task is an automated local MCP OAuth fixture; do not run a model turn."})["result"]
                return result["thread"]["id"]

            def grouped_read(thread_id,label,allow_error=False):
                result=app.rpc("mcpServer/tool/call",{"threadId":thread_id,"server":name,"tool":"pipeline",
                    "arguments":{"request":{"operation":"pipeline_get","arguments":{"team":"auth-team","pipeline":"private","instance_vars":{}}}}},allow_error=allow_error)
                evidence["steps"].append({"step":label,"response":result})
                print("Completed direct Codex call:", label, flush=True)
                return result

            login(["read"])
            first=thread()
            app.rpc("mcpServerStatus/list",{"threadId":first,"detail":"toolsAndAuthOnly"})
            grouped_read(first,"read_only_group_call")
            fixture.control("advance",seconds=16*60)
            grouped_read(first,"expired_access_group_call",allow_error=True)
            app.close()
            app = start_app()
            restarted = thread()
            # A second expiry requires the restarted process to use the saved,
            # rotated refresh credential, not merely its still-valid access token.
            fixture.control("advance",seconds=16*60)
            grouped_read(restarted,"rotated_credential_process_restart_read")
            revoked=fixture.control("revoke")["revoked"]
            if revoked<1:
                raise RuntimeError("No fixture grant was revoked")
            evidence["steps"].append({"step":"server_grant_revocation","revoked":revoked})
            grouped_read(restarted,"revoked_group_call",allow_error=True)
            app.close()
            app = start_app()
            login(["read","pipelines:write"])
            second=thread()
            app.rpc("mcpServerStatus/list",{"threadId":second,"detail":"toolsAndAuthOnly"})
            grouped_read(second,"new_consent_group_call")
            evidence["oauth_completions"] = app.notifications
            evidence["steps"].append({"step":"final_server_grant_revocation","revoked":fixture.control("revoke")["revoked"]})
    except Exception as exc:
        # Errors here are our fixed descriptions, never raw child stderr or OAuth bodies.
        failure = type(exc).__name__ + ": " + str(exc)
        evidence["failure"] = failure
    finally:
        if app:
            app.close()
        if flags:
            logout = subprocess.run([codex,"mcp","logout",name,*flags],capture_output=True,text=True,timeout=30)
            evidence["local_test_credential_logout_exit"] = logout.returncode
        if fixture:
            try:
                evidence["http_events"] = fixture.control("events")["events"]
                fixture.control("close")
                evidence["fixture_cleanup"] = True
            finally:
                fixture.close()
        evidence["finished_utc"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        save(root/"evidence.json",evidence)
    print("Evidence:",root/"evidence.json")
    if failure:
        raise SystemExit(failure)


if __name__ == "__main__":
    main()
