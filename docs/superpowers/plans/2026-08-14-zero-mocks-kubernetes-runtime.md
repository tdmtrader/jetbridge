# Mock-Free Kubernetes Runtime Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the shared JetBridge exec recorder and client-go action/reactor assertions with a deterministic pod runtime, real HTTP protocol behavior, and final Kubernetes object state.

**Architecture:** A test-only in-memory pod runtime implements `jetbridge.PodExecutor` by modeling namespaces, pods, containers, files, installed programs, process exits, and tar streaming. Tests inspect that domain state and public stream/process results. Client-go remains as the approved in-memory Kubernetes API, but tests stop reading its action log or injecting arbitrary non-watch failures; log transport uses an HTTP test API and node mutations are verified from stored objects.

**Tech Stack:** Go 1.25, Ginkgo v2/Gomega, Kubernetes client-go object/watch API, `httptest`, `archive/tar`, JetBridge live/K3s CI gates.

**Spec:** `docs/superpowers/specs/2026-08-14-zero-mocks-design.md`

## Global Constraints

- Execute every multi-command shell block with fail-fast semantics; stop on the first non-zero status even when a snippet does not repeat `set -e`.
- Keep client-go's in-memory object and watch semantics. `PrependWatchReactor` remains allowed only where watch lifecycle is the behavior.
- Remove `.Actions()` assertions and non-watch `PrependReactor`/`AddReactor` failure injection.
- The pod runtime must have stable domain semantics; it must not expose `Calls`, `CallCount`, `ArgsForCall`, `Returns`, `Stub`, or an arbitrary per-invocation callback.
- Assert files, process state, streams, exit status, pod phase, node labels, logs, artifacts, and trace spans.
- Preserve likely terminated/missing pod/container/program errors through modeled state. Drop arbitrary API and “return this configured error” cases.
- Do not execute local K3s on macOS. Run live/K3s checks only in CI after the unit suite is green.
- Do not modify the two untracked review documents.

---

### Task 1: Build a Deterministic Pod Runtime Model

**Files:**
- Create: `atc/worker/jetbridge/pod_runtime_test.go`
- Verify interface: `atc/worker/jetbridge/volume.go:35-70`

**Interfaces:**
- Consumes: `jetbridge.PodExecutor.ExecInPod`, tar stream commands, resource JSON stdin/stdout, and process exit codes.
- Produces: `podRuntime`, `podKey`, `containerState`, `program`, and immutable query methods `File`, `Processes`, and `TerminalSessions`.

- [ ] **Step 1: Write a tar round-trip contract against the missing runtime**

Create source and destination pod/containers, seed `/tmp/source/data.txt`, call the model's missing `ExecInPod` with the exact production tar-create command into a buffer, then call it with the exact tar-extract command and that buffer as stdin. Assert:

```go
data, found := runtime.File(podKey{"test-namespace", "destination-pod", "main"}, "/tmp/destination/data.txt")
Expect(found).To(BeTrue())
Expect(data).To(Equal([]byte("artifact payload")))
```

Run: `ginkgo -ginkgo.focus='round trips files through pod exec tar streams' ./atc/worker/jetbridge`

Expected: compile failure because `newPodRuntime` and its types do not exist.

- [ ] **Step 2: Implement immutable pod/container state**

Use these shapes:

```go
type podKey struct {
	Namespace string
	Pod       string
	Container string
}

type program struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Effect   execEffect
}

type execEffect string

const (
	execCompletes    execEffect = "completes"
	execOOMKillsPod  execEffect = "oom-kills-pod"
	execDeletesPod   execEffect = "deletes-pod"
	execRecreatesPod execEffect = "recreates-pod"
)

type modeledProcess struct {
	Command    []string
	Stdin      []byte
	TTY        bool
	Supervised bool
}

type supervisorRun struct {
	Command  []string
	Log      []byte
	ExitCode int
}

type containerState struct {
	Files       map[string][]byte
	Programs    map[string]program
	Processes   []modeledProcess
	Supervisors map[string]supervisorRun
}

type podRuntime struct {
	mu         sync.Mutex
	clientset  kubernetes.Interface
	containers map[podKey]*containerState
}
```

Construct it with the same client-go object/watch client the public worker uses. `AddContainer` must ensure a corresponding Running Pod/container object exists; `Terminate` updates that Pod's status through the client-go API. Provide domain setup/query methods `AddContainer`, `PutFile`, `InstallProgram`, `Terminate`, `File`, `Processes`, and `TerminalSessions`; every query returns copies. `ExecInPod` derives missing/terminal state from the Kubernetes object rather than a second internal boolean and returns stable sentinel errors.

