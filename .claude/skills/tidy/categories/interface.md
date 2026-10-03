# interface

Collapse one Go interface that has exactly one implementation and no consumer
outside its package: callers take the concrete type, the interface goes.

Merge: review
Instance key: `<interface>@<package>`

## Scan

```bash
grep -rnE --include='*.go' --exclude-dir=vendor '^type [A-Z][A-Za-z0-9]* interface' . | grep -v _test.go | while IFS=: read -r f l decl; do
  name=$(echo "$decl" | awk '{print $2}'); dir=$(dirname "$f")
  methods=$(awk -v s="$l" 'NR>s && /^}/{exit} NR>s && /\(/{n++} END{print n+0}' "$f")
  outside=$(grep -rlw --include='*.go' "$name" . | grep -v "^$dir/" | wc -l | tr -d ' ')
  [ "$outside" -eq 0 ] && [ "$methods" -le 3 ] && echo "$methods	$name	$f:$l"
done | sort -n
```

For each survivor confirm one implementation: grep for a type with every
method of the interface, and for assignments `var _ Name = ...`. Pick the
smallest method count first.

## Predicate

- Exactly one type satisfies the interface in the whole tree, tests included.
- No file outside the declaring package names the interface.
- At most three methods.
- The implementation lives in the same package.

## Exclude

- Interfaces whose single implementation is in production and whose test
  substitute lives in `*fakes/` or is a hand-written double in a `_test.go`
  file: that is a seam, and removing it is fake removal, a different job.
- Interfaces embedded in another interface.
- Interfaces named in `CONTEXT.md` or `docs/adr/` as a boundary (search the
  name before collapsing).
- `atc/worker/jetbridge/brine/`.

## Gate

