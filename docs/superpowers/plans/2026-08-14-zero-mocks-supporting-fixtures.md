# Mock-Free Supporting Fixtures Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove handwritten interaction mocks outside the generated-fake migrations while retaining deterministic state, protocol, and lifecycle fixtures.

**Architecture:** Forwarding decorators and configurable error collaborators are replaced with production PostgreSQL components, real HTTP/AWS/OPA protocols, persistent event/state inspection, and small immutable value/function models. Generic counts and captured arguments disappear; lifecycle synchronization remains only as domain-named channels where timing itself is the contract.

**Tech Stack:** Go 1.25, Ginkgo v2/Gomega, PostgreSQL notifications and fixtures, `httptest`, AWS SDK protocol handlers, real Concourse factories, and bounded wall-clock cache-expiry contracts.

**Spec:** `docs/superpowers/specs/2026-08-14-zero-mocks-design.md`

## Global Constraints

- Execute every multi-command shell block with fail-fast semantics; stop on the first non-zero status even when a snippet does not repeat `set -e`.
- A helper is unacceptable when tests configure method returns or query generic call history. Rename-only changes do not satisfy this plan.
- Retain OPA/AWS/S3/GCS/artifact HTTP servers, client-go object/watch behavior, fake clocks, `runtimetest`, and domain lifecycle channels.
- Preserve likely missing secret/artifact/worker/resource and authorization errors. Drop arbitrary database-query, listener, executor, and collaborator failures used only for line coverage.
- Replace callback-order assertions with response, database, emitted-event, protocol-request, trace, or lifecycle outcomes.
- Run every database-backed package with Ginkgo and do not overlap suites.
- Do not modify the two untracked review documents.

---

### Task 1: Remove Remaining Engine and Exec Recorders

**Files:**
- Modify: `atc/engine/check_delegate_test.go`
- Modify: `atc/engine/build_step_delegate_test.go`
- Modify: `atc/engine/engine_suite_test.go`
- Modify: `atc/engine/engine_test.go`
- Modify: `atc/exec/check_step_test.go`
- Modify: `atc/exec/get_step_test.go`
- Modify: `atc/exec/put_step_test.go`
- Modify: `atc/exec/task_step_test.go`
- Modify: `atc/exec/exec_suite_test.go`
- Modify: `atc/exec/load_var_step_test.go`
- Modify: `atc/exec/set_pipeline_step_test.go`
- Modify: `atc/exec/task_config_source_test.go`

**Interfaces:**
- Consumes: real engine/exec delegates, PostgreSQL build events/scopes/caches, OPA HTTP `Requests`, real lock factories, runtime containers, and `worker.Streamer`.
- Produces: no `checkTimingScope`, `checkStartRecordingBuild`, `countingRateLimiter`, `eventRecordingBuild`, `recordingChecker`, `abortListenerRecordingBuild`, `countingStepperFactory`, recording delegate, `recordingLockFactory`, `imageFetchStepper.ranPlans`, or generic `recordingStreamer` API.

- [ ] **Step 1: Convert check timing and rate limiting to real state**

Use a real resource-config scope and its persisted last-check timestamps. Hold the production advisory check lock to prove a second check waits/skips, then release it and assert the scope/build progresses. Advance the existing controlled clock where elapsed time is the behavior. Delete exact `UpdateScopeLastCheckStartTime`, limiter, and callback counts plus injected scope/database failures.

- [ ] **Step 2: Read build events and policy requests from their production stores**

Replace `eventRecordingBuild` with `ConsumeEngineBuildEvent`/the build-event query helpers already present in the suite. Replace `recordingChecker.actions` with the existing OPA HTTP server's immutable `Requests()` records. Replace abort-listener and stepper counts with persisted aborted/build status and lifecycle channels on `engine.StepperFactoryFunc`, the concrete function adapter introduced by Task 1 of the engine/exec plan before this supporting plan runs.

- [ ] **Step 3: Remove exec delegate and lock forwarding assertions**

