# Answer — rca-jb-004

**WITHHELD. Never expose any part of this directory to the agent under test.**

All line numbers below are at the pre_state ref
`3b00cbb7b8c0ec0e41b94d7f1215f4e66e9d47dd`.

## Root cause, in one sentence

`Worker.FindDaemonResourceCache` writes the **artifact-daemon pod IP** into the
`ArtifactLocator` under `ArtifactLocation.NodeName` — a field that
contractually holds a **Kubernetes Node object name** — and the very next thing
the build does reads that value back out as a node name and hands it to
`NodeIPResolver`, which asks the Kubernetes API for a Node called
`100.68.228.107` and gets a 404.

It is a type confusion across a seam: two different kinds of address are
carried in the same `string` field, and the write side only ever learns the
kind the read side cannot use.

## The mechanism, end to end

1. `atc/exec/get_step.go:461` — on a `get`, the step asks
   `worker.FindDaemonResourceCache(ctx, resourceCache.ID())`.
2. `atc/worker/jetbridge/worker.go:341` — `FindDaemonResourceCache` probes the
   live daemons (`FindResourceCache` → `DaemonClient.ProbeResourceCache`). A hit
   yields **`daemonIP`**, the pod IP of whichever daemon answered the
   `HEAD /resource-caches/rc-{id}` probe. A probe learns nothing else: it does
   not, and cannot, learn a Node name.
3. `atc/worker/jetbridge/worker.go:364-367` — the hit path then does:

   ```go
   // Record in locator so downstream steps get node affinity.
   if dsb, ok := w.storageBackend.(*DaemonSetBackend); ok && dsb.artifactLocator != nil {
       dsb.artifactLocator.Record(cacheKey, daemonIP, cacheKey)
   }
   ```

   `ArtifactLocator.Record(key, nodeName, hostDir)` — the second parameter is
   `NodeName`. A pod IP goes in where a Node name belongs. **This is the defect.**
4. `atc/exec/get_step.go:418` — whatever `attemptGet` returned is wrapped:
   `worker.ArtifactFromVolume(volume)`.
5. `atc/worker/jetbridge/worker.go:386-395` — `ArtifactFromVolume` calls
   `storageBackend.WrapVolumeForLookup(key, ...)` with `key = "rc-{id}"`
   (`ArtifactKey` is the identity function, `config.go:51`).
6. `atc/worker/jetbridge/storage_daemonset.go:490-495` — `WrapVolumeForLookup`
   does `sourceNode, _ = b.artifactLocator.LocateNode(key)` and gets the pod IP
   back. It builds a `DaemonSetVolume` whose `sourceNode` is `"100.68.228.107"`
   and whose `sourceIP` is empty.
7. `atc/worker/jetbridge/volume_daemonset.go:280-299` — the first downstream
   read calls `StreamOut` → `daemonURL`. `sourceIP` is empty, so it takes the
   resolver branch: `nodeIPResolver.Resolve(ctx, "100.68.228.107")`.
8. `atc/worker/jetbridge/node_ip_resolver.go:50` —
   `clientset.CoreV1().Nodes().Get(ctx, "100.68.228.107")` → 404, wrapped twice
   into the operator's message:

   ```
   resolve node IP for 100.68.228.107: get node 100.68.228.107: nodes "100.68.228.107" not found
   ```

The bitter part is step 7: the *correct* address was in hand at step 2 and
there is already a constructor for it — `NewDaemonSetVolumeFromIP`, which sets
`sourceIP` and skips the resolver entirely. `FindDaemonResourceCache` uses that
constructor for the volume it returns. The poisoned locator entry then routes
the *wrapped* artifact down the other branch, throwing the good address away.

## Why the observed pattern follows exactly

| Observation | Explanation |
|---|---|
| Only cache-**hit** builds fail | `FindDaemonResourceCache` is the only writer of the bad entry, and it only runs — and only writes — on a probe hit. A miss never touches the locator. |
| First build for a version passes, every later one fails | The first build is the miss that populates the on-disk cache; every rerun is a hit. |
| Wiping `/var/concourse/artifacts/steps/rc-*` buys exactly one green build | It forces one more miss. The next build hits again. |
| Restarting `web` changes nothing | The locator is in-memory and would be cleared, but the entry is (re)written by the same build that then reads it — the poison and the read are one step apart in one build. There is no stale state to clear. |
| Restarting the daemon changed the IP in the message immediately | Same reason: the value is re-derived from a live probe on every hit. It tracks the current daemon pod IP, which is the tell that it is a pod IP and not a stale node record. |
| The daemon serves the bytes fine over HTTP | Nothing is wrong with the daemon, the cache, the symlink, or RBAC. The failure is entirely in how the ATC addresses the daemon. |

## Provenance — two commits, and both should be named

