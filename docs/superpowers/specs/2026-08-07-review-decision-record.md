# Decision record: the pull request, and why JetBridge builds review in-platform

**Status:** the removal is decided and **executed**, 2026-08-05 → 2026-08-07. The replacement is
designed and **not built**: no MVP item in Part VI has been implemented, and the experiment in §6.2
has not been run.
**Outcome:** the provider-native pull request stack is deleted (−40,973 non-doc lines across 15
commits, on top of the 5,432-line Azure DevOps adapter removed separately in `b32d386908` on
2026-08-05); review is *planned* as a native sealed object; forge integration is deferred
indefinitely.

> **This direction is an untested bet.** Its central assumption — that the humans who must review
> agent output will do so on a JetBridge surface rather than in a forge they already live in — is
> unproven, and no falsifier in §6.2 tests it: all four measure behaviour *inside* the review screen
> and therefore presuppose reviewers are using it. Parts I–III are findings about code and about
> other systems. Parts IV–VI are a design and a plan, and nothing in them has shipped. If reviewers
> will not leave the forge, §3.4 (disposition-triggered review) is the fallback, and it is recorded
> here for that reason.

This is the full record — what was built, what was wrong with it, everything that was considered
as a replacement, why each was rejected, and what is to be built instead. The summary version is
[2026-08-07-review-direction.md](2026-08-07-review-direction.md); this document is the reasoning
behind it.

**A note on code citations.** Most of the code described here no longer exists. References to
deleted code name the commit that removed it, so `git show <sha>` recovers it. References to
surviving code use live paths.

---

## Part I — What was built

### 1.1 The shape of the provider-native PR stack

The goal was reasonable: let an agent open a pull request, watch it for human review, respond to
comments, revise the code, and merge. What was built to do that:

| Component | What it did |
|---|---|
| `agent/pullrequest/` (~12.9k lines at the package root; 18.7k counting the subpackages below) | Provider-neutral core: `Observer`/`Mutator` seam, monitor coordinator, revision executor, trigger arbitration, cursor machinery |
| `agent/pullrequest/github/`, `azuredevops/` | Two forge adapters (GitHub 1,859 src / 1,108 test; Azure DevOps 2,657 / 2,425 — both counted as of the 2026-08-05 audit; the Azure adapter was removed two days before this wave, in `b32d386908`) |
| `agent/pullrequest/resource/` + `cmd/forge-pr-resource/` | A **Concourse resource** — `check`/`in`/`out` — so a PR appeared to the pipeline as a versioned input |
| `atc/db/agent_pr_bindings_factory.go` (1,871 lines) | A **45-column** binding table: cursor, reservation token, active action digest, lifecycle state, approved baseline, attention reason |
| `atc/db/agent_pr_monitor_runs_factory.go` (300 lines) | Monitor-run evidence projection — **zero call sites anywhere, including tests** |
| `agent/pullrequest/pipeline.go` (647 lines) | Rendered **one Concourse pipeline per watched PR**, and rewrote it on every cursor change |
| `agent/functions/prmonitor/`, `pullrequestresponse/` | Workflow functions: materialize both repositories, draft a response, authorize it |
| `agent/workflow/seeds/pr-monitor-v3/` | The workflow tying it together |
| `pull-request/v1`, `pull-request-response/v1` | Sealed record contracts for the observation and the reply |
| `pr_approval` | A variant of `await_snapshot` threaded through `atc/exec` and `agent/workflow/typecheck.go` |
| `atc.PRApprovalIntent` | A wire type on the build plan, with sentinel decode machinery in `agent/workflow/parse.go` |
| `agent/publisher/` PR half | `ModePullRequest`, `pr_actions.go`, `pr_approval.go`, `pr_impact.go`, plus an `AcquirePR`/`CompletePR` persistence layer in `atc/db/agent_publications_factory.go` |

The data flow: a monitor polled the forge, wrote an opaque cursor into a binding row, projected
that cursor into a Concourse pipeline's resource config, and the resource's `check` emitted a
version when the cursor moved. A build consumed that version, materialized two whole repository
snapshots, ran an agent, and wrote back.

### 1.2 It could never run inside the ATC

This is the single most important fact about the whole subsystem, and it was not discovered until
the audit.

- `atc/atccmd/agent_publisher.go` appended `incompletePRAuthoritySpineError` **unconditionally**
  whenever `PullRequestsEnabled` was set — once in `validateAgentPublisher` (so the web node
  refused to boot) and once in the executor constructor (so it refused to construct). Removed in
  `c8cf2ab8d7`.
- **Neither** production call site of the workflow-resource-source composition
  (`atc/atccmd/command.go:1836`, `atc/atccmd/agent_experiments.go:237`) passed a **monitor policy
  resolver**, and `workflow_resource_sources.go:184` gates the reconciler on exactly one being
  supplied — so `NewMonitorPipelineReconciler` and `NewAgentPRBindingsFactory` were never
  constructed **in production**. Both appear in unit tests only.
- `NewAgentPRMonitorRunsFactory` had **zero call sites**, including tests.

Consequence: there were no live bindings, no in-flight PRs, nothing to drain, and no migration
hazard. Roughly 37,000 of the removed lines — the `agent/pullrequest` src and test bodies — had
never executed in production. Every "live cutover" risk raised during design review was, on
inspection, false.

The qualifier matters: the code was not literally inert. It could be exercised **out of process**
through the standalone `cmd/forge-pr-resource` binary, which does not boot the ATC and so never
meets the refusal above. That is how the two defects in §1.3 were found. Nothing inside the web
node could reach any of it.

### 1.3 Two defects proving it had never worked end-to-end

Both found by running against real GitHub rather than fixtures. Both were fixed before the decision
to delete — the fixes are what made it possible to evaluate the design on its merits rather than
on its bugs.

**Defect 1 — `GIT_DIR` hijacked `git init`.** Fixed in `36719c487d`.

