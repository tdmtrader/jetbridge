# Core: relationships and invariants

Vocabulary: [`atc/CONTEXT.md`](../../atc/CONTEXT.md). This file records how
the terms relate, which identities are stable, and what the store refuses.

## Ownership

```
Team ─┬─ Pipeline ─┬─ Job ─── Build
      │            └─ Resource ─── Resource config scope ─── Version
      ├─ One-off build
      └─ Worker, Container
```

- A **team** owns pipelines, one-off builds and workers. Deleting a team is
  the only thing allowed to delete a run payload that still has builds.
- A **job** is identified by its name within its pipeline. Re-setting the
  pipeline updates the job row in place; builds keep pointing at it. A job
  absent from the new config becomes inactive, not deleted.
- A **build** belongs to a job, or to a team alone (one-off), or to a
  resource (check). Exactly one of those.
- A **resource config** is one identity (type plus source) shared by every
  pipeline resource, resource type and task image that hashes the same.
- A **resource config scope** owns version history. A resource with private
  history gets a scope of its own; a resource with shared history uses the
  one scope that has no owning resource, and that scope is shared across
  every pipeline that hashes the same. This is the only way version history
  crosses pipelines.
- A **resource cache** is identified by config, version and params. It is
  cluster-wide and pipeline-blind; that is what makes it eligible for the
  runtime's durable tier.

## Build lifecycle

```
pending ──▶ started ──▶ succeeded | failed | errored | aborted
```

- Abort is a request flag, not a status. A pending build that is aborted is
  re-scheduled so the scheduler can settle it; the terminal status is
  written by finishing.
- Finishing a build that belongs to a pipeline run takes the run lock
  first, so run completion and build completion never race.
- Aborting requires pipeline-operator on any team the build is associated
  with.
- Aborting a v2 run build is scoped to that build. If it cannot finish over
  an open execution or an unsettled output handoff, finishing records its
  build closure and leaves it unfinished; the run keeps running and a rerun
  of its job is admitted. The build finishes aborted when the closure has
  settled that work, and the run completes through ordinary completion.

## Checks

A check is a build in every way but two: it belongs to a resource rather
than a job, and it usually lives in memory rather than the store. Duplicate
checks are suppressed per resource config scope in process; the scope also
serializes checking and records check timing. Checks skip entirely until
the scope's interval has elapsed unless triggered by hand.

A check of a v2 run's payload is always a stored build, owned by the run. It
is collected by the same rules as any other check: a resource keeps its
scope's last check, and a resource type its newest. An executed check goes
with its execution evidence, and only once every execution is closed: a check
produces no run result, so a closed check execution is inert. So is a closed
get inside the check build, which fetched the check's custom image on a
resource cache miss. An executed check is also kept while it is its
resource's newest check. An open execution, and a build a run output start
names, is never collected.

## Pipeline runs

```
Template ──1:n──▶ Pipeline run ──1:1──▶ Payload ──▶ Jobs ──▶ Builds
              (run header)          (instanced pipeline, instance var `run`)
```

Creation, under a lock on the template:

1. Refuse if the template is instanced, not a template, archived or paused.
2. Normalize run params against the param schema.
3. Allocate the run number and run id; materialize the template's config
   against them; hash it.
4. Insert the run header as running; save the payload as an instanced
   pipeline bound to the run.
5. Create one pending, manually-triggered build per entry job and request
   scheduling.

Completion, under the run lock, whenever a run build finishes:

1. Only a running run completes.
2. Blocked while any run build is pending or started, while any build
   closure of the run is open, or while the payload has a job with
   scheduling requested and not yet performed.
3. Take the latest terminal build of each job. Status is the worst of them:
   errored over aborted over failed over succeeded.
4. A would-be success is refused while any expected job has no terminal
   build in this run. Any other status settles immediately.
5. Write the status, pause the payload as run-completed, and announce.
   Announcement is a wake-up, not delivery.

Reclamation, when a run is terminal and past its retention:

1. Refuse if the run is running or any run build is still pending or
   started.
2. Delete the run's checks, as deleting a pipeline deletes its checks; an
   executed check goes with its closed execution evidence. Detach the job
   builds (and any check whose execution is somehow still open) from their
   job, pipeline, resource or resource type.
3. Delete the payload. The header and detached builds stay.

### What a v2 run fixes, and what it does not

A v2 run's reproducibility is deliberately bounded. It fixes, for the life
of its header: the retained definition revision it was admitted against,
the explicit params and the defaults applied to them, its exact named
Hangar inputs and their routes to task slots, and its immutable terminal
result manifest. Nothing else. Ordinary resource versions, registry tags,
task images, credentials and any other ambient input are not pinned, and
are not recoverable after the payload is reclaimed. Re-running the same
definition, params and inputs is not a claim to reproduce the same result.
There is no other class: migration 1789793147 dropped every legacy_v1 run
with its payload, definition, builds and events, and the schema admits only
`v2`. `run_contract_version` stays as a birth-time field so a future class
can be named without a migration; nothing branches on it.

