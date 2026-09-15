# Selected MCP shape for JetBridge

Use **precise resource groups** with ordinary MCP `tools/list` and `tools/call`.
Each branch fixes the operation and its argument/result schemas. Expose only
implemented, enabled operations allowed by independent consent and provable
account restrictions. Omit empty groups. Keep `capabilities_explain` available to
any authenticated grant so absent tools can be distinguished from missing consent
or account authority. All execution still crosses the authorized API boundary.

This is the owner's selected direction after the local experiment, superseding
the earlier conventional-catalog starting recommendation. It is implemented on
the unmerged precise MCP branch, with no deployment change.

| Shape | Practical tradeoff | Local evidence |
| --- | --- | --- |
| Conventional typed catalog | Most direct schemas and separate approval identities; more names exposed to clients that eagerly load tools. | 28/31 valid strict passes; matched-task token baseline. |
| Search → describe → execute | Small initial catalog but extra decisions/calls; generic execution needs a separately described schema. Discovery is an application design, not an MCP protocol requirement. | 28/32; 20.9% more total tokens and 115 calls versus flat 53 on the same 28 successes. |
| Client-managed search/deferred loading | Can keep the complete ordinary catalog while controlling model context; depends on the actual client. | Not independently tested; schema text returned by a mock search tool is not native deferred loading. |
| Precise resource groups | Exact operation branches, fewer names, shared implementation metadata; client approval can apply to the entire mixed group. | 28/31; 7.5% fewer matched-task tokens in this single screen. Chosen by owner. |
| Generic resource arguments | Fewer schemas and strong token challenger, but weaker validation/recovery guidance. | 28/32; 12.1% fewer matched-task tokens. Insufficient evidence to establish a reliability winner. |

There were 32 tasks × 6 shapes = 192 sessions, one trial per cell. Permission information
was absent from the filtered catalog in four scenarios; 19 strict failures were
missing-scope explanations, with no checked unauthorized or duplicate effects.
A separate eight-session precise-diagnostic followup passed all eight, using
183,758 total tokens. Changed prompts and catalog size prevent a clean A/B claim.
See the original results (`benchmarks/tool-shapes/baseline/RESULTS.md` in `~/jetbridge-evidence/precise-mcp-20260915/` (sha256 in its `MANIFEST.sha256`, out of tree)) and
[followup results](../../benchmarks/tool-shapes/RESULTS.md).

## Representative interaction

Tools expose nested `request.oneOf`, with a branch equivalent to:

```json
{
  "type": "object",
  "properties": {
    "operation": {"const": "pipeline_pause"},
    "arguments": {
      "type": "object",
      "properties": {
        "team": {"type": "string"},
        "pipeline": {"type": "string"},
        "instance_vars": {"type": "object"}
      },
      "required": ["team", "pipeline", "instance_vars"],
      "additionalProperties": false
    }
  },
  "required": ["operation", "arguments"],
  "additionalProperties": false
}
```

Actual schemas add bounds and are derived from the
[operation registry](../../atc/mcp/operations.go). Calls include:

```json
{"name":"pipeline","arguments":{"request":{"operation":"pipelines_list","arguments":{"query":"deploy","limit":20}}}}
{"name":"build","arguments":{"request":{"operation":"build_logs_read","arguments":{"build_id":42,"max_bytes":32768}}}}
{"name":"job","arguments":{"request":{"operation":"job_trigger","arguments":{"team":"main","pipeline":"deploy","instance_vars":{},"job":"unit"}}}}
{"name":"capabilities_explain","arguments":{"resource":"pipeline","operation":"pipeline_config_set"}}
```

A read-only grant's last call reports missing `pipelines:write` and
`request_new_consent`, subject to account/target checks. A `container_exec`
diagnostic reports an absent adapter; more `hijack` consent cannot implement it.
`admin` implies no other category. Config changes use supplied YAML plus an atomic
version precondition; trigger and abort retain distinct effect/retry semantics.

## Implementation and remaining experiment

The completed first slice adds operation contracts/catalog pruning and diagnostics,
then shared strict config writes, finite list/log reads, the remaining exact
mutations, and real-client/benchmark verification. See the
[implementation review](../plans/precise-mcp/implementation-review.md) for evidence
and rollout/rollback, and [usage/contracts](../mcp-auth.md) for API and CLI details.
Shared API actions/DTOs and domain validation give CLI/API/MCP a common execution
contract; the UI may adopt the same additive APIs. No generated application
framework or alternate authorization engine is introduced.

No new protocol capability, DCR or client-ID metadata document is required.
Codex CLI 0.153.1 needs a pre-registered ID and exact callback/listener port;
it negotiated 2025-06-18 with the declared SDK v1.6.1 / 2025-11-25 server. Real OAuth
renewal/revocation and re-listing passed. Interactive whole-group approval,
seamless same-process re-enrollment and other clients remain uncertain.
The next useful small experiment is one actual owner approval/re-enrollment
session in the intended production client on a disposable pipeline, followed
by a paired, repeated token comparison only if context cost becomes material.
Do not reopen the shape decision merely to expand the benchmark framework.

Current protocol evidence uses primary specifications and the pinned client
implementation, linked in the [client probe](../../hack/mcp-oauth-probe/README.md)
and [access contract](mcp-precise-access.md). MCP defines tool listing/calling;
client search/deferred schema loading and approval UI are separate client features.
