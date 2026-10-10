# Precise resource groups and capability explanations

On 2026-09-14 the owner selected **precise resource groups** after the local
tool-shape benchmark. This replaces the earlier flat-tool default and the proposed
additional shape-selection round. The selected surface remains ordinary MCP
tools with exact operation-discriminated input schemas.

This document records the current code contract. Deployment is separate. The
benchmark and its original scores remain historical evidence, not conformance
results for the reduced discovery contract.

## Executable tools

Generate each resource tool's nested `request.oneOf` from the operations currently
eligible for the connection. For example, `pipeline` may contain get/config-get
branches for one connection, and config-set/pause branches for another. Do not
retain a forbidden branch behind an optional parameter. Omit empty resource tools.

Eligibility intersects:

1. An implemented, enabled MCP adapter with an explicit operation classification.
2. The connection's consent for that exact operation.
3. Account requirements that can actually be established without a target.

Use existing API accessors and effective custom roles for the third check. Prune
account-admin-only actions for non-admins, and team-role actions when no eligible
team exists. Preserve public-read routes that remain usable without team membership.
Do not infer authorization kind from the default role table alone: API wrappers
have additional distinctions. Pause and unpause may have different custom roles
and must be pruned independently.

**Tool listing establishes operation eligibility, not authority over every possible
argument.** Team/pipeline/instance, build-output visibility, payload
policy and operation preconditions remain execution checks. There is no existing
targetless external-policy entitlement API. Do not probe policy with empty targets
or interpret an advisory denial as a blocking rule.

At execution, select the exact operation, recheck its independent consent, and use
the existing fully authorized API boundary with authenticated trusted claims.
Never authorize only the outer resource-tool name. Unknown classifications fail
closed. Mixed read/write groups keep conservative effect/approval annotations.
Client approval applies to the whole resource tool in the tested Codex runtime.
A mixed group may therefore require approval for reads too; a pruned read-only
group keeps read-only annotations. A client-policy refusal is not missing OAuth
consent, and the server diagnostic cannot promise the client will allow a call.

## One informational tool

**`capabilities_explain`** explains registered operations, including those absent
from the connection's executable catalog. It cannot execute operations and does
not return their argument schemas. It supports browsing one resource family and
optionally an exact operation.

```json
{
  "name": "capabilities_explain",
  "description": "Explain MCP support and this connection's access to a resource family's capabilities. Use when an action is absent from the executable tools. Entries are descriptions, not executable tools or target-access guarantees.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "resource": {"type": "string", "minLength": 1, "maxLength": 256},
      "operation": {"type": "string", "minLength": 1, "maxLength": 256}
    },
    "required": ["resource"],
    "additionalProperties": false
  },
  "annotations": {
    "readOnlyHint": true,
    "destructiveHint": false,
    "idempotentHint": true,
    "openWorldHint": false
  }
}
```

Return all matching registered operations in `{items: [...]}`, without a cursor.
An absent exact operation or resource returns an `isError` tool result with
`NO_REGISTERED_OPERATION: no registered MCP operation`. This does not claim that
the CLI/API lacks the action, and additional consent cannot register it. An exact
registered ID inconsistent with the selected resource is an argument error.
Guessed calls to absent operations or resource tools (including `container`,
`resource`, `team` and `server`) return the same clear tool error, never an OAuth
consent challenge. Ordinary actions continue directly through their resource tool.

The C10 cut reduces the registry from 20 definitions (11 executable and nine
metadata-only entries) to 11 executable operations: six pipeline, four build and
one job. It removes discovery's base64 **offset** cursor, whose 20-entry limit
exceeded every resource's inventory. The executable list/log tools retain their
pagination. Retired IDs are no longer valid disabled-operation restrictions.

This diagnostic is available to **every valid authenticated MCP grant**, including
write-only, hijack-only and admin-only grants. It reports generic product metadata
and the caller's own permission context, so it requires no application `read`
consent and no new consent category. Revoked or invalid grants cannot use it.

## Example: permission could enable an implemented action

Illustrative response for `pipeline_config_set`:

```text
capabilities_explain({resource: "pipeline", operation: "pipeline_config_set"})
```

