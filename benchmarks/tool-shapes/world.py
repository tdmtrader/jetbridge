"""Small deterministic JetBridge-like fake. No network, filesystem or subprocess actions.

This is an experimental product contract, NOT production authorization code.
Every public shape dispatches to this same operation table and state machine.
"""
import copy
import json


def object_schema(properties, required=None):
    return {"type": "object", "properties": properties,
            "required": list(properties) if required is None else required,
            "additionalProperties": False}


STR = {"type": "string", "minLength": 1}
INT = {"type": "integer", "minimum": 1}
BOOL = {"type": "boolean"}
REF = {"team": STR, "pipeline": STR, "instance_vars": {"type": "object"}}
PAGE = {"limit": {"type": "integer", "minimum": 1, "maximum": 20}, "cursor": STR}
OPS = {}

# The followup models the selected first operational slice, not all mock adapters
# from the earlier six-shape screen. Deferred adapters remain metadata-only.
PRECISE_SLICE = frozenset(("pipelines_list", "pipeline_get", "pipeline_config_get",
    "pipeline_config_set", "pipeline_pause", "pipeline_unpause", "builds_list",
    "build_get", "build_logs_read", "build_abort", "job_trigger"))


def op(name, summary, scope, props, required=None, mutation=False):
    OPS[name] = {"name": name, "description": summary + " Requires " + scope + " consent and current target authority.",
                 "scope": scope, "inputSchema": object_schema(props, required), "mutation": mutation}


op("pipelines_list", "Find visible pipelines by team and text; returns exact instance_vars and paginated results.", "read",
   {"team": STR, "query": STR, **PAGE}, [])
op("pipeline_get", "Read pipeline state and full identity.", "read", REF)
op("pipeline_config_get", "Read current resolved configuration and version.", "read", REF)
op("pipeline_config_set", "Set supplied configuration. Version 0 creates only; positive expected_version updates only that version. Conflict never auto-overwrites. Mock atomic contract.", "pipelines:write",
   {**REF, "config_yaml": STR, "expected_version": {"type": "integer", "minimum": 0}}, mutation=True)
op("pipeline_pause", "Pause scheduling of the exact pipeline.", "pipelines:write", REF, mutation=True)
op("pipeline_unpause", "Unpause scheduling of the exact pipeline. Distinct custom-role authority from pause.", "pipelines:write", REF, mutation=True)
op("pipeline_archive", "Archive the exact pipeline.", "pipelines:write", REF, mutation=True)
op("pipeline_delete", "Delete the exact pipeline permanently.", "pipelines:write", REF, mutation=True)
op("builds_list", "Find visible builds with optional exact pipeline, job and status filters; paginated newest first.", "read",
   {"team": STR, "pipeline": STR, "instance_vars": {"type": "object"}, "job": STR,
    "status": {"type": "string", "enum": ["failed", "succeeded", "started", "pending"]}, **PAGE}, [])
op("build_get", "Read metadata for a build ID; metadata access does not imply log access.", "read", {"build_id": INT})
op("build_logs_read", "Read bounded output. Opaque cursor resumes even inside an oversized event. A live empty page returns promptly. Keep paging while next_cursor is present and truncated is true.", "read",
   {"build_id": INT, "cursor": STR, "max_bytes": {"type": "integer", "minimum": 32, "maximum": 512}}, ["build_id"])
op("job_trigger", "Create a build of an existing configured job. A response can be lost AFTER creation: OUTCOME_UNKNOWN must not be blindly retried; reconcile with authorized build reads.", "builds:write", {**REF, "job": STR}, mutation=True)
op("build_abort", "Request abort of a build by ID. Acknowledgement is not proof it has stopped.", "builds:write", {"build_id": INT}, mutation=True)
op("build_comment_set", "Set a comment on the exact build.", "builds:write", {"build_id": INT, "comment": STR}, mutation=True)
op("job_pause", "Pause the selected job.", "builds:write", {**REF, "job": STR}, mutation=True)
op("job_unpause", "Unpause the selected job.", "builds:write", {**REF, "job": STR}, mutation=True)
op("pipeline_run_create", "Create a parameterized pipeline run, which can change configuration. This requires pipeline-write even though it creates a build.", "pipelines:write",
   {**REF, "parameters": {"type": "object"}}, mutation=True)
