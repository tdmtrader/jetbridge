# Builds fail with `nodes "100.68.228.107" not found` whenever the resource cache hits

**Type:** bug / root-cause investigation
**Component:** JetBridge K8s runtime — DaemonSet artifact cache
**Reported:** 2026-04-23 (operator, `theborg` cluster)
**Severity:** high — resource-cache reuse is effectively broken on the cluster

## Symptom

Since the artifact-read routing work went out on the 18th, pipelines on the live
cluster have been failing in a way we have not seen before. The failing step is
never the `get` — the `get` reports a cache hit and goes green. The *next* step,
whichever one first reads that artifact, errors out with:

```
resolve node IP for 100.68.228.107: get node 100.68.228.107: nodes "100.68.228.107" not found
```

`100.68.228.107` is not a node. We have one node and it is called `theborg`.
Nothing in the pipeline config, the resource config, or any var mentions that
address.

Full build output for a failing build and for the passing build immediately
before it is in [`evidence/build-output.md`](evidence/build-output.md). What we
checked on the cluster, and the pattern we have been able to establish over the
last three days, is in
[`evidence/cluster-observations.md`](evidence/cluster-observations.md).

The short version of the pattern:

- A build only ever fails if its `get` step printed `INFO: found existing
  resource cache`. Builds that actually run the get — cache misses — are always
  fine, and they leave the cache populated.
- So the *first* build of a new resource version passes, and every build of that
  same version after it fails. Bump the version, and you get one more green
  build followed by the same failure.
- Wiping the daemon's artifact directory makes the next build green again, and
  the one after that fails. Restarting `web` changes nothing.
- It reproduces on every pipeline we have tried, with `git` and with
  `registry-image`. It is not resource-type specific.

## Expected behavior

A `get` step that resolves from the DaemonSet artifact cache must produce an
artifact that downstream steps can actually read. A cache hit is supposed to be
a pure speed-up: the build should behave exactly as it does on a cache miss.

## What is being asked

1. **Name the root cause.** Not "where the error is printed" — what value is
   wrong, who put it there, and why the K8s API is being asked for a Node with
   that name at all. The explanation has to account for the whole observed
   pattern above, including why cache-miss builds are unaffected and why wiping
   the daemon's artifact directory buys exactly one good build.
2. **Say when it became wrong.** If this is a regression, identify the change
   that introduced the defect and, if it is a different change, the one that
   made it reachable. Be explicit if those are two different commits.
3. **Fix it**, with a regression test in `atc/worker/jetbridge` that fails
   before the fix and passes after. The test must exercise the path the operator
   actually hit — a cache hit followed by a downstream read — not just the unit
   that raises the error.
4. **Check for siblings.** If the same class of mistake can be made elsewhere in
   this subsystem, say so, with call sites and a verdict for each.

## Constraints

- Do not change the RBAC the web pod requests. `nodes/get` is granted today but
  ArgoCD has a history of reverting patches to that ClusterRole, so any fix that
  leans harder on the Nodes API is not deployable here.
- Do not regress the flows that work today: producer-recorded step outputs, the
  reaper's cleanup, and input scheduling affinity are all behaving correctly and
  must keep behaving correctly.
- Cache **miss** behavior must not change at all.
- Unit / package-level coverage is enough to prove this one. The behavioral and
  K3s integration suites take hours and are not needed here.
- No new dependencies.

## Deliverable

Two things, in the repository you are given:

- `RCA.md` at the repository root — a short write-up covering items 1, 2 and 4:
  the root cause and the mechanism, when it became wrong (and what made it
  reachable), the evidence that settles it, and the sibling audit with a verdict
  per call site. Say which claims you verified and which you inferred.
- The change itself, with the regression test from item 3.

If more than one explanation survives the evidence, rank them and name the
single observation that would separate them.
