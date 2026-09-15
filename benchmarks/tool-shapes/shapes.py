"""Alternative presentations of the same operations. No backend behavior here."""
import copy
import re
from world import OPS, REF, BOOL, STR, Failure, object_schema, validate

SHAPES = ("flat", "compact", "resource_union", "resource_loose", "discovery", "search_inline")


def public_tool(name, description, schema, mutation=False):
    return {"name": name, "description": description, "inputSchema": schema,
            "annotations": {"readOnlyHint": not mutation, "destructiveHint": mutation,
                            "idempotentHint": False, "openWorldHint": True}}


def group(name):
    return name.split("_")[0].removesuffix("s")


class Surface:
    def __init__(self, shape, world):
        if shape not in SHAPES:
            raise ValueError(shape)
        self.shape, self.world = shape, world
        self.operations = {x["name"]: x for x in world.catalog()}
        self.tools = self.make_tools()
        if world.precise_slice:
            self.tools.append(public_tool("capabilities_explain",
                "Explain a capability's MCP support, missing consent and known account restriction without reading targets or granting access. Browse a resource family or an exact operation. Unsupported adapters cannot be enabled by more consent. At most 20 metadata entries per page.",
                object_schema({"resource": {"type": "string", "enum": ["pipeline", "build", "job", "resource", "container", "team", "server", "worker"]}, "operation": STR, "cursor": STR}, [])))
        self.events = []

    def instructions(self):
        base = ("JetBridge fixture operations: pipeline discovery/configuration/scheduling, builds/logs/abort, "
                "job triggers, resource versions/checks/pins, finite container execution, team and server administration. "
                "Each tool enforces independent consent plus target authority. Scope-filtered catalogs do not prove target access. ")
        if self.world.precise_slice:
            base = ("JetBridge first-slice fixture: pipeline reads/configuration/pause, build reads/logs/abort, and configured-job triggers. "
                    "Other product capabilities may not have MCP adapters. Use capabilities_explain when a requested action is absent or blocked. "
                    "Consents are independent; catalog presence does not guarantee target access. ")
        if self.shape in ("discovery", "search_inline"):
            base += "Search operation descriptions (empty query browses); execute exact operation IDs. "
            base += "Describe selected operations for their precise schemas. " if self.shape == "discovery" else "Search includes precise schemas. "
        return base

    def make_tools(self):
        ops = list(self.operations.values())
        if self.shape in ("flat", "compact"):
            ts = [public_tool(o["name"], o["description"], o["inputSchema"], o["mutation"]) for o in ops]
            if self.shape == "compact":
                for prefix in ("pipeline", "job"):
                    names = {prefix + "_pause", prefix + "_unpause"}
                    if not names.intersection(self.operations):
                        continue
                    ts = [t for t in ts if t["name"] not in names]
                    props = {**REF, **({"job": STR} if prefix == "job" else {}), "paused": BOOL}
                    ts.append(public_tool(prefix + "_set_paused", "Set desired pause state. Selects and authorizes the exact pause or unpause operation, including distinct policy checks.", object_schema(props), True))
            return ts
        if self.shape.startswith("resource_"):
            ts = []
            for resource in sorted({group(o["name"]) for o in ops}):
                children = [o for o in ops if group(o["name"]) == resource]
                if self.shape == "resource_union":
                    branches = [object_schema({"operation": {"type": "string", "const": o["name"]}, "arguments": o["inputSchema"]}) for o in children]
                    schema = object_schema({"request": {"oneOf": branches}})
                else:
                    schema = object_schema({"operation": {"type": "string", "enum": [o["name"] for o in children]}, "arguments": {"type": "object"}})
                descriptions = []
                for o in children:
                    fields = ", ".join(k + ("?" if k not in o["inputSchema"]["required"] else "") for k in o["inputSchema"]["properties"])
                    descriptions.append(o["name"] + "(" + fields + "): " + o["description"])
                ts.append(public_tool(resource, "\n".join(descriptions), schema, any(o["mutation"] for o in children)))
            return ts
        search = public_tool("operations_search", "Search supported operations by intent or exact ID. Empty query browses deterministically. Discovery is consent-filtered; not target authorization." + (" Returns input schemas." if self.shape == "search_inline" else " Describe a result for its schema."),
                             object_schema({"query": {"type": "string"}, "limit": {"type": "integer", "minimum": 1, "maximum": 30}, "cursor": STR}, ["query"]))
        describe = public_tool("operation_describe", "Get the exact input schema and consent for a supported operation ID.", object_schema({"id": STR}))
        execute = public_tool("operation_execute", "Execute a supported operation ID with its exact arguments. May mutate data or execute commands; each chosen operation rechecks its own consent/authority. Discovery is not permission.", object_schema({"id": STR, "arguments": {"type": "object"}}), True)
        return [search, describe, execute] if self.shape == "discovery" else [search, execute]

    def search(self, a):
        aliases = {"stop": "abort", "cancel": "abort", "output": "logs", "failure": "failed logs", "rerun": "trigger",
                   "freeze": "pause", "resume": "unpause", "configuration": "config", "terminal": "container exec",
                   "shell": "container exec", "permissions": "team roles", "permission": "team roles"}
        words = re.findall(r"[a-z0-9]+", a["query"].lower())
        words += [v for w in words for v in aliases.get(w, "").split()]
        ranked = []
        for o in self.operations.values():
            text = o["name"].replace("_", " ") + " " + o["description"].lower()
            score = sum(w in text for w in words) + (10 if a["query"] == o["name"] else 0)
            if score or not words:
                ranked.append((-score, o["name"], o))
        ranked.sort(key=lambda x: (x[0], x[1]))
        rows = []
        for _, _, o in ranked:
            row = {"id": o["name"], "description": o["description"], "scope": o["scope"]}
            if self.shape == "search_inline":
                row["inputSchema"] = o["inputSchema"]
            rows.append(row)
        result = self.world.page(rows, {**a, "limit": a.get("limit", 5)})
        result["hint"] = "Use an exact ID, another keyword, or empty query to browse if no result matches."
        return result

    def call(self, name, a):
        try:
            t = next((t for t in self.tools if t["name"] == name), None)
            if not t:
                raise Failure("UNKNOWN_TOOL", "Tool not in this consent-filtered surface")
            validate(t["inputSchema"], a)
            if self.world.state["revoked"] and name in ("operations_search", "operation_describe"):
                raise Failure("GRANT_REVOKED", "Grant revoked; do not continue")
            if name == "capabilities_explain":
                response = {"ok": True, "result": self.world.capabilities(a)}
            elif name == "operations_search":
                response = {"ok": True, "result": self.search(a)}
            elif name == "operation_describe":
                if a["id"] not in self.operations:
                    raise Failure("UNKNOWN_OPERATION", "Operation unavailable under current consent")
                response = {"ok": True, "result": copy.deepcopy(self.operations[a["id"]])}
            else:
                if name == "operation_execute":
                    op, args = a["id"], a["arguments"]
                elif self.shape.startswith("resource_"):
                    req = a["request"] if self.shape == "resource_union" else a
                    op, args = req["operation"], req["arguments"]
                elif name.endswith("_set_paused"):
                    op = name.removesuffix("_set_paused") + ("_pause" if a["paused"] else "_unpause")
                    args = {k: v for k, v in a.items() if k != "paused"}
                else:
                    op, args = name, a
                response = self.world.call(op, args)
        except Failure as e:
            response = {"ok": False, "error": {"code": e.code, "message": e.message}}
        self.events.append({"tool": name, "arguments": copy.deepcopy(a), "response": copy.deepcopy(response)})
        return response

    def encode_call(self, name, args):
        """Oracle/test adapter only; never offered to the evaluated model."""
        if self.shape in ("discovery", "search_inline"):
            return "operation_execute", {"id": name, "arguments": args}
        if self.shape.startswith("resource_"):
            request = {"operation": name, "arguments": args}
            return group(name), {"request": request} if self.shape == "resource_union" else request
        if self.shape == "compact" and name in ("pipeline_pause", "pipeline_unpause", "job_pause", "job_unpause"):
            return name.split("_")[0] + "_set_paused", {**args, "paused": name.endswith("_pause")}
        return name, args