- [ ] **Step 3: Implement `ExecInPod` semantics**

Read stdin fully, then interpret the execution by domain shape:

- interpret a command shaped `[]string{"tar", "cf", "-", "-C", root, relativePath}` by writing a real tar archive from modeled files to stdout;
- interpret a command shaped `[]string{"tar", "xf", "-", "-C", root}` by extracting the stdin archive into modeled files;
- before generic lookup, recognize the production task-supervisor envelope only when `attrs.Purpose == "step-command"`, argv is `[]string{"sh", "-c", script}`, the script starts with `S='/tmp/concourse-task-`, and it contains the fixed `( trap '' HUP; ... >>"$S/log" 2>&1; ... ) &` launch markers. Extract the state directory and decode the embedded child argv using only the inverse of JetBridge's `shellQuote` grammar (single-quoted words, `'\''` for an apostrophe, and one separating space); reject a recognizable but malformed envelope with a stable error rather than implementing a general shell parser;
- for a new supervisor state directory, resolve the decoded child executable in `Programs`, append `modeledProcess{Command: childArgv, Supervised: true}`, apply the program once, combine its stdout/stderr into the supervisor log as production does, cache log/exit status by state directory, emit the log to exec stdout, and return `&jetbridge.ExecExitError{ExitCode: n}` when non-zero;
- for an already completed supervisor state directory, replay the cached log/status without appending another modeled child or reapplying its effect. A different state directory launches a fresh child, and Pod deletion/recreation clears this ephemeral supervisor state;
- for other commands, look up the first executable path in `Programs`, append `modeledProcess{Command: command, Supervised: false}`, copy its stdout/stderr, apply its semantic `Effect`, and return `&jetbridge.ExecExitError{ExitCode: n}` when non-zero;
- record TTY sessions in process state; do not special-case test names or invocation indexes.

Apply effects during `ExecInPod`, after the public process has already passed `waitForRunning`: `execOOMKillsPod` updates the real client-go Pod/container status to terminated with reason `OOMKilled`; `execDeletesPod` deletes the Pod; `execRecreatesPod` deletes and recreates the same pod name with a new UID and Running status, clearing terminal and supervisor state from the prior object. These preserve the current exec-time OOM, deletion, and recreation race contracts; pre-seeding a terminal Pod would exercise the wrong branch. Missing installed programs return a stable `program not found` error. Do not add an `OriginalCommand` metadata shortcut, supervisor decoder callback, raw-wrapper history, callbacks, invocation-index behavior, or a generic `err` field.

- [ ] **Step 4: Characterize the model's state and error semantics**

Under `Describe("Pod runtime model", ...)` in `pod_runtime_test.go`, cover tar round trips, installed-program stdout/stderr/exit status, stdin capture, terminal sessions, immutable query copies, and stable missing/terminated errors. Also prove that installing only `/bin/sh` executes a supervised `/bin/sh -c ...`; spaces, apostrophes, and shell metacharacters decode to the exact child argv; supervised stdout/stderr merge and exit propagation; two identical envelopes replay but leave one modeled child; a different state key starts a second child; and an ordinary raw `sh -c` remains `Supervised: false`. These are trust tests for a shared deterministic boundary model. Do not touch `volume_test.go` yet: its shared `fakeExecExecutor` definitions are still required by the other package files and remain compile-compatible until Task 2 migrates every consumer.

- [ ] **Step 5: Run and commit the model**

Run:

```bash
set -e
gofmt -w atc/worker/jetbridge/pod_runtime_test.go
ginkgo -ginkgo.focus='Pod runtime model' ./atc/worker/jetbridge
if git grep -n -E 'CallCount|ArgsForCall|ReturnsOnCall|Invocations|Calls|Stub' -- 'atc/worker/jetbridge/pod_runtime_test.go'; then false; else test $? -eq 1; fi
```

Expected: the model specs pass and the search has no matches. Legacy exec-recorder matches elsewhere in the package are expected until Task 2.

```bash
git add atc/worker/jetbridge/pod_runtime_test.go
git commit -m "test(jetbridge): model pod files and exec semantics"
```

### Task 2: Migrate JetBridge Exec Consumers to Runtime State

**Files:**
- Modify: `atc/worker/jetbridge/artifact_integration_test.go`
- Modify: `atc/worker/jetbridge/behavioral_runtime_spec_test.go`
- Modify: `atc/worker/jetbridge/container_test.go`
- Modify: `atc/worker/jetbridge/integration_test.go`
- Modify: `atc/worker/jetbridge/jetbridge_suite_test.go`
- Modify: `atc/worker/jetbridge/podname_integration_test.go`
- Modify: `atc/worker/jetbridge/process_test.go`
- Modify: `atc/worker/jetbridge/resource_test.go`
- Modify: `atc/worker/jetbridge/volume_test.go`
- Modify: `atc/worker/jetbridge/worker_test.go`
- Verify CI behavior: `atc/worker/jetbridge/live_test.go` and other `//go:build live` files.

