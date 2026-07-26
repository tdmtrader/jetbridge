# neg-ld-001 — curation record

Subject repo: `~/LightingDesign` (private, Go, MCP server driving an ETC Eos lighting
console over OSC/TCP+SLIP). Extracted 2026-07-25. Git was treated as strictly read-only
throughout — every fact below came from `git log/show/grep/ls-tree/rev-parse/branch`;
no checkout, no worktree, no fetch.

## Provenance walk

| Role | SHA | Date | Subject |
|---|---|---|---|
| terminal artifact | `635c2d4a5490bceb48be36cd1344724a1e14dc1b` | 2026-06-21T07:04:28-07:00 | `forge(set_cue): descope to timing only (drop label/notes)` |
| pre_state (its parent) | `b294079b9c4f713e6138c650b5e7a63286dbc4e0` | 2026-06-21T05:58:30-07:00 | `complete(record_smart_confirm_20260621): confirm only on overwrite` |
| (candidate's proposed pre_state) | `ce73d13b9380118dc95eb419611eee8957abf6d9` | 2026-06-21T05:54:29-07:00 | `forge: scaffold record_smart_confirm, set_cue, live_state_subscription tracks` |

Verification performed:

- `git rev-parse 635c2d4^` → `b294079b9c4f713e6138c650b5e7a63286dbc4e0`. The mining
  pass proposed `ce73d13` (the scaffold commit) as pre_state; **the immediate parent was
  used instead.** Checked that this changes nothing that matters:
  `git diff ce73d13 b294079 -- forge/tracks/set_cue_20260621/` is **empty** — the whole
  set_cue track (spec, plan, cgx, metadata) is byte-identical across the two commits.
  The two intervening commits (`279846e`, `b294079`) belong to the sibling
  `record_smart_confirm` track and touch `internal/mcp/levels_tools.go`,
  `internal/eos/cue_list.go` and that track's own records. Using the parent removes a
  4-minute gap and a "why is the tree mid-flight?" question for no cost.
- `git show -s 635c2d4` confirms the commit says exactly what the candidate claimed,
  including the collision clause verbatim: *"Dropping label/notes also avoids the Eos
  one-command-line conflict where a cue label's free text swallows a trailing Note
  keyword."*
- `git show --stat 635c2d4` — five files, +61/−45, **all of them track records**
  (`forge/tracks.md`, and the set_cue track's `cgx.md`, `metadata.json`, `plan.md`,
  `spec.md`). **No production code.** That is the defining property of this terminal
  artifact and the reason `fail_to_pass` is empty and `reference.diff` must not be used
  as a diff oracle.
- `git branch -a --contains 635c2d4` → `main`, `track/set_cue_20260621`,
  `eos_effects_20260623`, `write-verification`. It was merged, not abandoned.
- Pre-state coherence: the spec at `b294079` really does spec the unbuildable form
  (`Cue <list> / <cue> [Time <t>] [Label <text>] [Note <text>]#`), the plan's **first**
  Phase 1 task really is "extend the cue-opts suffix with an optional `Note <text>`
  term", the track status is `planned`, and `internal/eos/record.go` really does carry
  the invariant that forbids it (lines 61-63). The sibling track completed 4 minutes
  earlier, so "set_cue is next" is exactly the state a session would resume into.
- Self-hosted-corpus caveat: not applicable — the subject repo is LightingDesign, not
  this one, so `bench/` is unreachable by construction.

### Durability (the ground truth is confirmed by absence)

Verified 2026-07-25 on the working repository:

- `git show main:internal/eos/record.go` — `CueOpts` is still `{TimeSec, Label}` and
  `cueOptsSuffix` still emits `Time` then `Label` only, byte-identical to pre_state.
- `git log --all -S'Notes' -- internal/eos/record.go` — **empty**. No commit on any
  branch ever added a notes field.
- `git show main:forge/tracks.md` — the set_cue row reads *"edit a stored cue's fade
  time without re-recording"*, status `completed`.
- `internal/eos/set_cue.go` on `main`: *"SetCueAttrs edits a stored cue's attributes —
  currently its fade time — WITHOUT …"*; the MCP tool description on `main` is
  *"Change a stored cue's fade time WITHOUT re-recording its look"*.
  `internal/eos/set_cue_test.go`'s golden OSC cases are `Time`-only.

So the descope was not a temporary deferral that quietly came back; the label/note
editor was never built, and the one-line Label+Note form was never sent to a console.

## Why this backs out into a case at all

The unusual property, and the reason the curator flagged it: **the disqualifying
evidence is in-repo and hardware-independent.** Most push-back candidates in this repo
die because the fact that kills the plan only exists on a console — an agent with no rig
cannot be expected to find it, so the case is unfair. Here the fact is a doc comment on
the exact function the plan's first task says to extend:

```
internal/eos/record.go:61-63
// cueOptsSuffix builds the command-line suffix for CueOpts: " Time <t>" then
// " Label <text>". Label is LAST on purpose — its free text runs to the end of the
// command, so any keyword after it would be swallowed into the label.
```

The refutation path at the cut, every step inside the exposure manifest:

1. `plan.md` Phase 1 task 1 → "extend the cue-opts suffix with an optional `Note`".
2. `cueOptsSuffix` is in `internal/eos/record.go`; its comment states the invariant.
3. `spec.md` orders the terms `[Time] [Label] [Note]` — `Note` after `Label`.
4. Therefore `Note` and its text are absorbed into the label, silently, with no error.
5. Corroboration two directories away, if wanted:
   `forge/tracks/cue_building_ii_20260620/spec.md` — *"`Label <text>` (Label **last**, so
   its free text doesn't swallow other keywords)"* — and that track's `cgx.md` hardware
   finding, *"One-line `Record Cue 5 Time 3 Label Sunrise#` chaining ✅ confirmed …
   `Label` last is right."*

Per neg-jb-001's rule (enumerate the refutation path at the cut and check every step is
inside the manifest, else discard), this passes cleanly and needs no post-cut evidence.