For check/get/put/task, assert resource scopes/versions, resource-cache uses, build events, runtime process/container state, and trace exporter spans. For lock behavior, hold a real lock on a second factory and assert the exported step outcome before and after release. Delete exact callback order/count assertions when they add nothing beyond those outcomes.

- [ ] **Step 4: Replace the generic streamer recorder**

Use `worker.NewStreamer` with the package's real compression and a `runtimetest` artifact/volume. Assert the loaded variable, parsed task config, or persisted pipeline plan. Where closing the stream is the public lifecycle contract, use a narrowly named `closeDetectingReader` with a `closed chan struct{}`; it must not record arguments or counts.

- [ ] **Step 5: Verify both suites and the interaction signature search**

Run:

```bash
set -e
gofmt -w atc/engine/check_delegate_test.go atc/engine/build_step_delegate_test.go atc/engine/engine_suite_test.go atc/engine/engine_test.go atc/exec/check_step_test.go atc/exec/get_step_test.go atc/exec/put_step_test.go atc/exec/task_step_test.go atc/exec/exec_suite_test.go atc/exec/load_var_step_test.go atc/exec/set_pipeline_step_test.go atc/exec/task_config_source_test.go
ginkgo ./atc/engine
ginkgo ./atc/exec
if git grep -n -E 'checkTimingScope|checkStartRecordingBuild|countingRateLimiter|eventRecordingBuild|recordingChecker|abortListenerRecordingBuild|countingStepperFactory|recordingLockFactory|ranPlans|recordingStreamer|CallCount|ArgsForCall|ReturnsOnCall|Invocations' -- 'atc/engine/*.go' 'atc/exec/*.go'; then false; else test $? -eq 1; fi
```

Expected: suites pass and the search has no matches.

- [ ] **Step 6: Commit the engine/exec semantic cleanup**

```bash
git add atc/engine atc/exec
git commit -m "test(engine): observe delegates through persisted behavior"
```

### Task 2: Remove API Database Recorders and the Event-Handler Fake

**Files:**
- Modify: `atc/api/api_suite_test.go`
- Modify: `atc/api/artifacts_test.go`
- Modify: `atc/api/builds_test.go`
- Modify: `atc/api/config_test.go`
- Modify: `atc/api/containers_test.go`
- Modify: `atc/api/jobs_test.go`
- Modify: `atc/api/pipelines_test.go`
- Modify: `atc/api/resources_test.go`
- Modify: `atc/api/teams_test.go`
- Modify: `atc/api/workers_test.go`

**Interfaces:**
- Consumes: real API database factories, `buildserver.NewEventHandler`, persisted build and event rows, `TeamCacheChannel`, worker runtime state, and HTTP responses.
- Produces: endpoint suites with no forwarding decorators exposing `Calls`, snapshots, counters, or injected errors.

- [ ] **Step 1: Wire the real build event handler**

Replace `constructedEventHandler.Construct` with `buildserver.NewEventHandler` in `newAPIServer`. In builds tests, persist distinct known events on a target build and a decoy build, issue the target build's SSE route with a cancelable context, decode one `event.Envelope` from the streaming response, and assert its event name/type, target payload, and per-build event ordinal while rejecting the decoy payload. The SSE envelope has no build-ID field—its `ID` is the event ordinal—so prove route selection through the distinct persisted payloads and the target build ID in the request URL, not by comparing the SSE ID with `build.ID()`. Then cancel the request context and close the response body. The production event handler waits for cancellation after writing and does not produce EOF by itself, so do not use `io.ReadAll` or wait for the stream to end. Delete assertions against `constructedEventHandler.build` and the sentinel string `fake event handler factory was here`.

- [ ] **Step 2: Translate API recorders to real observables**

Use this file-by-file map:

