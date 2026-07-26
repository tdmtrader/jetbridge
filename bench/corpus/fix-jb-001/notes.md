# Curation record — fix-jb-001

## Provenance walk

Backed out of a merged fix commit in this repo's jetbridge-era history.

| Role | SHA | Date | Subject |
|---|---|---|---|
| terminal artifact | `a1af1068c9cc1b231315e676b80f92be7277b31c` | 2026-07-11T13:04:45-07:00 | `fix(ci-agent): recover panicking tool handlers on the SSE path [review finding]` |
| pre_state (parent) | `33d699c0d21b1e61dd0ecbd21d5e000030f2dd7a` | 2026-07-11T13:03:39-07:00 | `fix(ci-agent): dev-mcp waits for graceful drain before exiting on SIGTERM [review finding]` |

Verification performed:

- `git show -s` on the terminal SHA confirms the commit message states exactly
  what the candidate claimed: the SSE `tools/call` goroutine ran the
  `ToolHandler` with no recovery; `net/http` recovers panics only in the request
  goroutine, so a panicking handler killed the sidecar process; the fix recovers
  and emits the `-32603` frame the `toolResponse` helper already uses for
  marshal failures.
- `git show --stat` confirms the commit is exactly two files, +36/-0:
  `ci-agent/devmcp/server.go` (+8) and `ci-agent/devmcp/server_test.go` (+28).
  No docs companion, no version bump, no CHANGELOG — nothing to strip.
- `git rev-parse a1af106^` resolves to the claimed parent, so pre_state is the
  literal parent commit; there is no gap and no intervening churn.
- Reading `server.go` at the parent confirms the defect is really present and is
  the only unrecovered handler invocation: `handleToolsCall` calls `handler(...)`
  twice — once inline on the non-SSE path (inside the request goroutine, which
  `net/http` recovers) and once inside `go func(){ ... }()` on the SSE path
  (which it does not). `fmt` is already imported, so the reference fix needs no
  import change.
- `git branch -a --contains a1af106` shows the commit on `jetbridge` (the
  mainline) among others — it was merged, not abandoned. `ground_truth.outcome`
  is `merged`.
- `git ls-tree 33d699c bench/` is empty: the pre_state predates `bench/`, so the
  self-hosted-corpus caveat in the schema is satisfied — replaying this case
  cannot expose the corpus or its answers.

Pre-state coherence: the parent is itself a small sibling fix from the same
hardening review pass (SIGTERM drain in `ci-agent/cmd/dev-mcp`), landed 66
seconds earlier. It touches a different file and a different concern, and it
leaves the tree green (verified: the devmcp + runner suites pass at pre_state).
The dev-mcp server itself had landed in the ten commits before that
(`4dd93839e8` … `70ca1d79b0`), which is why a fresh-code robustness gap is
plausible as a work item at this instant.

## Leakage analysis

Nothing at pre_state gives the answer away, so `withheld: []`.

Checks run against the pre_state tree (all via `git grep <pattern> 33d699c`, no
checkout):

- `-iE "panick|handler panic|tool handler panic|recover\(\)"` over `*.md`: five
  hits, all unrelated — a nil-stub aside in `11-dispatch.md`, and four archived
  `forge/archive/**` track docs about other panics (build tracker, cache
  locator, stub volume StreamOut). None concerns dev-mcp.
- `-iE "panick"` over `ci-agent/`: zero hits.
- `docs/superpowers/plans/agentic-platform/04-dev-mcp.md` (3346 lines, the
  dev-mcp plan, which does embed the server source as reference listings):
  greps for `panic|recover|goroutine|-32603|internal error|crash|robust` return
  only the error-taxonomy sentence (§ "JSON-RPC error codes"), the embedded
  `toolResponse` marshal-failure line, and an unrelated `panic("runtime.Caller
  failed")`. The plan never anticipates the defect.
- `docs/superpowers/plans/agentic-platform/REVIEW.md` — this is the big in-tree
  review document (findings F1–F39) and was the obvious leak risk, since the
  commit is tagged `[review finding]`. Its dev-mcp row reads: *"04 | dev-mcp | 1
  | **sound** | Scoped precisely to charter; clean seams, correct taxonomy, no
  scope leakage; only a minor drift-guard test gap."* It does not contain this
  finding. Confirms the curator's instruction to verify rather than assume.