```json
{
  "items": [{
    "operation": "pipeline_config_set",
    "description": "Apply supplied YAML with an atomic version precondition. Version 0 creates only; a positive version updates only that version. Never retry a conflict automatically. No implicit read or credential expansion.",
    "mcp_support": "implemented",
    "executable_branch_present": false,
    "missing_scopes": ["pipelines:write"],
    "account_access": "target_check_required",
    "next_step": "request_new_consent",
    "explanation": "Ask the user whether to authorize pipeline writes for this connection. This may enable the operation on permitted targets; it does not grant a team role."
  }]
}
```

Support means **MCP support in this deployment**. The registry contains only
operations with adapters; it does not inventory other CLI/API actions.

| Situation | Explanation and next step |
|---|---|
| Implemented, missing consent, no known account-wide denial | Name the exact missing scopes; `request_new_consent`. Success is still subject to target checks. |
| Implemented, provable account restriction | `account_access: ineligible`; `contact_admin`. Additional consent alone will not help. Report missing consent too if both blockers exist. |
| Implemented, consent present, remaining checks depend on target | `account_access: target_check_required`; `use_available_tool`, or `refresh_catalog` if the client retains an older schema. |
| Implemented but administratively disabled | `mcp_support: disabled`; explain operator action, without suggesting broader consent. |
| No registered operation matches | `NO_REGISTERED_OPERATION` tool error. No consent request and no claim of product impossibility. |
| Access data cannot be evaluated | Return an ordinary temporary diagnostic error. Fail closed for affected executable branches; do not turn an outage into a permanent permission or support claim. |

`account_access` normally remains `target_check_required`; do not advertise a
general `eligible` status. Explain a known global exclusion only when the existing
authorization rules prove it. No target IDs, object existence, raw identity claims,
other teams' names or private policy-rule details appear in this metadata.

## Consent, privacy and freshness

Consent categories stay independent. `admin` never implies read/build/pipeline/
hijack consent. Reauthorization means a new user-approved consent flow: the shipped
refresh path rotates credentials but cannot broaden the existing grant. The agent
requests only the relevant category and never implies that consent grants a role.

Keep diagnostics targetless initially. Execution against a hidden or nonexistent
target preserves the existing combined unavailable/not-permitted response. Do not
perform extra existence probes to produce a more specific explanation. An operation's
missing scope can be explained without looking up its target.

Reuse existing role/cache freshness and immediate MCP grant revocation. Derive
catalogs for the authenticated principal and current grant, never cache solely by
scope set across users. After new consent or a known access change, re-list tools.
Stale schemas confer no execution authority. Adding list-change notifications can
be evaluated later; the current stateless transport does not require them.

## Shared implementation and checks

Use one operation definition for resource family, stable ID, description,
input/output schemas, exact API action and consent, and effect/retry semantics.
Every definition must bind to a complete adapter. Generate the executable union and
capability explanations from it. Reuse the role hierarchy and policy enforcement
already owned by the API; do not build a second policy interpreter for discovery.

Implementation order:

1. Define the selected contract and existing action classifications. Preserve the
   shipped `pipeline_status` signature while introducing a grouped read slice.
2. Add branch pruning and the metadata diagnostic using the same authenticated
   context. Check missing scope, known role denial, both blockers, admin-only and
   write-only grants, public reads, empty groups and absent operations.
3. Add a small set of precise write branches through the existing API, preserving
   independent pause/unpause policy. Check stale schemas, revocation between list
   and call, and indistinguishable hidden/nonexistent targets.
4. Preserve the historical benchmark scope-diagnosis evidence as a separate cohort;
   its unsupported/unknown taxonomy is not current server conformance. Check one actual
   client displays/loads the pruned union and re-lists after reauthorization.

The repository audit identifies these existing integration points: AccessFactory
and effective roles in `atc/api/accessor/`; authorization kinds in
`atc/wrappa/api_auth_wrappa.go`; concrete policy requests and blocking/advisory
semantics in `atc/api/policychecker/`; current consent dispatch in `atc/mcp/`.
Build metadata and build logs, team update and team creation, and ordinary versus
check-container execution retain their different checks.

No MCP protocol, transport, SDK, dynamic registration or client-ID metadata change
is required. This is an ordinary application tool plus permission-filtered schemas
on the declared [2025-11-25 tool protocol](https://modelcontextprotocol.io/specification/2025-11-25/server/tools).
Preserve shipped HTTP authentication/scope challenges and the existing
[authorization profile](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization).

The independent repository and architecture reviewers support this contract.
The owner's choice of precise grouping is recorded; capability-diagnostic details
are presented for review. This document does not claim production implementation
or deployment.
