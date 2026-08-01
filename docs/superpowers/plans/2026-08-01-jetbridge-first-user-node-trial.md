# Jetbridge First-User Node Trial Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Author, live-run, iterate, and release evidence-backed code-review and log-diagnosis reusable nodes while preserving a first-user findings record.

**Architecture:** Keep each node as a schema-1 package under `agent/workflow/seeds`, with typed-output mechanics in its prompt and the reasoning method in a bundled skill. Exercise exact immutable versions on `home` using typed snapshots from benchmark exposures, then compare version 1 and version 2 before releasing the better result.

**Tech Stack:** Go node compiler tests, YAML manifests, Markdown prompts/skills, Fly CLI, Jetbridge snapshot contracts, benchmark corpus fixtures.

## Global Constraints

- Work only in `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-platform-rebase` on `codex/agentic-platform-rebase`.
- Preserve the user's existing `.superpowers/sdd/2026-07-28-agentic-foundations-semantic-rebase/progress.md` modification.
- Do not push, merge, open a PR, or modify the main checkout.
- Keep scope at reusable-node authoring; do not modify workflows or platform implementation to hide trial friction.
- Use only snapshot types compiled into the live registry.
- Never expose benchmark `case.yaml`, `notes.md`, or `ground_truth` to a live node.
- Release only after a successful direct run with an inspected typed output.
- Update `JETBRIDGE_FIRST_USER_FINDINGS.md` after every material observation.

---

### Task 1: Make the shipped code-review node a complete portable package

**Files:**
- Modify: `agent/workflow/seed_test.go`
- Modify: `agent/workflow/seeds/code-review-node-v1/node.yaml`
- Modify: `agent/workflow/seeds/code-review-node-v1/prompts/review.md`
- Create: `agent/workflow/seeds/code-review-node-v1/skills/review/SKILL.md`
- Modify: `JETBRIDGE_FIRST_USER_FINDINGS.md`

**Interfaces:**
- Consumes: schema-1 node compiler, `repository/v1`, and `review/v1`.
- Produces: `code-review` with `MINIMUM_SEVERITY`, a bundled review method, no placeholder sidecar, and deployment-selected model routing.

- [ ] **Step 1: Attempt the documented sample unchanged**

Run `fly -t home agent nodes import agent/workflow/seeds/code-review-node-v1`.
Record the exact result before editing. Expected: missing selected skill-tree
failure, or an import whose compiled sidecar still points at `registry.example`.

- [ ] **Step 2: Establish RED with the existing contract**

Run:

```bash
go test ./agent/workflow -run TestCodeReviewReusableNodeSeedFreezesItsAtomicImplementation -count=1
```

Expected: FAIL because `skills/review/SKILL.md` is absent.

- [ ] **Step 3: Make the test require portability and method assets**

Replace the model/sidecar assertions with:

```go
if agent.Model != "" || agent.BudgetSliceUSD != 5 ||
	!reflect.DeepEqual(agent.Skills, []string{"review"}) {
	t.Fatalf("frozen agent implementation = model %q budget %v prompt %q skills %q", agent.Model, agent.BudgetSliceUSD, agent.Prompt, agent.Skills)
}
if definition.Function.SkillFiles["skills/review/SKILL.md"] == "" {
	t.Fatalf("compiled review skill tree = %#v", definition.Function.SkillFiles)
}
if len(agent.Sidecars) != 0 {
	t.Fatalf("portable code-review node has sidecars = %#v", agent.Sidecars)
}
```

- [ ] **Step 4: Verify the changed test is RED for the intended reasons**

Run the Step 2 command. Expected: FAIL because the manifest still pins the
model/capability and the selected skill is absent.

- [ ] **Step 5: Implement the minimal package**

Keep ports, parameter, budget, prompt file, and `skills: [review]`. Delete the
top-level capability, step capability, and `model: claude-sonnet`.

Create `skills/review/SKILL.md`:

```markdown
# Evidence-driven code review

1. Inventory changed paths and the behavior each can alter.
2. Trace each risk from triggering state/input to an externally visible failure path.
3. Read callers, invariants, and nearby tests before declaring a defect.
4. Compare `before` for attribution; separate introduced, aggravated, and pre-existing issues.
5. Cite the smallest useful file/line range.
6. Try to falsify every candidate. Drop style, speculation, and unverified assumptions as false positives.
7. Rank by impact and likelihood, then apply `MINIMUM_SEVERITY`.

Prefer a short high-confidence review. A clean review is valid when no finding survives falsification.
```

