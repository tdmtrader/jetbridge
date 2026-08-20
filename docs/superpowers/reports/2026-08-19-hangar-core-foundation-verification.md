# Hangar core foundation verification

Date: 2026-08-20

This report records the Task 8 implementation evidence produced locally. It
does not claim a local K3s run or final controller review.

## Behavioral coverage

`TestHangarDaemonStrictGCSFullTreeFlowFailsClosed` exercises the real artifact
daemon HTTP handler, Hangar service, materializer, official GCS client, and a
strict in-process HTTP fake of GCS. It:

- publishes a raw tree containing a binary-named regular file, executable
  source mode, empty directory, and contained symlink through the protected
  publish endpoint with a verified client-certificate state;
- removes the producing node-local directory, fetches the exact returned
  generation, and checks canonical bytes, types, modes, and normalized link;
- signs a real capability and materializes into a different
  `steps/<handle>/<volume>` path through the public materialization DTO;
- checks contents, types, sealed modes, exact receipt JSON, and no dependence
  on the deleted producer path; and
- injects an absent object, corrupted stored bytes, corrupted metadata, and a
  replacement generation. Every case returns non-2xx and leaves no partial or
  completed destination.

The strict fake requires a create-only GCS upload precondition and exact
generation reads. It implements both official-client upload forms encountered
by the suite and rejects unexpected requests.

`TestLiveHangarGeneratedPodMaterializesStrictTree` is guarded by the existing
`live` build tag and the narrower `hangar_live` tag, and additionally skips
Darwin and missing `KUBECONFIG`. The narrow tag keeps this internal-package
contract independently runnable while the older external live suite has an
unrelated PostgreSQL fixture migration gap. In a Linux/K3s environment it
generates a Pod through `Container.buildPod`, proves every init/main mount
resolves to a Pod Volume, checks both required readiness affinities and the
read-only strict mount, and runs the generated BusyBox init and task container
against a same-node BusyBox daemon fixture. The task checks tree contents,
types, modes, mutation failure, symlink target, and exact receipt. It was
intentionally not executed on macOS. The existing live harness does not
provide a reusable failure-injection fixture, so daemon-level tests own the
absent/corrupt/replacement failure matrix.

## RED and narrow fix

The new daemon flow initially timed out after 20 seconds in GCS publication.
`GCSStore.EnsureTree` explicitly stopped its cancellation hook and its deferred
cleanup invoked the same stopper again. `context.AfterFunc` stop functions are
single-use: the second call returned false and waited forever on a callback
completion channel even though the cancelled callback had never run.

Focused regression tests reproduced the double-stop hang before the source
change. `closeReadCloserOnCancel` now protects the stop-or-wait operation with
`sync.Once`, making the returned cleanup function idempotent and safe for
concurrent callers. The cancellation callback remains responsible for closing
the reader and can close it at most once.

## Commands run

All passing Go commands below used the task-specific cache
`/private/tmp/hangar-task8-gocache`.

