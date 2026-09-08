# Task 6 report: exact Hangar tree inputs in ordinary JetBridge task Pods

## Result

Implemented fail-closed exact Hangar inputs for the Kubernetes runtime. `runtime.Input`
now carries exactly one of an ordinary artifact or a Hangar tree ref. Strict inputs are
materialized by a second DaemonSet init container into the already-declared task input
volume, then exposed read-only to the task and every sidecar. Ordinary artifact behavior,
including writable input/output overlap, is unchanged.

The web process loads an exact raw 32-byte capability key once, constructs one 15-minute
grant signer, and passes that signer to both JetBridge config compositions. Enabling
Hangar requires the Kubernetes namespace, artifact-daemon hostPath, complete client TLS,
and capability key. Presenting a strict ref while Hangar is disabled rejects Pod
construction synchronously.

The strict init request is a bounded Go-marshaled batch with the Task 5 wire shape
`{"items":[{"ref":...,"handle":...,"volume":...,"grant":"Bearer <token>"}]}`.
Each grant is minted only after the exact handle and existing input volume are known.
The POSIX shell stores the base64-decoded request, response, and headers in a private
temporary directory, cleans them on exit/signals, retries at most five times only when
there is no HTTP status or status 503, and fails every other non-2xx or partial/error
response without printing payloads, responses, or tokens. The signing key is absent
from Pod commands, environment, and volumes.

Production Pod construction now rejects any init/main/sidecar volume mount without a
matching Pod volume. Strict inputs also add `concourse.dev/hangar-v1 In [ready]` to the
same required selector term that contains `concourse.dev/artifact-cache In [ready]`.

## TDD evidence

RED, storage contract and strict batch:

```text
env GOCACHE=/tmp/hangar-task6-gocache go test ./atc/worker/jetbridge -run '^TestDaemonSetBackend_BuildFetchInitContainers_AppendsExactHangarBatch$' -count=1
# compile failure: Config.HangarEnabled/HangarGrantSigner, runtime.Input.HangarTree,
# and the error-returning BuildFetchInitContainers contract did not exist
```

GREEN:

```text
go test ./atc/worker/jetbridge -run '^(TestDaemonSetBackend_BuildFetchInitContainers_AppendsExactHangarBatch|TestDaemonSetBackend_BuildFetchInitContainers_RejectsDisabledOrUnsignableHangar)$' -count=1
ok github.com/concourse/concourse/atc/worker/jetbridge 0.589s
```

RED, Pod semantics:

```text
go test ./atc/worker/jetbridge -run '^TestDaemonSetMode_(StrictInputValidationFailsClosed|StrictInputsAreReadOnlyEverywhereAndPodMountsResolve|OrdinaryOverlappingInputRemainsWritable|BuildPodRejectsUnresolvedMounts)$' -count=1
# missing/two-source, disabled, and overlap cases were accepted; strict main/sidecar
# mounts were writable; Hangar affinity was absent; an unresolved init mount was accepted
```

GREEN:

```text
go test ./atc/worker/jetbridge -run '^(TestDaemonSetBackend_BuildFetchInitContainers|TestBuildFetchInitContainers|TestDaemonSetMode_(StrictInputValidationFailsClosed|StrictInputsAreReadOnlyEverywhereAndPodMountsResolve|OrdinaryOverlappingInputRemainsWritable|BuildPodRejectsUnresolvedMounts))' -count=1
ok github.com/concourse/concourse/atc/worker/jetbridge 0.590s
```

RED, web flags and validation:

```text
go test ./atc/atccmd -run '^TestSuite$' -testify.m '^TestHangarRuntime' -count=1
# compile failure: RunCommand Kubernetes HangarEnabled/HangarCapabilityKey did not exist
```

GREEN:

```text
go test ./atc/atccmd -run '^TestSuite$' -testify.m '^TestHangarRuntime' -count=1
ok github.com/concourse/concourse/atc/atccmd 0.688s
```

## Verification

```text
# All standalone, non-Ginkgo JetBridge tests (238 tests; TestJetbridge excluded by exact name)
go test ./atc/worker/jetbridge -run '<exact generated alternation of the 238 listed test names>' -count=1
ok github.com/concourse/concourse/atc/worker/jetbridge 30.684s

# Database-backed suite, run with Ginkgo per AGENTS.md
ginkgo -r -p ./atc/worker/jetbridge
369/369 specifications passed, total 58.295s

go test ./atc/atccmd -count=1
ok github.com/concourse/concourse/atc/atccmd 0.669s

go test . -run '^TestAgenticLayerIsImportedOnlyAtItsWiringPoint$' -count=1
ok github.com/concourse/concourse 1.217s

git diff --check
# pass

# Supplemental host-shell parse only
awk '<extract daemonHangarMaterializationCommand template>' atc/worker/jetbridge/storage_daemonset.go | sh -n
# pass
```

## Changed files

- `atc/runtime/types.go`
- `atc/worker/jetbridge/config.go`
- `atc/worker/jetbridge/storage.go`
- `atc/worker/jetbridge/storage_daemonset.go`
- `atc/worker/jetbridge/container.go`
- `atc/atccmd/command.go`
- `atc/atccmd/command_test.go`
- `atc/worker/jetbridge/storage_daemonset_test.go`
- `atc/worker/jetbridge/daemonset_integration_test.go`
- `atc/worker/jetbridge/daemon_tls_test.go`
- `atc/worker/jetbridge/container_test.go`
- `atc/worker/jetbridge/resource_test.go`
- `atc/worker/jetbridge/artifact_integration_test.go`
- `atc/worker/jetbridge/integration_test.go`
- `atc/worker/jetbridge/podname_integration_test.go`
- `atc/worker/jetbridge/live_test.go`
- `atc/worker/jetbridge/live_worker_test.go`