- `ci/dogfood/FINDINGS.md` — exists at pre_state; its only `panic` mentions are
  about `atc/auditor`'s exhaustive-switch panic, unrelated.
- Active (non-archive) `forge/tracks/**` at pre_state: two tracks
  (`dead_suite_removal_20260610`, `native_resolver_insecure_ca_certs_20260607`),
  neither touching ci-agent or dev-mcp.

So the review that produced this fix was an ad-hoc code review whose findings
were never committed as a document. Good for us: the exposure manifest is clean
without withholding anything.

**Grading tests.** `ci-agent/devmcp/server_test.go` exists at pre_state with six
specs; the seventh (`survives a panicking tool handler on the SSE path and keeps
serving`) was written with the fix and is post-cut. It lives only in
`ground_truth/` — both as a patch (`test.diff`) and as the full post-state file
(`withheld_tests/…/server_test.go`) so a grader can restore it verbatim over
whatever the agent wrote in that file.

**What I deliberately withheld beyond the commit** (one item, recorded here per
the schema's honesty requirement):

- *The panic stack trace.* A real operator hitting this would have had the
  goroutine dump, and it names `handleToolsCall.func2` at `server.go:182` and the
  `created by … handleToolsCall` frame. Pasting it into `task.md` would have been
  defensible as authentic triage evidence, but it collapses the case to
  copy-the-line-number and destroys the localization work that is most of the
  difficulty here. `task.md` therefore states the symptom and the expectation and
  stops. Nothing false was added — the omission is subtractive only.
- Relatedly, `task.md` does **not** mention that the crash is specific to the
  progress-streaming path. The reference fix's whole subtlety is *which* of the
  two `handler(...)` call sites is fatal; naming the transport hands that over.
  An earlier draft carried a constraint ("a caller must not be able to reach the
  fatal path by choosing different request options") that implied path-dependence
  and was cut for the same reason.

The trigger was a review finding, i.e. the real-world work item almost certainly
*did* name the location. `task.md` is therefore a reframing, not a transcript —
it reads as the defect report an operator would have filed for the same symptom.
That is the intended `small-fix` shape; a future `code-review`-workflow case
should be built from a review round where the finding text itself survives.

## Open questions

- **Is a crash-shaped `fail_to_pass` acceptable to the harness?** At pre_state
  the test binary dies (`panic: tool exploded`) rather than reporting a failed
  spec, so Ginkgo emits no summary and per-spec parsing yields nothing. Exit-code
  grading works fine; anything that parses spec counts will misread this case.
  Flagged in `case.yaml#grading`. If the harness later standardizes on parsed
  results, this case needs a wrapper that treats a non-zero exit with no report
  as "fail".
- **Difficulty is localization-bound, not insight-bound.** `recover()` in a
  spawned goroutine is one of the most idiomatic patterns in public Go, so
  `memorization_risk: none` (true — this repo is private and post-cutoff) should
  not be read as "the remedy is unfamiliar". Graded `moderate`; if pilot runs
  show near-100% pass, re-grade to `trivial` rather than reworking the task.
- **A generous fix scores the same as the reference.** Wrapping handlers at
  `AddTool` covers both call sites and passes the same mechanical gate. The
  rubric explicitly allows it. Worth watching whether agents produce the broader
  fix — that would be a mild positive signal the mechanical rubric cannot see.
- Sibling candidate not built: the parent commit (`33d699c`, dev-mcp SIGTERM
  drain) is the same shape from the same review pass and would make a second
  cheap ci-agent case. Left for a later pass; noted so it is not re-mined blind.

## Extractor pre-check (informational — not the formal validation pass)

Run by the extractor at build time to confirm the case is real before sealing
it. It is superseded by the formal `## Validation` pass below, which is what
`case.yaml#validation` (`status: validated`) cites.

Method: `git archive <sha> ci-agent dev-mcp.yml | tar -x` into three throwaway
trees (no checkout, no worktree — the repo was treated as read-only), Go 1.25.6
darwin/arm64, module cache warm, no network needed.

| Tree | Contents | Command | Result |
|---|---|---|---|
| `pre` | pre_state as-is | `go test ./devmcp/ ./runner/... -count=1` | **PASS** (baseline green) |
| `pre_with_test` | pre_state + withheld `server_test.go` | `go test ./devmcp/ -count=1` | **FAIL**, exit 1 — `panic: tool exploded`, stack shows `handleToolsCall.func2` at `server.go:183`, `created by … handleToolsCall` |
| `post` | terminal artifact as-is | `go test ./devmcp/ -count=1` | **PASS**, 37/37 specs, 0.378s |

Fail-to-pass and pass-to-pass both confirmed; the defect is real, the ground
truth is real, and the case is non-trivial to satisfy.

Environment gotcha found while doing this: extracting only the `ci-agent/`
subtree makes `devmcp/repoconfig_test.go` fail (`read config: open
../../dev-mcp.yml: no such file or directory`) — that spec reads the repo-root
`dev-mcp.yml`. The whole repository must be materialized. Recorded in
`case.yaml#grading.environment.notes` so it is not mistaken for a real failure.

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `33d699c0d21b1e61dd0ecbd21d5e000030f2dd7a`, post `a1af1068c9cc1b231315e676b80f92be7277b31c`
- outcome: **validated**

### fail_to_pass
`git show a1af1068c9cc1b231315e676b80f92be7277b31c:ci-agent/devmcp/server_test.go > ci-agent/devmcp/server_test.go && cd ci-agent && go test ./devmcp/ -count=1`

PRE (FAIL, exit 1 — hard process panic, no Ginkgo summary, as documented):
```
Will run 37 of 37 specs
•••••••panic: tool exploded

goroutine 77 [running]:
github.com/concourse/ci-agent/devmcp_test.init.func3.8.1(...)
	.../ci-agent/devmcp/server_test.go:117 +0x2c
github.com/concourse/ci-agent/devmcp.(*Server).handleToolsCall.func2()
	.../ci-agent/devmcp/server.go:183 +0xd8
FAIL	github.com/concourse/ci-agent/devmcp	0.338s
```

POST (PASS, exit 0):
```
ok  	github.com/concourse/ci-agent/devmcp	0.374s
```

### pass_to_pass
`cd ci-agent && go test ./devmcp/ ./runner/... -count=1` (no overlay; pre tree restored to its own server_test.go first)

PRE (exit 0):
```
ok  	github.com/concourse/ci-agent/devmcp	0.371s
ok  	github.com/concourse/ci-agent/runner	3.517s
```
POST (exit 0):
```
ok  	github.com/concourse/ci-agent/devmcp	0.370s
ok  	github.com/concourse/ci-agent/runner	3.490s
```

- corrected_cmd: none — both commands ran verbatim.
- notes: no Postgres, no network; ~4s total per leg with a warm module cache. Grade the f2p leg on exit code (the panic aborts the suite before Ginkgo prints counts).

## Fixup 2026-07-25

Curator-fixup pass over the dual leakage audit. Both auditors voted `pass`;
their shared flag was dissolved by contract, and the pass turned up two real
grading defects that the leakage lens had not been looking for.

### Dissolved by the exposure contract (no edit)

- **Both auditors:** "`case.yaml` title / `focus_spec` / `curation.learnings`
  state the remedy." Per `schema/benchmark-case-v1.md` § "The exposure contract",
  the solver sees `pre_state` − `withheld` + `task/` and nothing else;
  `case.yaml`, `notes.md`, `ground_truth/` and the case id/path are harness-side.
  Case titles and grading configs may state the answer freely. **Nothing renamed
  or retitled.** The residual obligation is on the runner, not the case: a
  hand-run must materialize `task/` into a neutrally-named directory and must not
  hand the solver `case.yaml`. Auditor wording ("the harness must ship task/ +
  pre_state only") already describes exactly this and needs no change.
- Both auditors independently confirmed no spoiler vocabulary reached `task.md`
  (no panic/recover/goroutine/SSE-locality language), so the dissolution is
  clean: the leak was never in the exposed set.

### Real defects fixed

1. **Grading overlay clobbers a task deliverable.** `task.md` constraint 4 asks
   the agent to add a regression spec to the existing `devmcp` suite; the
   restore step overwrites `ci-agent/devmcp/server_test.go` wholesale, so the
   agent's spec was destroyed before it could be scored, and rubric item 6 was
   ungradeable in practice. `case.yaml#grading` now carries CAVEAT 1: capture the
   agent's file (and note new `*_test.go` files) before restoring, and score item
   6 from that capture. `ground_truth/rubric.md` gained a
   § "Mechanical caveats" telling the scorer how — including the sharper check
   (apply the agent's spec to the pre_state tree; a real regression spec crashes
   there, one that passes pins nothing).
2. **False-fail hazard from a surviving agent test file.** If the agent writes
   its spec in a *new* file in package `devmcp_test`, the overlay does not
   replace it and a redeclared helper becomes a compile error — a correct fix
   scored as a fail. Both `case.yaml#grading` and the rubric now instruct the
   grader to move agent-added test files aside for the `fail_to_pass` leg.
3. **`fail_to_pass` pins wording `task.md` leaves open.** The withheld spec
   asserts `ContainSubstring("panicked")`, but `task.md` deliberately never names
   the failure mode — it asks only for a message "diagnosable from the agent
   transcript". A fix emitting `tool handler crashed: %v` satisfies every stated
   requirement and would have been graded incorrect. The withheld spec is **not**
   edited (it is the human artifact the validation run was measured against);
   instead the flexibility moved into the rubric — that substring failing alone,
   with `-32603`, the echoed `id`, the SSE frame and the follow-up `ping` all
   holding, is a pass with a recorded wording deviation — and CAVEAT 2 in
   `case.yaml#grading` points the mechanical grader at it.
4. **`pass_to_pass` semantics clarified.** That leg runs on the agent's tree and
   therefore executes the agent's own spec; the comment now says that a red run
   there may be a rubric item-6 judgment rather than a regression, and the score
   must say which.
5. **Internal inconsistency in this file.** `notes.md` carried two `## Validation`
   headings (ambiguous target for `case.yaml`'s `notes.md#validation`), an empty
   "Formal validation" stub the appended pass had already superseded, and a stale
   sentence claiming `case.yaml` still records `validation.status: unvalidated`
   (it records `validated`). The extractor pre-check is now its own
   `## Extractor pre-check` section and `## Validation` is unique and
   authoritative.

Manifest date check: `information_cut` `2026-07-11T13:03:39-07:00` is exactly the
`pre_state` commit timestamp, the terminal artifact lands 66s later, and
`task.md`'s `Reported: 2026-07-11` sits inside that day — consistent, no reframing
needed.

### Priced deflator kept

`docs/superpowers/plans/agentic-platform/04-dev-mcp.md` stays exposed
(`withheld: []` unchanged). It is authentic pre-state design history, `task.md`
legitimately cites its § "JSON-RPC error codes" for the taxonomy the fix must
honor, and its reference listing of the server embeds the *unfixed* `go func()`
call site — unmarked as a defect, and no more informative than `server.go`
sitting in the same tree. Verified again at pre_state: `-iE "recover|panic"` over
the doc returns only the taxonomy sentence, the `toolResponse` marshal-failure
line and an unrelated `panic("runtime.Caller failed")`. Per the deflator rule the
rubric now carries § "Judging the reasoning, not the citation": credit localizing
the spawned-goroutine invocation from code and symptom, not quoting the taxonomy.

Residual, priced, deliberately not edited: `task.md`'s happy-path constraint
names "the progress-notification stream and its heartbeat cadence" as things not
to break. It is the nearest thing to a locality hint in the exposed set, but it
states a regression boundary (mirroring `pass_to_pass`), never ties the crash to
that path, and removing it would weaken an authentic constraint. Recorded here so
a later reader does not mistake it for an oversight against the
"withheld the SSE locality" note above.

### Difficulty

Unchanged at `moderate`, and now on the record as a decision rather than a
default. Sonnet's phrase "honestly hard" reads as an endorsement of
non-triviality, not a vote for the `hard` bucket: the change is 8 lines in one
file with a single hypothesis to test, and `hard` is reserved for multi-file or
multi-hypothesis work. It is also not `trivial` — the pre_state tree contains two
`handler(...)` call sites and only one is fatal, and `task.md` withholds both the
stack trace and the transport. The existing standing rule still applies: if pilot
runs pass near-100%, re-grade to `trivial` rather than reworking the task.

### Leak channels

Checked the operator environment on this machine: the project auto-memory
(`~/.claude/projects/-Users-tdmtrader-concourse-concourse/memory/`, including
`project_bench_corpus_v0.md`, which does discuss the corpus build) contains no
statement of this case's answer — no dev-mcp panic/recover finding, no reference
to `a1af106`/`33d699c`, no `-32603` handler discussion. `known_leak_channels` is
therefore **not** set for this case. The general README rule still holds: replay
harnesses must not mount project memory or session history.
