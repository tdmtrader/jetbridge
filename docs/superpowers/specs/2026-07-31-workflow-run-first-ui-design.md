# Workflow-run-first UI — design

Date: 2026-07-31

Status: approved design. Supersedes the exploratory
`decision-handoff.md` as the statement of what will be built. Product
decisions recorded in that handoff remain settled; this document resolves its
twelve open questions against current code and defines the implementation
contracts.

## Source context and baseline correction

This design was checked against the working tree at
`claude/workflow-run-ui-design-f3bb27`, branched from `origin/jetbridge` at
`7008b0fec0`.

**The semantic rebase has landed.** `codex/agentic-platform-rebase` is zero
commits ahead of `origin/jetbridge` and seventeen behind, with an empty diff
across `agent/workflow`, `agent/api`, `atc/db/agent_workflow_run.go`, and both
Elm agent pages. The handoff's authority order named the rebase worktree as
authoritative for v3; that worktree is now equivalent to jetbridge, so
`origin/jetbridge` is the single current base. Head migration is
`1773106156`.

Three findings from the code investigation shape everything below.

**The semantic DAG is already derivable.** A v3 workflow's `plan:` is an
ordered list of steps carrying explicit named `inputs:` and `outputs:`
(`agent/workflow/seeds/small-fix-v3/workflow.yaml`).
`agent/workflow/typecheck.go` already walks that plan tracking
producer-to-consumer snapshot bindings through `try`, `ensure`,
`in_parallel`, and failure hooks. No new graph authoring is required, and the
settled decision that execution wrappers are decorations matches how the
checker already treats them.

**There is no durable per-node occurrence record.** `agent_run_attempt_metrics`
and `agent_run_transcripts` cover agent steps only; `agent_workflow_waits`
covers `await_snapshot`; `agent_publications` covers Publish;
`agent_run_checkpoint_heads` covers checkpointed agent steps. Deterministic
task steps have no durable record at all — only build events, which are
GC-owned. Open question 1 therefore resolves to "nothing does"; the projection
the handoff hypothesised is genuinely required.

**Authored node identity already exists for typed nodes.**
`agent/workflow/typecheck.go:1114` requires a nonblank, workflow-unique,
authored `function_id` for every typed agent and task node. The
`leaf.FunctionID = leaf.Name` fallback at `agent/workflow/node_parse.go:208`
is scoped to reusable *node definitions*, whose single leaf takes the node's
own contract name. The identity gap is narrower than the handoff assumed: only
`await_snapshot`, `publish_snapshot`, resource sources, and workflow ports lack
a registered identity.

One further constraint: `agent_workflow_runs.actual_plan` is stored encrypted,
and `RunSummary` deliberately omits it because it may carry private plans and
prompts. Every graph served to a browser is therefore derived and redacted
server-side. Raw plans are never shipped.

## Governing constraint

One graph model, one occurrence model, three views.

The handoff's sharpest architectural warning is against building the ticket
thread as a second aggregation system or the run page as a second graph model.
Every contract below is shared by the workflow overview, the individual run
page, and the ticket journal. Where the surfaces differ, they differ by which
state lookup is supplied to a shared renderer — never by having their own
graph.

## Stable node identity

Graph nodes divide into two classes, and only one of them carries durable
identity.

**Execution nodes** — agent, task, await, publish, load — are the nodes that
actually run, and they are the only kinds
`agent_workflow_run_node_occurrences` stores. Their IDs are the workflow-local
identities described below, and the durable projection is keyed on them.

**Endpoint nodes** — workflow inputs, workflow outputs, and resource sources —
exist to make dataflow legible on the canvas. They never execute and never
carry an occurrence. Their IDs are kind-qualified (`input:repository`,
`output:review`, `source:main`) so they cannot collide with an execution
identity.

The qualification is not cosmetic. The shipped seed
`agent/workflow/seeds/code-review-v3/workflow.yaml` declares output port
`review` (`from: review`) alongside an agent whose `function_id` is `review` —
which is idiomatic, since the port takes its name from the binding that feeds
it. With bare endpoint IDs the output node collides with the agent node, and a
graph builder that de-duplicates by ID silently drops the workflow's only
public output. Kind-qualifying endpoints removes the collision class entirely
rather than forbidding a legal and natural authoring pattern.

