# Answer — rca-jb-002

**WITHHELD. Never expose any part of this directory to the agent under test.**

## Root cause, in one sentence

CI was never running the code under investigation: the `k8s-e2e` pipeline reuses
the **mutable image tag** `concourse-kind-runner:v33` as the `rootfs_uri` for the
integration and behavioral tasks, and the Concourse worker serves that tag from
its local image cache — silently ignoring `build-kind-runner`'s fresh push to the
same tag. Both jobs therefore compiled Concourse from a **stale `/src`**, so the
April FK guards in `atc/exec/check_step.go` were never exercised in CI.

## Why this is the answer and not the alternatives

The FK-guard code and `db.IsForeignKeyViolation` were correct the whole time.
That was already established at the cut by three independent lines of evidence in
the diagnostic record (the `cmd/fkrepro` reproduction, the real-DB
`atc/db/errors_test.go` spec, and the static audit of every FK surface reachable
from a check). Nothing in the instrumented run contradicts them. **This case
doubles as a negative for the application code**: the correct action there is no
change.

The decisive evidence is an **absence**, not a stack trace:

> the freshly-pushed instrumentation — both the `Using Concourse image …`
> provenance line (`ded0ca4ae7`) and the on-failure `concourse-web` log dump
> (`e9de3901fe`) — was **completely absent** from the run, even though
> `build-kind-runner` #177 reported success after the push.

Instrumentation that is on the pushed branch, and whose job reported a successful
rebuild, cannot be missing from the output unless the binary that ran was built
from different source. That forces the conclusion that the task rootfs, not the
application image, is stale.

The refuted hypothesis in the record is a genuine trap and must not be scored as
the answer. Phase 2a checked the **application** image (`concourse-local:latest`,
built inside the behavioral task from a freshly compiled binary, content-hashed
COPY layers, deployed `IfNotPresent` into a fresh testcontainers K3s) and
correctly refuted staleness *there*. The image nobody checked is the **task
rootfs** — the kind-runner image that supplies `/src` and the Go toolchain to the
task in the first place. The record's own "Ruled out" section contains the false
premise verbatim:

> `build-kind-runner` #176 pushed fresh to `registry.home` ("succeeded");
> behavioral task recompiles from that `/src`.

The push did succeed. The consumption is what failed.

## Mechanism, precisely

In `deploy/k8s-e2e-pipeline.yml` at pre_state:

1. `build-kind-runner` builds `--no-cache` and pushes to a **fixed tag**:
   `IMAGE_TAG="v33"` → `${REGISTRY}/concourse-kind-runner:v33`, then re-tags and
   pushes `registry.home/concourse-kind-runner:v33`.
2. `k8s-integration-tests` and `k8s-behavioral-tests` each declare
   `rootfs_uri: docker:///registry.home/concourse-kind-runner:v33` and start with
   `cd /src`, compiling `./cmd/concourse` from the image's baked-in source.
3. The worker resolves that rootfs **by tag** and serves a previously cached copy
   of `v33`. A new push to an already-cached tag is not noticed.

So the source that the tests build is whatever was in the image the last time the
tag was genuinely pulled — arbitrarily old, and silently so.

Corroborating detail: the second behavioral failure moved to a *different*
guarded surface (`PointToCheckedConfig` / `resources_resource_config_scope_id_fkey`
rather than `SaveVersions` / `resource_config_versions_...`). Both "guarded" paths
leaking is expected if neither guard is present in the running binary; it is hard
to explain if both guards are present and correct.

## The fix that was applied

`4cdf75c6ccf9d884ec3147696d33982dc89c827e` —
`ci(k8s-e2e): bump kind-runner image tag v33 -> v34 to bust stale worker cache`.
Three lines in `deploy/k8s-e2e-pipeline.yml`: `IMAGE_TAG="v33"` → `"v34"` and both
`rootfs_uri`s `…:v33` → `…:v34`, followed by a `set-pipeline`. Bumping to a
never-cached tag forces a genuine pull. This is the project's established
cache-bust pattern — the same file's history contains `v3`, `v4→v5`, `v6`,
`v29`, `v30`, `v32`, and a `v33` "force fresh pull" — all reachable at pre_state
via `git log -- deploy/k8s-e2e-pipeline.yml`.

## Verification named at the time

The next run must show the instrumentation that was missing: the
`Using Concourse image "…": sha256:… created=…` line, and — on any failure — the
`concourse-web` log dump. Their presence proves the run is finally testing
current code; only then does the FK question become answerable.

## Recorded outcome

- Root cause committed at `c2a0b65d534daae8e02363b8664f8727bdcce92e`
  ("ROOT CAUSE CONFIRMED (2026-05-30): CI ran stale kind-runner images").
- Track closed at `5de49fc2ea` — *"Root cause was CI image staleness, not the FK
  code — the worker served a cached kind-runner image by mutable tag, so the
  April FK guards never ran in CI. Fixed by the v33->v35 tag bump; behavioral
  #102 and #103 both green (298/0)."* (A second bump to `v35`, `19b2dda76b`, was
  needed to ship a later integration-test fix; it does not change the root cause.)
- Follow-on track `ci_reliability_k8s_e2e_20260530` closed at `c8407f87ab`
  ("source decoupled from toolchain image") — the durable structural fix that the
  tag bump only papered over.

## Generalized anti-pattern (recorded in `cgx.md` by the fix)

> Reusing a mutable image tag (v33) for CI rootfs means workers serve cached
> content and silently ignore fresh pushes — invalidating CI results. Either use
> immutable tags/digests or always bump on change.

## What a *better than ground truth* answer looks like

The tag bump is a cache-bust, not a cure: the next change to the source needs
another bump, and forgetting one reintroduces exactly this failure silently. An
answer that applies the bump **and** names the structural remedy — pin by digest,
derive the tag from the source commit, or decouple the tested source from the
toolchain image (fetch it as a git resource rather than baking it into the
rootfs) — is strictly better than what was done at the time, and matches what the
project actually did next (`1e36ad10c0`, `c8407f87ab`). Credit it.