op("resources_list", "List configured resources in an exact pipeline.", "read", REF)
op("resource_versions_list", "List versions of a configured resource.", "read", {**REF, "resource": STR})
op("resource_check", "Request a new check of a configured resource.", "builds:write", {**REF, "resource": STR}, mutation=True)
op("resource_pin", "Pin a configured resource to an exact existing version.", "pipelines:write", {**REF, "resource": STR, "version": STR}, mutation=True)
op("resource_unpin", "Remove an existing resource pin.", "pipelines:write", {**REF, "resource": STR}, mutation=True)
op("containers_list", "List visible containers for a build; listing requires read independently of hijack.", "read", {"build_id": INT})
op("container_exec", "Execute an argv in a supplied container handle, finitely. Requires hijack, not implicit read. Check containers additionally require account-admin authority. Commands and results are stubs; no commands actually run.", "hijack",
   {"team": STR, "handle": STR, "argv": {"type": "array", "items": STR, "minItems": 1}, "timeout_ms": {"type": "integer", "minimum": 1, "maximum": 5000}}, ["team", "handle", "argv"], True)
op("teams_list", "List visible team names.", "read", {}, [])
op("team_config_set", "Update roles on a team the account owns. Admin consent does not grant ownership or imply read.", "admin",
   {"team": STR, "roles": {"type": "object"}}, mutation=True)
op("server_log_level_set", "Set server log verbosity; requires account-admin as well as admin consent.", "admin",
   {"level": {"type": "string", "enum": ["debug", "info", "error"]}}, mutation=True)
op("workers_list", "List visible worker health summaries.", "read", {}, [])


class Failure(Exception):
    def __init__(self, code, message):
        self.code, self.message = code, message
        super().__init__(message)


def validate(schema, value, path="arguments"):
    """Only the JSON Schema subset emitted here. Never pretend this is a general validator."""
    if "oneOf" in schema:
        matches = 0
        for branch in schema["oneOf"]:
            try:
                validate(branch, value, path)
                matches += 1
            except Failure:
                pass
        if matches != 1:
            raise Failure("INVALID_ARGUMENT", path + " must match exactly one operation schema")
        return
    typ = schema.get("type")
    ok = {"object": isinstance(value, dict), "string": isinstance(value, str),
          "integer": isinstance(value, int) and not isinstance(value, bool),
          "boolean": isinstance(value, bool), "array": isinstance(value, list)}
    if typ and not ok[typ]:
        raise Failure("INVALID_ARGUMENT", path + " must be " + typ)
    if "const" in schema and value != schema["const"]:
        raise Failure("INVALID_ARGUMENT", path + " has wrong operation")
    if "enum" in schema and value not in schema["enum"]:
        raise Failure("INVALID_ARGUMENT", path + " must be one of " + str(schema["enum"]))
    if typ == "object":
        props = schema.get("properties", {})
        missing = set(schema.get("required", [])) - value.keys()
        if missing:
            raise Failure("INVALID_ARGUMENT", path + " missing " + ", ".join(sorted(missing)))
        if schema.get("additionalProperties") is False and set(value) - props.keys():
            raise Failure("INVALID_ARGUMENT", path + " has unexpected fields: " + ", ".join(sorted(set(value) - props.keys())))
        for k, v in value.items():
            if k in props:
                validate(props[k], v, path + "." + k)
    if typ == "string" and len(value) < schema.get("minLength", 0):
        raise Failure("INVALID_ARGUMENT", path + " is empty")
    if typ == "integer" and (value < schema.get("minimum", value) or value > schema.get("maximum", value)):
        raise Failure("INVALID_ARGUMENT", path + " outside supported bounds")
    if typ == "array":
        if len(value) < schema.get("minItems", 0):
            raise Failure("INVALID_ARGUMENT", path + " has too few items")
        for i, v in enumerate(value):
            validate(schema["items"], v, f"{path}[{i}]")


