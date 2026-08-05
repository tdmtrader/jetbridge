# Node authoring friction — design

Fixes for the issues found while building the `small-fix` node
(`bench/nodes/FIRST-USER-2026-08-04.md`), 2026-08-04.

## Scope correction, before anything else

Four of the reported issues were re-checked against source and the live server
while scoping this design. **Two of them do not exist.** They were reported from
inference and stale memory rather than from running the command, and the design
must start by deleting them rather than building them:

| Reported | Reality |
|---|---|
| "No way to list a node's runs; no cancel" | `fly agent nodes runs\|show-run\|cancel-run` all ship, with matching API routes (`atc/routes.go:396-398`). `fly -t home agent nodes runs small-fix 1` lists all four runs from this session. **Not a gap.** |
| "`agent/experiment` cannot target a node" | `TargetNode` and `Target.NodeParameters` exist, are validated (`agent/experiment/types.go:113-148`), are bound (`agent/workflowrun/binder.go:756`, `experiment_binder.go:53`), and are exercised by `runner_test.go:599`. Only fly's `add-variant` **grammar** cannot express a node target. |
| "Typed inputs are mounted writable (defect)" | Writable is correct and required. See §3. |
| "The docs claim inputs are read-only" | They claim nothing. The claim was my inference from a Task 9 line about the **sidecar's** projections. |

Nothing in this design should be implemented before its premise is re-verified
the same way. The pattern that produced three of these four errors was reasoning
from an adjacent fact instead of running one command.

## Objectives

1. Make `repository-change/v1` authorable without re-implementing a private
   server hash (§2).
2. Make `repository-change/v1` creatable outside a node run (§4).
3. Write down the input-mutability rule that nothing currently states (§3).
4. Write down how to seal a historical `repository/v1` without leaking the
   answer (§5).
5. Let the bench loop A/B node configurations through the experiment machinery
   that already exists, instead of hand-rolled shell loops (§6).
6. Make more than 2 of 13 `small-fix` corpus cases mechanically gradable (§7).

Explicitly out of scope: a second provider adapter (codex). It needs an adapter
implementation plus an agent-runner image change and deserves its own spec.

## 1. Shape

Two waves. Wave 1 changes nothing that runs in the cluster and can land and be
reviewed on its own. Wave 2 is the platform change and carries the single
build/deploy cycle.

| Wave | Items | Deploy |
|---|---|---|
| 1 | §3 D1 docs · §5 D2 docs · §7 H1 corpus + fixgrade · §6 P5 fly variant grammar (conditional, see §6) · correct the findings doc per "Scope correction" | none |
| 2 | §2 P1 derived input facts · §4 P2 declared base | web + agent-runner, then re-run the bench loop |

Wave 1 is first because §7 is the biggest throughput unlock, is completely
independent of the platform changes, and carries a *different* risk class
(editing sealed corpus files). Wave 2's proof run then covers many more cases
than it otherwise could.

Branch: directly on `jetbridge` in the main checkout. The 12 pre-existing
modified files there are unrelated and must not be swept into any commit.

## 2. P1 — `describe_output` returns each declared input's derived facts

**Problem.** `repository-change/v1` requires `body.repository_id`, which the
server derives privately as
`sha256("concourse.repository/v1\n" + object_format + "\n" + join(root_commits,"\n") + "\n")`
(`agent/snapshot/contracts/repository.go:599`). The agent is given
`InputAuthority.Ref` — id, type, digest — and nothing else, so every prompt has
to re-implement the hash in shell. It works and is also a private contract
copied into user-land that will break silently.

**Design.** ATC propagates the sealed snapshot's already-computed
`intrinsic_metadata` into the authority; the builder returns it verbatim.

- `outputbuilder.InputAuthority` gains `IntrinsicMetadata json.RawMessage`.
- `atc/exec/output_builder_authority.go:41` populates it from the snapshot
  manifest ATC already holds. Precedent for reading it server-side:
  `atc/api/agentchildexecutions/sealer.go:86`.
- `outputbuilder.InputDescription` gains the same field, so `describe_output`
  exposes it per declared input.
- The `small-fix` prompt drops the shell hash and reads
  `describe_output → inputs[] → intrinsic_metadata.repository_id`.