| Endpoint file | Replacement observable |
| --- | --- |
| `artifacts_test.go` | HTTP artifact bytes/status and real build/volume ownership |
| `builds_test.go` | returned build visibility, persisted status/events, creator and plan |
| `config_test.go` | reloaded pipeline config version and drained resource-scanner notification |
| `containers_test.go` | runtime container/hijack state and response stream/status |
| `jobs_test.go` | real pending/check builds, job pause state and creator |
| `pipelines_test.go` | reloaded pause/expose/config state and scoped list response |
| `resources_test.go` | decode the returned build ID, reload it through the real `BuildFactory`, and assert its checkable IDs, manual/started state, private `CheckPlan`, resource-config scope/version, and response |
| `teams_test.go` | persisted team auth plus `TeamCacheChannel` notification |
| `workers_test.go` | persisted worker fields, expiry/landing state and visible response |

For “must not notify,” drain the real buffered domain channel before the request and use `Consistently(channel).ShouldNot(Receive())`; do not wrap the factory in a counter.

Do not wait on the API fixture's local `checkBuildChan`: these handlers call `TryCreateCheck(..., toDB=true)`, which persists and returns a build without sending to the in-memory-check channel. For pinned-job checks, compare check-build rows for the resource before/after the request, reload the new build through `BuildFactory.Build`, and assert its private plan contains the pinned `FromVersion`, recursive interval choice, and expected checkable identity.

- [ ] **Step 3: Drop arbitrary database/factory error branches**

Delete cases reached only by forwarding decorators that return a configured query/save/find error. Keep likely missing row/artifact/team/worker behavior by leaving the real row absent and asserting the public status/body.

- [ ] **Step 4: Run the full API package and search for generic recorders**

Run:

```bash
set -e
gofmt -w atc/api/api_suite_test.go atc/api/artifacts_test.go atc/api/builds_test.go atc/api/config_test.go atc/api/containers_test.go atc/api/jobs_test.go atc/api/pipelines_test.go atc/api/resources_test.go atc/api/teams_test.go atc/api/workers_test.go
ginkgo ./atc/api
if git grep -n -E 'fakeEventHandlerFactory|constructedEventHandler|CallCount|ArgsForCall|ReturnsOnCall|Invocations|func .*Calls\(' -- 'atc/api/*.go'; then false; else test $? -eq 1; fi
```

Expected: PASS and no matching interaction API. Domain helpers such as protocol `Requests()` and lifecycle reset counts remain acceptable.

- [ ] **Step 5: Commit endpoint state assertions**

```bash
git add atc/api
git commit -m "test(api): replace database recorders with route outcomes"
```

### Task 3: Replace Cache, Variable, and Secret Counters with State Models

**Files:**
- Modify: `atc/api/accessor/claims_cacher_test.go`
- Modify: `atc/creds/cached_secrets_test.go`
- Modify: `atc/db/pipeline_test.go`
- Modify: `atc/creds/conjur/conjur_test.go`
- Modify: `vars/suite_test.go`
- Modify: `vars/multi_vars_test.go`
- Modify: `vars/named_vars_test.go`
- Modify: `vars/template_test.go`
- Modify: `vars/tracker_test.go`

**Interfaces:**
- Consumes: real access-token storage, `vars.StaticVariables`, a stateful in-memory secret backend, and secret maps.
- Produces: `errorVariables` and `conjurSecrets map[string][]byte`; no lookup counts, captured references, configurable method stubs, or invented cache clocks.

- [ ] **Step 1: Prove claims caching by changing the database underneath it**

`claimsCacher` has no TTL or clock. Prove its actual contracts through database state:

- persist token A, fetch it through the cacher, delete the database row, and fetch again to observe the cached value;
- call `claimsCacher.DeleteAccessToken("token-a")`, then assert the next fetch observes the absent database row;
- for an oversize token, fetch once, delete its row, and assert the next fetch is not found, proving it was not cached;
- for LRU, persist three approximately 400-byte tokens under the 1,000-byte limit, fetch 1/2/3, delete all three rows, then assert token 1 is absent while token 3 is still returned from cache;
- for race coverage, persist three tokens, fetch them concurrently, and collect/assert all three returned values and errors rather than counting database reads.

Delete `countingAccessTokenFetcher` and the disconnected-database case.

- [ ] **Step 2: Prove secret caching by changing values and presence**