Output ports in particular are in no identity namespace at either compile or
render time, so nothing upstream prevents this — the graph layer must handle
it.

The rest of this section concerns execution nodes, whose identity does have to
be unique because durable history is keyed on it.

There is one workflow-local identity namespace per workflow version.

- **agent and task nodes** use the authored `function_id`, as enforced today.
- **`await_snapshot` and `publish_snapshot`** use the step's binding name
  (`await_snapshot: approval`). This is not a display name: downstream steps
  reference it in `inputs:`, and Publish references it in `approval:`.
  Renaming it is a dataflow edit, not a cosmetic rename, so it satisfies the
  rule that identity is never inferred from a display name. No new authored
  field is introduced, because a second identifier would mean two names for one
  thing.
- **resource sources** use the declared source name.
- **workflow inputs and outputs** use the port name, which is already contract
  identity through `PublicSignature`.

All four kinds register into the same namespace, with uniqueness enforced
across it. This closes a real existing hole: today an agent's `function_id`
can silently collide with an `await_snapshot` name.

Implementation note, verified while landing this rule: `RenderFunction`
prepends a synthetic `load_snapshot` per declared input port and per bound
resource source (`agent/workflow/render.go:746-752`, `:766`) before calling
`TypeCheckFunction`. Registering load names therefore places input-port and
resource-source names into the same namespace automatically on the render
path, which is exactly the rule stated above rather than an accident. The
practical consequence is that a workflow whose input port is named `repo` and
whose agent carries `function_id: repo` type-checks unrendered but is rejected
at render. That is correct — an input-port load is a graph node — and Phase B
may rely on the namespace being uniform across all four kinds.

Rename and replacement behaviour follows from this. A display-name change on an
agent or task node preserves history because `function_id` is separate. A
change to any binding name is a dataflow edit that yields a new identity and
starts new history, which is the correct semantics for a semantic replacement.
Identity is never derived from graph position.

## Component: `agent/workflow/graph`

A pure derivation from a compiled definition to a redacted semantic DAG.

```
Build(compiled *workflow.FunctionConfig) (Graph, error)
```

It reuses typecheck.go's producer-to-consumer walk rather than reimplementing
traversal, so wrapped and conditional steps stay consistent with the type
checker.

**Nodes** carry stable id, kind, display name, snapshot type refs, and — for
reusable-node bindings — the workflow-local instance name plus the exact
reusable node name and version. Node kinds are `input`, `resource_source`,
`agent`, `task`, `await`, `publish`, and `output`.

**Edges** run producer to consumer, labelled with the snapshot port name and
type ref.

**Decorations** — retry, timeout, `do`, `try`, `ensure`, `on_failure`,
`on_error` — attach to the node or branch they wrap. They never become nodes.

**Redaction is structural.** Prompts, task configs, and broker profiles have no
fields in `Graph`. Redaction cannot be forgotten at a call site because the
type cannot express the private values.

Because a compiled definition is immutable and content-hashed per version,
`Build` is a pure function of `(definition_id, version)` and its result is
cacheable by content hash.

## Component: `agent/workflowrun/occurrence`

One derivation, two call sites.

```
Derive(run, plan, attempts, metrics, waits, publications, buildSteps) []NodeOccurrence
```

**Live call site.** The overview and run endpoints call `Derive` against
current authoritative rows, so "what needs attention now" — the page's primary
job — is never stale.

**Frozen call site.** Run finalization calls the same function and writes its
output to `agent_workflow_run_node_occurrences`, before build and template GC
can reclaim the sources. Deterministic task steps, the one kind with no durable
source, are covered precisely because the freeze happens while build step state
still exists.

Sharing one function between the two call sites is what prevents a second
execution truth. The projection is derived from authoritative state and frozen
when appropriate; it never originates a fact.

### The projection stores only nodes the graph contains

`Derive` is given the execution-node ID set from `graph.Build` for the run's
own workflow version, and projects only nodes in that set.