**Why not derive it in the builder.** The builder could run the repository
validator over the mounted input. It must not: the mount is writable, so a node
that edited in place would be handed a *derived-from-mutated-tree* value — wrong
in exactly the case that matters, and it would launder the mistake instead of
letting the seal gate surface it. Propagating the sealed value keeps the answer
identical to what the gate compares against.

**Safety.** No new information reaches the agent. `repository_id`, `head_sha`,
`tree_sha` and `root_commits` are all derivable from the mount it already reads.
The authority file has a 1 MiB cap (`maxAuthorityFileBytes`); `root_commits` is
the only unbounded field, so the propagation must be bounded and the cap
asserted in a test.

**Verification.** Unit tests for propagation and for the describe surface; then
the wave-2 live proof — a `small-fix` prompt with no hash recipe seals a
`repository-change/v1`.

## 3. D1 — document input mutability

**Problem.** Nothing states the rule, and the silence let a wrong inference reach
a shipped prompt.

**Design.** Add a section to `docs/operations/reusable-node-definitions.md`
stating both halves:

> Typed inputs are mounted **writable**, and are a per-run copy —
> `<daemonHostPath>/steps/<handle>/<subdir>`, materialized by byte copy with no
> shared inodes. Build in them, run tests in them; nothing you do reaches the
> sealed snapshot or another run.
>
> One exception. An input named as a **subject of a `repository-change/v1`**
> output is re-read from its mount and re-hashed when the record is written
> (`repository_change.go:190`, the only caller of `OpenInput`). Mutating it does
> not corrupt anything — it makes your own record unsealable with `canonical
> digest does not match its authority`. Work in a copy for those.

Add the same note to the `repository-change/v1` port documentation, since that is
where a reader hits the constraint.

**Verification.** Documentation only. The claims are already evidenced in
`bench/nodes/FIRST-USER-2026-08-04.md` §4.

## 4. P2 — `fly agent snapshots create --base`

**Problem.** `repository-change/v1`'s seal gate requires the base repository as a
*declared input*. `fly agent snapshots create` has no way to declare one, so the
type can only be produced as a node output. A reviewed diff with no owning node
has no honest typed home and travels as `opaque/v1` — which is why the
`code-review` node still has an `opaque/v1` change port.

**Design.**

- `fly agent snapshots create` gains a repeatable
  `--base NAME=SNAPSHOT-ID`.
- The create API request gains a corresponding declared-bases map.
- The server builds a `snapshot.ValidationContext` from those bases and passes it
  to `AdmitForSeal`, instead of today's empty context.
- Authorization: the caller must be able to read each referenced snapshot; a base
  the caller cannot read is rejected before any validation runs.

**Non-goal.** This does not let a caller assert an arbitrary base. The validator
still re-derives lineage, compares the base's content digest against its
immutable reference, and rejects a patch that does not apply.

**Verification.** API tests for the authorization and validation-context paths;
fly integration test for the flag; then a live proof creating a
`repository-change/v1` standalone and retargeting `code-review`'s `change` port
to it.

## 5. D2 — document the `repository/v1` pre-state procedure

**Problem.** `repository/v1` requires complete, non-shallow history. For a clone
that means *all refs*, including every descendant of the commit being sealed. For
benchmark pre-states that ships the commit that fixes the bug.

**Design.** Add a "Sealing a historical revision" subsection next to the
`repository/v1` port docs giving the exact procedure — branch at the target SHA,
delete all other branches/tags/remote refs, `reflog expire --expire=now --all`,
`gc --prune=now` — and, load-bearing, the assertion that closes it:

```sh
git cat-file -e <descendant-sha>   # must FAIL
```

State the general rule too: a sealed repository carries everything reachable from
its refs, so pruning is how you control what a consumer can see.

## 6. P5 — express a node target in `fly agent experiments`

**Problem.** The domain, validation, binder and runner all support node
experiments. `fly agent experiments add-variant` parses only
`label=workflow@version` and `label=workflow@version#function-id`, and has no way
to pass `NodeParameters`. So node A/B is hand-rolled shell, which is what this
session did.

**Design.**

- Extend the VARIANT grammar with a node form, `label=node:NAME@VERSION`.
- Add a repeatable `--param K=V`, populating `Target.NodeParameters`. Reject
  `--param` for workflow and function variants, matching
  `types.go:148`.
- Update the command's help text, which currently says "workflow or function".

