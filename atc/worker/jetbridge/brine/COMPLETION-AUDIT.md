# Brine consolidation completion audit

Verified on `core`, 2026-09-08. The original seven-feature/support/restored-Go
scope is unchanged. The additional requirement is at least 40% **Brine-only
production statement coverage** of `atc/worker/jetbridge`, not repository-wide
or combined Go-test coverage.

The goal is complete in the workspace. Production behavior and shell templates
were not changed. Nothing was pushed, merged or deployed.

## Before and after

| Measure | Baseline | Final |
|---|---:|---:|
| Scoped counted code lines | 13,425 | 12,079 |
| Scoped raw lines, including comments/blanks | 18,278 | 16,662 |
| Registered definitions used in scope | 298 | 288 |
| Registered definitions in the full Brine suite | 1,099 | 1,088 |
| Brine `ExecInPod` implementations | 10 | 2 |
| Expanded Brine cases in scope | 174 | 184 |
| Scoped Go test leaves, including native tests | 87 | 83 |
| Full Brine CLI cases | 568 | 578 |
| Normal full CLI wall time | 147.795s | 169.203s |
| Brine-only production statement coverage | not measured at starting commit | 1,762/2,241 = 78.625614% |

The counted reduction is **10.026071%**, meeting the **12,082-line ceiling**.
All new shared test helpers are counted. Uncounted edits in `steps/worker.go`
and `steps/pod_failure.go` remove adapters/cleanup or reuse counted helpers;
they introduce no relocated helper declarations and earn no reduction credit.
Documentation, measurement tooling, comments and statement packing earn no
credit. The runtime increase is **14.484928%**, below the **177.354s ceiling**.

Eight redundant expanded Brine cases were absorbed into shared contracts.
Eighteen added cases cover the returned-volume handoff, additional real
cancellation states, and production callers alongside retained legacy
diagnostics. The net increase of ten cases is intentional; there is no raw
scenario-count quota.

## Requirement-by-requirement result

| Requirement | Result and authoritative evidence |
|---|---|
| 1. Reproducible baseline | **Pass.** Starting commit `74aaa83d7e7781d69aeeca8701116ff1a0056285`. The final audit independently reads that commit and reproduces 18,278 raw / 13,425 counted lines, 174 expanded cases and 10 executor declarations. A separately compiled historical registry reproduces 1,099 definitions / 298 used. The original CLI log ends with 568 passed and `wall_seconds=147.795`. |
| 2. One ordinary executor | **Pass.** `steps/local_executor.go` owns real command execution, streams, exit translation, process-group cancellation, PTY handling and owned supervisor state. Ordinary task, integration/resource and intercept fixtures install it and reach production `execProcess`. `severingExecutor` is the sole second implementation: it drains stdin and injects the named connection/pod failure, but executes no ordinary command to duplicate or delegate. Other fault fixtures use the shared executor's failure options. |
| 3. Compact, faithful artifact/lifecycle contracts | **Pass.** `ArtifactHandoffDefinitions` obtains producer/output and consumer/input through production APIs, checks exact paths/content and the absence of the adjacent decoy, reads the returned output through gzip, deletes the producer pod, reads the daemon-backed artifact, streams into the returned input and runs the consumer. Raw/gzip/S2 and transfer failures are outline data. Independent codec validation prevents a mutually wrong encoder/decoder from passing. Current wrong-destination, disconnected-executor, transfer-error, cleanup and duplicate-mount faults fail the intended cases. |
| 4. Retention/deletion discipline | **Pass.** All 81 GAP/REFUTED rows name current retained Go contracts and concrete distinctions. Their current fixtures/assertions were audited, including the documented weak historical assertions. All 174 baseline Brine cases and 87 scoped Go leaves are accounted for. Removed cases have the per-case comparisons and distinguishing evidence indexed below; no historical verdict is retroactively relabeled on broad green tests. |
| 5. Smaller implementation and vocabulary | **Pass.** The unchanged fixed census reports 12,079 counted lines, 288 used definitions and 1,088 total definitions. All three vocabulary guards pass: zero unused, undefined or ambiguous definitions. The guards reject empty file/step scans. Every removed case/row has a documented replacement; outline row-number shifts are not mistaken for lost cases. |
| 6. CI and verification | **Pass.** The Brine CI job builds, vets and tests its nested Go module, including vocabulary guards, before `sh scripts/coverage` invokes the full CLI. The final source passes nested Go tests, vet, vocabulary, root Ginkgo including native tests, all 578 CLI cases, the runtime limit and the coverage gate. Changed coverage has targeted mutation evidence. No production shell template changed, so the conditional Busybox validation requirement was not triggered. |
| 7. Before/after report and limits | **Pass.** This audit, `CONSOLIDATION.md` and `DISPOSITION-jetbridge.md` record counts, changes, retained contracts, commands, mutation evidence, timing, coverage and the residual limits below. Work remains local. |
| Added 40% coverage criterion | **Pass.** `scripts/coverage` instruments the production package and adapter entrypoint, filters the profile to the exact production package, rejects empty/unexpected data, and compares the unrounded ratio against 40%. Independent inspection confirms 1,762/2,241 statements and zero fixture entries. Go unit-test coverage is excluded. |