This is not a filter for tidiness — it makes the graph-to-occurrence join exact
by construction, which is the load-bearing contract of the whole read model.
Without it the join is a guess: `RenderFunction` prepends a synthetic
`load_snapshot` per input port and per resource source, named with the **bare**
port name, so the runtime plan contains a node called `before` while the graph
calls the same concept `input:before`. A join that tried `load:X`, then
`input:X`, then `source:X` would be ambiguous the moment an authored
`load_snapshot` shared a name with an input port.

Filtering against the graph resolves it correctly and without special cases:
an authored `load_snapshot` is a graph execution node with a bare ID and is
kept, while a synthetic input-port load is not an execution node at all and is
dropped — matching the rule that endpoint nodes never carry occurrences.

It also means a plan node the graph does not recognise can never reach the
projection, so `Node.ID` remains a single namespace shared by both.

### `agent_workflow_run_node_occurrences`

Columns: `workflow_run_id`, `node_id`, `attempt`, `team_id`, `workflow_name`,
`workflow_definition_id`, `workflow_version`, `node_kind`,
`reusable_node_name`, `reusable_node_version`, `plan_id`, `status`, `wait_id`,
`publication_id`, `started_at`, `completed_at`, `duration_seconds`, `cost_usd`,
`frozen_at`.

- Primary key `(workflow_run_id, node_id, attempt)`.
- `status` is constrained to `pending`, `running`, `waiting`, `succeeded`,
  `failed`, `errored`, `aborted`, `skipped`.
- Rows are immutable after freeze, enforced by trigger, following the
  established pattern in `agent_run_attempts` and `agent_run_checkpoint_heads`.
- Index `(team_id, workflow_name, node_id, completed_at DESC)` serves the
  cross-revision aggregate path; index `(workflow_run_id)` serves the run page.

Aggregation is keyed by `workflow_name` rather than `workflow_definition_id`
because overview history groups by the workflow-local logical role across
revisions, exactly as decision 8 requires.

## Retry-chain resolution

Resolves open question 3, including the branching case.

For each pair of retry closure and `node_id`, order occurrences by
`(run.created_at, run.id)`. The **effective** occurrence is the last terminal
one, unioned with any currently-active occurrences.

Consequences, matching the settled product decisions:

- failed then retry running — running, and attention-worthy;
- failed then retry succeeded — resolved, and off the attention view;
- failed with no successful continuation — failed, and attention-worthy;
- no matching activity in the window — no-data, rendered distinctly from
  success.

Branching retries resolve deterministically without inventing causal edges.
Nothing is deleted: the failed occurrence remains in run history and in
evaluation statistics.

## Window semantics

History is `completed_at` within `[now − window, now]`. Active runs — status
`admitting`, `running`, or `canceling` — are unioned in regardless of age.

The API states this rather than implying it, returning
`{window, from, to, includes_active_before_window: true}`. The graph, selected
node detail, and run list share one scope. Windows are `24h`, `7d` (default),
and `30d`. Custom and adaptive windows are out of scope.

## Ticket association

The current model links a ticket to one run through
`agent_tickets.workflow_run_id` with a unique index. Later runs use `retry` or
other origins whose reference does not identify the ticket, so it cannot
satisfy the requirement.

The replacement follows the codebase's established idiom of a nullable live
foreign key beside immutable copied evidence, as used by
`agent_workflow_waits.build_id` and `build_id_evidence`:

- `agent_workflow_runs.ticket_id` — nullable, `REFERENCES agent_tickets(id)
  ON DELETE SET NULL`.
- `agent_workflow_runs.ticket_reference` — immutable text evidence, non-empty
  whenever `ticket_id` was set, retaining the external reference after the
  mutable intake ticket is deleted or archived. This resolves the retention
  half of open question 7.
- Both immutable after insert, enforced by trigger. Zero-or-one association
  falls out of it being a column rather than a table.
- Index `(ticket_id, created_at DESC, id DESC)` for the journal;
  `(ticket_reference, created_at DESC)` for search.

`agent_tickets.workflow_run_id` is backfilled into the new columns and then
dropped along with its unique index. Keeping both would create the two-truths
condition the design exists to avoid.