Replace `countingSecrets` with a mutex-protected in-memory secret backend exposing domain state operations `Set`, `Delete`, `Fail`, and `Recover`; its `Get` returns only current value/error/lease state and records no history. Use short real durations (`Duration: 25ms`, `DurationNotFound: 15ms`, a purge interval longer than the spec) because `CachedSecrets` delegates time to `patrickmn/go-cache` and has no injectable clock.

Fetch a value, change the backend, and assert an immediate fetch still returns the cached old value; then use `Eventually` with a 250ms bound and 5ms polling until the new value is returned. For negative caching, add a previously absent secret, assert it is still absent immediately, then `Eventually` observe it after `DurationNotFound`. To prove errors are not cached, put a path into failed state, observe the likely backend error, recover it with a value, and immediately observe success. Preserve lease-shorter-than-cache behavior by storing a real near-future expiration and polling for the changed value after that deadline. In pipeline routing, put the successful value only at the source-qualified path; the result proves routing without a global lookup count. Replace fixed `time.Sleep` calls with these bounded outcome polls.

- [ ] **Step 3: Replace Conjur method stubbing with a path store**

Use:

```go
type conjurSecrets map[string][]byte

func (secrets conjurSecrets) RetrieveSecret(path string) ([]byte, error) {
	value, found := secrets[path]
	if !found {
		return nil, errors.New("secret not found")
	}
	return value, nil
}
```

Seed only the expected pipeline/team/full path. Returned values prove the lookup order and path; remove `MockConjurService`, `stubGetParameter`, and argument assertions.

- [ ] **Step 4: Replace `FakeVariables` with values and an immutable error model**

Add in `vars/suite_test.go`:

```go
type errorVariables struct{ err error }

func (v errorVariables) Get(Reference) (any, bool, error) {
	return nil, false, v.err
}

func (v errorVariables) List() ([]Reference, error) {
	return nil, v.err
}
```

Use `StaticVariables` for all successful and not-found lookups, including a nested map plus a `Reference` with fields to prove the full reference is routed. Use `errorVariables` only for sentinel failures. In `MultiVariables`, a successful middle `StaticVariables` source followed by `errorVariables` proves short-circuiting because the result would otherwise be an error. In `NamedVariables`, non-selected sources should be `errorVariables`; successful output proves they were not consulted. In the tracker case, `StaticVariables` is intentionally a value source that does not implement `SecretRefResolver`. Do not add function fields or recording state.

- [ ] **Step 5: Verify packages and commit**

Run serially:

```bash
set -e
gofmt -w atc/api/accessor/claims_cacher_test.go atc/creds/cached_secrets_test.go atc/db/pipeline_test.go atc/creds/conjur/conjur_test.go vars/suite_test.go vars/multi_vars_test.go vars/named_vars_test.go vars/template_test.go vars/tracker_test.go
ginkgo ./atc/api/accessor
ginkgo ./atc/creds
ginkgo ./atc/creds/conjur
ginkgo -ginkgo.focus='Pipeline' ./atc/db
go test ./vars -count=1
if git grep -n -E 'countingAccessTokenFetcher|countingSecrets|MockConjurService|stubGetParameter|FakeVariables|GetCallCount|GetVarDef' -- '*.go'; then false; else test $? -eq 1; fi
```

Expected: tests pass and the search has no matches.

```bash
git add atc/api/accessor/claims_cacher_test.go atc/creds/cached_secrets_test.go atc/db/pipeline_test.go atc/creds/conjur/conjur_test.go vars
git commit -m "test(creds): prove caches and routing through state"
```

### Task 4: Exercise Lidar, Components, and Notifications Through PostgreSQL

**Files:**
- Modify: `atc/lidar/lidar_suite_test.go`
- Modify: `atc/lidar/scanner_test.go`
- Modify: `atc/component/runner_test.go`
- Modify: `atc/db/notifications_bus_test.go`