**Interfaces:**
- Consumes: `podRuntime` from Task 1 and production JetBridge container/process/resource/worker APIs.
- Produces: no shared exec interaction recorder across the JetBridge suite.

- [ ] **Step 1: Translate process command assertions to modeled process state**

Install the intended child executable/program in the target container, invoke the public process/container API, then assert the resulting semantic `modeledProcess.Command`, stdin JSON, TTY flag, `Supervised` flag, and public exit/output. For the supervisor gate table, assert `process.Command == []string{"/bin/sh", "-c", "echo hi"}` and `process.Supervised == wantSupervisor`; never install or assert the outer `sh -c <supervisor-script>` envelope. In the web-reattach scenario, run both public waits against the surviving pod and assert replayed output/exit plus exactly one modeled child after both, rather than two equal executor-call records. Hijack/intercept similarly assert the semantic `/bin/bash -l` or `/bin/sh` child and TTY state. Convert the existing exec-time OOM, pod-deleted, and same-name-pod-recreated scenarios to `program.Effect` values and assert their public `Wait` result plus final client-go Pod identity/status; do not replace them with a Pod that was terminal before `ExecInPod`. Keep `supervisor_test.go`, `supervisor_script_test.go`, and the real busybox CI test required by `AGENTS.md` as the owners of private wrapper formatting and real shell behavior.

- [ ] **Step 2: Translate resource tests to real JSON protocol results**

Install `/opt/resource/in`, `/out`, or `/check` with deterministic `program{Stdout: expectedJSON}`. Run the real resource API and assert decoded version/metadata plus the modeled process stdin JSON. Use `ExitCode` for likely resource-script failure; delete arbitrary context/error fields.

- [ ] **Step 3: Translate artifact and integration tests to files and persisted state**

Seed source files, run the public get/put/task/artifact flow, and assert destination files, artifact keys/locations, build status, and process output. Clear modeled state only by constructing a new runtime/container; do not mutate a call slice between phases.

- [ ] **Step 4: Convert volume streaming to modeled files**

Replace `volume_test.go` command/argument/attribute assertions with actual tar round trips through the public `jetbridge.Volume`. Keep handle/source/DB-volume behavior and modeled terminated/missing-container errors; delete arbitrary `execErr` branches.

- [ ] **Step 5: Translate hijack and intercept tests to terminal sessions**

Run the public hijack/intercept operation, then assert one modeled terminal session for the expected pod/container with the requested shell and TTY mode. Use modeled missing/terminated pod state for user-likely failures.

- [ ] **Step 6: Delete the shared recorder only after all consumers compile**

Run `ginkgo ./atc/worker/jetbridge` once with all consumers migrated while the old type definitions still exist. After that passes, remove `fakeExecExecutor`, `execCall`, `execCalls`, `execStdout`, `execFunc`, and `execErr` from `volume_test.go`, and remove the now-unused `expectSupervisedExec` helper from `jetbridge_suite_test.go`, then run the suite again. This keeps both commits/tasks compile-safe; do not delete the shared definitions during Task 1.

- [ ] **Step 7: Verify the whole package has no exec recorder**

Run:

```bash
set -e
gofmt -w atc/worker/jetbridge/artifact_integration_test.go atc/worker/jetbridge/behavioral_runtime_spec_test.go atc/worker/jetbridge/container_test.go atc/worker/jetbridge/integration_test.go atc/worker/jetbridge/jetbridge_suite_test.go atc/worker/jetbridge/podname_integration_test.go atc/worker/jetbridge/process_test.go atc/worker/jetbridge/resource_test.go atc/worker/jetbridge/volume_test.go atc/worker/jetbridge/worker_test.go
ginkgo ./atc/worker/jetbridge
if git grep -n -E 'fakeExecExecutor|execCalls|execFunc|execStdout|execErr|expectSupervisedExec' -- 'atc/worker/jetbridge/*.go'; then false; else test $? -eq 1; fi
```

Expected: PASS and no matches outside historical design documents.

- [ ] **Step 8: Measure and commit**

Run: `/usr/bin/time -p ginkgo ./atc/worker/jetbridge`

Expected: no K3s startup and no unexplained material regression.