Association is explicit run context, never inferred from origin strings or
snapshot lineage. Propagation is audited across every admission path: ticket
dispatch, manual launch from ticket context, direct manual launch, retry,
resource-triggered admission, automated follow-on launch, experiment
candidate and evaluator launch, and Publish or PR follow-up workflows. Retry
copies both fields from its source run. Experiments remain unattached unless
explicitly launched in ticket context.

Association does not alter a workflow's type signature and does not create a
required work-item input port.

## API surface

A bounded set of endpoints, not one aggregate endpoint.

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/agent/workflows/:name/overview` | promoted graph, node state, revision boundaries, historical-only-nodes indicator |
| `GET /api/v1/agent/workflows/:name/runs` (extended) | run list with window, scope, node, node-status, revision, origin, and text filters plus cursor |
| `GET /api/v1/agent/workflows/:name/runs/:id/graph` | exact run DAG with that run's occurrences |
| `GET /api/v1/agent/tickets/:id/runs` | chronological cross-workflow journal |

Graph and run list are separate endpoints because the list paginates
independently of the graph: selecting a node repaginates the list without
refetching the DAG.

The overview response labels its window and aggregation semantics explicitly
and never emits a single ambiguous aggregate node status. Per node it returns
active counts by state, windowed history counts by terminal status, and the
resolved attention state.

The existing all-time operational-status-count endpoint is insufficient for
this page and is superseded by the overview endpoint.

### Scope classification

Experiment runs are excluded from default operational evaluation and from the
default run list, and remain one explicit scope choice away. Classification is
a deliberate server-side contract over origin kind rather than a permanent
negative string comparison against `"experiment"`, so that new origin kinds
are classified explicitly when introduced.

### Indexed search in the first slice

Resolves open question 8. Server-side indexed support covers exact durable run
ID, ticket reference by exact match and prefix, exact workflow revision, and
exact snapshot ID through the `agent_workflow_run_snapshots` join, which
already carries the required index
`(snapshot_id, workflow_run_id, direction)` and needs no schema change.
Unbounded JSON scanning is prohibited. Free text over prompts and transcripts
is explicitly out of the first slice.

## Elm decomposition

`AgentWorkflowRun.elm` is 1201 lines and `AgentWorkflow.elm` is 789. Adding a
graph to both without splitting them would produce two files that cannot be
held in context. The graph becomes its own unit, shared by both pages.

| Module | Responsibility | Depends on |
|---|---|---|
| `AgentGraph/Model.elm` | `Node`, `Edge`, `Kind`, `Decoration`, `Graph` | — |
| `AgentGraph/Layout.elm` | pure `Graph -> LaidOut` with ranks, positions, edge paths | Model |
| `AgentGraph/View.elm` | SVG for `LaidOut`, parameterised by a `NodeState` lookup | Model, Layout |
| `AgentGraph/Decoder.elm` | JSON decoding | Model |
| `AgentWorkflow/RunList.elm` | the coupled chronological list | — |
| `AgentWorkflow/Filters.elm` | filter and selection state to and from the URL | — |
| `AgentWorkflow/Panels.elm` | Start and Versions overlays | — |
| `AgentWorkflowRun/NodeDetail.elm` | selected-node durable detail | — |
| `AgentTicket/Journal.elm` | chronological journal | — |

`Layout.elm` is pure, which is the main reason Elm-native layout earns its
cost over reusing `web/public/graph.mjs` through ports: it is fuzz-testable,
it keeps the graph DOM inside Elm, and it makes the coupled graph-to-list
interaction ordinary Elm state rather than a two-way port dance.
`web/public/graph.mjs` is domain-neutral — one job or resource mention across
1007 lines — and serves as the reference implementation for the rank and
column algorithm.

`View.elm` taking a `NodeState` lookup is the mechanism that stops the run page
from becoming a second graph model: aggregate counts and single-run status are
two lookups into one renderer.

## Surface 1 — workflow overview

Header carries workflow name, revision indicator, the window control, and the
Start and Versions actions. Below it, one search field with visible
`attention`, `active`, and `all` choices and a single secondary "more filters"
affordance. Below that, the promoted graph beside the chronological run list,
with the list flowing under the graph on narrow screens.

There is no permanent metrics strip, no KPI card grid, and no per-node
miniature charts.

### Definition management placement

Resolves open question 4. Start and Versions are header actions opening
overlay panels above the graph. Start holds the manual run form. Versions holds
the version timeline, the promotion control, revision comparison, and the
historical-only-nodes affordance. Experiments become a scope choice beside
`attention`, `active`, and `all`. Nothing leaves the page and no new routes are
required.

### Color-independent state language

Resolves open question 10. Node bodies stay neutral to preserve the graph's
shape. State is expressed as a glyph plus a count plus a word, so color is only
ever reinforcement:

- `> 2 running`, `|| 1 waiting`, `x 1 failed`;
- a compact check when there is no unresolved attention;
- **no activity in the window** renders as a dashed node border with no badge,
  visibly distinct from success rather than a gray that reads as green;
- **selection** is a thicker border and halo, never a color swap.

### Aggregate semantics on the canvas

The overview aggregates across durable run occurrences without pretending
concurrent executions have one canonical status. Whole-node worst-status fills,
stacked charts, dense proportional bars, and simultaneous display of cost,
duration, revision, inputs, and history are all excluded.

Current attention is distinguished from recent history. Current-state
indicators answer what needs action now; recent historical failures remain
discoverable after selection and remain in evaluation statistics, but do not
keep the canvas red after a successful retry.

### Revision behaviour

The canvas shows the currently promoted workflow graph. A union graph spanning
historical revisions is never rendered. Each run stays pinned to its exact
immutable revision, and selecting a run opens that exact historical graph.
Revision boundaries appear as subtle markers in the run list and in
selected-node history. Nodes removed from the promoted graph stay off the
default canvas and remain discoverable through the Versions panel; historical
runs always render them in their original graph.

### Run row fields

Resolves open question 5, and is deliberately small. Before expansion a row
shows the effective-state glyph, run ID, relative time, duration, ticket
reference when present, and an attention cue such as `waiting at approval`.
Revision markers appear only on the row where the revision changes. Cost and
outcome live behind selection.

### Empty and unpromoted states

Resolves open question 6. With versions present but none promoted, the page
renders the latest imported version's graph labelled "not promoted — showing
version N", with Promote one click away in the Versions panel. With no versions
at all, an empty state points at import.

### Scale

Resolves open question 9. The graph fits to width by default with pan and zoom
and rank-level virtualization, truncating long labels to tooltips. The run list
is cursor-paginated at fifty rows with all filtering performed server-side, so
the client never scans. Node selection repaginates the list without refetching
the DAG. Graph correctness never depends on the latest hundred runs.

### Interaction, navigation, and URL state

Selecting a node filters the run list to occurrences that reached it. Selecting
a node's specific status indicator narrows further. Clearing selection restores
the full view. Clicking a run row navigates directly to the run page; there is
no inline preview.

Resolves open question 11. `window`, `scope`, `q`, `node`, `node_status`,
`status`, `origin`, `version`, and `panel` live in the URL, so back, forward,
links, and refresh behave predictably. Scroll position stays out of the URL:
the page model keeps a per-route scroll cache restored through
`Browser.Dom.setViewportOf`, which honours restoration "where feasible" without
polluting shareable links.

## Surface 2 — individual workflow run

The run page renders the exact DAG of one immutable run occurrence using the
same renderer with a single-run state lookup, built from that run's own
revision. It is not the aggregate graph with a filter applied.

The DAG stays visually dominant. The header carries only run ID and effective
state, exact workflow revision, associated ticket when present, start and
completion time with duration, retry relationship, and one concise attention
cue.

The existing flat cards — inputs and sealed outputs, waits, outcomes, review
projections, repository-change projections, run metrics, and transcripts — are
re-homed rather than discarded. Node-scoped detail moves into
`NodeDetail.elm` under the selected node; genuinely run-level items such as
outcomes and overall metrics stay below the graph.

### First-release selected-node detail

Resolves open question 12. The first release shows attempts and effective
attempt state, error, interruption, and recovery information, exact inputs and
sealed outputs, logs and transcript, duration and cost, human question and
answer with resolution audit, validation evidence, and Publish result or
external projection. Duration and cost distributions, revision comparisons, and
outcome analytics are the later expansion.

The run page links back to its ticket thread when associated, makes retry
attempts easy to traverse, and never requires a ticket.

## Surface 3 — optional ticket journal

A chronological list of every associated run occurrence. No graph, no composed
cross-workflow DAG, no invented causal edges.

Each entry shows workflow name and exact revision, run status and timestamps,
retry grouping, concise outcome or publication state, outstanding action, and a
direct link to the full run. Repeated executions of the same workflow are
separate entries. Concurrent runs are ordered by timestamp and state.
Unresolved waits, failures, and requested human actions are elevated; completed
detail collapses.

The journal is served by one query ordered by durable run occurrence time, not
one query per workflow name. Snapshot lineage and explicit downstream-run
relationships may appear as secondary provenance where available, but are never
required to construct or lay out the journal.

Tickets are optional throughout. Standalone workflows and their runs are fully
supported.

## Implementation slices

Six slices, mapping onto the handoff's five. The handoff's first slice divides
cleanly into a pure part and a persistence part, which is worth doing because
the pure part unblocks everything else and carries no migration risk.

| Slice | Content | Ships |
|---|---|---|
| A | identity registration and `agent/workflow/graph` | nothing user-visible; pure Go, no schema; unblocks all others |
| B | `occurrence.Derive`, projection table, freeze hook | durable node history that survives build GC |
| C | overview and extended runs endpoints | aggregate state and server-side filtering |
| D | overview UI | layout, view, run list, panels, URL state |
| E | exact run DAG | run graph endpoint and run page reorganization |
| F | ticket association and journal | schema, propagation audit, endpoint, journal UI |

A, B, C, D, E form the dependency chain. F is independent of them and may run
in parallel or ship first, since its schema change touches different tables.

The slices share the identity and read contracts defined above. The ticket
thread is not a second aggregation system, and the run page is not a second
graph model.

## Testing strategy

**Go.** Golden-graph tests for `graph.Build` against all nine seed workflows.
Table tests for `occurrence.Derive` across every node kind, explicitly
including the deterministic-task case that has no durable source before freeze.
DB specs for projection immutability and for freeze occurring before build GC.
Handler tests pinning window and scope semantics, including active runs older
than the window. Up and down migration specs.

**Elm.** Fuzz tests for `Layout` covering determinism, rank monotonicity, and
absence of node overlap. View tests extending the existing
`AgentWorkflowPageTests` and `AgentWorkflowRunPageTests`. Elm tests run through
`yarn test`, which invokes `elm-test` in `web/elm`.

**Integration.** ATC integration coverage for the overview endpoint. Live
verification on theborg against real workflow shapes.

PostgreSQL-backed suites run serially because they share fixed ports.

## Scope estimate

The handoff's three-to-six-week estimate is credible for slices A through E.
Slice F spans an eight-path propagation audit plus a backfill-and-drop of
`agent_tickets.workflow_run_id`, and is additive to that estimate rather than
absorbed by it.

## Non-goals

Unchanged from the handoff, and binding on implementation:

- a dashboard-heavy workflow homepage;
- a permanent KPI summary strip;
- a union graph spanning all historical revisions;
- a single dominant color pretending to summarize mixed concurrent runs;
- a cross-workflow mega-DAG for a ticket;
- a prescribed lifecycle sequence for tickets;
- mandatory tickets or work-item inputs for workflows;
- more than one ticket association per run;
- experiments mixed into default operational health;
- an inline run-preview interaction layer;
- custom or adaptive time windows in the initial version;
- inferred stable identity based on names or graph position.

## Success criteria

The design succeeds when a user can open a workflow and recognize its shape
immediately; see running, waiting, or unresolved work without decoding dense
analytics; see a resolved state after a successful retry while still finding
the earlier failure; select a node and immediately narrow the adjacent run
history; find a run by ID, ticket reference, or exact identity; open that run
and inspect its exact historical DAG and evidence; change the history window
without changing the meaning of active state; understand revision boundaries
without seeing a union graph; open a ticket and see every relevant run
occurrence in order; and run and inspect workflows that have no ticket at all.

It fails if the graph becomes a telemetry dashboard, if ticket history requires
invented causal edges, if experiments distort ordinary evaluation, or if
durable history disappears after the underlying Concourse execution rows are
reclaimed.
