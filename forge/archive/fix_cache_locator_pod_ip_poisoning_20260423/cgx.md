# Conductor Growth Experience (CGX)

**Track:** `fix_cache_locator_pod_ip_poisoning_20260423`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Frustrations & Friction

<!-- Log moments of frustration, confusion, or repeated attempts -->
<!-- Format: - [YYYY-MM-DD] Description of friction point -->

---

## Patterns Observed

### Good Patterns (to encode)
<!-- Workflows that worked well and should be automated/standardized -->

### Anti-Patterns (to prevent)
<!-- Mistakes or inefficiencies that should be caught earlier -->

---

## Missing Capabilities

<!-- Tools, commands, or features that would have helped -->
<!-- Format: - Description | Suggested solution | Scope (project/global) -->

---

## Insights & Suggestions

<!-- General observations about improving the development experience -->

### ArtifactLocator.Record audit (2026-04-23)

After the fix, all production callers of `ArtifactLocator.Record(key, nodeName, hostDir)`
were re-audited to confirm the `nodeName` argument is always a real K8s Node
object name and never a pod or daemon IP.

| Site | nodeName source | Verdict |
| --- | --- | --- |
| `atc/worker/jetbridge/storage_daemonset.go:425` (`RecordOutputs`) | flows from `process.go:915` `p.fetchPodNodeName(ctx)` which returns `pod.Spec.NodeName` | ✓ real K8s Node name |
| `atc/worker/jetbridge/storage_daemonset.go:552` (`RegisterResourceCache`) | flows from `worker.go:320` `dsb.artifactLocator.LocateNode(...)` — only ever populated by `RecordOutputs` above | ✓ real K8s Node name (transitive) |
| ~~`atc/worker/jetbridge/worker.go:366` (`FindDaemonResourceCache`)~~ | (deleted) used to write `daemonIP` from `ProbeResourceCache` — the bug | ✗ removed in `fc36f66737` |

No other production sites. Test fixtures use synthetic node-name strings
("node-1", "node-a", etc.) which match the contract. The `worker_test.go`
stale-entry test deliberately writes an IP-shaped string into the locator
(`locator.Record("rc-42", "10.0.0.99", ...)`) to exercise the dead-node
guard path — this is intentional and the test asserts the cache is treated
as a miss, so no downstream lookup ever consults that entry.

Defense-in-depth: even if a future caller accidentally passes an IP,
`NodeIPResolver.Resolve` now short-circuits with `ErrNodeNameIsIP` before
hitting the K8s API.

---

## Improvement Candidates

<!-- Concrete suggestions for new/modified extensions -->
<!-- Format:
### [Type: skill|command|agent] Name
- **Scope:** project | global
- **Rationale:** Why this would help
- **Source:** Specific conversation/moment that inspired this
-->
