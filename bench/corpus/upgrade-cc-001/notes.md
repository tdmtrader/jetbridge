# Curation record — upgrade-cc-001

## Provenance walk

Backed out of a merged upstream Concourse pull request. All SHAs resolved in
this repository (the fork carries `upstream/master`); every claim below was
checked with read-only git commands.

| Role | SHA | Committer date | Subject |
|---|---|---|---|
| terminal artifact | `85aa5482acca6fabd1e55841e4e36a6c868dab74` | 2025-08-26T12:52:02-04:00 | `Merge pull request #9261 from concourse/go-jose-v4` (body: *Upgrade go-jose from v3 to v4*) |
| pre_state (first parent) | `f46b5e57b3ce90e7a270115b5be6fad6c87258c6` | 2025-08-26T12:52:01-04:00 | `Merge pull request #9060 from concourse/renovate/all` (body: *fix(deps): update all dependencies*) |
| PR head (second parent) | `9375bef01c4fb57783a63b99dee01cac120664ca` | 2025-08-26T12:52:02-04:00 | `make testflight test for idtoken stricter` |

PR commit chain (`f46b5e5..9375bef`, oldest first):

| SHA | Subject |
|---|---|
| `e63361398cf3d01e2914de75c9ee6435a746478e` | bump codebase to go-jose/v4 |
| `1aff88d38389a7be28816bc22774ad32dadf238a` | update dbfakes to use go-jose/v4 |
| `436e1dd3a7a0b7961da9fc29e8a09ea1786f08ff` | add tests for seq_generator |
| `9375bef01c4fb57783a63b99dee01cac120664ca` | make testflight test for idtoken stricter |

Verification performed:

- `git show -s` on the merge confirms both parents are exactly as the candidate
  claimed, and that the merge body is the PR title only — the informative
  (leaky) prose lives in `e6336139`'s body, not in the merge.
- `git branch -a --contains 85aa5482ac` lists `upstream/master` plus
  `release/8.0.x` and `release/8.1.x`. Merged, and shipped.
  `ground_truth.outcome: merged`.
- `git diff --stat` pre..post: **93 files, +99 / −1472**. Name-status confirms
  the composition: 24 pure import-path edits, 39 `atc/db/dbfakes` files, `go.mod`,
  `go.sum`, one new file (`atc/util/seq_generator_test.go`).
- `git grep -ln go-jose/v3 f46b5e57b3 -- '*.go'` → **28 files** at pre_state, 0 at
  post. `git grep -ln go-jose/v4 f46b5e57b3 -- '*.go'` → **0** at pre_state, i.e.
  the v4 requirement in `go.mod` had no importer. No `vendor/` directory exists
  at either end.
- Read the pre_state and post_state of every load-bearing file. Pre-state
  coherence confirmed by building it (below): the tree is green, `go-jose/v3
  v3.0.4` and `go-jose/v4 v4.1.2` are both direct requirements, and
  `atc/creds/idtoken.DefaultAlgorithm = jose.RS256` already exists.