The ordinary integration fixtures were updated to supply real ordinary artifacts because
the new exact-one-source invariant correctly rejects source-less inputs. The two live-test
files only remove historical artifact resolve-capability plumbing for fields/daemon flags
that no longer exist; no live test cases, TLS adoption, worker setup, or artifact behavior
coverage was deleted.

## Exact remaining gaps

- A real BusyBox Pod/K3s run is CI-only on this macOS environment, per `AGENTS.md`.
  The host `sh -n` result above is supplemental and is not claimed as BusyBox proof.
- `go test -tags live ./atc/worker/jetbridge -run '^$' -count=1` still cannot compile
  because of two unrelated pre-existing stale live-test symbols:
  `postgresrunner.StandardTestRunner` and `Config.ArtifactDaemonNamespace`. The obsolete
  artifact resolve-capability references owned by this task are gone. Those unrelated
  symbols were not broadened into this change.

## Fix round 1: verified materialization outcome

Review and independent security adjudication retained the planned in-command batch
transport. The Pod spec therefore intentionally contains recoverable 15-minute grants,
each limited to one exact ref/handle/volume. This is accepted exposure, not grant secrecy.
The long-lived 32-byte HMAC key remains absent from the command, environment, mounts, and
serialized init-container Pod spec, with a focused regression assertion.

The same-node `${HOST_IP}` route is not server-authenticated because BusyBox wget uses
`--no-check-certificate`; NetworkPolicy is optional, defaults off, and depends on CNI
enforcement. The init no longer treats transport success as the outcome boundary. It now
requires wget success, an exact final HTTP 204, and a zero-byte response, then independently
verifies every materialized input through a generated fixed read-only
`/hangar-inputs/input-N` mount. The daemon writes through its separate hostPath, so the
init has no writable strict input mount.

For each tree, verification requires a non-symlink directory with exact mode 0555 and a
non-symlink regular `.hangar-materialized` file with exact mode 0444. The receipt must be
byte-for-byte identical to the safely base64-encoded Go `json.Marshal(TreeRef)` bytes;
wrong refs/generations, malformed/trailing content, and mode/type changes fail the whole
batch. Real server-authenticated local transport remains future hardening.

Strict input/output validation now rejects equality and either separator-aware
ancestor/descendant direction after cleaning paths, while allowing siblings such as
`/work/exact` and `/work/exact2`. Signal traps for HUP, INT, and TERM remove the private
temporary directory and terminate nonzero without recursively invoking the EXIT trap.

### Fix-round TDD evidence

RED:

```text
go test ./atc/worker/jetbridge -run '^(TestDaemonSetBackend_(BuildFetchInitContainers_AppendsExactHangarBatch|HangarInitRequiresExactResponseAndReceipt|HangarInitSignalsCleanUpAndFail)|TestDaemonSetMode_(StrictInputValidationFailsClosed|StrictInputAllowsSiblingOutput|StrictInputsAreReadOnlyEverywhereAndPodMountsResolve))$' -count=1
# descendant overlaps reached signer validation; init mount was writable at the user
# destination; false 204s and altered/missing receipts exited 0; signal cleanup could
# exit successfully
```

GREEN after implementation and expanded cases:

```text
go test ./atc/worker/jetbridge -run '^(TestDaemonSetBackend_(BuildFetchInitContainers|HangarInit)|TestBuildFetchInitContainers|TestDaemonSetMode_(StrictInputValidationFailsClosed|StrictInputAllowsSiblingOutput|StrictInputsAreReadOnlyEverywhereAndPodMountsResolve|OrdinaryOverlappingInputRemainsWritable|BuildPodRejectsUnresolvedMounts))' -count=1
ok github.com/concourse/concourse/atc/worker/jetbridge 5.375s
```

The matrix covers exact success, false 204, missing/wrong-ref/wrong-generation/malformed/
trailing/symlink receipts, symlink roots, wrong receipt/root modes, 200/201, nonempty
whitespace/truncated/HTML 204 bodies, wget failure after 204, bounded no-status/503 retries,
terminal other statuses, multiple fixed mounts, and HUP/INT/TERM during wget and retry.

### Fix-round verification

```text
# Every listed standalone JetBridge test except the Ginkgo entrypoint
go test ./atc/worker/jetbridge -run '<exact generated alternation>' -count=1
ok github.com/concourse/concourse/atc/worker/jetbridge 35.358s

ginkgo -r -p ./atc/worker/jetbridge
369/369 specifications passed (20.741s specs; 1m17.271s command total)

go test ./atc/atccmd -count=1
ok github.com/concourse/concourse/atc/atccmd 0.685s

go test . -run '^TestAgenticLayerIsImportedOnlyAtItsWiringPoint$' -count=1
ok github.com/concourse/concourse 0.928s

gofmt -w <four changed Go files>
git diff --check
# pass

# Supplemental host-shell parse only
awk '<extract daemonHangarMaterializationCommand template>' atc/worker/jetbridge/storage_daemonset.go | sh -n
# pass
```

BusyBox `stat -c '%a'`, `cmp`, and `base64 -d` are the target applets. The generated
script includes a macOS `stat -f '%Lp'` fallback solely so supplemental host execution
can test the same script. A real BusyBox Pod/K3s execution remains CI-only and is not
claimed here.