**Interfaces:**
- Consumes: real `db.CheckFactory`, `fixture.CheckBuilds`, real PostgreSQL notification bus/listener, and component runner lifecycle.
- Produces: no `lidarCheckFactory.Calls`, `recordingBus`, `stubExecutor`, or `stubListener`.

- [ ] **Step 1: Replace lidar check recording with real check builds**

Wire `db.NewCheckFactory` from the existing lidar fixture. After scanning, drain `fixture.CheckBuilds`, inspect the check build's resource/config IDs and private plan, and query persisted scopes/versions. For “not scanned,” assert the channel remains empty and state unchanged. Drop query/panic fault switches and injected failures.

- [ ] **Step 2: Exercise component notifications with the real bus**

Use the suite PostgreSQL connection and production notifications bus. Publish the component notification, assert the runnable's domain start/finish channel, then cancel and assert the listener is released by observable process exit. Remove exact `Listen`/`Unlisten` calls.

- [ ] **Step 3: Exercise notification delivery without executor/listener stubs**

Create two real bus/listener instances on independent DB connections. Retain delivery, channel isolation, coalescing, cancellation, and relistening behavior through actual notifications. Delete injected listener/executor error branches.

- [ ] **Step 4: Run and commit**

Run serially:

```bash
set -e
gofmt -w atc/lidar/lidar_suite_test.go atc/lidar/scanner_test.go atc/component/runner_test.go atc/db/notifications_bus_test.go
ginkgo ./atc/lidar
ginkgo ./atc/component
ginkgo -ginkgo.focus='NotificationBus' ./atc/db
if git grep -n -E 'lidarCheckFactory|recordingBus|stubExecutor|stubListener|func .*Calls\(' -- 'atc/lidar/*.go' 'atc/component/*.go' 'atc/db/notifications_bus_test.go'; then false; else test $? -eq 1; fi
```

Expected: tests pass and the search has no matches.

```bash
git add atc/lidar atc/component/runner_test.go atc/db/notifications_bus_test.go
git commit -m "test(component): use real notification and check state"
```

### Task 5: Replace Remaining Service and Command Mocks with Real Boundaries

**Files:**
- Create: `atc/api/health_test.go`
- Delete: `atc/api/infoserver/health_test.go`
- Modify: `atc/api/info_test.go`
- Modify: `cmd/logging_runner_test.go`
- Modify: `cmd/artifact-daemon/server_test.go`

**Interfaces:**
- Consumes: the full API HTTP fixture's real DB pinger and worker factory, AWS JSON HTTP protocol, `ifrit.Runner`, and artifact-daemon peer HTTP protocol.
- Produces: `runnerFunc` lifecycle adapter and protocol/state tests; no `fakePinger`, `fakeWorkerFactory`, `stubSecretsManagerAPI`, `fake_runner_v2`, or `recordingMirror`.

- [ ] **Step 1: Drive health through the full API route and real database state**

Move the health contract under `Describe("Health", ...)` in `atc/api/health_test.go` and issue `GET /api/v1/health` through the suite's production router/wrappers with `anonymousProfile`. The API access plan has already wired independent `apiDB.HealthConn` as `dbPinger`, kept authorization/team lookups on `apiDB.Conn`, and built the real worker factory over `apiDB.WorkerConn`.

Keep these externally visible scenarios:

```text
live DB + one persisted worker -> 200, healthy=true, db="ok", workers="ok"
live DB + no workers -> 503, healthy=false, db="ok", workers="none"
`apiDB.disconnectHealth()` -> 503, db="unhealthy"
`apiDB.disconnectWorker()` -> 503, db="ok", workers="error"
both once-guarded disconnect helpers called -> 503, db="unhealthy", workers="error"
```

Persist the healthy worker with `apiDB.Deps.workerFactory.SaveWorker`. Decode `infoserver.HealthStatus` from the HTTP response and assert the status plus fields above. Never call `WorkerConn.Close` or `HealthConn.Close` directly: `openRealDB` cleanup calls the same once-guarded helpers, and the underlying notifications listener cannot be closed twice. Delete `atc/api/infoserver/health_test.go`, `fakePinger`, and `fakeWorkerFactory`; do not add a second PostgreSQL suite to the leaf package.