- **`864eba169ff9e01a0ebbc4b92a34dbd1277be663`** (2026-04-01, "fix(cache): use
  hostPath symlinks instead of daemon API for cache registration") **introduced
  the defect.** That commit changed `StorageBackend.FindResourceCache` from
  returning a node name to returning a daemon pod IP, renamed the local variable
  `nodeName` → `daemonIP` at the call site, and then left it flowing into the
  unchanged `Record(cacheKey, …, cacheKey)` call — annotating the change with:

  ```go
  // Record in locator so downstream steps get node affinity. The daemon
  // IP is used as a placeholder — on single-node clusters this is fine,
  // and on multi-node the affinity is best-effort.
  ```

  That comment is the fault, written down: a deliberate decision to put the
  wrong kind of value in a typed slot because the only consumer at the time
  (soft scheduling affinity, `preferredInputNode`,
  `storage_daemonset.go:361-384`) degraded silently rather than failing.
- **`dc5c936532`** (2026-04-18, "fix(artifact): route step-produced artifact
  reads through DaemonSet") **made it reachable.** It added
  `Worker.ArtifactFromVolume` and put it on the `get` step's return path, which
  is the first consumer that reads a locator entry for an `rc-*` key and treats
  the result as authoritative. Before it, the poisoned entry only ever fed a
  `preferredDuringSchedulingIgnoredDuringExecution` affinity term — a soft
  preference for a nonexistent node, which K8s ignores.

A complete answer names the write site as the defect. Naming `864eba169f` is
the human answer of record (the fix commit says "Bug regressed in 864eba169f").
Additionally identifying `dc5c936532` as the activating change — and being
clear about the distinction — is **better** than the human answer and should be
scored above it, because the operator's "it started on the 18th" is only
explicable that way.

## The correct remedy

Two halves, and the first is not optional.

1. **Delete the `Record` call.** There is nothing to fix in place: a probe hit
   learns a pod IP and only a pod IP, so there is no correct value to write into
   `NodeName`. The entry cannot be made right; it can only be not written.
2. **Make the read side handle the resulting locator miss.** With the write
   gone, `WrapVolumeForLookup` finds no entry for `rc-{id}` and builds a
   `DaemonSetVolume` with neither `sourceNode` nor `sourceIP`, whose `StreamOut`
   fails with `DaemonSetVolume.StreamOut: no source node known (key=rc-42)`.
   The humans' fix (`b19a0fb2bc`) re-probes the live daemons inside
   `WrapVolumeForLookup` when the locator has no entry **and** the key is
   resource-cache-shaped, and on a hit returns `NewDaemonSetVolumeFromIP` —
   binding to the pod IP directly and bypassing `NodeIPResolver` altogether.
   This requires plumbing `ctx` through `StorageBackend.WrapVolumeForLookup`
   (a one-method interface change) and a small `isResourceCacheKey` predicate.

   This half is load-bearing and was verified as such: deleting the `Record`
   call alone, at pre_state, leaves the downstream-read regression test red
   (see `notes.md#validation`).

3. **Defence in depth (the humans also did this, `04eade3c88`):**
   `NodeIPResolver.Resolve` short-circuits when `net.ParseIP(nodeName) != nil`,
   returning a typed `ErrNodeNameIsIP` sentinel instead of issuing a Nodes GET
   that can only 404. This does not fix anything by itself — it converts a
   misleading "node not found" into a loud, greppable misuse error. Valuable,
   optional, and must not be accepted **in place of** (1)+(2).

## What is not the answer

- **Translating on the read side.** "If `sourceNode` parses as an IP, use it as
  `sourceIP`" makes the downstream-read test pass while leaving a pod IP stored
  in a field that means Node name. It ratifies the type confusion instead of
  removing it, and it keeps a per-pod address in a store that is never
  invalidated when the daemon pod is replaced (the operator watched the daemon
  come back on a new IP; a translated stale entry would then point at a dead
  pod). The withheld spec "probe hits must not write to the ArtifactLocator" is
  the discriminator, and it is red for this shape of fix. Treat this as a
  partial: correct symptom relief, wrong root-cause resolution.
- **Granting or widening `nodes/get` RBAC.** Explicitly ruled out by the task,
  and irrelevant — the 404 is real, not a permissions error.
- **Making `NodeIPResolver` fall back to treating the IP as reachable.** Same
  objection as translation, one layer deeper, and it also breaks the
  defence-in-depth property.
- **Removing `NodeIPResolver` / the `sourceNode` path.** It is correct and in
  use for producer-recorded artifacts, the reaper, and affinity — the task says
  so and it is true.
- **Blaming the daemon, the symlink registration, the cache TTL sweeper, the
  `nodes/get` ClusterRole, or ArgoCD.** The evidence rules all of these out.
- **Reverting `dc5c936532`.** It made the defect visible; it is not the defect.
  The artifact-read routing is correct and load-bearing for a separate,
  already-fixed production failure (`exec stream: pods "…" not found`).

## Sibling audit (asked for by task item 4)

The humans ran this audit after the fix and recorded it at
`efc827aec0eaf28aed34c81f2248dd28ed0b7465`. Every production caller of
`ArtifactLocator.Record(key, nodeName, hostDir)`:

| Site (pre_state) | Where `nodeName` comes from | Verdict |
|---|---|---|
| `storage_daemonset.go:425` (`RecordOutputs`) | `process.go` `fetchPodNodeName(ctx)` → `pod.Spec.NodeName` | ✓ real Node name |
| `storage_daemonset.go:535` (`RegisterResourceCache`) | `worker.go:320` `artifactLocator.LocateNode(...)`, only ever populated by `RecordOutputs` | ✓ real Node name (transitive) |
| `worker.go:366` (`FindDaemonResourceCache`) | `ProbeResourceCache` → daemon **pod** IP | ✗ the bug |

No other production sites. Test fixtures use synthetic node names (`node-1`,
`node-a`), except the pre-existing `worker_test.go` "stale entry for a dead
node" spec, which deliberately writes `locator.Record("rc-42", "10.0.0.99",
"rc-42")`. That one is intentional and harmless — it asserts the entry is
treated as a miss — but note its comment calls `10.0.0.99` a "dead node IP",
which is the same conflation in the test suite. Flagging that comment is a
bonus, not a requirement.