Update the prompt to retain the exact output variables and ordering contract,
define accepted severity values, require the skill method, and explain that
inputs may be plain filesystem trees without Git metadata.

- [ ] **Step 6: Verify GREEN locally**

Run:

```bash
go test ./agent/workflow -run 'TestCodeReviewReusableNodeSeedFreezesItsAtomicImplementation|TestOnlySupportedEngineeringSeedsRemain' -count=1
go test ./agent/workflow/... -count=1
```

Expected: PASS.

- [ ] **Step 7: Record observed sample and authoring behavior**

Append the unchanged-import result, compiler diagnostics, and prompt/skill
separation experience to `JETBRIDGE_FIRST_USER_FINDINGS.md`.

---

### Task 2: Execute code-review version 1 on `review-jb-003`

**Files:**
- Modify: `JETBRIDGE_FIRST_USER_FINDINGS.md`

**Interfaces:**
- Consumes: Task 1 package and refs `54b541a81e6235ca74256dfbd50666ec45a18d2c` / `199ab7497399aa157065b660537caa652373791c`.
- Produces: imported version, two `repository/v1` snapshots, one durable run, and an evaluated `review/v1` output.

- [ ] **Step 1: Import and inspect**

Run:

```bash
fly -t home agent nodes import agent/workflow/seeds/code-review-node-v1
fly -t home agent nodes list
fly -t home agent nodes show code-review IMPORTED_VERSION --json
```

Use the returned version rather than assuming `1` if the unchanged attempt
created a version.

- [ ] **Step 2: Materialize neutral inputs**

Run:

```bash
REVIEW_FIXTURE_ROOT=$(mktemp -d /tmp/jetbridge-review-XXXXXX)
mkdir "$REVIEW_FIXTURE_ROOT/before" "$REVIEW_FIXTURE_ROOT/after"
git archive 54b541a81e6235ca74256dfbd50666ec45a18d2c | tar -x -C "$REVIEW_FIXTURE_ROOT/before"
git archive 199ab7497399aa157065b660537caa652373791c | tar -x -C "$REVIEW_FIXTURE_ROOT/after"
```

Confirm neither tree contains benchmark ground truth or the terminal fix.

- [ ] **Step 3: Create typed snapshots**

Run and retain each returned ID:

```bash
fly -t home agent snapshots create --type=repository/v1 --from="$REVIEW_FIXTURE_ROOT/before" --json
fly -t home agent snapshots create --type=repository/v1 --from="$REVIEW_FIXTURE_ROOT/after" --json
fly -t home agent snapshots show BEFORE_SNAPSHOT_ID --json
fly -t home agent snapshots show AFTER_SNAPSHOT_ID --json
```

- [ ] **Step 4: Run the unreleased exact version**

```bash
fly -t home agent nodes run code-review CODE_REVIEW_VERSION \
  --input before=BEFORE_SNAPSHOT_ID --input after=AFTER_SNAPSHOT_ID \
  --param MINIMUM_SEVERITY=low \
  --idempotency-key=first-user-code-review-v1-review-jb-003 --json
```

- [ ] **Step 5: Inspect terminal evidence and output**

Poll with bounded repeated
`fly -t home agent nodes show-run code-review RUN_ID --json` reads. If a build
ID is present, use `fly -t home watch -b BUILD_ID`. On success, inspect and
download the output with `fly agent snapshots show/download`.

Only after terminal execution, compare `record.json` with
`review-jb-003/ground_truth/expected_findings.yaml` and `rubric.md`. Record
typed validity, F1-F3/D1 recall, unsupported findings, duration, and friction.

---

### Task 3: Author and execute log-diagnosis version 1

**Files:**
- Modify: `agent/workflow/seed_test.go`
- Create: `agent/workflow/seeds/log-diagnosis-node-v1/node.yaml`
- Create: `agent/workflow/seeds/log-diagnosis-node-v1/prompts/diagnose.md`
- Create: `agent/workflow/seeds/log-diagnosis-node-v1/skills/diagnosis/SKILL.md`
- Modify: `JETBRIDGE_FIRST_USER_FINDINGS.md`