- [ ] **Step 2: Replace the Secrets Manager method stub with AWS JSON HTTP**

Use `httptest.NewServer` and an AWS JSON 1.1 handler matching the existing SSM/Secrets Manager package tests. Point a real AWS SDK client at it; answer `ResourceNotFoundException` for likely missing-secret behavior and one service response only if the info endpoint exposes a distinct status/body for it. Delete `stubSecretsManagerAPI`.

- [ ] **Step 3: Replace the ifrit fake with lifecycle channels**

In `cmd/logging_runner_test.go` add:

```go
type runnerFunc func(<-chan os.Signal, chan<- struct{}) error

func (f runnerFunc) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	return f(signals, ready)
}
```

The child function closes `ready`, waits for a real signal sent by the test, and returns a sentinel error. Assert the wrapped runner forwards readiness/signal through behavior, returns the sentinel, and writes `logging-runner-exited`. Remove `github.com/tedsuo/ifrit/fake_runner_v2`.

- [ ] **Step 4: Replace `recordingMirror` with a peer protocol server**

Seed `${storagePath}/steps/handle/output/data.txt`, then start an `httptest` peer that accepts `PUT /stream-in/handle/output`, untars the request body, records immutable method/path/header/content state, and returns `201 Created`. Split the peer listener address into host/port; create a client-go `EndpointSlice` in the resolver's namespace containing that host and label it `discoveryv1.LabelServiceName` with the exact service name passed to `daemon.NewPeerResolver`. Build the resolver with that service name and a different local pod IP.

Construct the real mirror with a complete `daemon.MirrorConfig`: `StoragePath`, the peer `Port`, `Scheme: "http"`, `Replicas: 2`, bounded `Concurrency`/`PerPeerTimeout`, `Peers`, a non-nil `http.Client`, and the logger. Wire `server.SetMirrorTrigger(mirror.Trigger)`, POST `{"key":"handle/output"}` to the public `/mirror` endpoint, assert `202 Accepted`, then call `mirror.Stop()` to drain the asynchronous job. Assert the peer received the exact `/stream-in/<key>` PUT, `MirrorOriginHeader`, tarred `data.txt` bytes, and answered 201. Mirroring reads the producer's existing `${StoragePath}/steps/<key>` tree and changes peer/status state; it does not mutate local artifact state.

For an empty key and invalid JSON, assert the public 400 status and no peer request.

Migrate both remaining stream-in consumers before deleting the helper: `TestStreamIn_SchedulesMirrorTrigger` must use the same real mirror/peer fixture, PUT the tar to `/stream-in/handle/output`, call `mirror.Stop()`, and assert the resulting peer PUT plus stored local bytes. `TestStreamIn_MirrorOriginWrite_DoesNotRetriggerMirror` must use that fixture with `MirrorOriginHeader`, stop the mirror, and assert no peer request while the inbound artifact is still stored. Only then delete `recordingMirror` and its `calls()` API; deleting it after the `/mirror` endpoint case alone leaves these two tests uncompilable.

- [ ] **Step 5: Verify and commit**

Run serially:

```bash
set -e
gofmt -w atc/api/health_test.go atc/api/info_test.go cmd/logging_runner_test.go cmd/artifact-daemon/server_test.go
go test ./atc/api/infoserver -run '^$'
ginkgo -ginkgo.focus='Health|Info API' ./atc/api
go test ./cmd -count=1
go test ./cmd/artifact-daemon -count=1
if git grep -n -E 'fakePinger|fakeWorkerFactory|stubSecretsManagerAPI|fake_runner_v2|recordingMirror|func .*calls\(' -- '*.go'; then false; else test $? -eq 1; fi
```

Expected: tests pass and the search has no matches.

```bash
git add atc/api/health_test.go atc/api/info_test.go cmd/logging_runner_test.go cmd/artifact-daemon/server_test.go
git add -u atc/api/infoserver/health_test.go
git commit -m "test: replace service mocks with real protocols"
```