The controlled git runner set `NoRepository: true` for commands that must not discover an ambient
repository, which points `GIT_DIR` at a nonexistent path. But `git init <dir>` **honours `GIT_DIR`
over its own directory argument**. So `init` created the repository inside the ephemeral credential
scratch, left the destination empty, and the subsequent fetch failed with "not a git repository."

Materialization had therefore never once succeeded, and production reached it unconditionally —
`cmd/forge-pr-resource/main.go` passed empty `Dependencies`, so the production path always used the
real runner. The unit test could not have caught it: it emulated `init` with
`os.MkdirAll(.git)`, which is structurally incapable of reproducing a `GIT_DIR` override.

**Defect 2 — cross-review reply resolution bricked the observer.** Fixed in `72e7e179c6`.

A reply posted through `POST /pulls/{n}/comments/{id}/replies` is filed by GitHub under a **new
review**, while its `in_reply_to_id` still names the root comment in the **previous** review.
`normalizeReview` filtered comments to a single review and built its root index from only that
subset, so the reply was unresolvable and `Observe` failed — **permanently**, because the cursor
could not advance past the failure.

Observed live on `tdmtrader/jetbridge#3`:

```
poll 1 (human's root review comment):            batches=1 threads=1   OK
poll 2 (advances to the platform's own reply):   FAILED
        github review reply has no root
```

Poll 1 succeeded only because the selector processed one review at a time and happened to pick the
human's. **The monitor bricked itself the first time it spoke.** Every `testdata/` fixture placed
all comments under one `pull_request_review_id`, so the shape was unreachable from unit tests.

### 1.4 The storage measurement

Measured on this repository, not estimated:

| Sealed per observation | Compressed | Retention |
|---|---|---|
| `pull-request/v1` (both repositories) | **394.9 MiB** | **permanent pin, NULL expiry** |
| `repository/v1` source | 197.4 MiB | binding, 7d |
| `repository/v1` target | 197.4 MiB | binding, 7d |
| **total** | **789.8 MiB** | |

The base repository's object set is a **99.98% subset** of the head's. Adding the second ref to a
repository that already holds the head costs **0 additional git objects, 404 KiB, and 1 filesystem
entry**. So 50.1% of transferred bytes and 49.4% of fetch CPU were pure duplication.

Compression cannot bridge it: two byte-identical trees in one stream measured **2.0001×** — margi­nally
*worse* than 2× — because Hangar pins zstd to an 8 MiB window and the copies sit 250 MB apart, and
72.5% of the bytes are already-deflated packfile compressing at ratio 0.975.

Plus a latent failure: `in` validated **each** checkout against the entry and byte limits
independently, while the seal-time canonicalizer applied *those same limits* to the combined
observation tree, which is ~2× either repository. A repository with 50k–100k entries would pass
both per-directory checks and then fail the seal — **after both full fetches had already been
paid for**.

---

## Part II — The diagnosis

### 2.1 The missing noun

The audit's structural conclusion, which every subsequent decision rests on:

> Every change-centric system separates the **identity** of a logical change from the **content**
> of any particular version of it, and makes each version an immutable, individually addressable
> object. GitHub does not: a PR's identity is a mutable branch ref plus a mutable head SHA. There
> is no revision noun, so commits, reviews, comments and CI all collapse onto one moving pointer.

Every pathology measured is downstream of that. Concretely:

- **The double clone exists because a snapshot has a HEAD and a change has a BASE.**
  `repository/v1` is a worktree at one commit. To express "this change, against that base," you
  need two of them.
- **The impossibility proof** (recorded in `c88d9289a0`): `respond`'s draft requires base = PR
  head; the rebased candidate requires base = base tip; and the frozen rule
  `base-sha-must-equal-the-base-repository-head` pins base to literal HEAD in both gates. So
  collapsing the PR step outputs into one directory is not an optimization away — it requires a
  contract revision.
- **The cursor exists because the answer lives somewhere else.** A 45-column mirror, an opaque
  cursor, version-as-log-position, an action digest, and a reservation token are all consequences
  of one decision: putting the review conversation in a system JetBridge does not own.
- **The cursor became the retry mechanism.** Because Concourse consumes a version at build start
  regardless of outcome, a failed build had to be handled by *refusing to advance the cursor* —
  conflating "have I seen this" with "did the work succeed."

### 2.2 What everyone else actually does