**Interfaces:**
- Consumes: required `log-bundle/v1`, optional `deployment-snapshot/v1`.
- Produces: `diagnosis/v1` and a live diagnosis of `rca-jb-003`'s exposed bundle.

- [ ] **Step 1: Add failing catalog and compile-contract tests**

Add `log-diagnosis-node-v1` to `TestOnlySupportedEngineeringSeedsRemain`. Add
`TestLogDiagnosisReusableNodeSeedFreezesItsAtomicImplementation` that compiles
the directory, looks up every port in `contracts.NewRegistry()`, and asserts:

```go
logs, deployment := definition.Function.Inputs[0], definition.Function.Inputs[1]
if definition.Name != "log-diagnosis" ||
	logs.Name != "logs" || logs.Type != "log-bundle/v1" || logs.Optional ||
	deployment.Name != "deployment" || deployment.Type != "deployment-snapshot/v1" || !deployment.Optional {
	t.Fatalf("log-diagnosis inputs = %#v", definition.Function.Inputs)
}
agent := definition.Function.Plan[0].Config.(*atc.AgentStep)
if agent.Model != "" || agent.BudgetSliceUSD != 5 || len(agent.Sidecars) != 0 ||
	!reflect.DeepEqual(agent.Skills, []string{"diagnosis"}) {
	t.Fatalf("log-diagnosis agent = %+v", agent)
}
if definition.Function.SkillFiles["skills/diagnosis/SKILL.md"] == "" {
	t.Fatalf("compiled diagnosis skill tree = %#v", definition.Function.SkillFiles)
}
```

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./agent/workflow -run 'TestOnlySupportedEngineeringSeedsRemain|TestLogDiagnosisReusableNodeSeedFreezesItsAtomicImplementation' -count=1
```

Expected: FAIL because the directory is absent.

- [ ] **Step 3: Implement the package**

Create `node.yaml` with the exact ports above and:

```yaml
step:
  agent: diagnose
  function_id: diagnose-logs
  budget_slice_usd: 5
  prompt_file: prompts/diagnose.md
  skills: [diagnosis]
```

Adapt the workflow seed prompt without changing its environment-variable,
lexicographic subject-ordering, hypothesis/action-ordering, optional-input, or
hermeticity rules. Create this bundled method:

```markdown
# Evidence-driven log diagnosis

1. Build a timeline from captured evidence before naming a cause.
2. Separate the first causal error from downstream noise.
3. Rank hypotheses; cite evidence and actively search for counterevidence.
4. Compare environment-specific facts when behavior differs across machines.
5. Calibrate confidence; use `inconclusive` when no cause survives.
6. Recommend the smallest next action that separates or resolves hypotheses without contacting a live system.

Every `identified` conclusion needs decisive evidence anchored to immutable inputs.
```

- [ ] **Step 4: Verify GREEN**

```bash
go test ./agent/workflow -run 'TestOnlySupportedEngineeringSeedsRemain|TestLogDiagnosisReusableNodeSeedFreezesItsAtomicImplementation' -count=1
go test ./agent/workflow/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Import and create the exact exposed input**

```bash
fly -t home agent nodes import agent/workflow/seeds/log-diagnosis-node-v1
fly -t home agent nodes show log-diagnosis IMPORTED_VERSION --json
fly -t home agent snapshots create --type=log-bundle/v1 \
  --from=bench/corpus/rca-jb-003/task --json
```

The snapshot source includes only exposed `task.md` and `evidence/`, never
harness metadata or ground truth.

- [ ] **Step 6: Run and evaluate**

```bash
fly -t home agent nodes run log-diagnosis LOG_DIAGNOSIS_VERSION \
  --input logs=LOG_SNAPSHOT_ID \
  --idempotency-key=first-user-log-diagnosis-v1-rca-jb-003 --json
```

Follow to terminal state, download the output, then compare it with
`rca-jb-003/ground_truth/answer.md` and `rubric.md`. Record whether it identifies
the shell-specific `kill -- -PID` mechanism, decisive environment evidence,
POSIX-safe fix/verification, and whether it invents observations.

---