- `git ls-tree f46b5e57b3 -- bench` is empty (this is an upstream tree that
  predates the fork's `bench/` entirely), so the self-hosted-corpus caveat in
  the schema is satisfied.

**Where the candidate's account was wrong.** The mining summary named one API
break (`jwt.ParseSigned` gaining a required algorithm allow-list). There are
**two**. `jwt.Builder.CompactSerialize` was renamed to `Serialize` in v4, and
`atc/creds/idtoken/token_generator.go` is the only caller. This was not visible
in the summary and was found by materializing the pre_state and applying the
naive rewrite (see Validation). The second break matters for the case: it is the
one an agent working package-by-package from `skymarshal/token` outward will hit
last.

**Committer-date artifact.** All four PR commits and both merge parents carry
committer dates within one second of each other — the branch was rebased onto
master at merge time. The *author* date of the first PR commit is 2025-08-20, so
the work really spans six days. `information_cut` uses the pre_state's committer
date (2025-08-26T12:52:01-04:00) because that is the instant the exposed tree
actually corresponds to.

## Scope decisions for `reference.diff`

`reference.diff` is `git diff pre post` restricted to 29 files. Deliberately
excluded:

- **`go.sum`** (−93 lines) — mechanically regenerated, no information.
- **`atc/util/seq_generator_test.go`** (+44, new) — an unrelated drive-by. The
  PR author noticed an empty suite for `atc/util` and filled it in; it has
  nothing to do with go-jose. Scoped out of the task and out of the reference,
  and `rubric.md` tells the judge not to reward or penalise it.
- **37 of the 39 `atc/db/dbfakes` files** — these changed *only* because the
  regenerating counterfeiter was a newer version that no longer takes every
  per-method lock inside `Invocations()`. Verified by reading
  `fake_worker.go`'s diff (−60 lines, all `fake.xMutex.RLock()` pairs) and by
  `git grep`: exactly **two** fakes reference go-jose
  (`fake_signing_key.go`, `fake_signing_key_factory.go`), and those two are
  included.

`go.mod` is included whole even though only the `go-jose/v3` line is
load-bearing; the `go 1.24.2` + `toolchain go1.25.0` → `go 1.25.0` collapse and
the two dropped indirect requirements are `go mod tidy` artifacts. `rubric.md`
says to accept but not require them.

## Leakage analysis

`withheld: []` — nothing present at pre_state gives the answer away.

Checks run against the pre_state tree (all `git grep <pat> f46b5e57b3`, no
checkout):

- `go-jose` outside `*.go` and `go.sum`: **two hits, both in `go.mod`** (the v3
  and v4 require lines). No design doc, no ADR, no CI config mentions the
  migration. Upstream Concourse keeps no in-tree plans, so the "in-tree plan
  describes the fix" failure mode that bites jetbridge cases cannot occur here.
- `SignatureAlgorithm` outside `*.go`: zero hits.
- `.github/` contains `renovate.json` but no pinned-upgrade note naming go-jose.

**What I scrubbed, and why.** The real trigger for this work was the PR author's
own decision; the nearest thing to a work item is the body of commit
`e6336139`, which reads:

> *v3 is only getting security releases and v5 is already another planned
> release. Best to not be two major versions behind.* / *Found an unused parse()
> function, removed that.* / *Dex doesn't expose via a package the alg they use
> for their tokens. They use go-jose as well. Went through their codebase and
> figured out they use RSA256.*

Paragraph 1 is **rationale** and I kept it, paraphrased, in `task.md` — that is
what an upgrade request legitimately carries and withholding it would make the
task read as unmotivated. Paragraphs 2 and 3 are **the answer** (the dead helper
by name; the algorithm, the fact that Dex is the issuer, and the research method)
and are withheld entirely. `task.md` never mentions `ParseSigned`, an algorithm
allow-list, RS256, Dex, `CompactSerialize`, or `parse()`.

Two judgement calls worth recording:

- `task.md` says "expect the compiler to reject some call sites that were fine
  under v3". This is generic — it is what any major-version bump means and is
  the first thing the module's own release notes say — but it does tell the
  agent the migration is not a pure rename. I kept it because an upgrade request
  that omitted it would be unrealistically terse, and because the compiler will
  say the same thing within one build. It does not localise anything.
- An earlier draft of the constraints read "do not loosen any verification step
  in order to make the compiler happy". That is the exact wrong-answer trap
  stated out loud, so it was cut. The surviving constraint is the symmetric
  invariant ("tokens that verified before must still verify; tokens that were
  rejected before must still be rejected"), which states the property a grader
  checks without pointing at the mechanism.

**Not a leak, but worth knowing:** `go.mod` at pre_state already requires
`github.com/go-jose/go-jose/v4 v4.1.2` as a *direct* dependency even though no
file imports it (it arrived with the idtoken feature and renovate has been
bumping it). So the agent does not have to choose a target version. This is
genuinely part of the pre_state, it is stated openly in `task.md`, and it
removes a decision that would otherwise have been part of the task. Anyone
comparing this case against an upgrade case where the version *is* a choice
should account for the difference.

**Grading tests.** None are withheld. Every command in the gate exercises tests
that already exist at pre_state and pass there. `withheld_test_paths: []` is
correct and is a structural property of upgrade cases, not an oversight.

**Memorization.** `high`, and worse than the usual upstream case. This is a
public PR from August 2025, comfortably inside the training window, and the
load-bearing answer is a single token (`RS256`) attached to a memorable phrase
("Dex uses RS256"). A model that has seen concourse/concourse#9261 can produce a
correct patch without reasoning. Per `bench/README.md` this case must never
anchor an efficacy claim on its own.

## Open questions

- **Is a goal assertion a legitimate `fail_to_pass`?** It is not a test that
  went from red to green; it is a predicate over the tree that was false before
  and true after. Mechanically it behaves identically (exit 1 at pre_state,
  exit 0 at post), and for upgrade workflows there is no alternative short of
  writing a synthetic test. Flagged so the harness authors decide deliberately
  rather than discovering it. If the harness ever wants a *pure* test-transition
  signal for upgrades, the honest answer is that upgrades do not have one.
- **The mechanical rubric is blind to the only interesting decision.** Verified,
  not assumed: replacing `jose.RS256` with `jose.ES256` in
  `skymarshal/token/access_token.go` at the terminal artifact leaves
  `go test ./skymarshal/token/` fully green, because `claimsParserNoVerify` is
  never exercised — the suite drives a counterfeiter fake of the `ClaimsParser`
  interface. So `rubric: mechanical` overstates what the gate proves, and
  `grading.judge_overlay` is load-bearing rather than decorative. Should the
  schema grow a `mechanical+judge` rubric value? Right now the single-enum
  `rubric:` field cannot express this case honestly.
- **Should a case ship a regression test the humans never wrote?** The obvious
  one here is a spec that runs `claimsParserNoVerify.ParseClaims` against a real
  RS256-signed token and asserts an ES256-signed token is rejected. It would
  turn the central decision into a mechanical signal. It would also be synthetic
  ground truth (`bench/README.md`: "a supplement for smoke tests, never a
  substitute") and would arguably change what the case measures from "did you
  research Dex" to "did you make our test pass". Left undone; flagged as a
  corpus-policy question, because several upgrade candidates will have this
  shape.
- **Port types do not exist yet.** `upgrade-request/v1` and `upgrade-report/v1`
  are named in `signature:` on the curator's instruction, but neither appears
  anywhere in the platform source (`grep` over `agent/`, `atc/`: zero hits;
  `repository/v1` and `work-item/v1` are equally aspirational and are already
  used this way by `fix-jb-001`). Recorded as a platform gap: the harvest
  adapter will need these declared before any upgrade case can import.
- **`fly/integration` is not a usable run-gate.** While checking the wrong-alg
  variant, `go test ./fly/integration/` failed on
  `logout Command … try to logout from all targets, but one fails`
  (expected `/test2/sky/logout`, got `/test1/sky/logout`) — an order-dependent
  flake with no relation to go-jose. The gate compiles that package and does not
  run it. Anyone widening the gate should re-check this first.
- **Sibling candidate not built:** the same PR's `atc/util/seq_generator_test.go`
  is a self-contained "write tests for an untested package" task with a real
  terminal artifact. Different workflow shape (test authoring), cheap to
  validate, no Postgres. Noted so it is not re-mined blind.

## Validation

### Extractor pre-check (informational — not the formal validation pass)

Run by the extractor at build time, before the formal pass below. `case.yaml`
now records `validation.status: partial` — the formal pass confirmed the
discriminating half and was disk-blocked on the rest.

Method: `git archive <sha> | tar -x` into throwaway trees under the session
scratchpad (no checkout, no worktree — both repos treated as read-only).
go1.25.6 darwin/arm64, network available.

| Tree | Contents | Command | Result |
|---|---|---|---|
| `pre` | pre_state as-is | `go build ./atc/... ./skymarshal/... ./fly/...` | **PASS** (11.9s) |
| `pre` | pre_state as-is | `go test -count=1 ./skymarshal/token/ ./atc/creds/idtoken/ ./atc/api/accessor/` | **PASS** (baseline green, no Postgres) |
| `pre` | pre_state as-is | full composite gate | **FAIL**, exit 1, `GATE-FAIL:v3-imports-remain` |
| `naive` | pre_state + `sed s\|go-jose/go-jose/v3\|…/v4\|g` over all `*.go` + v3 line deleted from `go.mod` | `go build ./atc/... ./skymarshal/... ./fly/...` | **FAIL** — two distinct breaks (below) |
| `post` | terminal artifact as-is | full composite gate | **PASS**, exit 0 |
| `wrongalg` | terminal artifact with `jose.RS256` → `jose.ES256` in `skymarshal/token/access_token.go` | `go test -count=1 ./skymarshal/token/` | **PASS** — the gate does not catch a wrong algorithm |

The naive-rewrite failure, verbatim (first two stanzas from `go build`, the
third from the compile-only `go test -run NONE_COMPILE_ONLY` pass):

```
# github.com/concourse/concourse/skymarshal/token
skymarshal/token/access_token.go:143:32: not enough arguments in call to jwt.ParseSigned
	have (string)
	want (string, []jose.SignatureAlgorithm)
# github.com/concourse/concourse/atc/creds/idtoken
atc/creds/idtoken/token_generator.go:79:72: jwt.Signed(signer).Claims(claims).Claims(customClaims).CompactSerialize undefined
	(type "github.com/go-jose/go-jose/v4/jwt".Builder has no field or method CompactSerialize)
# github.com/concourse/concourse/testflight_test
testflight/idtoken_test.go:43:34: not enough arguments in call to jwt.ParseSigned
testflight/idtoken_test.go:59:34: not enough arguments in call to jwt.ParseSigned
```

Fail-to-pass and pass-to-pass both confirmed; the goal assertion is real; the
lazy solution demonstrably does not compile; and the case's semantic core
demonstrably is not covered by the mechanical gate.

Environment gotchas found while doing this, recorded so they are not mistaken
for case signals:

- `go build ./...` fails at **both** ends on darwin —
  `worker/runtime/runtimefakes` cannot compile because `worker/runtime` is
  Linux-only (containerd). Hence the package subsets in the gate.
- `go vet` is unusable as a gate: `atc/db/listener.go` (context-leak) and
  `atc/exec/artifact_input_step_test.go` (unkeyed struct literal) already report
  at pre_state.
- `go test -run NONE_COMPILE_ONLY ./atc/db/ ./atc/gc/` compiles and links those
  suites **without** Postgres (Ginkgo's `BeforeSuite` never runs when no test
  function matches), which is what makes a Postgres-free gate possible for
  packages whose *test* files import go-jose.
- The host's disk filled during validation (Go build cache had grown to 17 GB
  with < 400 MB free; linker failed with `no space left on device`). Trimmed a
  quarter of the content-addressed build cache to unblock. Worth knowing that
  materializing upstream trees costs several GB of build cache per tree.

### Formal validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `f46b5e57b3ce90e7a270115b5be6fad6c87258c6`, post `85aa5482acca6fabd1e55841e4e36a6c868dab74` (both resolve locally via the `upstream` remote, as documented)
- outcome: **partially validated — environment-blocked (disk) on the compile/test half**
  - grep gate (the discriminating half): **validated** — exit 1 at pre, exit 0 at post
  - `go build ./atc/... ./skymarshal/... ./fly/...`, `go test ./skymarshal/token/ ./atc/creds/idtoken/ ./atc/api/accessor/`, `go test -run NONE_COMPILE_ONLY ...`: **environment-blocked**

### Grep gate — validated
```
if grep -rqF go-jose/go-jose/v3 --include="*.go" . ; then echo GATE-FAIL:v3-imports-remain; exit 1; fi
if grep -qF  go-jose/go-jose/v3 go.mod;             then echo GATE-FAIL:v3-in-go.mod;     exit 1; fi
```
PRE: prints `GATE-FAIL:v3-imports-remain`, exit 1 — exactly the recorded pre-state behaviour. Files still on v3 include `testflight/idtoken_test.go`, `fly/integration/suite_test.go`, `atc/atccmd/command.go`, `atc/db/access_token.go`, `atc/db/access_token_factory_test.go`, `atc/db/signing_key_factory.go`; `go.mod:43: github.com/go-jose/go-jose/v3 v3.0.4`.
POST: no v3 hits in `*.go` or `go.mod`, gate falls through (exit 0). The same files now import v4 and `go.mod:41` pins `github.com/go-jose/go-jose/v4 v4.1.2`.

### Compile/test half — environment-blocked
The host ran out of disk. Even the smallest leg fails at link time:
```
$ go test -count=1 ./skymarshal/token/
# github.com/concourse/concourse/skymarshal/token.test
/opt/homebrew/Cellar/go/1.25.1/libexec/pkg/tool/darwin_arm64/link: mapping output file failed: no space left on device
FAIL	github.com/concourse/concourse/skymarshal/token [build failed]
```
`df` at the time: 227Mi available on /System/Volumes/Data (100% full). This is a host condition, not a case defect — the go-jose/v4 module resolved fine, so network was not the limiter.

### Environment requirement to finish this case
~6-10 GB free disk (cold GOCACHE for atc + skymarshal + fly + the compile-only link of atc/db, atc/gc, fly/integration, testflight) and a warm-or-networked GOMODCACHE for `go-jose/v4` (nothing imports it at pre_state). Go 1.25 toolchain (validated here on go1.25.6 darwin/arm64).

- corrected_cmd: none — the composite command is correct as written; it just needs disk headroom. Note it must be run from the repo root of a materialized tree, and the leading `grep` gate is what carries the pre/post signal.

## Fixup 2026-07-25

Curator-fixup pass over the dual leakage audit (opus: borderline; sonnet: fail).
Every audit item resolved below; four files edited.

### Dissolved by the exposure contract — no action

The entirety of sonnet's FAIL, and opus's "case.yaml must stay curator-only".
Both objections are that `case.yaml` states the answer: the `judge_overlay`
comment names both API breaks and reports that substituting `jose.ES256`
compiles and passes every runnable suite, and `curation.learnings` repeats it.
Per `bench/schema/benchmark-case-v1.md` §"The exposure contract", the solver
sees exactly `pre_state − withheld + task/`; `case.yaml`, `notes.md` and
`ground_truth/` are harness-side and never exposed, and grading configs may
state the answer freely. **Nothing was renamed, retitled, softened or moved out
of `case.yaml`** — that information is what makes the case gradeable, and
deleting it would only make the curation record worse. The one operational
consequence (a hand-run must materialize `task/` into a neutrally-named
directory) is the schema's, not this case's; the case id `upgrade-cc-001`
announces the workflow but not the answer in any event.

Both auditors independently confirmed the exposed surface is clean, which is the
part that matters: `task/task.md` never mentions `ParseSigned`, `Serialize`,
`CompactSerialize`, an algorithm allow-list, RS256/ES256, Dex or the dead
`parse()` helper, and a whole-tree grep at pre_state finds `go-jose` only in
`go.mod`/`go.sum`. `withheld: []` stands. No priced-deflator in-tree doc exists
here (upstream Concourse keeps no in-tree plans), so the keep-vs-withhold
judgement did not arise.

### Real defect fixed — a graded output port with no delivery channel

`signature.outputs` declared `report: upgrade-report/v1` and `rubric.md` item 8
graded "honesty about what was verified", but `task.md` never asked for a report
or said where to put it. A solver could do the whole migration correctly and
lose the item for not guessing that a write-up was wanted, and the port had no
artifact to bind to at harvest time.

- `task/task.md`: new **"What to hand back"** section asking for the change plus
  `UPGRADE-REPORT.md` at the repository root, covering what changed and any
  judgement calls, which commands were actually run and what they reported, and
  what could not be verified here. Wording kept generic reporting hygiene — it
  names no package, no API and no algorithm, so it does not localise anything.
  The "do not change unrelated code" constraint gained a parenthetical so the
  report is not read as out-of-scope churn.
- `ground_truth/rubric.md` item 8 rewritten to grade that file by name, to state
  that a change delivered without it is incomplete, and to warn the judge that
  `reference.diff` contains **no** report (the humans wrote none) so the item
  must be scored on content, never on diff similarity.
- `case.yaml`: comment on the `report` port recording the delivery channel.
- No grading collision: the gate's tree scan is `--include="*.go"` and its second
  clause reads `go.mod`, so a root-level markdown file cannot affect either the
  fail_to_pass or the pass_to_pass result. Re-validation not required.

### Real defect fixed — spurious-pass gate had no stated verdict rule

The gate is verified to pass on a wrong answer (`jose.ES256` in
`skymarshal/token/access_token.go`), and `rubric: mechanical` is the manifest's
headline. The judge overlay was described as required in prose but nothing said
what to do when the two disagree.

- `case.yaml#grading`: added a **VERDICT RULE** — the case verdict is
  `min(mechanical, judge)`; a run that is green on fail_to_pass/pass_to_pass but
  fails rubric item 3 is a FAIL, and a green gate must never be reported as a
  case pass on its own. `rubric: mechanical` names the primary gate, not the
  whole contract (the schema has no `mechanical+judge` value — the open question
  above stands).
- `case.yaml#grading`: added a **NOT PINNED** note — the gate asserts the goal,
  never a file list or a per-call-site shape.
- `ground_truth/rubric.md`: new **"Mechanical caveats the judge must carry"**
  section holding that latitude (any placement satisfying items 1–6, including a
  shared constant rather than literals at each site) plus the necessary-not-
  sufficient rule.

### Memorization priced into the rubric rather than left to the reader

`memorization_risk: high` was declared but only in the manifest, where the judge
never sees it. Added to `rubric.md`: score item 3 on the *derivation* — where the
agent established what the issuer signs with, and why that call site's list is
narrow — and treat a bare correct constant with no account of how it was reached
as a weak pass. This is the same "credit causal reasoning from evidence, not
quotation" discipline the corpus applies to priced-deflator docs, aimed at the
weights channel instead of an in-tree doc.

### Difficulty — reviewed, held at `moderate`

Opus argued weak *evidence*, not lower difficulty. Recorded the reasoning in
`case.yaml` above `difficulty:`: above trivial because the naive
`sed s|v3|v4|` fails to compile in two distinct places across 28 files including
packages that only compile; below hard because the work is compiler-guided once
started and the target version is already pinned in `go.mod`. The one genuinely
hard sub-task is a single judged item, and memorization pushes its *effective*
difficulty down, not up.

### Known leak channels — none added

Grepped this machine's project auto-memory
(`~/.claude/projects/-Users-tdmtrader-concourse-concourse/memory/`) and the
repo's `CLAUDE.md` for `go-jose`, `RS256`, `ES256`, `Dex`, `jose.`: **zero hits**
— the memory files are all jetbridge/K8s-runtime material. This case's answer is
not in the operator environment, so no `known_leak_channels` entry. Its live
leak channel is model weights, already declared as `memorization_risk: high` and
now priced in the rubric.

### Internal-consistency fixes to this record

- The extractor pre-check said "`case.yaml` still records
  `validation.status: unvalidated`"; the manifest records `partial`. Corrected.
- Two sibling `## Validation` headings existed, so `validation.notes:
  notes.md#validation` was ambiguous and the first section's "Formal validation"
  stub contradicted the filled-in second one. Merged: one `## Validation`
  anchor, with the formal pass folded in as `### Formal validation`.
- Dates checked: `information_cut` (2025-08-26T12:52:01-04:00) is exactly the
  pre_state committer date recorded in the provenance table, `task.md` carries no
  dates of its own, and the committer-date/author-date artifact is already
  documented above. No inconsistency to reconcile.

### Residual

`borderline` — and deliberately not `pass`. Exposure is resolved, but two things
keep the case from being strong evidence on its own: `memorization_risk: high` on
a public Aug-2025 PR whose answer is a single token, and a mechanical gate that
is structurally blind to the only judged decision. Both are declared and priced;
neither is fixable by curation. Not `quarantine`: nothing exposed leaks, and the
case remains the corpus's best format-development vehicle for the upgrade
signature. Per `bench/README.md` it must never anchor an efficacy claim alone.