A survey of nine agentic coding platforms — GitHub Copilot coding agent, Devin, OpenAI Codex,
Google Jules, Cursor, claude-code-action, Qodo/PR-Agent, OpenHands, Aider — found a recurring shape
and **five properties none of the nine shares with JetBridge**. (Not "every commercial platform":
two of the nine are open-source projects, and the survey does not cover CodeRabbit, Sweep, Greptile
or the cloud vendors' own reviewers.)

The recurring shape: **an explicit imperative addressed to the agent by a write-access human**,
delivered in-forge or by webhook. Transport is not uniform — Copilot is in-forge with no webhook,
claude-code-action and the OpenHands resolver ride GitHub Actions events, Aider has no forge loop at
all, and only Devin is documented as webhook-delivered. Nor is submitted-review batching enforced:
it is *requested of humans* (see the GitHub quote below), while the underlying mechanism is
per-comment.

Nobody polls a forge for review *state*. Polling itself is not unique to us — Qodo/PR-Agent ships a
supported GitHub polling deployment (`pr_agent/servers/github_polling.py`) that walks the
notifications feed with a `since` / `If-Modified-Since` cursor and a handled-id dedupe set. It polls
for **mentions**, not for review state, which is why the narrower state-diff finding survives.

GitHub documents the batching rule and instructs humans to change behaviour to suit the agent:

> "Because Copilot starts looking at comments as soon as they are submitted, if you are likely to
> make multiple comments on a pull request it's best to batch them by clicking **Start a review**…
> triggering Copilot to work on your entire review, rather than working on individual comments
> separately."

The five properties none of the nine surveyed shares:

1. None of them mirrors forge review state server-side and diffs it to derive work.
2. None of them projects forge state into CI configuration.
3. None of them treats the forge cursor as the retry mechanism.
4. None of them seals whole-repository images per review round.
5. None of them operates below submitted-review granularity.

These are negative findings over a nine-item sample, not universals over the industry.

Two findings worth keeping regardless of direction:

- **Author-identity filtering is the field default and is known-wrong at the edges.**
  `anthropics/claude-code-action#1299` documents a PR wedged with a **permanent red required
  check**: human asks `@claude` to fix something → Claude commits as `claude[bot]` → the push fires
  `pull_request` → the review workflow aborts with "Workflow initiated by non-human actor" → the
  check can never go green. GitHub's own mechanism suppresses at the **write**, keyed on *which
  credential wrote*, never at the read keyed on *who wrote this*.
- **Acknowledgement markers beat cursors.** Jules and Copilot independently converged on a 👀
  reaction per comment plus a timeline event — per-item, idempotent, human-visible, and they don't
  burn on build failure the way a consumed Concourse version does.

---

## Part III — Everything considered, and why it was rejected

### 3.1 Adopt an existing open-source forge

The instinct was right — this *is* a solved problem, since 2008. It was rejected on specifics.

**Gerrit** is the best model available and remains the reference design:

- `Change-Id` trailer gives a change identity independent of any commit.
- Each revision is a **patch set** at `refs/changes/NN/CCCC/P`, documented as "a static reference,"
  individually fetchable, naming its own parent.
- **Comment porting** across patch sets is the most thoroughly documented open-source
  anchor-forwarding algorithm available (`CommentPorter.java`, `GitPositionTransformer`, a
  documented fallback ladder, shipped counters). The ported comment *is* the original — resolving
  the ported view resolves the original. It is not the only one: GitLab CE ships
  `Gitlab::Diff::PositionTracer`, whose header comment is "Finds the diff position in the new diff
  that corresponds to the same location specified by the provided position in the old diff."
- Service Users is a built-in group with the right semantics for a bot.
- `webhooks` is core-bundled; `stream-events` exists.

Rejected on three findings:

1. **A Gerrit change is one commit.** A multi-commit agent run becomes N dependent changes with N
   review conversations. This fails the primary requirement using our own definition of a change,
   and no API quality compensates. (Gerrit's own concept documentation: "A change represents a
   single commit under review," with patch sets being successive versions of that one commit.)
2. **Robot comments were removed in 3.13** (disabled by default in 3.12) — the strongest-verified
   of the three, confirmed three ways: the 3.12 release note announcing the default flip, the 3.13
   release note citing the removal changes, and zero occurrences of "robot" in the stable-3.14 REST
   documentation. The one machine-authorship primitive in the industry was deleted.
   `andygrunwald/go-gerrit` still ships the dead types, which is how the folklore survives.
3. **No server-side API for posting check results.** The Checks API is a *browser* TypeScript
   plugin API — results live in your database and the browser fetches them per page load. The
   server-side plugin carries a deprecation notice and has no branch past `stable-3.11`. Gerrit's
   support page lists 3.11 as EOL; the specific date **2026-05-15 is taken from that page and has
   not been independently re-confirmed here.**

Also: `tag` is not present in the webhook or stream-events payload, so self-suppression on Gerrit
is *also* an author-identity filter. Adopting it would not have bought loop suppression.

**Review Board** has the best machine-reviewer API surveyed (atomic draft-then-publish, `ship_it`,
a real `issue_status` state machine, Status Updates linking a check to a specific revision *and* a
review) and refuses to guess at anchors — the anchor is a `filediff_id` inside an immutable
revision, so it can never go stale. Rejected because `base_commit_id` is **optional and
client-supplied** — the API docs say it "may not be provided for all diffs or repository types,
depending on how the diff was uploaded," and the canonical example shows `null` — so "each revision
names its own base" is a promise the caller keeps rather than one the system enforces; it needs the
base reachable from the repo; the *server* cannot merge, land or gate anything (there is no merge
API, and `approved` / `approval_failure` are advisory booleans — landing exists only client-side, in
RBTools' `rbt land`); and there is no Go client.

**Radicle** has the best data model of anything surveyed — `Revision { id, base: Oid, oid, resolves }`
is literally the "a change has a base" complaint expressed as a struct field. Rejected because its
HTTP API is **read-only by policy** (writes are local `rad cob update`), there is no server-side
review UI, and Radicle 2 is announced as the next master with CLI commands explicitly not stable
APIs.

**GitLab CE** is the interesting near-miss: `merge_request_diffs` genuinely record
`base_commit_sha`/`start_commit_sha`/`head_commit_sha` per version, which is closer to a revision
noun than GitHub has. Rejected because change identity is still a mutable branch, the diff *files*
of superseded versions are **deleted at merge** by `DeleteNonLatestDiffsService` (the version rows
and their sha triples survive, but the diff content does not), external status checks are
Ultimate-only, and the operational baseline is 8 vCPU / 16 GB plus Postgres, Redis, Gitaly, Sidekiq,
and Workhorse.

**Forgejo and Gitea** inherit GitHub's model wholesale and add nothing. (The Forgejo *project*
prohibited AI-generated contributions to itself in March 2026 — a signal about the ecosystem's
posture toward this kind of work, not a technical bar to self-hosting the software.)

**The general argument against adopting any of them:** a second forge is a second identity system,
a second backup-and-restore path you have actually tested, a second upgrade treadmill, and a second
set of 3am pages. Gerrit's specific tax — Java 21 mandatory since 3.12, full reindex on that
upgrade, Jetty 9→12 in 3.14, zero-downtime only under the HA or multi-site plugins, and a
Kubernetes on-ramp that is a 38-star repo with a single `v0.1` tag — is a permanent part-time job
in exchange for a data model. And the review UI is a *human* tool: the agent is indifferent to
where it POSTs.

### 3.2 Finish the design that was half-built

Phase 2 of the full-state monitor: full-state observation, delete `ActionFor`, add a server-side
policy, batch by ID, window the threads, plus a `pull-request/v1` contract revision.

**Rejected** because it was 1–2 weeks of work to reach a shape whose every load-bearing property is
unique to us — the mirror, the cursor-as-retry, forge state in pipeline config, a pipeline per PR,
and the double clone all survive. It would have been finished only to reach a shape we *expect*
degrades badly past a few dozen open PRs, at which point every open PR's pipeline is rewritten on
every cursor movement. That ceiling was reasoned about, not measured — the subsystem never ran, so
no scaling number here is an observation.

### 3.3 Keep GitHub, throw away the binding

Two variants were worked out:

- **Mention-gated** — trigger on `@jetbridge` from a write-access human, at submitted-review
  granularity, using GitHub's own per-thread resolved bit as the acknowledgement instead of a
  cursor. This is what Copilot, Devin, Codex, Jules and Cursor all do.
- **Content-hash** — `check` becomes a pure function: fresh read → drop platform-authored nodes →
  sha256 → one version. One instanced pipeline per PR, set once, never rewritten. Stock
  `resource_config_versions` becomes the only "have we handled this" memory.

Both delete ~8–10k non-test lines for ~500 added. **Rejected** because mention-gating inherits the
`#1299` wedge-the-PR failure and requires humans to remember a magic string; content-hashing makes
identity filtering the single load-bearing loop guard, and one write through the wrong credential
silently re-enables echo runs; and both keep a pipeline per PR. Neither can express "revision 3 of
this change, based on X" — the missing noun stays missing.

### 3.4 Disposition-triggered review (the best forge-bound design)

This was the strongest forge-hosted option and it was **displaced, not disproven**. The insight:
trigger on a *completed* review rather than individual comments, and route on its disposition.

| Disposition | Agent action | Self-triggers? |
|---|---|---|
| Approved | rebase if needed, merge | **No, by construction** — writes nothing to the review stream |
| Changes requested | revise and push | **No** — a push does not mint a review |
| Comment only | write textual responses | **Yes** — the only one that does |

That narrows self-triggering from "every action can echo" to exactly one case, and shrinks that one:
make the agent **speak in reviews too** — one `POST /pulls/{n}/reviews` per turn instead of N reply
calls, which collapses N potential echoes into one (and collapses GitHub's 128-comment-per-review
batch limit at the same time). It does **not** eliminate self-triggering: a posted review *is* a
submitted review, and submitted reviews are the trigger unit. Closing it needs the ledger-based
write-side suppression rule in Part VIII, not this batching change.

Three further consequences: the durable state becomes **one integer per PR** (highest review ID
acted on) instead of 45 columns; `ActionFor`'s digest arbitration dies because three dispositions
are three actions; and because the disposition is known at trigger time, **materialization halves
for the common case** — only approve→merge needs both refs, with no contract change.

What was already there and what was missing, verified in the code before deletion:

- `selectReview` already filtered to `SubmittedAt != nil` — submitted-review granularity was
  **already the trigger unit**.
- But `review.State` was decoded and **never read again**, and `PullRequestReviewBatch` had no
  state field. `ActionKind` had one `ActionReviewBatch` for every review regardless of verdict.
  **The disposition was decoded and thrown away.**

It remains the right shape if forge-hosted review ever becomes necessary, and it is recorded here
for that reason.

### 3.5 Azure DevOps

Mapped in full, because it is the second forge that would have to be supported. It fails in the
**opposite direction** from GitHub, which is what finally settled the argument.

**What ADO has that GitHub does not:**

- **A real revision noun.** `GitPullRequestIteration` names its own `sourceRefCommit`,
  `targetRefCommit`, **and `commonRefCommit`** — head, target, merge base. That is exactly the
  triple ghstack must synthesize on GitHub from three parallel refs, served server-side with a
  stable small-integer ordinal. It deletes the ref triple, revision-to-revision diffing
  (`?$compareTo=`), and base-movement tracking (`reason=retarget` with `oldTargetRefName`/
  `newTargetRefName`).
- **Revision-scoped CI status** — `GitPullRequestStatus.iterationId`: "status applies only to the
  code that was evaluated and none of the future updates."
- **Forge-enforced revision-bound approval** via `requireVoteOnEachIteration`.
- **A native external property bag** — `PATCH .../pullRequests/{id}/properties`, documented as
  existing so third parties need not maintain their own storage.
- **Real compare-and-swap on refs** (`oldObjectId` / `staleOldObjectId`).
- **A richer resolution vocabulary** — `active`/`fixed`/`wontFix`/`closed`/`byDesign`/`pending`,
  with a blocking "check for comment resolution" branch policy.
- **A five-valued disposition**, including `5 = approved with suggestions`, which satisfies the
  approval policy *and* carries work — GitHub cannot express it.

**What ADO does not have:** any review object at all. No review id, no `submitted_at`, no draft
staging, no "Submit review" action in the API *or* the web UI. Nothing joins N comments to one
verdict — different resources, different OAuth scopes, different event ids.

Our own deleted adapter had already discovered this and rebuilt the verdict stream by mining system
comment threads for `CodeReviewThreadType == "VoteUpdate"`, reading `CodeReviewVotedByTfId` and
`CodeReviewVoteResult` off the property bag. But it implemented **one** disposition — the transition
into vote `-5` — and `selectLatestIteration` fetched up to 256 iterations, validated them, and
**kept only `max(id)`**. It had the revision noun in hand and discarded it.

**The unportable piece:** comment-only review has **no completion signal anywhere in the model**. A
reviewer who writes eight comments and never votes produces no trigger. The boundary must be
invented — a quiet period, a vote-0 convention, or a marker comment.

Three more that would bite. **Where the `resetOnSourcePush` branch policy is enabled**, the agent's
own landing push wipes the approval it is acting on — the policy is an opt-in checkbox and clears
approvals only, leaving reject and wait votes standing (`resetRejectionsOnSourcePush` clears
everything). Whether a bot **can clear a human's stale vote is unproven, not disproven**: Microsoft
documents `PATCH .../pullRequests/{id}/reviewers` whose stated purpose is "Reset the votes of
multiple reviewers on a pull request," with a body parameter described as "IDs of the reviewers
whose votes will be reset to zero"; the only contrary evidence is `azure-devops-node-api#611`, whose
working repro resets **the caller's own** vote. Treat it as a capability flag defaulting to false —
which is exactly how [the ADO mapping](2026-08-06-azure-devops-forge-mapping.md) grades it — never
as a settled fact in either direction. And there is **no textual diff or patch media type anywhere**
— iteration changes are file-level lists with blob OIDs and no hunks.

**Two forges, opposite halves, neither complete.** That asymmetry is the argument for owning the
object. Any forge-bound design would have been two adapters papering over two different missing
halves, with a leaky abstraction between them and no ability to evolve as either platform changes.

---

## Part IV — The decision

### 4.1 What is to be built

**Review is a native sealed object. The forge is not in the loop.** None of what follows exists
yet; this section is the design, and Part VI is the plan to build it.

The unit is a **Round**: an ordered pair of sealed snapshots over one immutable candidate.

- **The agent's half is `review/v1`**, reused unchanged. Verified: its `subject_shape` declares
  `subject_type: null` and `uniform_subject_type: false`, so **any** type may be the primary
  subject — a review of a `repository-change/v1` is legal today with zero contract changes.
  Evidence subjects are unbounded and untyped. Findings are an entity-set with stable ids,
  severity, blocking, markdown description, evidence anchors, and an **explicitly open** `category`
  identifier set. It already carries a frozen rule named
  `non-observation-finding-requires-evidence` — the contract has always believed claims need proof.
- **The human's half is `review-response/v1`**, one new *document* type: a typed disposition per
  finding (`must-fix` / `waived` / `not-a-defect` / `acknowledged`), free-text directives for what
  no finding covered, and a record of what was never opened.
- **Identity of a round is `(review digest, response digest)` over a fixed candidate digest.**
  Content addressing supplies the revision noun for free, so nothing is minted for it.

### 4.2 Why the human's half must be a document, not a record

The reason is cost and reversibility, not impossibility. An earlier draft of this argument claimed a
human-authored record was *structurally impossible* to upload, because the upload path built
`NewValidationContext(nil, nil)` and every record requires at least one subject rebound to a
declared exposed input. **That is no longer true, and it stopped being true before this decision was
taken.** `eca08edd50` (2026-08-05) gave the direct-create path declared bases:
`agent/snapshot/sealer.go:297` now builds `NewValidationContext(baseInputs, …)` from
`authorizeDeclaredBases`, and its commit message says so explicitly — "an empty context blocked
every subject-bearing record type from direct creation." A human-authored record could be uploaded
today, with the review snapshot itself as the declared base.

It is still the wrong first move. **Registering a record type is the one irreversible act in this
codebase**, and the human half has no vocabulary worth freezing yet. The escape is the pattern the
platform already uses — capture-not-upload, server-materialized.
**Document types need no schema document, no `recordSchemaHistories` entry, no record prototype,
no frozen descriptor, and no parity fixtures.** Verified: `agent/snapshot/contracts/schemas/`
contains zero files for `work-item`, `question`, `human-answer`, `opaque`, or `log-bundle`. The
cost is a Go struct, one validator case, one registry line, and a count assertion.

The irreversibility is the whole argument: descriptor digests are derived from document bytes and
the histories are append-only, so a record type gets bought **after** the behaviour proves out, and
bought knowing the vocabulary the corpus actually produced. Five of the six candidate designs minted
a new record type to test a *behavioural* hypothesis; that is the wrong order.

### 4.3 What this gives that a forge PR structurally cannot

1. **A typed per-finding disposition that is a machine input to the next attempt** — not prose in a
   thread that the next agent must re-parse and re-interpret.
2. **The attention receipt.** `examined` / `unexamined`, computed from what the reviewer actually
   opened, sealed into the response and carried forward. GitHub's "viewed" checkbox is ephemeral
   per-user UI state; a reviewer's silence about a file is indistinguishable from scrutiny of it;
   and approval is a total verdict over a moving head SHA. Here *"closed with 9 of 14 files never
   opened"* is permanent, attributable data, and the next agent is told explicitly that unexamined
   is not approved. **No forge can express this.**
3. **Evidence as a first-class subject.** `opaque/v1` accepts any non-empty tree and
   `SubjectRoleEvidence` is a first-class role, so proof — traces, request/response pairs, test
   output, eventually screenshots — attaches to the review as sealed, content-addressed data rather
   than a pasted image in a comment.
4. **Agent-authored reading order.** The agent that wrote the change can sequence how a human walks
   it. This is the problem GitHub's "layered PRs" is reaching for; here it is a property of the
   object rather than a rendering hint.
5. **Replayability.** A round is immutable and content-addressed. What was claimed, what was shown,
   what was adjudicated, and what was ignored are recoverable forever.

### 4.4 Ideas borrowed rather than invented

- **Radicle's declarative `resolves`.** Do not forward comment anchors heuristically. Pin each
  finding to an immutable commit OID and have the **next revision declare** which findings it
  addressed. This is strictly more accurate than porting when the author is an agent, because the
  agent *knows what it fixed*. It converts a twenty-year unsolved heuristic into a data field — and
  it removes the one capability Gerrit uniquely provided, which is a large part of why not adopting
  Gerrit is affordable.
- **Gerrit's fallback ladder** — range → file-level → patchset-level → nothing — as the model for
  how a stale anchor *displays*, and its practice of shipping counters alongside the algorithm.
  Nothing is ported: the ladder governs how a finding renders when its pinned commit OID no longer
  contains the anchored range, not how an anchor is moved.
- **Phabricator's pessimistic default** — a changed line leaves its finding *unresolved*. Explicitly
  decline to build a clever porter.
- **jj's stable change-id header** and **ghstack's base/head/orig ref triple** — held in reserve.
  Both exist to synthesize on GitHub what content addressing gives natively; neither is needed
  while the forge is out of the loop.

### 4.5 Why the agent cannot trigger itself

Four independent locks, none of which is author-identity filtering. Three hold at HEAD today; the
fourth is a constraint on an endpoint that does not exist yet, and is marked as such.

1. **Dispatch is human-tier by construction** — the dispatch handler deliberately carries no
   principal tier, on the stated grounds that an agent must not be able to spend the cluster's
   budget (`agent/dispatch/handler.go:13-18`, verbatim in the doc comment).
2. **Ticket state has two writers, both guarded.** `Store.Transition` moves state under optimistic
   concurrency on the expected `from`. `TransitionCurrentRunToNeedsReview`
   (`atc/db/agent_tickets_factory.go:536`) is the run-completion reconciler's projection edge — a
   raw `UPDATE … SET state = 'needs_review'` that can only fire for a ticket already `running` whose
   reservation key matches the run's idempotency key. It is a run-side writer, but it can only move
   `running → needs_review`; **nothing on the run side can put a ticket back into `queued`**, which
   is the transition that would restart work. (The in-code comment at `:461` still claims
   `Transition` is the single writer of `state`; that comment is stale and should be fixed.)
3. **The actor on a human artefact will be derived from the authenticated principal**, never from
   the request body — `responded_by` does not exist yet, and this is a constraint on the §6 item 3
   endpoint rather than an observed property. The pattern is already enforced for `human-answer/v1`:
   `agent/api/workflowwaits/handler.go:200-231` resolves the identity from the request, rejects a
   supplied document whose `answered_by` disagrees, and passes the resolved actor into
   `ReserveResolution`.
4. **No Concourse member credential exists inside a pod.** The runtime *does* hold a model-provider
   credential (`CLAUDE_CODE_OAUTH_TOKEN`, a secretKeyRef injected at `atc/exec/agent_step.go:539`),
   a broker MCP bearer token, and loopback-only MCP endpoints. What it does not hold is anything
   that authenticates to the ATC API as a team member — so nothing in the pod can call dispatch or
   transition a ticket.

A sealed response is inert data; nothing executes it. **Every iteration requires one human click,
so the loop terminates by human inaction.** This is the property the entire forge-bound track spent
weeks failing to achieve — though note it is a property of a design, not yet of running code.

---

## Part V — The blocker underneath

**Attempt 2 never sees attempt 1's code.** Independently verified during this work.

`repository_snapshot_id` has exactly two writers — ticket create
([agent_tickets_factory.go:65](../../../atc/db/agent_tickets_factory.go)) and `Update` (`:236-256`)
— and `Update` is reachable only from the API handler. **No run-side path advances it.** Not
harvest, not the workflow run, nothing. The `needs_review → queued` transition clears
`work_item_snapshot_id`, `dispatch_reservation_key`, and `pipeline_run_id`, and leaves the
repository pointer at the original base. Dispatch then binds `*current.RepositorySnapshotID`
verbatim ([dispatch.go:236](../../../agent/dispatch/dispatch.go)).

This is not a review-design problem. It means **any feedback channel built on the current loop is a
hint channel**: send back "the retry swallows `ErrStaleTransition`" and the agent rewrites the fix
from scratch against the original base. Carry-forward is item zero of the MVP and it is smaller
than any review object considered.

Related, and equally load-bearing: `small-fix-v3` — the ticket workflow — emits `change` + `report`
and **no `review/v1` at all**. Its step *named* `review` is a corrector emitting
`repository-change/v1`. The findings machinery exists (`code-review-v3` produces `review/v1`) but
the ticket loop has never used it. **There is currently nothing to adjudicate.**

---

## Part VI — The MVP

**None of this is built.** Roughly seven focused days — 6.5–7.5 by the table below. Ordered. Full
specification in [2026-08-07-native-agentic-review-mvp.md](2026-08-07-native-agentic-review-mvp.md)
§6.

| # | Item | Size |
|---|---|---|
| 0 | **Carry-forward.** Two nullable columns on `agent_tickets` (`review_response_snapshot_id`, `prior_change_snapshot_id`); `resolveTicketPorts` learns two optional zero-or-one types. Unknown types already fall through, so no existing workflow changes. | 1 day |
| 1 | **Make the ticket workflow produce findings.** A `critique` step in `small-fix-v3` emitting `review/v1`, **declared optional** so a malformed finding cannot fail the whole run. Delete the terminal `prepare-question` + `await_snapshot` pair. New signature ⇒ new immutable workflow version. | 1 day |
| 2 | **`review-response/v1`** document type, validator, registry entry. | 0.5 |
| 3 | **`POST /agent/tickets/:id/review-response`** — materialize the response server-side (idempotency-keyed), then **one transaction**: set both columns and transition, guarded on the expected `from`. Materialize-then-transition, never two PUTs: a 409 then leaves an orphaned pinned snapshot (harmless) rather than a ticket carrying feedback for a run that never happens. | 1 day |
| 4 | **`GET .../snapshots/:id/record`** — extract `record.json` from the canonical tar, 1 MiB cap. Replaces a per-type projection. | 0.5 |
| 5 | **Prompts.** Stable zero-padded finding ids; reuse an id verbatim when a finding persists; `resolves:f-XXXX` in `category` when fixed; never re-raise a waived finding — restate it as an observation so it stays visible and folds. Note `category` is a *single* identifier per finding, so a resolution marker consumes it: attempt N+1 emits one observation-severity finding per resolved id. Ids must be unique and lexicographically sorted (`ValidateEntityIDs`), which zero-padding already satisfies. | 0.5 |
| 6 | **The review screen.** Findings column; changed-file column with per-file patches **already present** in the `files` JSONB (`agent/repodiff.ChangedFile.Patch`, bounded at 64 KiB of unified diff — files past the bound come back `truncated` and the screen must say so rather than render a partial patch as whole); the receipt footer; three exits. `Build/AgentReview.elm` exposes only `view` — its finding card, header, severity badge and verdict row are private and bound to the build page's message type and state, so they must be extracted into a parameterised module first. The extracted verdict row already carries a **six-value feedback vocabulary** (`Concourse/AgentReview.elm:allVerdicts` — accurate / false_positive / noisy / overly_strict / partially_correct / missed_context); decide up front whether the four review-response dispositions replace it, sit beside it, or subsume it. | 2–3 days |

Three exits, deliberately distinct verbs: **Accept & close**, **Send back** (carries the response
forward), **Re-queue clean** (discards it, today's behaviour). "Start over" and "revise this" should
not be the same button.

### 6.1 Deferred, and why each is safe

- **Any new record type.** `review/v1`'s open `category` set plus markdown carry every vocabulary
  we might want, reversibly.
- **`review/v1` rev3.** `RevalidateSealed` runs today's validator over yesterday's bytes, so a
  tightening is a corpus migration rather than a revision. We do not yet know what to freeze.
- **Media evidence.** The *contract* permits it; the *pipeline* does not. No browser in the
  agent-runner image, no per-entry content endpoint, and an unmade XSS/origin decision about
  serving agent-authored bytes from the ATC origin. Three separate projects, none of which tests
  the bet stated at the top of this record.
- **Agent-declared coverage.** An unverified producer claim, monotonically gameable by listing more
  paths. The human-side receipt gives the same reader value with nothing to fake.
- **Standing precedent / waivers.** Needs dozens of tickets over weeks to measure. Also carries a
  trap: `review/v1` forces high/critical findings blocking and forbids `accept` alongside any
  blocking finding, and the `accepted_review` publication-evidence kind is gated on
  `conclusion == accept` (`agent/publisher/review_evidence.go:93`) — so a standing waiver of a high
  finding would permanently block **that** path. The other evidence kind, `human_wait`
  (`agent/publisher/types.go:89`), validates an approval and never reads a `review/v1` at all — and
  it is what the ticket loop uses today, via small-fix-v3's terminal `prepare-question` +
  `await_snapshot`. That split is itself a reason not to couple the two yet.
- **Wiring the new `review/v1` into the publisher's approval evidence.** Explicitly not. Do not
  couple the review loop to the merge gate before the review loop is trusted.
- **Forge projection of any kind.**

### 6.2 How we will know — and what falsifies it

**This experiment has not been run.** It is the plan for testing the direction, not evidence for it,
and the thresholds below are chosen targets rather than measurements.

Run on ~12 real tickets over 2–3 weeks. Most of the numbers derive from sealed records plus two
click instrumentations — resolution rate, file-set overlap, examined fraction, send-back rate and
waiver rate are all mechanical. The defect-escape signal in falsifier 4 has no automated source and
must be judged by hand.

What none of this measures: whether reviewers will use the screen at all. Every falsifier below
assumes a reviewer already sitting in front of it.

**The tell — targeted convergence.** For every send-back, does attempt N+1's `review/v1` carry
`resolves:<id>` for each `must-fix` id, *and* does its changed-file set overlap attempt N's by more
than half? Target **≥70% resolved, ≥50% overlap**. The overlap is the carry-forward check: if
attempt N+1 rewrites everything, the port is bound but the prompt is not honouring it — and every
richer design would fail identically.

**The second tell — attention.** The win looks like examined-fraction *falling* while send-back rate
holds flat and nothing defective reaches `main`. That is findings doing the reading.

**Falsifiers, stated in advance:**

1. **The reviewer's first interaction is consistently a file expand rather than a disposition
   click.** Then the findings are not carrying the review and the agentic-review premise is wrong
   for changes of this size. Instrument this on day one.
2. **`must-fix` resolution below ~50%, or file-set overlap below ~30%.** The channel exists and does
   not work.
3. **Waiver rate above ~60%.** The critique step is generating noise and the loop carries garbage
   forward at full token cost.
4. **Examined fraction falls *and* defects reach `main`.** The receipt is legitimizing
   rubber-stamping rather than exposing it. This is the specific failure the receipt is in the MVP
   to catch rather than to cause.

Any of 1–4 → **do not mint a record type.** The cost of being wrong is one document type, two
columns, one dispatch clause, and one screen — and items 0 and 4 justify themselves regardless.

---

## Part VII — The removal

Executed 2026-08-07 in 15 commits, `962c2c5faf` … `c0048b9859`. (The table below has 14 rows: its
last row covers two commits.)

**174 files changed, +1,273 / −42,246 — net −40,973 lines of code, excluding `docs/`.** The full
range including documentation is 179 files, +1,289 / −42,562. 92 non-doc files deleted (94 counting
the two operator-facing PR docs); the only new files are two forward migrations and their tests.
**No previously released migration was edited in place** — `c0048b9859` corrects a SQL comment in
`1773106167`, which was added four commits earlier in this same series.

| Commit | What went |
|---|---|
| `962c2c5faf` | the forge-pr Concourse resource, its Dockerfile, and its release job |
| `e665e3af90` | the chart's agentPublisher pull-request values |
| `5f40513504` | the `pr-monitor-materialize` and `authorize-pr-response` function-runner modes |
| `c8cf2ab8d7` | the boot surface: flags, `incompletePRAuthoritySpineError`, the mutator wiring |
| `53380c49bb` | the `pr_approval` variant of await and publish snapshot |
| `1337c1a99b` | the entire `agent/pullrequest` package (52 files) |
| `76753ff508` | the `pr_binding_id` discriminator across the source pipeline registry |
| `e0184ac891` | the binding schema, via forward migration `1773106166` |
| `d8056bddc0` | the orphaned publisher execution layer |
| `5e9d0fbc69` | the orphaned `pr-monitor-v3` seed |
| `9506c10a2f` | `atc.PRApprovalIntent` and all of its plumbing |
| `1d559e8385` | the publisher PR request types and `ModePullRequest` |
| `5a4fe98b5e` | the publication operation discriminator, via migration `1773106167` |
| `0d478ac97a`, `c0048b9859` | the last prose references and a migration cross-reference fix |

**Verification:** `go build ./...` and `go vet ./...` clean. `make test-unit` 72 suites green with
2 pre-existing timing flakes (a checkpoint clock assertion 1.8 ms over a ±0.001 tolerance, and a
1-second `Eventually` on a spawned binary's stderr) — both re-ran green in isolation, and
`git log` touches neither file. `go test -p 1 ./atc/db/ ./atc/db/migration/...` clean at 751s, with
the Migration Suite 245/245. Head constants in lockstep at `1773106167`
(`atc/db/migration/legacy_upgrade_test.go:37`).

**A note on the numbers `1773106160` / `1773106161`.** They appear in the bodies of `e0184ac891` and
`5a4fe98b5e` and in earlier drafts of this record. They are wrong: the rebase onto `jetbridge`
renumbered both migrations to land above the checkpoint-removal migrations already deployed there,
and `1773106160` is now occupied by an unrelated migration. `c0048b9859` — the last commit of this
wave — exists solely to fix a stale reference to the old number.

### 7.1 What deliberately survives

**The sealed-record read path.** `pull-request/v1` and `pull-request-response/v1` remain registered
in `agent/snapshot/contracts/registry.go`, with all five frozen schema documents and complete
`recordSchemaHistories` chains. `git diff` over `agent/snapshot/` across the entire removal is
**empty — not one byte changed.** Sealed bytes are immutable, and any observation already written
to storage must stay decodable forever. That is 1,518 lines — roughly 3.5% of the original line
count — and it is the one thing that could not have been undone by a later commit.

**Migration history.** `1773106151` and `1773106154` still *create* the PR tables, because history
cannot be rewritten. `1773106166` and `1773106167` drop them going forward.

**Two inverted test pins.** A deployment still carrying `--agent-publisher-pull-requests-enabled`,
or a policy naming `"mode":"pull-request"`, must now **fail loudly** rather than boot
half-configured. These are the only thing that catches a stale operator config.

### 7.2 Known remaining, flagged rather than silently ridden along

**`agent/publisher/workitem.go` + `WorkItemService` + `ModeComment`/`ModeState` + the `Router`'s
work-item lane.** The tracker-comment publisher. Not part of the PR stack, and unreachable — though
not by the mechanism an earlier draft claimed. `Request.Validate` (`agent/publisher/types.go:287`)
explicitly *accepts* `WorkItemPublisher`, and `executor.go:31` routes to it. The actual gate is
boot-time policy validation: `validateAgentPublisherPolicy` (`atc/atccmd/agent_publisher.go:171`)
rejects every rule whose mode is not `branch` or `merge`, so no `comment` / `state` rule can be
configured, and `agent_publisher.go:164` wires the router's second lane to
`unavailablePublisherExecutor{}` — `WorkItemService` is never constructed in production at all.
Roughly 400–450 lines, concentrated in `workitem.go` + `workitem_test.go` (290 lines) with
single-line references scattered across ~12 files; removing it collapses `Router` from a two-lane
selector to a pass-through. Its own decision, not a rider on this one.

**Documentation outside `docs/superpowers/`** was excluded from the sweep. The plans and specs
under `docs/superpowers/` *should* keep describing the stack — they are the record of why this
happened — but anything operator-facing that presents it as a live feature is now wrong and has not
been audited.

---

## Part VIII — If a forge ever returns

It returns as a **projection**, never as the system of record and never as the trigger: push refs,
open a PR for documentation and for humans outside the loop, write a status. Import, if ever, is
narrow and explicit.

The work needed to do that well is already done and recorded, so it does not have to be redone:

- **The disposition-triggered design** (§3.4) — the trigger unit, the three-way routing, the
  agent-speaks-in-reviews fix, the one-integer cursor.
- **[The Azure DevOps mapping](2026-08-06-azure-devops-forge-mapping.md)** — the full API mapping,
  the provider-neutral `Forge` interface (poll-only with an opaque cursor; webhooks confined to the
  adapter), twelve enumerated leaks each ruled *normalize* / *capability-flag* / *refuse to model*,
  a **sub-3,000-line** adapter budget against the 5,432 removed, the eight things the old adapter
  did that must not recur, and fifteen unsettled questions with the experiment that settles each.
- **The suppression rule**, which is the same on every forge: record the write in the publication
  ledger *before* issuing it, then classify any observed state the ledger fully explains as a
  no-op. Suppress at the write, keyed on "I caused this" — never at the read, keyed on "who wrote
  this."

---

## Reference

| Document | What it holds |
|---|---|
| [review-direction](2026-08-07-review-direction.md) | The short version of this record |
| [pr-interface-cleanup-audit](../plans/2026-08-05-pr-interface-cleanup-audit.md) | The original audit of the stack |
| [repository-snapshot-duplication-audit](../plans/2026-08-05-repository-snapshot-duplication-audit.md) | The storage measurements and the impossibility proof |
| [full-state-pr-monitor-design](2026-08-05-full-state-pr-monitor-design.md) | The forge-bound design that was displaced |
| [review-loop-prior-art-and-alternatives](2026-08-06-review-loop-prior-art-and-alternatives.md) | Industry survey + five native designs |
| [oss-review-systems-survey](2026-08-06-oss-review-systems-survey.md) | Gerrit / Review Board / Radicle / GitLab, adversarially verified |
| [azure-devops-forge-mapping](2026-08-06-azure-devops-forge-mapping.md) | The ADO mapping and the provider-neutral interface |
| [native-agentic-review-mvp](2026-08-07-native-agentic-review-mvp.md) | Six designs, six critiques, and the MVP specified |