### Task 4: Add benchmark-driven structured-method revisions and select releases

**Files:**
- Modify: `agent/workflow/seed_test.go`
- Modify: both node skill files
- Modify: `JETBRIDGE_FIRST_USER_FINDINGS.md`

**Interfaces:**
- Consumes: exact version-1 snapshots and evaluations.
- Produces: immutable version-2 imports, paired evidence, one selected release per node.

- [ ] **Step 1: Treat version-1 benchmark discrepancies as behavioral RED**

Write the version-1 comparison into the findings record before editing either
skill. For review, name which F1-F3/D1 obligations were missed or imprecise. For
diagnosis, name any wrong mechanism, weak evidence, unjustified confidence, or
non-discriminating action. This is the consumer-visible failure the revision
must change; do not add tests that merely grep instructional prose.

- [ ] **Step 2: Add the minimal structured passes**

Review must trace state transitions over time/retries/refreshes/reordering,
inspect second-order consumers, and verify misleading comments against control
flow. Diagnosis must build an explicit environment delta, run a direct
experiment against captured interpreter/utility semantics when possible, and
name a discriminating observation between surviving hypotheses.

- [ ] **Step 3: Verify package integrity and import both changed packages**

```bash
go test ./agent/workflow -run 'TestCodeReviewReusableNodeSeedFreezesItsAtomicImplementation|TestLogDiagnosisReusableNodeSeedFreezesItsAtomicImplementation' -count=1
go test ./agent/workflow/... -count=1
fly -t home agent nodes import agent/workflow/seeds/code-review-node-v1
fly -t home agent nodes import agent/workflow/seeds/log-diagnosis-node-v1
```

- [ ] **Step 4: Re-run the exact snapshots against version 2**

Use the Task 2/3 snapshot IDs with new idempotency keys
`first-user-code-review-v2-review-jb-003` and
`first-user-log-diagnosis-v2-rca-jb-003`. Follow both to terminal state and
download successful outputs.

- [ ] **Step 5: Evaluate behavioral GREEN and release**

For review, compare validity, recall, attribution, priorities, anchors, and
false positives. For diagnosis, compare validity, mechanism, decisive evidence,
confidence, counterevidence, and verification. Prefer v2 only if it is at least
as precise and materially stronger. If the named version-1 discrepancy remains,
record that version 2 stayed red and select the better successful version rather
than claiming improvement.

```bash
fly -t home agent nodes release code-review SELECTED_VERSION --compatibility=compatible
fly -t home agent nodes release log-diagnosis SELECTED_VERSION --compatibility=compatible
```

Replace all pending findings sections with observed results and exact version/run
identities.

---

### Task 5: Final verification and handoff

**Files:**
- Modify: `.superpowers/sdd/2026-07-28-agentic-foundations-semantic-rebase/progress.md`
- Modify: `JETBRIDGE_FIRST_USER_FINDINGS.md`

**Interfaces:**
- Consumes: authored packages and live evidence.
- Produces: fresh tests, live catalog proof, and the mandatory session handoff.

- [ ] **Step 1: Run fresh focused verification**

```bash
go test ./agent/workflow/... -count=1
go test ./fly/commands -run 'TestAgentNodes|TestAgentSnapshots' -count=1
git diff --check
```

If the Fly regex selects no snapshot tests, record that and run
`go test ./fly/commands -count=1` once.

- [ ] **Step 2: Verify live evidence**

```bash
fly -t home agent nodes list --json
fly -t home agent nodes runs code-review SELECTED_VERSION --status=succeeded --json
fly -t home agent nodes runs log-diagnosis SELECTED_VERSION --status=succeeded --json
```

- [ ] **Step 3: Review only the authored delta**

Inspect the findings, `seed_test.go`, both node packages, and this plan. Fix only
correctness, security, data-loss, or acceptance blockers. Record lesser ideas.

- [ ] **Step 4: Update the active-track progress record additively**

Append commits, tests, live versions/run IDs, release state, and dirty paths to
the existing progress file without overwriting the user's current modification.

- [ ] **Step 5: Commit completed trial artifacts**

Stage only the findings, node packages, tests, plan, and additive progress
update. Preserve unrelated changes. Commit as:

```bash
git commit -m "feat(agent): dogfood reusable nodes"
```