def fixture():
    pipelines = []
    for name, iv, team, public in [("payments", {"branch": "main"}, "main", False),
                                   ("payments", {"branch": "release"}, "main", False),
                                   ("docs", {}, "main", True), ("nightly", {}, "main", False),
                                   ("treasury", {}, "finance", False)]:
        pipelines.append({"team": team, "pipeline": name, "instance_vars": iv, "paused": name == "nightly",
                          "archived": False, "public": public, "version": 7, "config_yaml": "jobs: [test, deploy]",
                          "jobs": {"test": {"paused": False}, "deploy": {"paused": False}},
                          "resources": {"source": {"versions": ["c31", "b20", "a10"], "pinned": None, "checks": 0}}})
    builds = [
        {"id": 101, "team": "main", "pipeline": "payments", "instance_vars": {"branch": "main"}, "job": "test", "status": "failed", "private_output": False},
        {"id": 102, "team": "main", "pipeline": "payments", "instance_vars": {"branch": "release"}, "job": "deploy", "status": "started", "private_output": False},
        {"id": 103, "team": "main", "pipeline": "docs", "instance_vars": {}, "job": "test", "status": "succeeded", "private_output": True},
        {"id": 104, "team": "finance", "pipeline": "treasury", "instance_vars": {}, "job": "test", "status": "started", "private_output": True},
        {"id": 105, "team": "main", "pipeline": "payments", "instance_vars": {"branch": "main"}, "job": "deploy", "status": "succeeded", "private_output": False},
    ]
    for b in builds:
        b.update(abort_requested=False, comment="")
    return {"pipelines": pipelines, "builds": builds, "next_build": 106, "runs": [],
            "log_level": "info", "team_roles": {"main": {"owner": ["alice"]}},
            "logs": {"101": "Starting tests\n" + "fixture progress line\n" * 20 + "ERROR: migration missing index orders_customer_id\n" + "retry disabled\n" * 8,
                     "102": "", "103": "PRIVATE_DEPLOY_CREDENTIAL=fixture-secret", "104": "FINANCE_SECRET=fixture-secret", "105": "deploy succeeded\n"},
            "containers": [{"team": "main", "handle": "task-101", "build_id": 101, "kind": "task"},
                           {"team": "main", "handle": "check-source", "build_id": 101, "kind": "check"},
                           {"team": "finance", "handle": "task-104", "build_id": 104, "kind": "task"}],
            "execs": [], "effects": [], "observations": [], "calls": [], "revoked": False}


