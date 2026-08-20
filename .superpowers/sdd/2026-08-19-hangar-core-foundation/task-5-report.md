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
