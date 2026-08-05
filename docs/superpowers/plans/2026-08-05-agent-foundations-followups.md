# Agent foundations — surviving follow-ups

**Date:** 2026-08-05

The 2026-07-28 agentic-foundations semantic-rebase track is complete: all 19
tasks are on main (migrations `1773106139`–`1773106147`, head since advanced to
`1773106159`, retired-authority residue gates clean). Its plan, design, session
context, task preflights and deferred-item catalog were removed with the track;
recover them from git history under `docs/superpowers/` if needed.

These are the items from that catalog that were still genuinely open when the
track was retired, re-verified against main on 2026-08-05. Three catalog entries
were dropped as already-done (broader private-mount hardening — three server-owned
consumers now share one validated lifecycle; unreachable-Hangar-object reclaim;
experiments on exact node versions), one as stale sequencing bookkeeping, and
three as too thin to be worth carrying.

## 1. Durable whole-tree output commit journal

`agent/outputbuilder/builder.go` `commitStage` renames the live `record.json`
and `content` aside, moves the staged content in, and installs `record.json`
last, with rollback on ordinary errors. There is no durability barrier: no file
or directory fsync, and no recovery marker that a restart reconciles. A crash
between renames can leave new content beside the previous `record.json`, or
orphan `-old-record`/`-old-content`/stage directories that nothing cleans up.

This matters because the builder's output roots are the typed output volumes
selected in `atc/worker/jetbridge/container.go` (`managedOutputBuilderMounts`),
which can be daemon hostPath sources — node-local storage that outlives the pod.

The pattern already exists in-repo: `ci-agent/cmd/dev-capability/main.go` fsyncs
files, stage, and parent. Do this before producer workloads depend on multi-file
output updates surviving node loss.

## 2. Reusable-node catalog and selected-upgrade UI

Absent from main. Partly built on the unmerged `codex/agentic-platform-rebase`
branch: `c83d48048e` (design), `96ee05cf28` (catalog-overview API and
`atc/db/agent_nodes_factory.go` rollups), `35de69d568` (`web/elm/src/AgentNodes/`
and `AgentNode/`, routes, endpoints, page tests). Decide whether to land that
branch's node-catalog commits rather than restart the work.

## 3. Correct the GC cadence documentation

`README.md:112`, `JETBRIDGE.md:85`, `:308`, `:542` describe a reaper running
every 30 seconds, configurable via `--gc-interval`. That flag does not exist —
`atc/atccmd/command.go` defines only `--agent-snapshot-gc-interval` and
`--agent-checkpoint-gc-interval` (both `5m`, both consumed). The Kubernetes
reaper sets no explicit component interval and therefore uses
`defaultComponentInterval = 10 * time.Second` (`atc/atccmd/command.go:1028`;
see the deliberate omission noted at `:1732`).

Fix the cadence, drop the nonexistent flag, and recheck the remaining GC flag
table against `atc/atccmd/command.go`.

## 4. Remove retired storage terminology from JetBridge internals

`atc/worker/jetbridge/reaper.go` still describes cache cleanup as removing PVC
subdirectories, though the implementation delegates cleanup to the artifact
daemon over HTTP. `atc/worker/jetbridge/artifact_integration_test.go` still
labels an expectation as artifact-helper sidecar tar calls, while the current
path uses fetch init containers and the mandatory daemon.

Recasting that expectation needs a real observable-behavior assertion against
the daemon, not a rename — check the existing assertion for vacuousness first.

## 5. Two live cluster proofs were never executed

Both have unit and integration coverage; neither was ever proven end-to-end on a
real cluster, because no approved image digest and preconfigured recovery
environment were available on Borg:

1. delete an artifact-daemon cache entry and restore it from its exact
   generation-pinned Hangar object;
2. interrupt an agent pod/node and observe the documented capability-gated
   recovery outcome.

The original constraint still applies: use an already-approved image digest and
a preconfigured environment. Do not build or push a source-derived image just to
enable the proof.
