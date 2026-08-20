# Task 5 report: artifact-daemon Hangar composition

## TDD evidence

Initial handler RED, before production symbols existed:

```text
$ go test ./cmd/artifact-daemon -run 'TestHangar' -count=1
cmd/artifact-daemon/hangar_test.go:74:71: undefined: HangarService
cmd/artifact-daemon/hangar_test.go:84:14: undefined: HangarService
cmd/artifact-daemon/hangar_test.go:90:9: server.SetHangarService undefined
FAIL github.com/concourse/concourse/cmd/artifact-daemon [build failed]
```

Startup-prerequisite RED:

```text
$ env GOCACHE=/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run 'TestHangarConfig' -count=1
cmd/artifact-daemon/hangar_test.go:392:14: undefined: validateHangarOptions
FAIL github.com/concourse/concourse/cmd/artifact-daemon [build failed]
```

Scratch-symlink hardening RED found during self-review:

```text
$ env GOCACHE=/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run 'TestHangarScratchRejectsSymlink' -count=1
--- FAIL: TestHangarScratchRejectsSymlinkWithoutChangingItsTarget
    hangar_test.go:514: accepted symlink scratch directory
FAIL
```

Each RED was followed by the smallest implementation change and a focused green run. Final focused green:

```text
$ env GOCACHE=/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run 'Test(Hangar|TLS|Durable)' -count=1
ok  github.com/concourse/concourse/cmd/artifact-daemon  0.586s
```

## Final verification

```text
$ gofmt -w cmd/artifact-daemon/hangar.go cmd/artifact-daemon/hangar_handlers.go cmd/artifact-daemon/hangar_test.go cmd/artifact-daemon/server.go cmd/artifact-daemon/main.go
$ git diff --check
# exit 0, no output

$ env GOCACHE=/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon ./cmd/artifact-daemon/durable -count=1
ok  github.com/concourse/concourse/cmd/artifact-daemon          57.332s
ok  github.com/concourse/concourse/cmd/artifact-daemon/durable   4.137s
```

The loopback-based daemon/TLS tests require permission to bind local test ports in this environment. An initial sandboxed run failed at `httptest` listener creation with `bind: operation not permitted`; the identical approved runs above passed.

## Implementation and self-review

- Added an optional, nil-safe `HangarService`; disabled daemons register no Hangar routes and return router 404s.
- Added mTLS-protected canonical raw-tar publication and exact generation GET. Publication calls the strict `hangar.Store` directly. GET consumes and closes the complete store reader into private scratch before setting success headers.
- Added bounded `{items:[{ref,handle,volume,grant:"Bearer <token>"}]}` materialization. The complete bounded batch is strictly decoded and every exact grant is verified before the first item mutates.
- Mapped malformed, unauthorized, absent, conflict, limit, corrupt, and infrastructure outcomes to distinct sanitized 400/401/404/409/413/422/503 responses. Handler paths do not log request fields or capabilities.
- Added strict startup validation for complete usable TLS, GCS durable configuration, bucket access, deployment prefix, positive limits, an absolute private scratch directory outside artifact storage, and an exact raw 32-byte key. The strict GCS store is separate from `DurableTier`.
- The Hangar readiness label is added only after TLS, store, scratch, and key validation. Graceful shutdown removes the Hangar label before the legacy cache label and closes the separate GCS client after HTTP drain.
- Reviewed the cache diff: legacy routes are unchanged; only three conditional routes and one optional server field/setter were added. Regression coverage confirms broken durable stores still collapse resource cache GET/HEAD and restore to misses while the strict route returns 503.
- Reviewed request limits, trailing/unknown/duplicate JSON, route segment validation, exact response attributes, interrupted and concurrent publication, reader-close verification, grant-form uniformity, destination confinement, and no-visible-target-on-store-failure.

## Files and commit