```text
$ go test ./hangar -run '^TestCloseReadCloserOnCancel' -count=1 -v -timeout=20s
exit 0
ok github.com/concourse/concourse/hangar 0.320s

$ go test ./cmd/artifact-daemon -run '^TestHangarDaemonStrictGCSFullTreeFlowFailsClosed$' -count=1 -v -timeout=30s
exit 0
ok github.com/concourse/concourse/cmd/artifact-daemon 0.629s

$ /usr/bin/time -p env GOCACHE=/private/tmp/hangar-task8-gocache go test ./hangar -count=1
exit 0
ok github.com/concourse/concourse/hangar 1.136s
real 1.79
user 0.94
sys 1.91

$ /usr/bin/time -p env GOCACHE=/private/tmp/hangar-task8-gocache go test ./hangar ./cmd/artifact-daemon/durable ./cmd/artifact-daemon ./deploy/chart/tests -count=1
exit 0
ok github.com/concourse/concourse/hangar 2.713s
ok github.com/concourse/concourse/cmd/artifact-daemon/durable 3.644s
ok github.com/concourse/concourse/cmd/artifact-daemon 58.718s
ok github.com/concourse/concourse/deploy/chart/tests 41.385s
real 63.66
user 68.01
sys 16.83

$ /usr/bin/time -p env GOCACHE=/private/tmp/hangar-task8-gocache go test . -run '^TestAgenticLayerIsImportedOnlyAtItsWiringPoint$' -count=1
exit 0
ok github.com/concourse/concourse 1.004s
real 1.18
user 0.90
sys 1.62

$ /usr/bin/time -p env GOCACHE=/private/tmp/hangar-task8-gocache go test ./atc/worker/jetbridge -run '^(TestDaemonSetBackend_(BuildFetchInitContainers|HangarInit)|TestBuildFetchInitContainers|TestDaemonSetMode_(StrictInputValidationFailsClosed|StrictInputAllowsSiblingOutput|StrictInputsAreReadOnlyEverywhereAndPodMountsResolve|OrdinaryOverlappingInputRemainsWritable|BuildPodRejectsUnresolvedMounts))' -count=1
exit 0
ok github.com/concourse/concourse/atc/worker/jetbridge 5.525s
real 8.68
user 4.64
sys 3.09

$ /usr/bin/time -p env GOCACHE=/private/tmp/hangar-task8-gocache make test-unit
exit 0
Ginkgo ran 84 suites in 11m13.010266625s
Test Suite Passed
real 673.89
user 571.16
sys 1653.32

$ env GOCACHE=/private/tmp/hangar-task8-gocache go test ./hangar -run '^TestCloseReadCloserOnCancel' -count=1 -timeout=20s
exit 0
ok github.com/concourse/concourse/hangar 0.311s

$ env GOCACHE=/private/tmp/hangar-task8-gocache go test ./cmd/artifact-daemon -run '^TestHangarDaemonStrictGCSFullTreeFlowFailsClosed$' -count=1 -timeout=30s
exit 0
ok github.com/concourse/concourse/cmd/artifact-daemon 0.582s

$ env GOCACHE=/private/tmp/hangar-root-gocache go test -tags hangar_live ./atc/worker/jetbridge -run '^TestLiveHangarGeneratedPodMaterializesStrictTree$' -count=1 -v
exit 0
--- SKIP: TestLiveHangarGeneratedPodMaterializesStrictTree (0.00s)
PASS
ok github.com/concourse/concourse/atc/worker/jetbridge 0.600s

$ git diff --check
exit 0
```

`make test-unit` used the repository's documented parallel Ginkgo path; no
plain `go test ./...` was used.

## Truthful live/CI gap

The local compile-only probe of the repository-wide live test package was:

```text
$ go test -tags live ./atc/worker/jetbridge -run '^$' -count=1
exit 1
atc/worker/jetbridge/live_test.go:25:39: undefined: postgresrunner.StandardTestRunner
```

The historical live port omitted two companion dependencies. This branch
restores the small, production-correct daemon-namespace TLS SAN override and
its unit test. Its larger real-DB port still references a
`StandardTestRunner` from a divergent PostgreSQL-runner architecture, so the
unrelated broad suite remains blocked. The Hangar contract itself compiles and
is independently selectable with `-tags hangar_live`, as proved above. No K3s
cluster was started locally, consistent with the repository's macOS constraint.

The first sandboxed `go test ./hangar` attempt also exited 1 because the
sandbox denied `httptest` loopback binding. The identical approved loopback
run passed as recorded above; this was an execution-environment restriction,
not a test failure.

## Remaining handoff

Whole-branch architecture/security/code-quality review and its scoped
re-review are intentionally left to the final controller. This report is the
Task 8 implementation and test-evidence handoff, not the final completion
decision.
