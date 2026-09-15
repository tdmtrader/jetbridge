# Self-review: precise MCP specification and plan

Round 2, 2026-09-14. Reviewed the selected contract, spec, plan, benchmark limits,
current MCP preflight, configuration handlers and authorization accessors. This
is an author self-review, supplementing the prior independent repository and
design reviews. No application implementation was performed.

Verdict: satisfied after the following planning corrections. No remaining
blocking finding or question requires an owner decision.

| Finding | Why it mattered | Resolution |
|---|---|---|
| Client feasibility was left until the final phase. | The local benchmark did not validate production HTTP/OAuth or grouped approval presentation; discovering incompatibility after backend work would be expensive. | Phase 1 now gates substantial backend work on a small authenticated HTTP check of the actual reference/Codex clients, exact unions and pruning. Final real-operation verification remains in phase 6. |
| Cross-page consistency was unspecified. | Builds and permissions can change while an agent traverses a list; a frozen-snapshot promise would require unplanned infrastructure. | R6 and phase 4 now define live keyset traversal with stable identity ordering, per-request filtering and explicit concurrent-change tests. |
| Configuration representation and encoded byte accounting were ambiguous. | “Resolved configuration” could be mistaken for secret expansion; counting payload bytes alone misses JSON escaping and duplicate text/structured output. | R5 uses `config_yaml`, preserves credential/template references and reuses API parsing. R6 and phase 2 count the entire encoded result; phase 4 checks continuation near the cap. |
| Grouped preflight could mislabel malformed calls as consent problems. | The current compatibility tool challenges by tool name alone. Extending that directly to a discriminator would contradict the new malformed-input requirement. | R3 and phase 2 explicitly validate the full declared branch before a scope challenge, without target reads; the existing alias keeps its shipped behavior. |

Evidence: [current MCP preflight](../../../atc/mcp/handler.go),
[configuration parser and credential-check opt-in](../../../atc/api/configserver/save.go),
[configuration/version reader](../../../atc/api/configserver/get.go), and
[benchmark review and limits](http://localhost:6170/#/jetbridge-hearth/hearth/track?id=20260914T1624_mcp_tool_shape_coarse_local_benchmark).

The review retains the selected resource grouping, independent consent categories,
permission-pruned schemas and per-operation API authorization. It adds no generic
execution service, new protocol, automatic consent expansion or broader first
release. The shared strict-write and finite-event backend work remains necessary
to meet the stated conflict and bounded-output contracts.

Remaining uncertainty is empirical: client acceptance/approval presentation and
whether capability explanations resolve the four benchmark diagnosis failures.
The early feasibility check and eight planned local sessions address those;
neither requires another preference decision now. Numeric response limits remain
initial defaults to validate with realistic data.

Updated [specification](spec.md) and [plan](plan.md) contain these corrections.
Local document links and requirement/phase coverage were checked. Production
tests were not run for this documentation-only pass. The Anvil track remains in
`plan_review`; no owner approval, commit, implementation or deployment is implied
by this self-review. The latest readable drafts are attached to Loupe round 2;
structured Anvil revisions preserve the same contract.