- Created `cmd/artifact-daemon/hangar.go`
- Created `cmd/artifact-daemon/hangar_handlers.go`
- Created `cmd/artifact-daemon/hangar_test.go`
- Modified `cmd/artifact-daemon/server.go`
- Modified `cmd/artifact-daemon/main.go`
- Created this report
- Commit subject: `feat(artifact-daemon): serve strict Hangar trees`

## Local gaps and concerns

- No live GCS credentials were available. Startup performs a real bounded bucket-attributes check before readiness; the strict GCS implementation itself is covered by the existing fake and conformance tests, while this task's local verification used store fakes at the HTTP boundary.
- No Kubernetes cluster was used for a process-level label lifecycle test. Node label patch behavior remains covered by the existing fake-client tests; the shutdown order was reviewed directly in the small `main.go` composition diff.

## Fix round 1: close Hangar service boundaries

Independent review of `ea7f0f2488..2a82eb9117` was not approved. The findings were reproduced before fixes:

```text
$ env GOCACHE=/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run 'TestHangarScratchPaths' -count=1
cmd/artifact-daemon/hangar_test.go:532:13: undefined: validateHangarScratchPaths
FAIL github.com/concourse/concourse/cmd/artifact-daemon [build failed]

$ env GOCACHE=/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run 'TestHangar(OpenHashes|OpenPreserves|StatusPrecedence)' -count=1
--- FAIL: TestHangarOpenHashesActualSpooledBytesBeforeSuccess
    same-length mutation = 200 ... want sanitized 422
--- FAIL: TestHangarOpenPreservesTypedReadAndCloseFailures
    infrastructure/context/close returned 422, want 503
--- FAIL: TestHangarStatusPrecedenceNeverDowngradesCompoundFailuresToNotFound
    joined failures returned 404, want their fail-closed typed status
FAIL

$ env GOCACHE=/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run 'TestHangarMaterializationRequires' -count=1
--- FAIL: TestHangarMaterializationRequiresExactCaseSensitiveJSONVocabulary
    mixed-case Ref was accepted by encoding/json and reached authorization
FAIL

$ env GOCACHE=/tmp/hangar-task5-gocache go test ./hangar -run 'Test(NormalizeStorageEndpoint|NewStorageClientRootEndpoint)' -count=1
hangar/gcs_test.go:215:15: undefined: normalizeStorageEndpoint
FAIL github.com/concourse/concourse/hangar [build failed]
```

Fixes and self-review:

- Replaced separator-prefix containment with a pure `filepath.Rel` containment gate. Filesystem roots are rejected before `MkdirAll` or `Chmod`; tests verify `/` remains unchanged and symlink targets are not mutated.
- GET now hashes the complete final spool and compares it with the requested digest. Same-length mutations return sanitized 422 without a partial 200. Typed infrastructure/context read or close errors retain 503; local untyped I/O errors also fail closed as 503.
- Reordered status classification so infrastructure/context, unauthorized, corrupt, limit, and conflict cannot be downgraded by a joined `ErrNotFound`.
- Replaced the generic duplicate scan with an exact recursive token schema for `items`, item fields, and nested refs. Mixed-case aliases and semantic duplicates are rejected before `encoding/json`'s case-insensitive binding; unknown, trailing, and oversized control bodies remain bounded failures.
- Added actual mid-body interruption, invalid/absolute materialization segments, exact raw response vocabulary, and token/field-free response and Lager sink assertions.
- Startup now rejects readiness-label collisions, clears stale Hangar readiness even while disabled, binds the HTTP listener before adding readiness, and funnels bind failure, validation failure, graceful shutdown, and server failure through ordered label/server/client cleanup. Fake Kubernetes and listener tests cover disabled stale cleanup, bind failure, post-bind advertisement, and client closure.
- Normalized only Hangar's nonempty custom GCS endpoint: server root and `/storage/v1` forms become `/storage/v1/`. Durable GCS remains byte-for-byte unchanged. Nonempty endpoints retain the repository's unauthenticated emulator/proxy convention. Wire tests and a shared-root composition test cover strict startup bucket attributes and both clients reaching the same fake.