### Lock order

Everything that touches a run locks in one order: the team, then the
template, then the run, then the payload and its jobs, then the run's
builds, taking only the ones it needs. The Hangar claim suffix comes after
the run's locks. Run paths take the team `FOR SHARE`. Deleting a team
takes it first, `FOR NO KEY UPDATE` so that run creation's payload insert
(whose team foreign key takes `KEY SHARE`) is not blocked under the template
lock. Team purge and reclamation lock the run's builds before deleting any
evidence, because check collection locks a check build (`SKIP LOCKED`) and
then deletes its executions.

## Invariants the store enforces

- A template is never an instance; a payload is always one.
- A run has exactly one payload while running, and at most one ever.
- Run number is unique per template. Header fields (template, number,
  params, creator, config hash) are immutable once written.
- A v2 run may carry one correlation value and one `caused_by_run` edge,
  both immutable caller intent. The edge
  points only at a strictly earlier run of the same team, so it cannot form
  a cycle; it has no foreign key, so purging the predecessor leaves an
  unresolved-predecessor marker and never cascades.
- Run metadata on a job (expected, job key) exists only on payloads and is
  immutable.
- A build's run identity (run, run job name, run job key) is all present or
  all absent, and immutable.
- A task cache belongs to a job, or to a template plus run job name, never
  both.
- A payload cannot be deleted while it still has attached builds, except
  when its whole team is being purged.
- A run's evidence (execution attributions and witnesses, output starts and
  their decisions, cancellation work, credential handoffs, bound inputs) is
  immutable and cannot be deleted, again except by the purge of its team.
  The purge also releases the Hangar claims the team's runs held, leaving the
  claim rows as Hangar's tombstones.
- The one narrower exception is check collection: under its own
  transaction-local allowance it may delete a closed check or image get
  execution of a completed run check build, whose witnesses go with it.
  Task and put evidence, any job build's evidence, open executions, anything
  a run output start names, and a lone witness are refused.
- Run retention counts are positive; run status is one of the five.

## The run admission port

The one seam through which anything outside core creates a run. Its method
signatures name no store types. An admission is refused before any row is
touched if the contract key is outside the invocation alphabet; authorization
runs against the reference's team before the template is resolved, so an
unauthorized caller learns nothing about existence. The scoped replay lookup
comes next, before any activation check: a server that holds creation or
speaks for no activation epoch still replays the run an invocation already
admitted, and refuses everything else with the hold, whatever else is wrong
with the call. The caller's hook runs after the run and
payload exist and before commit; its error aborts the whole creation.

Every run is admitted as v2, through the port's one admission: the v2
create route and the `run_pipeline` step both call it. Terminal publication
has one writer, the Run result finalizer (component `run_results`); build
completion only wakes it.

Admission is activated by configuration, not by hand, and the Run contract
has its own activation, separate from the Hangar output epoch. Every web
node, at startup, reconciles the run activation marker from
`--pipeline-run-activation-epoch`, which the chart sets at every deploy: a
positive epoch admits at that epoch, zero stops admitting and keeps the
epoch. An epoch older than the recorded one refuses to start. Inside each
admission the marker must admit the epoch. A template that declares results,
or a run given inputs, additionally needs the node's Hangar output epoch
enabled (base and output facets, which only the Hangar output activation Job
enables). Admission checks nothing more, but executing any run -- with or
without results -- needs the Hangar output plane's execution control
(`hangarOutput.executionControl`) with the node's output capability key
configured and its Hangar output epoch enabled, because every step of a run
build starts through the exact-execution check. On a deploy missing any of
these, a run of a
template without results is admitted and every step then fails with "Run
result execution is not activated"; such a deploy sets
`web.pipelineRunActivationEpoch: 0` to keep admission closed.

A run is born under the Run epoch; its captures, credential deliveries and
bound inputs carry the Hangar epoch they were admitted under. Continuing a
running run -- starting its builds and producers, finalizing it, replaying its
invocation key -- needs the marker at or past the run's epoch, not the Hangar
epoch it was admitted under, so a Hangar rotation strands nothing. New Hangar
work (a capture, a credential delivery, an input upload) still needs its own
Hangar epoch enabled, and a new credential delivery also needs the marker
admitting: an admission hold stops new Runs and new credential grants, not
running Runs. Two limits are accepted and deferred: outputs published
under a Hangar epoch that is later disabled may become unreadable, and a
prior-run input bound across a rotation may be refused, because a prior
run's result is admitted as an input only under the Hangar epoch its claim
carries.
