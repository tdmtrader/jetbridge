# Direction: review is a native, sealed, agentic-first object

**Status:** decided 2026-08-07. Supersedes the provider-native pull request track entirely.

**Decision in one sentence:** JetBridge stops binding its review loop to a forge pull request and
models review natively as a pair of sealed snapshots over an immutable candidate — an
agent-authored `review/v1` and a human-authored `review-response/v1` — with forge integration
deferred indefinitely.

---

## 1. What was wrong

The provider-native PR stack was ~37k lines (20.5k src / 17k test) that **could never run**.
`atc/atccmd/agent_publisher.go:83` appends `incompletePRAuthoritySpineError` unconditionally when
pull requests are enabled, so the web node refuses to boot with the feature on; `:157` refuses to
construct the executor; and the sole production call site of the workflow-resource-source
composition (`atc/atccmd/command.go:1865`) passes no monitor policy resolver, so
`NewAgentPRBindingsFactory` is never constructed at all. `NewAgentPRMonitorRunsFactory` had zero
call sites anywhere, including tests.

Two defects proven live during the audit confirmed it had never worked end to end:

- **`GIT_DIR` hijacked `git init`.** `NoRepository: true` points `GIT_DIR` at a nonexistent path,
  and `git init <dir>` honours `GIT_DIR` over its own argument — so the repository landed in the
  ephemeral credential scratch and the fetch failed. Materialization had never once succeeded. The
  stub test emulated `init` with `os.MkdirAll(.git)`, so it was structurally incapable of catching
  it. Fixed in `4a7935b8fe`.
- **Cross-review reply resolution bricked the observer.** A reply posted via
  `POST /pulls/{n}/comments/{id}/replies` is filed by GitHub under a *new* review while its
  `in_reply_to_id` still names a comment in the *previous* one. The monitor failed permanently on
  its own first reply, because the cursor could not advance past it. Every `testdata/` fixture put
  all comments under one review id, so the shape was unreachable from unit tests. Fixed in
  `b0edb71a63`.

But the fixes did not make the shape right. A survey of every commercial agentic platform
(Copilot, Devin, Codex, Jules, Cursor, claude-code-action, PR-Agent, OpenHands) found **five
properties unique to JetBridge**: nobody else mirrors forge review state server-side and diffs it,
nobody projects forge state into CI configuration, nobody uses the resource cursor as the retry
mechanism, nobody seals whole-repository images per review round, and nobody operates below
submitted-review granularity.

The measured cost of the last one: **789.8 MiB sealed per observation**, 394.9 MiB of it pinned
with NULL expiry forever, with base objects a **99.98% subset** of head. Adding the second ref to a
repository that already holds the first costs 0 additional git objects and 404 KiB.

### The root cause

> Every change-centric system separates the IDENTITY of a logical change from the CONTENT of any
> particular version of it, and makes each version an immutable, individually addressable object.
> GitHub does not: a PR's identity is a mutable branch ref plus a mutable head SHA. There is no
> revision noun, so commits, reviews, comments and CI all collapse onto one moving pointer.

Every pathology measured is downstream of that missing noun. Including the impossibility proof
recorded in `6ffd2f1bba`: `respond`'s draft needs base = PR head, the rebased candidate needs
base = base tip, and both gates pin base to literal HEAD — because a snapshot has a HEAD while a
change has a BASE.

## 2. What was considered and rejected

**Adopting an existing forge.** Gerrit solved change/revision identity in 2008 and remains the
best model available (`Change-Id`, patch sets as static refs at `refs/changes/NN/CCCC/P`, the only
published open-source comment-porting algorithm in existence). It was rejected on three findings:
**a Gerrit change is one commit**, so a multi-commit agent run becomes N dependent changes with N
conversations — failing the requirement on our own definition of a change; **robot comments were
removed in 3.13**, deleting the one machine-authorship primitive in the industry; and there is **no
server-side API for posting check results** (the Checks API is a browser plugin API, and the
server-side plugin is deprecated with no branch past `stable-3.11`, EOL 2026-05-15). Review Board's
`base_commit_id` is optional and unvalidated; Radicle's HTTP API is read-only by policy and it has
no server-side review UI; Gitea and Forgejo inherit GitHub's model wholesale.

**Staying on GitHub with a better trigger.** A disposition-triggered design — submitted review as
the unit, three dispositions routing to merge / respond / revise — was worked out in detail and is
genuinely better than what existed. It was displaced, not disproven. It remains the right shape
*if* forge-hosted review ever becomes necessary.