```bash
git add atc/worker/jetbridge
git commit -m "test(jetbridge): assert runtime state instead of exec calls"
```

### Task 3: Remove Client-Go Action Inspection

**Files:**
- Modify: `atc/worker/jetbridge/behavioral_runtime_spec_test.go`
- Modify: `atc/worker/jetbridge/node_ip_resolver_test.go`
- Modify: `cmd/artifact-daemon/node_labeler_test.go`

**Interfaces:**
- Consumes: client-go's deterministic object/log behavior, final in-memory Kubernetes objects, destination writers, and public resolver errors.
- Produces: no selector calls to `.Actions()`.

- [ ] **Step 1: Assert pod-log delivery instead of inspecting the action log**

Keep `fake.NewSimpleClientset` for the complete Pod create/get/update/delete/watch lifecycle. Its `GetLogs` implementation returns deterministic `"fake logs"` bytes. In the dedicated-writer scenario, assert those bytes arrive in the named sidecar writer; in the prefix-fallback scenario, assert they arrive with the sidecar prefix in the main stdout writer. Those public stream outcomes prove the log subresource was used, so delete `.Actions()` inspection. Do not replace the clientset with a server that implements only `/pods/.../log`: the selected public path also requires the object lifecycle endpoints.

- [ ] **Step 2: Drop the zero-action node IP assertion**

Keep the invalid/missing node IP setup and assert the public returned error/value. The result itself proves the resolver stopped; delete `cs.Actions()` inspection.

- [ ] **Step 3: Assert node labels from stored state**

Run the real labeler against the client-go object API, fetch the node afterward, and assert the requested final labels plus preservation of an unrelated seeded label. Delete patch action/type inspection. Do not assert that `resourceVersion` incremented: client-go's object tracker does not reliably advance it for patches.

- [ ] **Step 4: Run and commit**

Run:

```bash
set -e
gofmt -w atc/worker/jetbridge/behavioral_runtime_spec_test.go atc/worker/jetbridge/node_ip_resolver_test.go cmd/artifact-daemon/node_labeler_test.go
ginkgo ./atc/worker/jetbridge
go test ./cmd/artifact-daemon -count=1
if git grep -n -E '\.Actions\(\)' -- '*.go'; then false; else test $? -eq 1; fi
```

Expected: tests pass and the search has no matches.

```bash
git add atc/worker/jetbridge/behavioral_runtime_spec_test.go atc/worker/jetbridge/node_ip_resolver_test.go cmd/artifact-daemon/node_labeler_test.go
git commit -m "test(kubernetes): assert protocol and object state"
```

### Task 4: Remove Non-Watch Reactor Error Injection

**Files:**
- Modify: `atc/worker/jetbridge/container_test.go`
- Modify: `atc/worker/jetbridge/process_test.go`
- Modify: `atc/worker/jetbridge/reaper_test.go`
- Preserve: `atc/worker/jetbridge/watch_test.go`

**Interfaces:**
- Consumes: client-go object lifecycle and the allowed watch reactor.
- Produces: no non-watch `PrependReactor`/`AddReactor` calls.

- [ ] **Step 1: Remove arbitrary create/get/delete API failures**

Delete cases that install reactors solely to return a configured Kubernetes API error for pod create/get/delete. Preserve missing pods through absent client-go objects, terminated pods through real `PodStatus`, and deletion/reaper behavior through final object absence.

- [ ] **Step 2: Keep watch lifecycle explicitly**

Leave `PrependWatchReactor` in `watch_test.go` only where the test controls watch event timing. Ensure it emits actual Added/Modified/Deleted events and assertions inspect the public lifecycle outcome rather than reactor invocation.

- [ ] **Step 3: Verify reactor policy and package behavior**

Run:

```bash
set -e
gofmt -w atc/worker/jetbridge/container_test.go atc/worker/jetbridge/process_test.go atc/worker/jetbridge/reaper_test.go atc/worker/jetbridge/watch_test.go
ginkgo ./atc/worker/jetbridge
if git grep -n -E '\.(PrependReactor|AddReactor)\(' -- '*.go'; then false; else test $? -eq 1; fi
```

Expected: the suite passes and the search has no matches. A separate search for `PrependWatchReactor` finds only lifecycle/watch tests.

- [ ] **Step 4: Commit and record the CI gate**

```bash
git add atc/worker/jetbridge
git commit -m "test(jetbridge): remove injected Kubernetes API failures"
```

Run in CI: the documented live/K3s exec, busybox supervisor, mount/volume, and artifact-flow gates.

Expected: PASS before merge; do not run them locally on macOS.