The two direct-mode cases in `container-run.feature` retain generated command,
directory/identity and seccomp specification checks with no executor. Their
fixtures call `Run` but do not `Wait` or claim that a local command executed.
Likewise, construction-only mount/concurrency tests are not ordinary task
execution. The explicitly named legacy pod-lifecycle cases retain
`Process.Wait`'s distinct completion, watcher, retry and immediate-delete paths;
the production task path is covered separately. Focused Go protocol mocks are
intentionally retained, not disguised as Brine replacements.

## Complete case and retention accounting

Final audit evidence directory: `/tmp/brine-completion-audit.4QF0LI/`.

- `case-audit.go` / `case-audit.json` parse baseline and current expanded
  scenarios and independently count the baseline source and executors.
- `prepare-baseline.go` materializes 59 byte-verified historical
  source/dependency/feature snapshots. Only the root module replacement becomes
  absolute. `baseline-module/audit.go` counts the actual registry without
  executing historical scenarios or their old cleanup code. Its command is
  `GOPROXY=off GOSUMDB=off go run ./audit.go`.
- `map-cases.go` / `case-decisions.json` account for **174/174** baseline
  cases with **zero unmapped**. 162 retain the ordered expanded steps after
  explicitly documented vocabulary/default-worker normalization; 12 require
  the recorded consolidation rationale. This is combined with source review
  and mutation evidence, not treated as standalone helper-equivalence proof.
- `map-go-tests.go` / `go-test-decisions.json` account for **87/87** original
  scoped Go leaves. 81 remain distinct; six root-stream leaves share two
  contracts. No other Go leaf is missing.
- The thirty-sixth-pass `retention-inventory-final.json` resolves all 81
  GAP/REFUTED rows to current named tests. The subsequent source audit checked
  their actual assertions, not merely their names.

| Removed/repeated responsibility | Current contract and distinguishing evidence |
|---|---|
| Closing task output, success property and worker ownership; plain-spaces quoting row | Successful task in `task-command.feature`. `hello world` contains the original `hello` expectation. Same ordinary supervised route; exact incidental image/namespace/default-ID expectations remain in Go. Eighth-pass paired stdout, property, label and quoting faults. |
| Closing persisted nonzero status and restart/pod reuse | Failing-task and restart cases retain returned/stored exit 3, both annotation states, Attach refusal, one command and one pod. Eighth-pass returned-exit, annotation, state-key, Attach and duplicate-pod faults. |
| Separate startup timing | Successful task also retains the >=1ms gauge check with nil-stdin supervised execution. Ninth-pass zero-duration and stdin-only-metric faults, plus output/property/label controls. |
| Standalone successful placeholder | Successful task retains both original pod predicates. Twenty-second-pass baked-command, empty-placeholder and delete-success-pod faults. |
| Standalone returned output readback | Handoff's pre-deletion returned-output gzip checkpoint retains `output.txt=hello-from-the-step`. Twentieth-pass disconnected output, omitted mount, wrong tar path and missing gzip faults; wrong pod is additionally detected. |
| Standalone S2 delivery | Handoff S2 row, with independently requested encoding checked against the codec. Twenty-third-pass bypass-decode, skipped write, wrong path, raw-label and nil-codec faults. |
| Six root-path Go StreamIn/StreamOut tests | Two current Go contracts retain exact namespace, pod, container, tar argv, metadata, call count, non-nil stdin, successful reads and opaque byte identity. Subdirectory, file-selector and late pipe-error tests stay separate. Eight paired production faults, replayed against final source below. |

The original task/restart and timing before/after failure logs were inspected
during this audit. The placeholder, returned-output and S2 source/result
verifiers were re-run successfully. Their fresh verification logs are in the
final audit directory. The final `steps/artifact_handoff.go` is byte-identical
to the fully challenged twenty-third-pass candidate.

Current retained-source review confirms the following boundaries:

- Permutation tests own exact volume/mount cardinality and hostPath assignments
  across task, Put, Get, check, overlap, sidecar and scratch configurations.
- Runtime/container tests retain exact TTY flags, command shape, security,
  resource quantities, secret lists, output/cache wiring, DB transition errors,
  concurrency and ordered sidecar specifications.
- Integration tests retain exact namespace/image/default process ID, Put
  mount/output shape, sidecar fields/full mount equality and zero-grace abort.