**Sequencing risk.** The deployed web image predates this session; `TargetNode`
may or may not be in it. **First step is a probe**, not a change: create an
experiment against `home` with a hand-written node-target JSON definition via
`fly agent experiments create`. If the server rejects it, this item moves to
wave 2 and rides the same deploy. The fly change alone is worthless against a
server that will not accept the definition.

**Verification.** fly unit tests for the grammar and the `--param` rejection
rule; then re-run the §7 `small-fix` matrix as a real experiment and compare its
scorecard against this session's hand-collected table.

## 7. H1 — normalize the corpus grading dialects

**Problem.** Withheld specs are declared four different ways across four `fix-*`
cases: mirrored repo-relative under `ground_truth/withheld_tests/`
(fix-jb-001); case-relative under `grading_tests/` with the repository
destination only implied (fix-jb-003); restored by the leg's own `cmd` via
`$CASE_DIR` (fix-jb-004); and declared but shipped nowhere, expected from a
post-cut commit (fix-jb-002). `fixgrade` implements one and refuses the rest, so
2 of 13 `small-fix` cases are gradable.

**Design.** Normalize the sealed `case.yaml` files to one explicit shape:

```yaml
fail_to_pass:
  - cmd: "cd ci-agent && go test ./devmcp/ -count=1"
    withheld_tests:
      - source: ground_truth/withheld_tests/ci-agent/devmcp/server_test.go
        destination: ci-agent/devmcp/server_test.go
```

Both ends explicit, both machine-readable, no implied destinations. Then:

- Survey **all 13** `small-fix` cases first. Four were inspected; the other nine
  may hold further dialects, and the normalization must be designed against the
  full set, not the sample.
- fix-jb-002 needs a withheld spec that was never shipped, extracted from its
  terminal commit into `ground_truth/withheld_tests/`.
- `fixgrade` reads `withheld_tests` and keeps refusing anything it cannot resolve
  explicitly. The refusal path stays; normalization removes the cases that hit
  it, it does not remove the guard.
- Add a corpus test pinning the normalized grading shape across every case,
  mirroring the existing `corpus_test.go` that pins the eight expected-findings
  dialects. Without it the dialects grow back.

**This bumps the corpus version.** Sealed `case.yaml` edits invalidate citations
of `03c0982a88`. Required: record the bump and its rationale in
`bench/corpus/INDEX.md`, and state that results citing the old commit remain
valid for cases whose *content* did not change — the normalization must not alter
any task, pre-state, or ground-truth answer. Any change that alters what a solver
sees is out of scope for this item.

**Risk.** This edits the only copy of a sealed artifact. Mitigation: mechanical
edits only, `git diff` reviewed per case, the pinning test written *before* the
edits, and `fixgrade` re-calibrated afterwards against every case's own
`reference.diff` — a reference fix must still score `pass`.

## 8. Sequencing and definition of done

**Wave 1**

1. Correct `FIRST-USER-2026-08-04.md`: delete the "no run listing / no cancel"
   claim per "Scope correction" above, and narrow the experiment claim to the
   real fly-grammar gap.
2. §3 and §5 docs.
3. §7 survey → pinning test → normalization → fixgrade support → recalibrate.
4. §6 probe against `home`; implement the fly grammar if the server accepts node
   definitions, otherwise defer to wave 2.

Done when: every mechanically-gradable `small-fix` case grades, each one's
`reference.diff` scores `pass`, and `make test-bench-harness` is green.

**Wave 2**

5. §2 P1 with unit tests.
6. §4 P2 with API and fly tests.
7. `make test-unit`, `make test-integration`, `make test-fly-integration`.
8. Build and deploy web + agent-runner through the normal release chain.
9. Live proof: a `small-fix` prompt with the hash recipe **removed** seals a
   `repository-change/v1`; a standalone `repository-change/v1` is created with
   `--base`; the config matrix runs as a real experiment.

Done when: the live proofs pass and the `small-fix` prompt is shorter than it is
today.

## 9. Testing notes

- PostgreSQL must be running for unit and integration tests (`pg_isready`).
- No `--race` on the unit suite; it breaks parallel compilation.
- `bench/` is outside the root module and is skipped by `make test-unit`; the
  harness has its own target, `make test-bench-harness`.
- The K8s suites remain CI-only from this machine — published container ports are
  not reachable from the Mac. Do not treat their absence as a wave-2 blocker.
