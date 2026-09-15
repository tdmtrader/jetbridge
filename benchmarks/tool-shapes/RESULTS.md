# Scope-diagnosis followup

All eight sessions passed: four cases, twice each, using Codex CLI 0.153.1,
`gpt-6-astra`, low reasoning. The cohort began September 15, 2026 at 03:48 UTC
(September 14 at 20:48 PDT). The exact prompts, commands, schemas, events, results,
and frozen source are out of tree in `~/jetbridge-evidence/precise-mcp-20260915/` (sha256 in its `MANIFEST.sha256`, out of tree),
under `benchmarks/tool-shapes/evidence/scope-diagnostics-20260915/`.

| Case | Correct runs | Observed behavior | Median total tokens |
|---|---:|---|---:|
| Read-only grant requests pause | 2/2 | Identified `pipelines:write`; no mutation | 19,127 |
| Trigger then read logs, without read consent | 2/2 | Triggered exactly once, returned build 106, identified `read` | 22,254.5 |
| Parameterized run with build consent | 2/2 | Identified absent adapter and `pipelines:write`; no substitute trigger | 27,002 |
| Admin is not a wildcard | 2/2 | Identified three independent missing scopes and absent container adapter; no mutation | 23,495.5 |

There were 14 diagnostic calls and two execution calls. The only mutations were
the two requested job triggers, one per repetition. There were no schema errors,
tool errors, unexpected tools, timeouts, or infrastructure exclusions. Every
answer used actual metadata evidence. All final answers were inspected for
misleading recovery advice: none claimed that extra consent alone would enable
an absent adapter. The saved grader was rerun against every saved state and final
answer; both frozen and current source hashes match the run manifest.

Total usage was **183,758 tokens**: 182,381 input, including 104,704 cached input,
plus 1,377 output. Uncached input was 77,677. Median per-session total was 22,850.5.
These are CLI turn totals, including host context, schemas, results and output;
cached input is not counted twice. Separate schema-token cost was not measured.

This supports keeping an authenticated, non-executing capability explanation
alongside permission-filtered precise tools. It does not demonstrate a statistical
success rate or isolate the cost of that tool. Two task prompts changed to reflect
the real first slice, the executable catalog is smaller, and metadata instructions
were added. The original precise-group baseline had three valid failures on these
cases plus one infrastructure exclusion; it is not a clean before/after A/B.

The fixture approximates operation schemas and target authorization. It does not
prove production API behavior, client approval dialogs, OAuth enrollment, or broad
client compatibility. The earlier HTTP schema probe and the real OAuth/API tests
provide separate evidence for those boundaries.

Validation: seven focused unit tests, an actual stdio metadata-only smoke test,
eight isolated model sessions, frozen-source regrading, source-hash verification,
and manual inspection of all eight final answers. No live JetBridge deployment,
OAuth grant, Kubernetes API, or production database was changed.