**Azure DevOps.** Mapped in full. It fails in the opposite direction: iterations are a real
revision noun (`sourceRefCommit` / `targetRefCommit` / `commonRefCommit` per revision, plus
iteration-scoped CI status and a native external property bag), but there is **no review object at
all** — no review id, no `submitted_at`, nothing joining N comments to one verdict. The removed
adapter had already discovered this and rebuilt the verdict stream from
`CodeReviewThreadType=VoteUpdate` system threads. Worst of all, comment-only review has **no
completion signal anywhere in the model**.

Two forges, opposite halves, neither complete. That asymmetry is the argument for owning the object.

## 3. The direction

**Review is a native sealed object. The forge is not in the loop.**

The unit is a **Round**: an ordered pair of sealed snapshots over one immutable candidate.

- The agent's half is **`review/v1`**, reused unchanged. Its `subject_shape` already permits any
  primary subject type, unbounded untyped evidence subjects, and an entity-set of findings with
  stable ids, severity, blocking, markdown description, evidence anchors, and an **explicitly open**
  `category` identifier set. It already carries a frozen rule named
  `non-observation-finding-requires-evidence` — the contract has always believed claims need proof.
- The human's half is **`review-response/v1`**, one new *document* type: a typed disposition per
  finding, free-text directives for what no finding covered, and a record of what was never opened.
- Identity of a round is `(review digest, response digest)` over a fixed candidate digest.
  **Content addressing supplies the revision noun for free**, so nothing is minted for it.

### Why the human half is a document, not a record

A human-authored *record* is structurally impossible to upload: uploads build
`NewValidationContext(nil, nil)` and every record requires at least one subject rebound to a
declared exposed input. Document types need no schema document, no `recordSchemaHistories` entry,
no record prototype, no frozen descriptor, and no parity fixtures — the cost is a Go struct, one
validator case, one registry line. Registering a record type is the one irreversible move in this
codebase; it gets bought after the behaviour proves out, not before.

### What this gives that a forge PR structurally cannot

1. **A typed per-finding disposition that is a machine input to the next attempt** — not prose in a
   thread that an agent must re-parse.
2. **The attention receipt.** `examined` / `unexamined`, computed from what the reviewer actually
   opened, sealed into the response and carried forward. GitHub's "viewed" checkbox is ephemeral
   per-user UI state; a reviewer's silence about a file is indistinguishable from scrutiny of it;
   approval is a total verdict over a moving head SHA. Here *"closed with 9 of 14 files never
   opened"* is permanent data, and the next agent is told explicitly that unexamined is not
   approved.
3. **Evidence as a first-class subject.** `opaque/v1` accepts any non-empty tree and
   `SubjectRoleEvidence` is a first-class role, so proof — traces, request/response pairs, test
   output, eventually screenshots — attaches to the review as sealed, content-addressed,
   individually-addressable data rather than a pasted image in a comment.
4. **Replayability.** A round is immutable and content-addressed. What was claimed, what was shown,
   what was adjudicated, and what was ignored are all recoverable forever.

### Ideas borrowed rather than invented

- **Radicle's declarative `resolves`** — do not forward comment anchors heuristically. Pin each
  finding to an immutable commit OID and have the *next* revision declare which findings it
  addressed. This is strictly better than porting when the author is an agent, because the agent
  knows what it fixed. It converts a twenty-year unsolved heuristic into a data field.
- **Gerrit's fallback ladder** (range → file-level → patchset-level → not ported) as the spec for
  degradation, and its practice of shipping counters alongside the algorithm.
- **jj's stable change-id** and **ghstack's base/head/orig ref triple** — held in reserve. Both
  exist to synthesize on GitHub what content addressing gives us natively; neither is needed while
  the forge is out of the loop.

## 4. The blocker underneath

**Attempt 2 never sees attempt 1's code.** `repository_snapshot_id` has exactly two writers —
ticket create (`atc/db/agent_tickets_factory.go:66`) and `Update` (`:237-255`) — and `Update` is
reachable only from the API handler. No run-side path advances it, and dispatch binds
`*current.RepositorySnapshotID` verbatim (`agent/dispatch/dispatch.go:236`). Re-queue after
`needs_review` re-runs against the original base.

This is not a review-design problem. It means any feedback channel built on the current loop is a
*hint* channel: send back "the retry swallows `ErrStaleTransition`" and the agent rewrites the fix
from scratch. **Carry-forward is item zero, and it is smaller than any review object considered.**

Related: `small-fix-v3` — the ticket workflow — emits `change` + `report` and **no `review/v1` at
all**. Its step *named* `review` is a corrector emitting `repository-change/v1`. The findings
machinery exists (`code-review-v3` produces `review/v1`) but the ticket loop has never used it.
There is currently nothing to adjudicate.

## 5. The MVP

