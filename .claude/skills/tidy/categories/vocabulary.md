# vocabulary

Rename one Go identifier that uses a term a glossary lists under _Avoid_ to the
entry's canonical term.

Merge: green
Instance key: `<old identifier>@<package>`

## Scan

Build the term list from the four glossaries, derive identifier forms, count
non-test production hits:

```bash
for ctx in atc atc/agent atc/worker/jetbridge hangar; do
  grep -h '^_Avoid_:' $ctx/CONTEXT.md | sed 's/^_Avoid_: *//; s/ (.*//' | tr ',' '\n' | sed 's/^ *//'
done | grep -E '[ -]' | sort -u | while read -r term; do
  camel=$(echo "$term" | perl -pe 's/[ -](\w)/\u$1/g'); pascal="$(echo ${camel:0:1} | tr a-z A-Z)${camel:1}"
  n=$(grep -rIlw --include='*.go' -e "$camel" -e "$pascal" . 2>/dev/null | grep -v _test.go | wc -l | tr -d ' ')
  [ "$n" -gt 0 ] && echo "$n	$term	$camel|$pascal"
done | sort -rn
```

Pick the term with the most files, then within it the identifier with the
fewest files (finish a term before starting the next). One identifier per day,
every reference to it renamed, compile-verified. Local variables count.

## Predicate

- The term appears under `_Avoid_` in the CONTEXT.md of the context the file
  belongs to, and the entry names the canonical replacement.
- The identifier is Go source: a type, function, method, field, variable, or
  package-level name.

## Exclude

- DB column names, migration SQL, JSON/YAML/wire struct tags, metric and label
  names, flag names, environment variable names. Rename the Go identifier and
  leave the tag string as it was.
- `atc/db/migration/migrations/` (immutable history).
- `atc/worker/jetbridge/brine/` (own module, own vocabulary guard).
- Single-word _Avoid_ terms (`run`, `node`, `cache`, `hold`, `receipt`, and
  every entry marked "alone"): the same word is legitimate in a neighbouring
  context, so the scan keeps only multi-word terms. Renaming a single-word
  term is a reviewed decision, not a daily pick.
- A term whose glossary entry is ambiguous about which canonical term applies
  at this site (e.g. `durableKey` may be a content key or an artifact key; read
  the entry and the code before choosing, and skip if unsure).

## Gate