`go build ./... && go vet ./...`

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-09-21 | BuildLogRetentionCalculator@atc/gc | gate-failed | `make test-quick` red before the card's own gate, entirely in the nested brine module: TestEngineReleaseDrainsRealDaemon, TestDocumentOwnsSelection, TestDocumentCheckEchoesRoster, TestDocumentRefusals and TestHoldPreservesThenDrainsRealResources want a `brine` binary on PATH; TestBusyboxScriptOutcomeBoundary wants `.build/busybox`; TestTraceCaptureExportsAndDisposesRealCollector wants `otelcol`. All seven reproduce identically on unmodified origin/core in this clone (`ginkgo -r` descends into the nested module despite its own go.mod), so the collapse was reverted unmerged and nothing is left on the branch but this row. Blocks every category here until the prerequisites exist in /Users/tdmtrader/concourse/tidy |
| 2026-09-21 | BuildLogRetentionCalculator@atc/gc | tidied | One method, one implementation, no namer outside atc/gc and no fake: the interface went and the struct took its name, so the exported constructor still returns an exported type. Supersedes the gate-failed row above, whose cause was the root ginkgo run descending into the nested brine module, fixed separately |
| 2026-09-26 | ClaimsParser@skymarshal/token | rejected | codex rejected under rule 2, the predicate fails: `skymarshal/token/access_token_test.go` is package `token_test`, a separate package from the declaring `token`, and it names `token.ClaimsParser` at line 52, so "no file outside the declaring package names the interface" is not satisfied. The card's scan cannot see this -- its `outside` check drops every path under the declaring directory, so an external test package sitting in the same directory reads as zero namers. Everything else was sound: one method, one implementation (`claimsParserNoVerify`), no fake or hand-written double, no nil check at any call site, and `make test-quick`, `go test .`, the hangar architecture tests, `go build ./...`, `go vet` and `go test ./skymarshal/...` all green. Branch `tidy/interface/2026-09-26` left unmerged for the weekly digest. Any future interface pick must check `_test` packages in the declaring directory by hand before trusting the scan |
| 2026-10-02 | Schedulable@atc/component | tidied | One method (`RunImmediately`), one implementation in the whole tree, tests and the brine module included (`*component.Coordinator`, same package, checked by grepping `RunImmediately`), no fake and no hand-written double: `runner_test.go` (package `component_test`) builds a real `Coordinator`. Checked the `_test` package in the declaring directory by hand per the 2026-09-26 row: no file outside `atc/component` names the type -- the hits in `atc/atccmd/command.go`, `atc/component/runner_test.go` and `atc/worker/jetbridge/brine/steps/run_cancellation_finality.go` are the struct field `Schedulable:` assigned `&component.Coordinator{...}`, which fits the new `*Coordinator` field unchanged. Not embedded, named in no `CONTEXT.md`, `docs/adr/` or `docs/architecture/` file. The interface went and the `Runner.Schedulable` field takes `*Coordinator`, keeping its name. `go build ./...`, `go vet ./...`, the brine module's `go build`/`go vet` and `ginkgo ./atc/component` (10/10) green |
| 2026-10-02 | ConcurrentRequestPolicy@atc/wrappa | tidied | One method (`HandlerPool`), one implementation in the whole tree (unexported `*concurrentRequestPolicy`, same package), no fake and no hand-written double. Checked the `wrappa_test` package in the declaring directory by hand per the 2026-09-26 row: `concurrent_request_policy_test.go` and `concurrent_request_limits_wrappa_test.go` only call `wrappa.NewConcurrentRequestPolicy(...)` and `.HandlerPool`, and their one textual hit is the label `Describe("ConcurrentRequestPolicy#HandlerPool")`, a string that names no type and stays accurate; `atc/atccmd/command.go` and `atc/atctest/atctest.go` likewise only call the constructor. Not embedded, named in no `CONTEXT.md`, `docs/adr/` or `docs/architecture/` file. As with BuildLogRetentionCalculator, the interface went and the struct took its name, so `NewConcurrentRequestPolicy` still returns an exported type (`*ConcurrentRequestPolicy`), which the `ConcurrentRequestLimitsWrappa` field and `NewConcurrentRequestLimitsWrappa` parameter now take. `go build ./...`, `go vet ./...`, the brine module's `go build`/`go vet` and `ginkgo ./atc/wrappa` (50/50) green |
| 2026-10-03 | (none) | skipped | all candidates excluded: the scan's 51 survivors split three ways and nothing is left. (1) The single implementation lives outside the declaring package, failing the fourth predicate line: every `atc/exec` delegate factory (`BuildStepDelegateFactory`, `CheckDelegateFactory`, `GetDelegateFactory`, `PutDelegateFactory`, `RunDelegateFactory`, `RunPipelineStepDelegateFactory`, `SetPipelineStepDelegateFactory`, `TaskDelegateFactory`) is satisfied only by `engine.DelegateFactory`; `ReceiptChecker`@atc/hangaroutput and `RunReceiptVerifier`@atc/db only by `*output.ReceiptSignatureVerifier`; `RunOutputVerifier`@atc/db only by `hangaroutput.ControlKeyRing`; `ExecutionSource` and `InputPublisher`@atc/runs only by `*jetbridge.OutputSource` and `*jetbridge.OutputControlClient`; `RunningBuildLookup`@atc/worker/jetbridge only by `db.BuildFactory`, as its own comment says; `SpentCapabilities`@hangar/executioncontrol only by `cmd/hangar-output-daemon`'s `capabilityReplayStore`, which its comment calls deliberate; `AgentFactory`@atc/policy only by `*opa.OpaConfig`; `OutputReadProfile`@hangar only by `*output.LeaseReadProfile`, and `hangar/architecture_test.go` asserts that one production type by name; `SsmAPI`@atc/creds/ssm and `SecretsManagerAPI`@atc/creds/secretsmanager only by the vendored AWS SDK clients. (2) More than one type satisfies it, failing the first: `ConnectionSession` and `ConnectionTracker`@atc/db each have a production `fake…` twin in `connection_tracker.go`; `NodeKeys`@atc/hangaroutput has `NodeKeysFunc` and `*ReadNodeKeys`; `ReadNodeNames` has `*jetbridge.OutputSource` and atctest's `*node`; `Handshaker`@atc/hangaroutput/activation has three; `PlanConfig`@atc is a type switch over every plan; `PutInputs` and `VersionSource`@atc/exec have three each; `TeamConfig`@skymarshal/skycmd has nine; `EmitterFactory`@atc/metric has every emitter config; `SessionSource`@atc/postgresrunner documents two (`*sql.DB` and `db.DbConn`); `DebtReporter`@atc/hangaroutput has none at all. (3) Excluded as a seam or by predicate 2: `BuildPlanner`, `BuildScheduler` and `LockDB` have counterfeiter fakes under `*fakes/`; `Auther`@atc/creds/vault has the hand-written `MockAuther` in `reauther_test.go`; and `AccessTokenFetcher`, `CohortSource`, `ComponentFactory`, `FailedVolume`, `IConjurClient`, `ImagePlanner`, `SecretLookupPath`, `SecretReader`, `SecretRefProvider`, `SecretsWithParams`, `StepConfig`, `StepperFactory` and `TeamFetcher` are each named by a file outside the declaring package -- for several of them an external `_test` package sitting in the declaring directory, the blind spot the 2026-09-26 row warns about, checked by hand here. `ClaimsParser`@skymarshal/token was already logged. One near miss worth naming for a future day: `CaptureChecker`@atc/hangaroutput passes every predicate line -- one method, one implementation (`ControlKeyRing`, same package), named only in `ports.go` and `coordinator.go`, no fake, not embedded, in no `CONTEXT.md` or `docs/adr/` -- but `coordinator.go:243` guards `HoldVerifier == nil` and returns `output.ErrIncomplete`, and `ControlKeyRing` is a struct value, so collapsing it would have to delete that guard and lose an error identity, which step 3 forbids |