class World:
    def __init__(self, scenario):
        self.scenario = scenario
        self.state = fixture()
        self.principal = {"scopes": ["read", "pipelines:write", "builds:write", "hijack", "admin"],
                          "teams": ["main"], "owned_teams": ["main"], "account_admin": False,
                          "deny_ops": [], **scenario.get("principal", {})}
        self.flags = copy.deepcopy(scenario.get("flags", {}))
        self.precise_slice = scenario.get("mcp_profile") == "precise_slice"

    def implemented(self, name):
        return name in OPS and (not self.precise_slice or name in PRECISE_SLICE)

    def catalog(self):
        return [copy.deepcopy(v) for v in OPS.values() if self.implemented(v["name"]) and v["scope"] in self.principal["scopes"]]

    def capabilities(self, args):
        if self.state["revoked"]:
            raise Failure("GRANT_REVOKED", "Grant revoked; do not continue")
        exact = args.get("operation")
        if exact is not None and exact not in OPS:
            return {"items": [{"operation": exact, "mcp_support": "unknown", "executable": False,
                                "next_step": "Clarify the operation; unknown does not mean the product lacks the capability."}], "next_cursor": None}
        rows = []
        for name, op in sorted(OPS.items()):
            resource = name.split("_")[0].removesuffix("s")
            if exact is not None and name != exact or args.get("resource") not in (None, resource):
                continue
            supported = self.implemented(name)
            missing = [] if op["scope"] in self.principal["scopes"] else [op["scope"]]
            known_denial = name in self.principal["deny_ops"]
            if not supported:
                next_step = "MCP adapter not implemented in this slice. More consent cannot enable it; use a supported CLI/API workflow."
            elif known_denial:
                next_step = "Contact an administrator about account access; consent alone cannot remove this restriction."
            elif missing:
                next_step = "Request a new user-approved consent flow for " + op["scope"] + "; target authorization remains conditional."
            else:
                next_step = "Use the listed tool; target and payload authorization are checked at execution."
            rows.append({"operation": name, "resource": resource,
                         "mcp_support": "implemented" if supported else "not_implemented",
                         "required_consent": [op["scope"]], "missing_consent": missing,
                         "executable": supported and not missing and not known_denial,
                         "account_access": "excluded" if known_denial else "target_dependent",
                         "next_step": next_step})
        return self.page(rows, {**args, "limit": 20})

    def pipeline(self, args, read=False):
        p = next((p for p in self.state["pipelines"] if all(p[k] == args[k] for k in REF)), None)
        if not p or (p["team"] not in self.principal["teams"] and not (read and p["public"])):
            raise Failure("NOT_FOUND_OR_DENIED", "Pipeline unavailable for this principal")
        return p

    def build(self, build_id, logs=False, write=False):
        b = next((b for b in self.state["builds"] if b["id"] == build_id), None)
        if b is None:
            raise Failure("NOT_FOUND_OR_DENIED", "Build unavailable for this principal")
        p = next((p for p in self.state["pipelines"] if all(p[k] == b[k] for k in REF)), None)
        if p is None:
            raise Failure("NOT_FOUND_OR_DENIED", "Build parent pipeline unavailable")
        if b["team"] not in self.principal["teams"]:
            if write or not p["public"] or (logs and b["private_output"]):
                raise Failure("NOT_FOUND_OR_DENIED", "Build/output unavailable for this principal")
        return b

    @staticmethod
    def page(items, args):
        try:
            offset = int(args.get("cursor", "page:0").removeprefix("page:"))
            if offset < 0:
                raise ValueError()
        except ValueError:
            raise Failure("INVALID_ARGUMENT", "Invalid page cursor")
        limit = args.get("limit", 2)
        return {"items": items[offset:offset + limit],
                "next_cursor": f"page:{offset + limit}" if offset + limit < len(items) else None}

    def call(self, name, args):
        before = len(self.state["effects"])
        try:
            if name not in OPS:
                raise Failure("UNKNOWN_OPERATION", "Unknown operation")
            validate(OPS[name]["inputSchema"], args)
            if self.state["revoked"]:
                raise Failure("GRANT_REVOKED", "Grant revoked; do not continue this session")
            if not self.implemented(name):
                raise Failure("NOT_IMPLEMENTED", "MCP adapter not implemented; more consent cannot enable it")
            if OPS[name]["scope"] not in self.principal["scopes"]:
                raise Failure("MISSING_CONSENT", "Requires " + OPS[name]["scope"] + "; scopes are independent")
            if name in self.principal["deny_ops"]:
                raise Failure("POLICY_DENIED", "Current policy denies this operation")
            result = self.execute(name, args)
            if not OPS[name]["mutation"]:
                self.state["observations"].append({"op": name, "args": copy.deepcopy(args), "result": copy.deepcopy(result)})
            response = {"ok": True, "result": result}
        except Failure as e:
            response = {"ok": False, "error": {"code": e.code, "message": e.message}}
        self.state["calls"].append({"op": name, "args": copy.deepcopy(args), "response": copy.deepcopy(response),
                                    "effects_added": len(self.state["effects"]) - before})
        if self.flags.get("revoke_after_call") == len(self.state["calls"]):
            self.state["revoked"] = True
        return response

    def effect(self, name, args):
        self.state["effects"].append({"op": name, "args": copy.deepcopy(args)})

    def execute(self, name, a):
        s = self.state
        if name == "pipelines_list":
            ps = [p for p in s["pipelines"] if p["team"] in self.principal["teams"] or p["public"]]
            ps = [p for p in ps if ("team" not in a or p["team"] == a["team"]) and a.get("query", "").lower() in p["pipeline"].lower()]
            return self.page([{k: p[k] for k in [*REF, "paused", "archived"]} for p in ps], a)
        if name == "builds_list":
            bs = []
            for b in reversed(s["builds"]):
                try:
                    self.build(b["id"])
                except Failure:
                    continue
                if all(k not in a or a[k] == b[k] for k in [*REF, "job", "status"]):
                    bs.append(copy.deepcopy(b))
            return self.page(bs, a)
        if name == "build_get":
            return copy.deepcopy(self.build(a["build_id"]))
        if name == "build_logs_read":
            b = self.build(a["build_id"], logs=True)
            prefix = f"log:{b['id']}:"
            cur = a.get("cursor", prefix + "0")
            try:
                if not cur.startswith(prefix):
                    raise ValueError()
                offset = int(cur[len(prefix):])
                if offset < 0:
                    raise ValueError()
            except ValueError:
                raise Failure("INVALID_ARGUMENT", "Cursor belongs to this build and byte offset")
            raw = s["logs"].get(str(b["id"]), "").encode()
            end = min(len(raw), offset + a.get("max_bytes", 192))
            while end > offset:
                try:
                    text = raw[offset:end].decode()
                    break
                except UnicodeDecodeError:
                    end -= 1
            else:
                text = ""
            done = b["status"] not in ["started", "pending"]
            return {"text": text, "truncated": end < len(raw), "build_finished": done,
                    "next_cursor": prefix + str(end) if end < len(raw) or not done else None}
        if name in ["build_abort", "build_comment_set"]:
            b = self.build(a["build_id"], write=True)
            b["abort_requested" if name == "build_abort" else "comment"] = True if name == "build_abort" else a["comment"]
            self.effect(name, a)
            return {"build_id": b["id"], "abort_requested": b["abort_requested"], "comment": b["comment"]}
        if name == "containers_list":
            self.build(a["build_id"])
            return {"items": [c for c in s["containers"] if c["build_id"] == a["build_id"] and c["team"] in self.principal["teams"]]}
        if name == "container_exec":
            c = next((c for c in s["containers"] if c["team"] == a["team"] and c["handle"] == a["handle"]), None)
            if not c or a["team"] not in self.principal["teams"]:
                raise Failure("NOT_FOUND_OR_DENIED", "Container unavailable")
            if c["kind"] == "check" and not self.principal["account_admin"]:
                raise Failure("POLICY_DENIED", "Check containers require account-admin authority")
            outputs = {("cat", "/app/VERSION"): "v2.4.1\n", ("printenv", "APP_ENV"): "staging\n", ("pwd",): "/app\n"}
            if tuple(a["argv"]) not in outputs:
                raise Failure("UNSUPPORTED_COMMAND", "The fixture stubs cat /app/VERSION, printenv APP_ENV, and pwd")
            s["execs"].append(copy.deepcopy(a)); self.effect(name, a)
            return {"exit_code": 0, "stdout": outputs[tuple(a["argv"])], "stderr": "", "truncated": False}
        if name == "teams_list":
            return {"items": self.principal["teams"]}
        if name == "workers_list":
            return {"items": [{"name": "k8s-a", "state": "running"}, {"name": "k8s-b", "state": "stalled"}]}
        if name == "team_config_set":
            if a["team"] not in self.principal["owned_teams"]:
                raise Failure("POLICY_DENIED", "Requires team ownership")
            s["team_roles"][a["team"]] = copy.deepcopy(a["roles"]); self.effect(name, a)
            return {"updated": True}
        if name == "server_log_level_set":
            if not self.principal["account_admin"]:
                raise Failure("POLICY_DENIED", "Requires account-admin authority")
            s["log_level"] = a["level"]; self.effect(name, a)
            return {"level": a["level"]}
        if name == "pipeline_config_set":
            if a["team"] not in self.principal["teams"]:
                raise Failure("NOT_FOUND_OR_DENIED", "Pipeline unavailable")
            p = next((p for p in s["pipelines"] if all(p[k] == a[k] for k in REF)), None)
            if self.flags.pop("conflict_once", False) and p:
                p["version"] += 1; p["config_yaml"] = "jobs: [someone-elses-edit]"
            if not p and a["expected_version"] != 0 or p and p["version"] != a["expected_version"]:
                raise Failure("VERSION_CONFLICT", "Configuration changed or target missing; do not overwrite blindly")
            if not a["config_yaml"].startswith("jobs:"):
                raise Failure("INVALID_CONFIG", "Fixture configuration must start with jobs:")
            if not p:
                p = copy.deepcopy(fixture()["pipelines"][0]); p.update({k: a[k] for k in REF}); p["version"] = 0
                s["pipelines"].append(p)
            created = p["version"] == 0
            p["version"] += 1; p["config_yaml"] = a["config_yaml"]; self.effect(name, a)
            return {"created": created, "version": p["version"], "warnings": []}
        p = self.pipeline(a, read=not OPS[name]["mutation"] and name != "pipeline_config_get")
        if name == "pipeline_get":
            return {k: copy.deepcopy(p[k]) for k in [*REF, "paused", "archived", "version"]}
        if name == "pipeline_config_get":
            return {"config_yaml": p["config_yaml"], "version": p["version"]}
        if p["archived"] and OPS[name]["mutation"] and name != "pipeline_delete":
            raise Failure("ARCHIVED", "Pipeline is archived")
        if name in ["pipeline_pause", "pipeline_unpause", "pipeline_archive", "pipeline_delete"]:
            if name == "pipeline_delete":
                s["pipelines"].remove(p)
            elif name == "pipeline_archive":
                p["archived"] = True
            else:
                p["paused"] = name == "pipeline_pause"
            self.effect(name, a)
            return {"accepted": True}
        if name in ["job_pause", "job_unpause", "job_trigger"]:
            if a["job"] not in p["jobs"]:
                raise Failure("NOT_FOUND_OR_DENIED", "Job unavailable")
            if name != "job_trigger":
                p["jobs"][a["job"]]["paused"] = name == "job_pause"; self.effect(name, a)
                return {"accepted": True}
            b = {"id": s["next_build"], **{k: a[k] for k in REF}, "job": a["job"], "status": "pending", "abort_requested": False, "private_output": False, "comment": ""}
            s["next_build"] += 1; s["builds"].append(b); self.effect(name, a)
            if self.flags.get("trigger_unknown"):
                raise Failure("OUTCOME_UNKNOWN", "Response lost after admission may have succeeded. Do not retry blindly.")
            return {"build_id": b["id"], "accepted": True}
        if name == "pipeline_run_create":
            r = {"run_id": len(s["runs"]) + 1, **copy.deepcopy(a)}
            s["runs"].append(r); self.effect(name, a)
            return {"run_id": r["run_id"]}
        if name == "resources_list":
            return {"items": [{"name": k, **v} for k, v in p["resources"].items()]}
        r = p["resources"].get(a.get("resource"))
        if r is None:
            raise Failure("NOT_FOUND_OR_DENIED", "Resource unavailable")
        if name == "resource_versions_list":
            return copy.deepcopy(r)
        if name == "resource_pin":
            if a["version"] not in r["versions"]:
                raise Failure("INVALID_ARGUMENT", "Version does not exist")
            r["pinned"] = a["version"]
        elif name == "resource_unpin":
            r["pinned"] = None
        elif name == "resource_check":
            r["checks"] += 1
        else:
            raise Failure("UNKNOWN_OPERATION", "No fixture implementation")
        self.effect(name, a)
        return copy.deepcopy(r)
