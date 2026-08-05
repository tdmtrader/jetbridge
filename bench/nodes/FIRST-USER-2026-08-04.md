# First-user pass 3 — building a `small-fix` node, 2026-08-04

Against `concourse.home` (theborg/cicd), web image
`sha256:8a7630c6…`, agent-runner `sha256:95d7657e…` (Claude Code 2.1.212).
Nine node runs, $14.46 total.

The two nodes that existed (`code-review`, `log-diagnosis`) both emit *document*
outputs. This pass built the untouched shape — **`small-fix`**: take a repository
and a work item, return a patch. It is the only node family in the corpus with 13
mechanically graded cases, so "which configuration works best" gets an exit code
instead of an opinion.

Delivered:

- `bench/nodes/small-fix/` — the node, at v3 after two rounds of correction.
- `bench/harness/cmd/fixgrade` — the mechanical grader (red / keep / green legs).
- `bench/harness/casespec/grading.go` — grading-block parser.
- `bench/nodes/code-review/` — repository port upgraded to the real type.

---

## 1. Codex cannot be a node agent, and that is a platform gap, not a config one

The brief said to use both Claude and Codex. Only one of those is possible today.

`agent/provider` is a real provider seam, and `agent/broker/profile.go:41` already
declares `AdapterCodex` — but the broker resolves *child* executions
(`request_review`, `consult_agent`), `agentBroker.enabled` is `false` on theborg,
and the `agent-runner` image installs exactly one CLI:

```
deploy/agent-runner/Dockerfile:  COPY --from=claude … /usr/local/bin/claude
```

`agent/runner/runner.go:508` falls back to `/usr/local/bin/claude` and there is no
second adapter to select. So a node's `model:` can name a Claude alias and nothing
else. **Provider is not a configurable axis of a node today** — closing that means
an adapter implementation plus an image rebuild, not a YAML change.

Codex was still worth using *locally*: it reviewed the node package against the
contract source and found four real defects (§5). Friction worth recording — it is
not on `PATH`; the working binary is bundled inside the ChatGPT app at
`/Applications/ChatGPT.app/Contents/Resources/codex`.

## 2. `repository/v1` seals now — the biggest unlock since the last pass

The 2026-08-01 pass recorded that `repository/v1` could not seal because the web
image had no `git`, forcing every repository port to `opaque/v1`. That is fixed.
`repository/v1` snapshots now seal with real intrinsic metadata (`head_sha`,
`tree_sha`, `root_commits`, `repository_id`), 220 MB in ~42 s.

That makes the honest `small-fix` signature possible on both sides:
`repository/v1` + `work-item/v1` → **`repository-change/v1`**, with no `opaque/v1`
anywhere. The output type works as a node output specifically because the node
binds `repository` as a declared input, which is exactly the base subject its seal
gate demands. `fly agent snapshots create` still cannot make one standalone — it
has no way to declare a base — so a *reviewed* diff with no owning node still has
no honest typed home and travels as `opaque/v1`.

## 3. Authoring `repository-change/v1` requires a formula the agent is never given

`body.repository_id` must equal what the server derives — and the derivation is
private (`agent/snapshot/contracts/repository.go:599`):

```
sha256("concourse.repository/v1\n" + object_format + "\n" + join(root_commits, "\n") + "\n")
```

The agent gets none of it. `InputAuthority.Ref` carries only id/type/digest, and
`describe_output` returns the schema, not the base's derived facts. Nothing in the
pod can look up the value; the only way through is to re-implement the formula in
the prompt:

```sh
OBJECT_FORMAT="$(git rev-parse --show-object-format=storage)"
REPOSITORY_ID="sha256:$( { printf 'concourse.repository/v1\n%s\n' "$OBJECT_FORMAT"; \
    git rev-list --max-parents=0 HEAD | LC_ALL=C sort; } | sha256sum | cut -d' ' -f1 )"
```

It works — every run produced the right ID first try — but a private identity
derivation copied into user-land prompts is a contract that will silently break.

**Fixed in code, awaiting deploy** (`9225c0baf3`). ATC already had
the value: every sealed snapshot carries `intrinsic_metadata` computed at seal
time. It is now forwarded into the output-builder authority and returned by
`describe_output` per declared input, so a prompt takes `repository_id` rather
than computing it. The value is forwarded verbatim and never re-derived in the
builder — the mount is writable, so deriving it from the tree would hand back a
value computed from a mutated repository, wrong in exactly the case that matters
and laundering the mistake the seal gate exists to catch.

## 4. Typed inputs are writable, which is correct — but one output type still needs a pristine mount

I first wrote this section up as a defect: `atc/exec/agent_step.go:401` builds
every typed input as `runtime.Input{…}` with `ReadOnly` unset, so typed inputs
are mounted writable. **The framing was wrong.** Writable is the design, and it
has to be — an agent that cannot write to a repository cannot build it, run its
tests, or install anything.