Roughly six focused days. Detailed in
[2026-08-07-native-agentic-review-mvp.md](2026-08-07-native-agentic-review-mvp.md) §6.

0. **Carry-forward** — two nullable columns on `agent_tickets`; `resolveTicketPorts` learns two
   optional zero-or-one types. Unknown types already fall through, so no existing workflow changes.
1. **A `critique` step in `small-fix-v3`** emitting `review/v1`, declared **optional** so a
   malformed finding cannot fail the run.
2. **`review-response/v1`** document type.
3. **`POST /agent/tickets/:id/review-response`** — materialize, then one transaction: set columns,
   transition. Materialize-then-transition, never two PUTs.
4. **`GET .../snapshots/:id/record`** — extract `record.json`, 1 MiB cap.
5. **Prompts** — stable zero-padded finding ids, reused verbatim when a finding persists; never
   re-raise a waived finding.
6. **The review screen** — findings with four disposition buttons, changed files with per-file
   patches already in the `files` JSONB, the receipt footer, three exits: **Accept & close**,
   **Send back**, **Re-queue clean**.

The agent cannot trigger itself. Dispatch is human-tier by construction, ticket state has exactly
one writer under optimistic concurrency, `responded_by` derives from the authenticated principal
rather than the body, and no member credential exists inside a pod. A sealed response is inert
data. **Every iteration requires one human click, so the loop terminates by human inaction.**

### Deferred, deliberately

- **Any new record type.** `review/v1`'s open `category` set carries every vocabulary we might want,
  reversibly.
- **Media evidence.** The contract permits it; the pipeline does not. No browser in
  `deploy/agent-runner/Dockerfile`, no per-entry content endpoint, and an unmade XSS/origin decision
  about serving agent-authored bytes from the ATC origin. Three projects, none of which tests the
  bet.
- **Agent-declared coverage.** An unverified producer claim, monotonically gameable by listing more
  paths. The human-side receipt gives the same reader value with nothing to fake.
- **Wiring the new `review/v1` into the publisher's approval evidence.** Do not couple the review
  loop to the merge gate before the review loop is trusted.
- **Forge projection of any kind.**

## 6. How we will know

Run it on ~12 real tickets over 2–3 weeks.

**The tell:** for every send-back, does attempt N+1 carry `resolves:<id>` for each `must-fix`, and
does its changed-file set overlap attempt N's by more than half? Both computable from sealed records
plus `agent_repository_change_projections.files`. Target ≥70% resolved, ≥50% overlap.

**The falsifier worth instrumenting on day one:** whether the reviewer's *first* interaction on the
page is a file expand or a disposition click. If it is consistently the diff, the findings are not
carrying the review and the premise is wrong for changes of this size.

Also fatal: `must-fix` resolution below ~50%; waiver rate above ~60% (the critique step is
generating noise and the loop carries garbage forward at full token cost); or examined-fraction
falling *while* defects reach `main` (the receipt is legitimizing rubber-stamping rather than
exposing it — the one failure the MVP is specifically instrumented to catch).

Any falsifier hit → **do not mint a record type.** The cost of being wrong is one document type,
two columns, one dispatch clause, and one screen — and items 0 and 4 justify themselves regardless.

## 7. What happens to the forge

Removed. Not deprecated, not feature-flagged — deleted, in a separate change, keeping only the
sealed-record read path for `pull-request/v1` and `pull-request-response/v1` so the existing corpus
stays decodable forever.

If a forge returns later it will be as a **projection** — push refs, open a PR for documentation and
for humans outside the loop, write a status — never as the system of record and never as the trigger.
The disposition-triggered design and the Azure DevOps mapping are recorded so that work does not
have to be redone.

---

## Reference

| Document | What it holds |
|---|---|
| [pr-interface-cleanup-audit](../plans/2026-08-05-pr-interface-cleanup-audit.md) | The original audit; what the stack was and what was wrong with it |
| [repository-snapshot-duplication-audit](../plans/2026-08-05-repository-snapshot-duplication-audit.md) | The 789.8 MiB / 99.98%-overlap measurements and the impossibility proof |
| [review-loop-prior-art-and-alternatives](2026-08-06-review-loop-prior-art-and-alternatives.md) | Industry survey and five native designs |
| [oss-review-systems-survey](2026-08-06-oss-review-systems-survey.md) | Gerrit / Review Board / Radicle / GitLab, verified |
| [azure-devops-forge-mapping](2026-08-06-azure-devops-forge-mapping.md) | The full ADO mapping and the provider-neutral interface, if a forge ever returns |
| [native-agentic-review-mvp](2026-08-07-native-agentic-review-mvp.md) | Six designs, six critiques, and the MVP specified |