Final fix-round verification:

```text
$ gofmt -w cmd/artifact-daemon/main.go cmd/artifact-daemon/hangar.go cmd/artifact-daemon/hangar_handlers.go cmd/artifact-daemon/hangar_test.go hangar/gcs.go hangar/gcs_test.go
$ git diff --check
# exit 0, no output

$ env GOCACHE=/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run 'Test(Hangar|TLS|Durable)' -count=1
ok  github.com/concourse/concourse/cmd/artifact-daemon  0.661s

$ env GOCACHE=/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon ./cmd/artifact-daemon/durable ./hangar -count=1
ok  github.com/concourse/concourse/cmd/artifact-daemon          57.403s
ok  github.com/concourse/concourse/cmd/artifact-daemon/durable   5.857s
ok  github.com/concourse/concourse/hangar                        1.074s
```

Fix commit subject: `fix(artifact-daemon): close Hangar service boundaries`.

## Fix round 2: preserve fail-closed daemon state

Scoped review of `2a82eb9117..c0c08969db` was not approved. New regression tests were added before the implementation changes. The first RED run showed that the required startup-composition seam did not yet exist (and also caught a mistaken test assumption that the durable GCS wrapper owned a public `Close` method):

```text
$ env GOCACHE=/private/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run 'TestHangar(OpenClassifiesReadAndCloseFailuresTogether|LabelCollisionClearsStaleReadinessBeforeRejection|LabelPreparationFailureRetriesCentralCleanup|CleanupRemovesLabelsThenClosesClient|AndDurableGCSShareRootEndpointAndValidateBucket)$' -count=1
cmd/artifact-daemon/hangar_test.go:911:87: too many arguments in call to prepareDaemonLabels
cmd/artifact-daemon/hangar_test.go:932:101: too many arguments in call to prepareDaemonLabels
cmd/artifact-daemon/hangar_test.go:945:101: too many arguments in call to prepareDaemonLabels
cmd/artifact-daemon/hangar_test.go:816:21: durableStore.Close undefined
FAIL github.com/concourse/concourse/cmd/artifact-daemon [build failed]
```

After adding the composition seam, the strengthened shared-endpoint test also failed rather than accepting the old fail-open miss:

```text
--- FAIL: TestHangarAndDurableGCSShareRootEndpointAndValidateBucket
    durable stat found=false err=<nil> paths=[GET /storage/v1/b/bucket GET /b/bucket/o/resource-caches%2Fshared]
FAIL
```

Fixes and self-review:

- GET always closes the opened tree and normalizes read and close errors independently, joins them, then applies the established fail-closed status precedence once. Infrastructure/context or untyped local I/O now outranks absence, corruption, and conflict in compound failures, and no success headers or bytes are emitted.
- Node-label startup now creates the Kubernetes client and Hangar label identity, clears stale Hangar readiness, and only then validates a colliding legacy key. A preparation error performs centralized best-effort removal of both daemon-owned labels before returning. With no node identity configured, legacy no-label startup remains unchanged.
- Cleanup coverage observes the actual patch sequence and asserts Hangar readiness removal, legacy readiness removal, HTTP shutdown, then strict-client close.
- The shared custom endpoint test now asserts the two exact wire paths: Hangar JSON bucket validation at `/storage/v1/b/bucket` and a successful durable XML body read at `/bucket/resource-caches%2Fshared`. It reads and closes the returned durable object and verifies its bytes, rather than treating a fail-open miss as evidence.
- Reviewed the scoped diff for cache semantics: no durable implementation or legacy handler was changed.

Final fix-round verification:

```text
$ gofmt -w cmd/artifact-daemon/hangar.go cmd/artifact-daemon/hangar_handlers.go cmd/artifact-daemon/main.go cmd/artifact-daemon/hangar_test.go

$ env GOCACHE=/private/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run Hangar -count=1
ok  github.com/concourse/concourse/cmd/artifact-daemon  0.634s

$ env GOCACHE=/private/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon/... ./hangar/... -count=1
ok  github.com/concourse/concourse/cmd/artifact-daemon          57.745s
ok  github.com/concourse/concourse/cmd/artifact-daemon/durable   4.262s
ok  github.com/concourse/concourse/hangar                        2.010s

$ git diff --check
# exit 0, no output
```

Files changed in this round: `cmd/artifact-daemon/hangar.go`, `cmd/artifact-daemon/hangar_handlers.go`, `cmd/artifact-daemon/main.go`, `cmd/artifact-daemon/hangar_test.go`, and this report.

Fix commit subject: `fix(artifact-daemon): preserve fail-closed daemon state`.

Local gap: the lifecycle tests use the Kubernetes fake client and listener/server callbacks; no live Kubernetes node was mutated. The full local suites otherwise pass, with loopback permission required for `httptest` listeners.

## Fix round 3: retain cleanup authority

The remaining scoped review findings were reproduced with behavioral tests before production changes:

```text
$ env GOCACHE=/private/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run 'TestHangar(OpenClassifiesReadAndCloseFailuresTogether|LabelPreparationFailureUsesFreshBoundedCleanupContext)$' -count=1
--- FAIL: TestHangarOpenClassifiesReadAndCloseFailuresTogether
    --- FAIL: .../joined-not-found-and-untyped-read
        status=404 body="not found\n", want sanitized 503 "service unavailable\n"
    --- FAIL: .../joined-conflict-and-untyped-close
        status=409 body="conflict\n", want sanitized 503 "service unavailable\n"
--- FAIL: TestHangarLabelPreparationFailureUsesFreshBoundedCleanupContext
    cleanup context live=false new=false bounded=false patches=3
FAIL
```

Fixes and self-review:

- `normalizeHangarIOError` now recursively walks each `Unwrap() []error` child and converts every untyped I/O leaf to infrastructure before rejoining. A joined low-precedence typed error can no longer hide an untyped backend failure. Ordinary single `%w` typed wrappers and context errors remain intact; coverage confirms a lone typed corruption still returns sanitized 422.
- Preparation failure cleanup now derives a fresh background context bounded to ten seconds and always cancels it. A context-aware Kubernetes client wrapper forces the first patch to expire its preparation context, then verifies both cleanup patches receive the distinct, live, bounded cleanup context and remove stale readiness.
- The pre-existing corruption-reader fixture was changed from a multi-error to an ordinary `%w` typed wrapper so it continues to exercise the intentionally preserved typed-wrapper behavior rather than representing a new untyped sibling failure.
- No legacy cache handler, durable-store implementation, TLS behavior, or route composition changed in this round.

Final fix-round verification:

```text
$ gofmt -w cmd/artifact-daemon/hangar.go cmd/artifact-daemon/hangar_handlers.go cmd/artifact-daemon/hangar_test.go

$ env GOCACHE=/private/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon -run Hangar -count=1
ok  github.com/concourse/concourse/cmd/artifact-daemon  0.601s

$ env GOCACHE=/private/tmp/hangar-task5-gocache go test ./cmd/artifact-daemon/... ./hangar/... -count=1
ok  github.com/concourse/concourse/cmd/artifact-daemon          57.669s
ok  github.com/concourse/concourse/cmd/artifact-daemon/durable   3.929s
ok  github.com/concourse/concourse/hangar                        1.406s

$ git diff --check
# exit 0, no output
```

Files changed in this round: `cmd/artifact-daemon/hangar.go`, `cmd/artifact-daemon/hangar_handlers.go`, `cmd/artifact-daemon/hangar_test.go`, and this report.

Fix commit subject: `fix(artifact-daemon): retain cleanup authority`.

Local gap remains unchanged: lifecycle behavior is covered with a context-aware Kubernetes fake rather than a live node.