- Volume tests retain constructor routing, raw byte streams, execution
  attributes, pointer/DB identity, stub error detail and late pod binding.
- DaemonSet tests retain opaque peer-body delivery, a real HEAD probe on a
  responsive missing peer, and zero speculative peer calls on success.

These reasons satisfy retention rather than pretending every contract has
become a local Brine scenario. `JB-container-034` now explicitly distinguishes
its **pre-Run** concrete-volume/HasExecutor check from the handoff's post-Run
streaming. Historical misleading titles and weaker bodies remain documented.

## Fresh final-source fault replay

`replay-streams.go` runs the five selected Ginkgo stream specs using current
tests and production-only overlays. The control passes **5/5**; all eight
historical faults preserve the exact expected failed identities, totaling
**16 test/fault detections**. Ginkgo suite hooks are excluded from spec counts.
See `streams-replay-verified.log` and each `streams-*/report.json`.

`replay-cli.go` builds isolated adapters from current source. Its controls pass
the five handoff rows, four cancellation rows and all 44 container-pod cases.

| Current production fault | Intended failing cases |
|---|---:|
| Disconnect returned-volume executor | All 5 handoff rows |
| Route returned-volume exec to a missing container | All 5 handoff rows |
| Return a nil S2 codec | S2 handoff row only |
| Swallow StreamIn's executor error | Write-refusal handoff row only |
| Omit supervised task pod cleanup | Both task cancellation rows |
| Duplicate input mounts | Same 4 container-pod cases as the original fault |

All nine CLI runs are terminal and verified, with **18 intended case/fault
detections**. The failed step and diagnostic text were inspected for each
fault: no-executor, missing-container, absent requested codec, wrong transfer
error boundary, retained cancelled-task pod, or duplicate mount. Build failures,
missing cases and empty summaries cannot count as detection.

The audit tools initially rejected failed-step count syntax and an oversized
snapshot argument; their guards were corrected. Already-terminal replay logs
were revalidated, not restarted as if their processes were still running.
The initial Go inventory also exposed four imprecise leaf-name spellings,
which were replaced with the exact baseline names. These tooling issues are
not counted as production faults. No task source or production file changed
during the final audit.

## Final gates and reproducibility

Run from `atc/worker/jetbridge/brine`:

```sh
go run ./cmd/brine-census
go test ./... -count=1
go vet ./...
go test ./steps -run 'TestEveryStepDefinitionIsUsedByAScenario|TestEveryScenarioStepResolvesToADefinition|TestNoStepLineMatchesTwoDefinitions' -count=1
go build -o .build/brine-adapter-jetbridge ./cmd/brine-adapter-jetbridge
brine run --mode sync --format brief
sh scripts/coverage
```

Run root database-backed suites with Ginkgo:

```sh
ginkgo --no-color ./atc/worker/jetbridge
```

The exact host PATH, JSON-report and shell timing commands are preserved in
`/tmp/brine-mount-layout.7rhlnY/gates.sh`. That completed final-source run
passes nested Go tests (**7.098s**), vet, vocabulary (**0.454s**), root Ginkgo
(**85/85**, plus native tests), full CLI (**578/578, 169.203s**) and coverage.
No competing test/build job ran during the timed normal CLI.

Every counted source file still matches that run's `scoped-source.sha256`.
No new full-suite result is claimed for this documentation/audit-only pass.
The isolated fault adapters never replace the normal adapter, whose SHA-256
remains `7a751f47d06d8eb3bea1336c5a0a4389d9e34398b2b05dad5c5a22d89cd4116f`.

Coverage profile: `/tmp/brine-coverage.hatWqo/coverage.out`, SHA-256
`b6d77d686d000dde2935f98889411bfe88bf12b9226276fddca0df478ed2970b`.
The profile and exact denominator remain unchanged after the final audit.

## Residual limits, not unfulfilled consolidation requirements

- The local executor runs host processes with fake Kubernetes objects. It does
  not reproduce SPDY, kubelet mount namespaces, real scheduling, image execution
  or automatic init-container delivery. The existing live/security/volume/
  sidecar suites remain in the repository; this pass does not claim to have
  run the real-cluster tier.
- SC-07's retained old Go assertion observes any log request, not a named
  sidecar writer or prefix. One retained overlap-name loop does not assert
  that it found the mount. Several old cache titles promise more than their
  bodies check. Their precise limitations remain in the dispositions; no
  equivalence or repaired coverage is claimed beyond demonstrated behavior.
- Focused Go mocks and the narrow severed-connection adapter remain for named
  protocol, identity and compatibility contracts. Repository-wide mock removal
  and repair of every historical test weakness were explicitly outside this
  goal.

There are no remaining in-scope implementation or verification steps.