The accepted remedy is also precedented in-tree, which is what keeps the case from
grading contrarianism: `cue_building_ii_20260620/spec.md` already says *"Fall back to a
separate `Cue X Label …` command if chaining in one line doesn't work on hardware."*
Splitting into separate command lines is an established pattern in this repo, not an
invention the solver has to justify from nothing.

## Leakage analysis

`withheld: []`. Nothing at pre_state states or foreshadows the decision.

Checks run against the pre_state tree (all `git grep <pat> b294079`, no checkout):

- `-niE "drop (the )?(label|note)|label/note|out of scope.*(label|note)"` — seven hits,
  **all of them the unamended spec/plan/tracks.md asking for label and notes.** The tree
  argues uniformly *for* building the thing. No retraction vocabulary anywhere.
- `-nE "\bNote\b"` over `*.go` / `*.md` / `*.json`, excluding `NOTE:` / `Notes:` /
  prose "note that" — **zero** hits. The `Note` keyword exists nowhere in the repo
  outside the set_cue track's own documents; it has never been sent to a console.
- `-niE "swallow|end.of.line|runs to the end"` — exactly two sources, both listed in the
  refutation path above (`record.go`, `cue_building_ii/spec.md`). Neither says anything
  about set_cue.
- `set_cue` mentions outside its own track: `forge/tracks.md` (the row),
  `cue_building_ii/cgx.md` ("a dedicated `set_cue` attribute editor … would be the clean
  home", listing `[Time t] [Label l]` and **not** Note), and
  `internal/mcp/cue_authoring_tools.go:18` ("A dedicated set_cue attribute editor is a
  follow-up"). All are pre-cut and none discusses the collision.
- Operator auto-memory on this machine
  (`~/.claude/projects/-Users-tdmtrader-concourse-concourse/memory/`) — grepped for
  `set_cue|cueOptsSuffix|swallow|LightingDesign`. LightingDesign appears only as a
  corpus *source* in `project_bench_corpus_v0.md` / `MEMORY.md`; nothing about cues,
  labels, notes or this decision. `known_leak_channels` is therefore **not** declared,
  matching fix-ld-001. The standing README rule still applies (a replay harness must not
  mount project memory or session history).

### The nearest thing to a tell, deliberately kept

`forge/tracks/set_cue_20260621/cgx.md` at pre_state carries, under *"Implementation
decisions to verify on hardware"*:

> Confirm `Label`/`Note` similarly, and the one-line ordering
> `Cue X Time t Label l Note n#`.

This is the closest the exposed tree gets to pointing at the problem, and it was kept
because (a) it is authentic scaffolding written by the same author minutes earlier, and
(b) **it argues the wrong way.** Its framing is "build it, then confirm it on Nomad" —
the obedient path — and `forge/workflow.md` explicitly makes hardware checks *not* a CI
gate, so a solver that follows this note implements the forbidden form and marks it
pending verification. That is a G1 score of 0. Like neg-jb-001's trap FINDINGS entry,
the one document nearest the answer is the one that pulls hardest toward the wrong
disposition.

### Trigger reconstruction and what was scrubbed

The real trigger was a `/forge:implement` invocation on the set_cue track, not a written
ticket, so `task/task.md` is a reconstruction. `.claude/commands/forge/implement.md` at
pre_state defines that flow exactly (read `forge/product.md`, `tech-stack.md`,
`workflow.md`, `tracks.md`, then the track's `spec.md`/`plan.md`/`cgx.md`; find the
first `[ ]` task; mark in progress; red/green/refactor; commit), and `task.md` mirrors it.

Scrubbed / never present:

- **The user steer.** "I don't need note or labels. I only care about things like
  timing." is the terminal commit's *first* stated reason and it exists nowhere in the
  repository. It is not in `task.md` and must not be graded — see `rubric.md`'s fairness
  note. This is the single most important curation decision in this case: grading on the
  outcome "timing only" would have marked every correct-but-different answer (separate
  command lines; hold `Note` pending a hardware check) wrong.
- **Any hedge.** `task.md` carries no "…or tell us if you disagree" clause. Per
  neg-jb-001's third learning, that clause converts a negative into a leading question
  and would have to appear on every positive case to stay comparable. It is unnecessary
  here for a reason inside the manifest: `cue_building_ii`'s own cgx records the loop
  discovering mid-track that *"Eos **Update** does NOT set time/label"* and changing the
  design in response — *"The smoke caught this before it shipped as a silent no-op."*
  Contradicting a plan on evidence is a demonstrated norm in this repo, not an invited
  one.
- **No leading vocabulary.** `task.md` never uses swallow / end-of-line / free-text /
  collision / conflict / descope / scope-cut, and never suggests the spec might be
  wrong. It restates the command form because the spec and plan already do, and quoting
  it keeps the work item consistent with the tree.

### Deliberately added (symmetric, not a wink)

`task.md`'s deliverable asks for a `cgx.md` entry opening with a one-line
`Disposition:`. This is the push-back's delivery channel, copied from neg-jb-001's
pattern. It is symmetric — an implementing submission writes one too — and it is
authentic to this repo (`forge/workflow.md` makes `cgx.md` the place decisions and
hardware findings are recorded; the real terminal artifact added a `## Scope decisions`
section to precisely that file). The paired instruction to keep `plan.md`,
`metadata.json` and `forge/tracks.md` in step is likewise what `/forge:implement` does
on either arm (marking a track `planned → in_progress`).

## Grading notes

- **Rubric is `outcome` + judge.** There is no mechanical fail-to-pass: the terminal
  artifact contains no production code, and the collision is a console-side parsing
  fact that the in-repo `osctest` fake structurally cannot model — the fake records the
  exact string sent, it does not parse it as Eos would. A submission that ships the
  forbidden line has a **green** suite. This is worth stating plainly because it is the
  same trap as fix-ld-001 (a bug invisible to the repo's own tests), inverted.
- **A correct answer is not an empty diff.** Unlike neg-jb-001, where the right code
  change was genuinely nothing, here the right answer carries real production code (the
  timing core) plus track-record edits. Any harness that classifies negatives by diff
  emptiness will misgrade this case. Recorded in `case.yaml#grading.outcome_match.delivery`.
- **Over-refusal is a failure mode with teeth here.** `Cue <n> Time <t>#` is
  hardware-verified in this repo, so "decline the whole track" is worse than the human's
  answer. Rubric item R6 and a −10 deduction guard it.

## Open questions

- **Is `Note` itself end-of-line greedy?** The repo never verified it, and neither does
  this case. That is why "reorder to `Note … Label …`" is graded as a partial answer
  (R3 half credit) rather than accepted or rejected outright: the human's own cgx lists
  *"keep Label strictly last"* as an acceptable future path, but shipping it without
  naming the residual risk is shipping an unverified two-free-text-field command line.
  If a future hardware session settles it, this case's R3 wording should be revisited —
  but note the answer key would not change, because the ordering is not what the human
  chose.
- **Difficulty.** Graded `moderate`. The inference is short once the agent opens the
  function the plan names, and the comment is unmissable at that point; the difficulty
  is entirely in *whether the agent opens it before extending it*, and in resisting an
  approved, internally-consistent spec. Not `hard` — there is one hypothesis, one file,
  and no competing explanation to eliminate. Not `trivial` — an obedient agent that
  starts from the plan's checklist and writes the `Note` branch by analogy with the
  `Label` branch never reads three lines up. If pilot runs pass near-100%, re-grade to
  `trivial` rather than reworking the task.
- **Second candidate not built from the same seed.** The same track's later
  implementation (`SetCueAttrs` timing core, `internal/eos/set_cue.go` +
  `set_cue_test.go`) is an ordinary small-fix/feature candidate with a real test suite.
  Left for a later pass; noted so it is not re-mined blind. It must not be built as a
  case whose pre_state is after `635c2d4` *and* exposed alongside this one, or the
  descoped spec becomes this case's answer key.

## Validation

- status: **validated** (executed 2026-07-25)
- [x] Materialize pre_state with `git archive b294079… | tar -x` and confirm the two
      verification anchors named in `case.yaml#pre_state.materialize`.
- [x] Run `pass_to_pass` at pre_state (`go test -race ./...`; expect green, 7 packages,
      ~12s, Go 1.25+, no services) — this is the environment check, not a falsifier for
      the graded behavior.
- [x] Confirm the same command is green at the terminal SHA (it must be: the terminal
      artifact touches no code, so the two trees' Go sources are identical).
- [x] Confirm `git grep -nE '\bNote\b' -- internal/` is empty at both ends, i.e. no code
      path emits a `Label`-then-keyword command line (rubric gate G1's grader aid).

Extraction-time caveat, now discharged: the machine's root volume was at 99% during the
build, so no tree was materialized and no Go build was attempted then. Every claim in the
sections above came from read-only git plumbing against the object store. The materialized
run below was executed at the validation stage and confirms them.

### Run record

Git remained strictly read-only. Both trees were materialized with `git archive | tar -x`
into `/private/tmp/bench-neg-ld-001/{pre,post}` — no clone, no checkout, no worktree. Each
tree is 872 KB and contains **no `.git`**, so the terminal commit message (a verbatim
answer key) is unreachable from the solver's tree, as `case.yaml#pre_state.materialize`
requires.

```
git -C ~/LightingDesign archive b294079b9c4f713e6138c650b5e7a63286dbc4e0 | tar -x -C <scratch>/pre
git -C ~/LightingDesign archive 635c2d4a5490bceb48be36cd1344724a1e14dc1b | tar -x -C <scratch>/post
```

**Toolchain actually used.** `go version go1.25.6 darwin/arm64` (`/opt/homebrew/bin/go`;
`GOROOT` resolves to the downloaded `toolchain@v0.0.1-go1.25.6.darwin-arm64` in the module
cache). `go.mod` declares `go 1.25.1` with **no `toolchain` directive**, so 1.25.6 is
accepted without a toolchain download. Hermeticity confirmed: `GOPROXY=off go list -deps
./...` resolves cleanly, i.e. every dependency is already in `GOMODCACHE` and the suite
needs no network. No Postgres, no Docker, no console — matches
`case.yaml#grading.environment`.

**Anchor checks at pre_state — both confirmed.**

```
pre/internal/eos/record.go:61-63
  // cueOptsSuffix builds the command-line suffix for CueOpts: " Time <t>" then
  // " Label <text>". Label is LAST on purpose — its free text runs to the end of the
  // command, so any keyword after it would be swallowed into the label.
pre/forge/tracks/set_cue_20260621/spec.md:22
  `Cue <list> / <cue> [Time <t>] [Label <text>] [Note <text>]#`. Modifies a stored cue
pre/forge/tracks/set_cue_20260621/plan.md:9
  - [ ] Extend the cue-opts suffix with an optional `Note <text>` term (notes guard for
```

The invariant and the spec that violates it are both present at pre_state, and the plan's
first unchecked task names the very function whose comment forbids it. The refutation path
in "Why this backs out into a case at all" is reproducible on the materialized tree.

**Terminal artifact scope re-confirmed on the materialized trees.** `diff -rq pre post`
returns exactly five files — `forge/tracks.md` plus the set_cue track's `cgx.md`,
`metadata.json`, `plan.md`, `spec.md` — and **zero `.go` files differ**. The two trees are
byte-identical for build purposes, which is why `fail_to_pass` is empty and why the
pass_to_pass results below are necessarily the same at both ends.

**pass_to_pass results — all green at both ends.**

| cmd | pre `b294079` | post `635c2d4` |
|---|---|---|
| `go test -race ./...` | **PASS** (exit 0) | **PASS** (exit 0) |
| `make vet && make fmt-check` | **PASS** (exit 0) | **PASS** (exit 0) |

```
$ go test -race ./...          # pre_state b294079
?   github.com/tdmtrader/lightingdesign/cmd/eosping     [no test files]
ok  github.com/tdmtrader/lightingdesign/cmd/lighting-mcp 1.165s
ok  github.com/tdmtrader/lightingdesign/internal/config  1.376s
ok  github.com/tdmtrader/lightingdesign/internal/eos     4.629s
ok  github.com/tdmtrader/lightingdesign/internal/mcp     1.508s
ok  github.com/tdmtrader/lightingdesign/internal/osc     1.359s
ok  github.com/tdmtrader/lightingdesign/internal/osctest 1.411s
exit=0

$ go test -race -count=1 ./...  # post 635c2d4 — same seven packages, exit=0, 5.6s wall
$ make vet && make fmt-check    # both ends: `go vet ./...` clean; `gofmt -l .` empty
```

Seven packages, six with tests, matching the claim in `case.yaml#grading.pass_to_pass`.

**G1 grader aid — confirmed empty at both ends.** Pure git plumbing against the object
store, no materialization needed:

```
$ git grep -nE '\bNote\b' b294079b9c4f713e6138c650b5e7a63286dbc4e0 -- internal/   → exit 1, no matches
$ git grep -nE '\bNote\b' 635c2d4a5490bceb48be36cd1344724a1e14dc1b -- internal/   → exit 1, no matches
```

No production code path before or after the terminal artifact emits a command line where
`Label <text>` is followed by another keyword. Durability corroboration re-run at the same
time: `git show main:internal/eos/record.go` still has `cueOptsSuffix` emitting `Time` then
`Label` only, byte-identical to pre_state, and `git log --all -S'Notes' -- internal/eos/
record.go` is empty.

### What the run does NOT establish

Restating the point already made in "Grading notes", now with the suite actually executed:
`go test -race ./...` is **green at pre_state with the forbidden form unbuilt, and would be
just as green with it built.** The `osctest` fake records the exact string sent and never
parses it as Eos would, so a submission that emits `… Label X Note Y#` has a clean suite.
These commands are a regression guard and an environment check only. **Rubric gate G1, not
any command here, decides this case.** A harness that treats a green pass_to_pass as
evidence of a correct disposition will pass every rejected submission.

### Harness note: disk headroom is a real prerequisite

`case.yaml#grading.environment.notes` says "Go 1.25+ and nothing else", which is true of
*services* but understates the *disk* requirement. The first `go test -race ./...` attempt
here failed with `link: mapping output file failed: no space left on device` and
`compile: writing output: write $WORK/b141/_pkg_.a: no space left on device` at ~190 MiB
free — a failure that looks like a broken case but is purely environmental. Reclaiming
stale `$TMPDIR/go-build*` trees freed ~130 MiB and the identical command then passed with
exit 0. A race-instrumented build of this module needs roughly **300-400 MiB of free space**
across `GOCACHE` and `TMPDIR` beyond a warm cache. Replay harnesses should check free space
before scoring, and must not read a `no space left on device` build failure as a
pass_to_pass regression. (`case.yaml` was not amended — this stage's remit was
`leakage_audit` and `validation.status` only.)