Worth recording *why* I got it wrong, because the next person will too. Nothing
in the docs or in the `code-review` / `log-diagnosis` prompts ever claimed
inputs were read-only — I inferred it from the semantic-rebase context doc's
Task 9 line, "the fixed pinned Agent sidecar receives only exact typed ports,
with input projections read-only." That statement is true **of the sidecar**
(`container.go:717` mounts the companion's projections `ReadOnly: true`) and I
generalized it to the main container, where it is false. The docs are not wrong
here; they are *silent*, which is how a wrong inference survived into a shipped
prompt.

The safety property is materialization, not permission, and it holds:

- The step volume is `<artifactDaemonHostPath>/steps/<handle>/<subdir>`
  (`storage_daemonset.go:72`), keyed by the **container handle**, so it is
  per-run. Compare `CacheVolume`, which uses a deliberately *stable*
  `stableCacheKey(jobID, stepName, cachePath)` — that is the shared one; inputs
  are not.
- The daemon materializes with plain `io.Copy`. There is no `os.Link`, reflink or
  clonefile anywhere in `cmd/artifact-daemon/` or the storage backend, so the
  step's tree does not share inodes with the cache.
- Confirmed live: snapshot `23` was the input to four separate runs and its
  digest is unchanged (`sha256:6955fc13…`), `content_state: available`.

**The real rule is narrower and easier to miss.** `agent/snapshot/validator.go`
exposes `OpenInput`, and exactly one contract calls it —
`repository_change.go:190`. That path tars the **live mount**, canonicalizes it,
and hard-fails when the result no longer matches the sealed reference:

```
output builder: input %q canonical digest does not match its authority
```

So:

> Edit inputs freely. The one exception is an input your output names as a
> **subject of a `repository-change/v1`** — that mount is re-hashed at write
> time, so mutating it does not corrupt anything, it makes *your own record
> unsealable*.

For `small-fix` the `repository` input is the base subject, so the copy-then-edit
step in its prompt is genuinely required — just not for the reason I originally
gave. `review/v1`, `diagnosis/v1` and the rest carry only subject type and digest
metadata and never re-open input content, so a reviewer is free to build and test
in `repository/`.

What is actually wrong here is the documentation's **silence**. Nothing states
the rule in either direction, so an inference drawn from an adjacent statement
about the sidecar survived all the way into a shipped prompt — and cost this node
a 220 MB copy justified by the wrong mechanism. A rule nobody writes down gets
guessed at.

## 5. What codex caught that a live run would not have

The first run passed, which proves nothing about the three paths it did not take.
Codex read the contract source and found four defects, all real, all fixed in v3:

| # | Defect | Why a passing run hid it |
|---|---|---|
| 1 | Read-only claim is false (§4) — though the conclusion I drew from it was not | The prompt's copy-then-edit flow never writes to the mount |
| 2 | `sha1` hard-coded in the `repository_id` recipe | This repo is sha1; a sha256 repo would fail seal |
| 2b | `sort` is locale-dependent, contract is bytewise | Hex SHAs happen to sort the same in most locales |
| 3 | Direct `record.json` fallback omits `record_version` and never creates `content/` | The builder path was live, so the fallback never ran |
| 4 | Empty patch rejected (`git apply --check`, no `--allow-empty`); no `--no-ext-diff` | No run produced an empty or externally-diffed patch |

Every one of these is a *latent* failure that only fires on an input shape the
happy path avoids. Reading the contract beat running the node.

## 6. Configuration results

### small-fix

| cfg | ver | model | EARLY_CHANGE | VERIFY_LEVEL | case | turns | cost | verdict |
|---|---|---|---|---|---|---|---|---|
| A | 1 | sonnet | true | test | fix-jb-001 | 38 | $1.17 | **pass** |
| B | 1 | sonnet | false | none | fix-jb-001 | 31 | $0.85 | **pass** |
| C | 1 | sonnet | true | none | fix-jb-001 | 39 | $1.06 | **pass** |
| D | 2 | haiku | true | test | fix-jb-001 | 25 | $0.22 | mech. fail / **adjudicated pass** |
| A′ | 1 | sonnet | true | test | fix-jb-004 | 46 | $1.99 | **pass** |
| A″ | 3 | sonnet | true | test | fix-jb-004 | 56 | $2.30 | **pass** |

**Scaffolding did not decide this node's outcome; model tier did.** Sonnet passed
fix-jb-001 under every configuration, including the bare one with no early write
and no verification. The prompt patterns that rescued `log-diagnosis` on 08-01
(write-early, verify-before-final) bought nothing here, because a fix is
self-checking in a way a diagnosis is not: the agent has a compiler. Note the
shape of the ablation though — B was the *cheapest* run and still passed, so on
this case the scaffolding cost ~25% more turns for no measured gain. That is a
real finding for easy cases and explicitly not one for hard cases: fix-jb-001 is
`moderate` and never discriminated.

**Haiku is the interesting result.** At **1/5 the cost of sonnet** it produced a
functionally correct fix: right error code (`-32603`), right echoed request id,
right SSE error frame, server survives, 36/37 specs green. The single failing
assertion was `ContainSubstring("panicked")` against its message
`"handler crashed: tool exploded"` — wording the task deliberately left open. This
is verbatim the case's own Caveat 2, which says to score it a pass and record the
deviation.

> **A purely mechanical grader would have reported this correct fix as a
> failure.** `fixgrade` prints every case's grading prose alongside its verdict
> precisely so a human sees the caveat that overrides it. Any harness that scores
> this corpus on exit codes alone will under-report cheap models.

### code-review

| ver | configuration | recall | conclusion | writer traces | turns | cost |
|---|---|---|---|---|---|---|
| 12 | `opaque/v1` repository (08-01 pass) | 1/2 | accept | n/a | — | — |
| 13 | `repository/v1` + "you can use git" | **0/2** | accept, 0 findings | 0 of 51 calls | 52 | $2.04 |
| 13 | identical config, repeat | **1/2** | changes-required | 0 of 62 calls | 63 | $2.96 |
| 14 | writer tracing made mandatory | **1/2** | changes-required | 1 of 49 calls | 50 | $1.87 |

**n=1 on a judgment node is worthless.** The same node version, same inputs, same
parameters produced `accept` with zero findings on one run and `changes-required`
with a correct finding on the next. I nearly reported the port-type upgrade as a
regression on the strength of the first run. Any claim about a judgment node's
configuration needs repeats; `small-fix`'s mechanical legs do not have this
problem, which is a strong argument for building mechanical nodes first.

**A soft invitation to use a capability is ignored.** v13 granted a real git
history and suggested `git log -S` / `blame`; across two runs the reviewer used
history *once*, as `git log --oneline`, which lists commits rather than tracing
writes. v14 made it a required deliverable, named the exact commands, and
explicitly ruled out `git log --oneline` — usage went 0 → 1, at the **lowest cost
of the three runs**. Neither found F1, which needs the writer of
`agent_tickets.pipeline_run_id`; three passes across two sessions have now missed
it. Granting a capability and asking for it politely are not the same thing as
requiring it.

## 7. Other friction

- **Sealing a `repository/v1` from a clone leaks the answer.** The type requires
  complete, non-shallow history — and for a clone "complete" means *all refs*,
  including every descendant of the pre-state, i.e. the commit that fixes the bug.
  Every case here needed: branch at the pre-state SHA, delete all other refs and
  tags and remotes, `reflog expire --expire=now --all`, `gc --prune=now`, then
  assert `git cat-file -e <terminal>` **fails**. This should be in the node docs,
  not rediscovered per case.
- **Node run discovery exists; I failed to look for it.** `fly agent nodes runs
  NAME VERSION`, `show-run`, and `cancel-run` all ship, backed by
  `ListAgentNodeRuns` / `GetAgentNodeRun` / `CancelAgentNodeRun`
  (`atc/routes.go:396-398`). I recorded "there is no way to list runs" and "there
  is no cancel" from an eight-week-old note instead of running `--help`. Both
  claims were false. The only real gap is discoverability: `show-run` demands a
  RUN-ID and its error does not mention that `runs` will list them.
- **Node A/B is first-class already; only fly cannot spell it.**
  `experiment.TargetNode` and `Target.NodeParameters` exist, are validated
  (`agent/experiment/types.go:113-148`), bound (`agent/workflowrun/binder.go:756`,
  `experiment_binder.go:53`) and runner-tested (`runner_test.go:599`). The
  hand-rolled shell matrix in §6 was avoidable. What is missing is the fly
  grammar: `add-variant` parses only `label=workflow@version` and
  `label=workflow@version#function-id`, with no way to pass node parameters.
- **Parameters are the right prototyping unit.** Node parameters become
  `AgentStep.Env`, so `--param` re-configures behavior against one immutable
  version. Model, prompt, `max_turns` and `budget_slice_usd` are baked into the
  version and need a re-import. Design behavioral knobs as parameters and you get
  a whole ablation matrix off a single import — configs A/B/C above cost one
  import between them.
- **Snapshot creation is content-addressed.** Re-sealing review-jb-004's
  `change.diff` returned the *existing* snapshot `3` from the 08-01 session rather
  than a new id. Convenient, and a trap for anyone who assumes a fresh id per
  create.
- **The corpus has at least four withheld-test restore dialects** —
  repo-relative mirrored under `ground_truth/withheld_tests/` (fix-jb-001);
  case-relative under `grading_tests/` with the destination only implied
  (fix-jb-003); restored by the leg's own `cmd` via `$CASE_DIR` (fix-jb-004); and
  declared but shipped nowhere, expected from a post-cut commit (fix-jb-002).
  Legs also carry `role: fallback-only` and `destructive: true`. `fixgrade`
  implements one dialect and **refuses by name** on the others rather than
  guessing a destination — guessing does not error, it silently grades a correct
  fix as a miss, which is exactly how `reviewgrade` once scored a correct review
  0/2. Skipped legs are always reported.

## 8. Reproducing

```sh
# inputs (repository snapshots must be pruned to the pre-state first — §7)
fly -t home agent snapshots create --type repository/v1 --from ./pre-state-clone --json
fly -t home agent snapshots create --type work-item/v1  --from ./work-item      --json

fly -t home agent nodes import ./bench/nodes/small-fix
fly -t home agent nodes run small-fix 3 \
  --input repository=23 --input work-item=24 \
  --param EARLY_CHANGE=true --param VERIFY_LEVEL=test \
  --idempotency-key=smallfix-v3-fixjb001 --json

fly -t home agent snapshots download <output-id> --to out.tar && tar -xf out.tar -C out/
go run ./bench/harness/cmd/fixgrade \
  --corpus bench/corpus --case fix-jb-001 \
  --patch out/content/change.patch --source-repo <pre-state-clone>
```

`fixgrade` is calibrated: the case's own `ground_truth/reference.diff` scores
`pass`, and the `red` leg independently proves the case fails at pre-state before
any verdict is issued. A `red` leg that passes invalidates the whole run.

## 9. What to fix next, in order — status as of 2026-08-04

Four of the five are done, in code and merged to `jetbridge`. Items 1 and 4
change server behavior and are **landed but not yet deployed**, so the live
proofs behind them are still outstanding.

1. **Return derived input facts from `describe_output`** (§3) — **landed,
   awaiting deploy.** ATC forwards each declared input's sealed
   `intrinsic_metadata` into the output-builder authority
   (`9225c0baf3`), so `describe_output` hands back
   `repository_id` and the prompt no longer has to reimplement the hash. The
   value is *forwarded*, never re-derived: the mount is writable, so deriving it
   from the tree would return a value computed from a mutated repository —
   wrong in exactly the case that matters.
2. **Document input mutability** (§4) — **done**, together with the
   historical-revision sealing procedure (`30df941554`).
3. **A second provider adapter** (§1) — **still open.** Needs an adapter plus an
   agent-runner image change; deliberately its own spec.
4. **Let `fly agent snapshots create` declare a base** (§2) — **landed, awaiting
   deploy** (`eca08edd50`). The direct-create path built an empty
   validation context, so no contract that reopens an input could ever be
   created there. It now accepts authorized declared bases. This turned out to
   generalize: every record type's `AdmitForSeal` calls
   `RebindSubjectsToExposedInputs`, so this fixes direct-create for *any*
   subject-bearing record type, not only `repository-change/v1`.
5. **Document the pre-state prune procedure** (§7) — **done** (`30df941554`).

Two items were added by the work itself and remain open:

6. **The corpus's four negative cases are still ungradable.** Each pins a
   `pre_state.repository.materialize:` directive requiring a refs-stripped clone
   that `fixgrade` does not implement — and correctly refuses rather than doing a
   plain checkout that would leak the withheld answer key.
7. **Node A/B cannot score itself.** `experiment.Definition` requires an
   `Evaluator` emitting `measurements/v1`, and none exists for `small-fix`.
   Building one was scoped and deliberately deferred pending a broader rethink of
   the experiment framework, which encodes in Go a surface that should be
   config — see §10.

## 10. The experiment framework is code where it should be config

Raised while scoping the evaluator, and worth recording as its own finding.

`fly agent experiments create` accepts "a complete experiment definition JSON
file", which reads as a config surface. It is not one. `Variant.SignatureHash`,
`TargetConfigHash` and `DevValidationProvenanceHash` are *derived* values that
must equal what the server computes, and `Definition.Validate` rejects any
variant whose hash disagrees with the experiment's frozen signature. Teaching
`fly` to add a node variant therefore required it to fetch the node, call
`Compiled.Instantiate(params)`, rebuild the `PublicSignature` and hash it —
reimplementing `agent/workflowrun/binder.go` client-side. The JSON is a format
only code mirroring server internals can produce.

That is the same shape as §3: a private server derivation the client is required
to reproduce but never given. Two independent instances in two subsystems make
it a pattern rather than an oversight.

The contrast is instructive, because the thing that worked in this session is
config-first. A node is `node.yaml` + a prompt, and its parameters become plain
environment variables — so the entire configuration matrix in §6 cost one import
and zero code. Whatever replaces the current experiment surface should aim at
that, not at a JSON document you need a program to write.