```bash
go build ./... && go vet ./...
```

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-09-21 | instancePipeline@atc/api/present | renamed | renamed to `payload`; `durable key` skipped as ambiguous (content key vs. artifact key) across all its sites; first ci-check attempt hit a postgres testdb teardown race (build 882228), unrelated to this diff and since addressed by "test: make postgres test-port selection atomic and self-healing" — rebased onto core and rerun |
| 2026-09-24 | (none) | skipped | all candidates excluded: `durable key` still ambiguous at every atc/worker/jetbridge site (storage.go's long-term-storage sense vs. resource_cache_key.go's content-key sense); `instance pipeline`'s only non-brine identifier (InstancePipeline/InstancePipelines@atc/db, called from atc/api/pipelinerunserver) is also called by atc/worker/jetbridge/brine/steps via the `replace github.com/concourse/concourse => ../../../..` module boundary, so renaming it would silently break brine's separate build with no way to fix the call sites under the brine exclude, and `go build ./... && go vet ./...` would not catch it; `durable restore` and `capability key`'s only hits (cmd/artifact-daemon, artifactwire, atc/atccmd) sit outside any bounded context whose CONTEXT.md avoids the term, or inside excluded brine |
| 2026-09-25 | InstancePipelines@atc/db | renamed | renamed `PipelineRunFactory.InstancePipelines` to `Payloads` (interface, impl, doc comment, caller in atc/api/pipelinerunserver/server.go, test calls in atc/db/pipeline_run_payload_batch_test.go); sibling method `InstancePipeline` (singular) left alone — it is also called from atc/worker/jetbridge/brine/steps, a separate Go module outside this diff's reach, so renaming it is a future day's instance; `durable key` and `durable restore` still pending, `capability key` unexamined |
| 2026-10-02 | badTemplatePipeline@fly/commands/internal/validatepipelinehelpers | renamed | renamed the test local to `badTemplate`; `template pipeline` is _Avoid_ under Template in atc/CONTEXT.md and fly is a core surface; the fixture declares `template: true` and its spec reads "a template whose declaration the server would reject"; 3 refs in validate_test.go only, `badTemplate` unused, fixture filename `bad-template-pipeline.yml` kept; gates: go build/vet ./..., ginkgo validatepipelinehelpers (7/7), make test-fly-integration (601/601) |
| 2026-10-02 | instancedTemplatePipelineContent@atc/exec | renamed | renamed the set_pipeline step spec's const to `instancedTemplateContent`; `template pipeline` is _Avoid_ under Template in atc/CONTEXT.md and the YAML it holds declares `template: true` for the "instanced target declares a template" case; 2 refs in set_pipeline_step_test.go only, new name unused; gates: go build/vet ./..., ginkgo atc/exec (625/625) |
| 2026-10-02 | resourceAPITemplatePipeline@atc/api | renamed | renamed the resources spec's stub type to `resourceAPITemplate` (with its doc-comment lead word); `template pipeline` is _Avoid_ under Template in atc/CONTEXT.md and the stub overrides `Template()` to true to stand in for the core Template; 5 refs in resources_test.go only (package api_test), new name unused; spec description strings left as they were; gates: go build/vet ./..., ginkgo atc/api (818/818) |
| 2026-10-02 | templatePipeline@atc/db | renamed | renamed the archiver spec's BeforeEach local to `template`; `template pipeline` is _Avoid_ under Template in atc/CONTEXT.md and the local holds a pipeline saved with `atc.Config{Template: true}`; 2 refs in pipeline_lifecycle_test.go only, no `template` identifier in the enclosing scopes (other db_test hits are struct fields), no text/template import; gates: go build/vet ./..., ginkgo atc/db (1419/1419) |
| 2026-10-02 | templatePipelineID@atc/runs | renamed | renamed the unexported `callerBuild` field to `templateID` (caller_build.go declaration, doc lead word and Scan target; admitter.go's recursion check); `template pipeline` is _Avoid_ under Template in atc/CONTEXT.md and the field doc says it is the template whose run the caller's pipeline is the payload of; no `templateID` existed in package runs; the SQL column `template_pipeline_id` in callerBuildQuery is unchanged; unexported, so brine cannot reach it; gates: go build/vet ./..., ginkgo atc/runs (131/131) |
| 2026-10-02 | templatePipeline@atc/runs | renamed | renamed the runs_test package var to `template` (declaration and doc lead word in runs_suite_test.go, its BeforeEach assignment, and uses in recursion_test.go and versioned_creation_gate_test.go); `template pipeline` is _Avoid_ under Template in atc/CONTEXT.md and the var is saved from `templateConfig`; all 15 test files are package runs_test, where `template` appeared only in comments and strings, so nothing collides or shadows; no text/template import; gates: go build/vet ./..., ginkgo atc/runs (131/131) |
| 2026-10-02 | templatePipeline@atc/api/pipelinerunserver | renamed | renamed the internal-test stub type (`struct{ db.Pipeline }` handed to the run handlers as the template they act on) to `template`, declared in sensitive_body_test.go and used in replay_signal_test.go and result_read_bound_test.go; `template pipeline` is _Avoid_ under Template in atc/CONTEXT.md; all 11 files are package pipelinerunserver and none, production included, declares or imports a `template` identifier; gates: go build/vet ./..., go test atc/api/pipelinerunserver (ok) |
| 2026-10-02 | TemplatePipelineID@atc/runs | renamed | renamed the exported `runs.Run` field to `TemplateID` (runs.go declaration, LookupRun's Scan target, portRun's composite-literal key, and two assertions each in db_free_consumer_test.go and lookup_run_test.go); `template pipeline` is _Avoid_ under Template in atc/CONTEXT.md and the field is the Template's id in the port's own terms; runs.Run has no tags and no importer serializes it or reads this field (composition reads ID and Number only); the db method `creation.Run.TemplatePipelineID()` and the sibling `PayloadPipelineID` are separate instances and stay; brine's only `TemplatePipelineID` is the atc.TaskCacheIdentity field; gates: go build/vet ./..., brine module go build/vet ./..., ginkgo atc/runs (131/131) |
| 2026-10-02 | PipelineRun.TemplatePipelineID@atc | renamed | renamed the `atc.PipelineRun` field to `TemplateID` (core's Template entry avoids "template pipeline"); the wire tag `json:"template_pipeline_id"` is unchanged; refs in atc/pipeline_run.go, atc/api/present/pipeline_run.go, atc/pipeline_run_test.go, go-concourse/concourse/pipeline_runs_test.go; the same-named field on `atc.TaskCacheIdentity` (also used by brine/steps/domain.go), `runs.Run.TemplatePipelineID` and the `db` `TemplatePipelineID()` methods are separate instances and untouched; gates: go build/vet root and brine module, ginkgo ./atc ./atc/api/present ./go-concourse/concourse |
| 2026-10-02 | payloadPipelineURL@fly/commands | renamed | renamed to `payloadPageURL` (core's Payload entry avoids "payload pipeline"; fly is a core surface per CONTEXT-MAP.md); it builds the web URL `fly run-pipeline` prints after `payload: `; plain `payloadURL` was not used because the caller and the body both hold a local of that name; 2 refs in fly/commands/run_pipeline.go only, unexported; gates: go build ./..., go vet ./fly/..., ginkgo ./fly/commands, make test-fly-integration (601/601) |
| 2026-10-02 | captureControlInitName@atc/worker/jetbridge | renamed | renamed the const to `controlInitName` (runtime's Control init entry avoids "capture control init"); it names the step pod's first init container, the one that takes the source hold; its value `"hangar-capture-control"` (a container name) is unchanged; refs in capture_control.go, process_control.go, capture_control_test.go and the `hangar_live`-tagged live_capture_flow_test.go; brine/steps/hangar_capture_pod.go keeps its own unexported const of the old name (brine excluded, separate module); gates: go build ./..., go vet ./atc/worker/jetbridge/ untagged and with `hangar_live`, `live`, `hangar_live,live`, go test ./atc/worker/jetbridge/ |
| 2026-10-02 | TestDaemonSetMode_CleanupPrecedesArtifactInits@atc/worker/jetbridge | renamed | renamed the test function, and its doc-comment lead, to `TestDaemonSetMode_CleanupPrecedesFetchInits` (runtime's Fetch init container entry avoids "artifact-init"); the test asserts the cleanup init container precedes the fetch init containers; 1 file, 2 refs, no build tag, named by no `-run` pattern or brine DISPOSITION ledger; the comment's remaining prose is untouched; gates: go build ./..., go vet ./atc/worker/jetbridge/, go test ./atc/worker/jetbridge/ -run 'TestDaemonSetMode_' (43 passed, the renamed test among them) |
| 2026-10-02 | exactSupervisorScriptTemplate@atc/worker/jetbridge | renamed | renamed the const to `exactSupervisorTemplate` (runtime's Supervisor entry avoids "supervisor script"); it is the exact-execution supervisor's shell template handed to `buildSupervisorCommand`; 2 refs, both in supervisor.go, unexported, no non-Go mention; the sibling `supervisorScriptTemplate` is a separate instance and untouched; gates: go build ./..., go vet ./atc/worker/jetbridge/ (untagged and `live`), go test ./atc/worker/jetbridge/ |
| 2026-10-02 | resourceCacheKey.durableKey@atc/worker/jetbridge | renamed | renamed the `durableKey` parameter of `resourceCacheKey(id, ...)` to `contentKey` (runtime's Content key entry avoids "durable key"); per-site decision, unambiguous here: its only argument is `cache.DurableKey()`, which atc/db/resource_cache.go documents as the cache's content name, and `ResourceCacheKey`'s doc and the Resource cache key entry both say it prefers the content key; every other `durableKey` in the package (the `DurableStorageKey` retention-class sense the 2026-09-24 row found ambiguous), the test-table field `tc.durableKey` and the db method stay; unexported; gates: go build ./..., go vet ./atc/worker/jetbridge/, go test ./atc/worker/jetbridge/ |
