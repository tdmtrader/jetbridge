# Core

The Concourse control plane as JetBridge ships it: pipelines, jobs, builds and
resources, the teams that own them, and the API, `fly` and web surfaces over
them. Upstream Concourse vocabulary applies unchanged unless a term below says
otherwise. Everything that runs a step lives in the
[JetBridge runtime](./worker/jetbridge/CONTEXT.md); durable output storage
lives in [Hangar](../hangar/CONTEXT.md); the MCP surface and composition live
in the [Agentic](./agent/CONTEXT.md) context, which reaches core only through
the run admission port.

## Language

### Pipelines and builds

**Pipeline**:
A named, team-owned set of jobs and resources that is scheduled while
unpaused.
_Avoid_: workflow, DAG

**Job**:
A named plan inside a pipeline that produces builds.

**Build**:
One execution of a job's plan, or of a one-off plan submitted outside any
job.
_Avoid_: run (reserved for a pipeline run), execution

**Step**:
One element of a build plan (get, put, task, set_pipeline, load_var,
`run_pipeline`, which admits a pipeline run on the build's own team, and the
composition steps around them).
_Avoid_: node

**Resource**:
A versioned external thing a pipeline reads with `get` and writes with
`put`.

**Resource type**:
The image and protocol that knows how to check, get and put one kind of
resource.

**Pinned image**:
The image a resource type declared with a direct `image:` runs in, named as
the resource cache of a `registry-image` get of that exact reference
(`db.PinnedImage`). The type's resource configs and caches are created under
it, so the type's identity is its image and never its name alone.
_Avoid_: image type, direct image (as a noun for the cache)

**Version**:
One discovered state of a resource, found by a check.

**Check**:
A build that asks a resource for new versions. Checks are scheduled by lidar.

**Resource config scope**:
The shared version history for one resource config, so identical resources
across pipelines share checks and versions.

**Resource cache**:
The fetched content of one resource version with given params. It is
addressed by content so a recreated database row still finds the same cache.

**Task cache**:
A directory a task declares as persisting between builds of the same job.
_Avoid_: cache (alone; a resource cache is a different thing)

**Team**:
The ownership and authorization boundary for pipelines, builds and workers.

**Instanced pipeline**:
A pipeline identified by its name plus instance vars, one of several
instances of the same name.

**Paused pipeline**:
A pipeline the scheduler, lidar and the pauser all skip. Scheduling and
checking skip a pipeline for exactly two reasons: it is paused, or it is a
template. Build-log reaping reaches a payload's builds through its template,
and the archiver leaves templates and payloads alone.

**Archived pipeline**:
A paused pipeline whose job and resource configs have been cleared. Its
cleared configs cannot be recovered; setting the pipeline again un-archives it
under the new config.

**Dashboard section**:
A collapsible block of a team's cards on the dashboard. A pipeline is in the
section its config's `display.section` names, otherwise in the team's own
section; a pipeline's name never places it. A team none of whose pipelines
names a section has no sections and nothing to collapse. A collapsed section's
header shows how many pipelines it hides, how many of them are failing
(failed, errored or aborted), and the most important of their statuses. Which
sections are collapsed is kept per browser.
_Avoid_: group (a pipeline's job groups and instance groups are other things)

**One-off build**:
A build submitted directly by `fly execute`, owned by a team and no job.

**Artifact**:
The contents of one step's output, handed between steps of a build by name.
_Avoid_: volume (that is the runtime's storage word)

### Auth

**Role**:
What a subject may do on one team: viewer, pipeline-operator, member or
owner, each including the ones before it. An action with no listed role is
admin-only.

**Admin**:
An owner of a team flagged as admin. The main team is created admin.

**Main team**:
The team named `main`, created on first start and the home of admins.

**Subject**:
The identity a role is assigned to: a user or a group, qualified by the
connector that vouches for it.

**Connector**:
The identity provider that authenticates users (GitHub, LDAP, OIDC and so
on). The local password store is a connector too.

**Local user**:
A user declared on the command line and authenticated by the local
connector.

**Access token**:
The bearer credential a session holds, stored with its claims so it can be
revoked. MCP clients hold an MCP grant instead; see the Agentic context.

**Claims**:
The verified facts about a caller carried on a token: subject, user,
connector, teams. A principal at the run admission port is exactly this.

### Pipeline templates and runs

**Template**:
A pipeline marked `template: true` that never builds itself; it declares
typed parameters, run retention and a cache scope, its tasks may declare run
results, and it exists only to be run.
_Avoid_: template pipeline, base pipeline, template shell

**Pipeline run**:
One numbered execution of a template, recorded as a durable header that
outlives the pipeline it materialized.
_Avoid_: durable run, numbered run, run (alone, where a build could be meant)

**Run header**:
The durable record of a pipeline run: number, params, status, creator,
config hash. It survives reclamation.

**Payload**:
The ordinary instanced pipeline a pipeline run materializes, which does the
scheduling and building.
_Avoid_: payload pipeline, run payload, instance pipeline, child

**Run number**:
The per-template counter that names a run and is exposed as the instance var
`run`.

**Run identity**:
The pair of run number and run id, injected into a payload as the reserved
placeholders `run` and `run_id`.

**Run params**:
The caller-supplied values for a template's declared parameters, normalized
against the param schema at creation.

**Param schema**:
One declared template parameter: its name, type, requiredness, default and
allowed values.
_Avoid_: parameter schema

**Materialization**:
Resolving a template's current config against a run identity and run params
into the payload's config.

**Config hash**:
The digest of a run's materialized config, stored immutably on the run
header.

**Entry job**:
A job with no `passed:` constraint on any input. A template needs at least
one; entry jobs are what a new run starts.

**Expected job**:
A job a successful run must have built: the entry jobs, plus every job that
triggers off a `passed:` chain made only of expected jobs. A job reachable
only through untriggered inputs is never expected.

**Run job key**:
The identity of a job across runs of one template, tolerant of interpolation
in the job's name, stamped on each build so history can be read after the
payload is gone.
_Avoid_: run policy key (the same value under another name), run job name
(the human-readable companion, not the identity)

**Run status**:
The run's derived lifecycle state: running, succeeded, failed, errored or
aborted.

**Terminal run**:
A completed run. Its payload is paused and it refuses further builds; doing
the work again means a new run.
_Avoid_: dormant run

**Run completion**:
The settlement that moves a run to a terminal status. A run fails, errors or
aborts as soon as one build does and nothing is pending; it succeeds only
once every expected job has a finished build. It is made only by the run
results component, as one terminal publication of status, time and result
manifest once the run's builds and captures have settled.

**Run cancellation**:
A durable request to stop a whole running pipeline run, recorded on the run
with who asked and why. The run cancellation component aborts its builds,
settles its captures and stops its executions, and the run then completes as
aborted through ordinary run completion. Asking again, or asking a terminal
run, is an outcome, not an error.
_Avoid_: abort (alone)

**Run lock**:
The row lock on a run header that serializes build admission, completion and
reclamation for that run.

**Build admission**:
The gate deciding whether a build may be created at all on a template or a
payload: templates never build, payloads take no one-off builds, and a
terminal run refuses everything.

**Build closure**:
An aborted run build's request to settle the captures and close the
execution it left open, without cancelling its run. It is open until that
work is settled and the build finishes aborted; the run then completes
through ordinary run completion.
_Avoid_: build cancellation, partial run cancellation

**Run retention**:
The template-declared policy (`keep_last`, `ttl_days`) that decides when a
completed run's payload may be destroyed.
_Avoid_: shell retention

**Reclamation**:
Destroying an eligible completed run's payload while keeping its run header
and build logs.
_Avoid_: shell-destroy, log rehome

**Run history purge**:
The only deletion of a run's durable history, made by its owner: deleting a
team purges the runs of all its templates, and destroying a template with
`purge_run_history` (`fly destroy-pipeline --purge-run-history`) purges that
template's runs. It deletes the run headers, their builds and logs, payloads,
results and captured outputs, bound inputs and input uploads (releasing their
Hangar claims), credential handoffs and cancellation records, in one
transaction under the one purge marker the evidence triggers honour. A
template's purge refuses while any of its runs is active or an unsettled
landing queue entry still depends on one; a settled entry keeps its settle
record but loses its link to the purged runs. An ordinary destroy of a
template with run history is refused.
_Avoid_: team purge (for the marker, or for a template's purge), hard delete

**Reclaimed run**:
A run whose payload is gone. Only its header and detached builds remain.

**Detached build**:
A build of a reclaimed run, kept with its logs but belonging to no job or
pipeline.

**Cache scope**:
A template's choice of whether its payloads share one growing task cache
(`template`) or get ephemeral ones (`none`, the default).

**Run result**:
One output of a template task, declared on that task (`run_result`), that a
succeeded run publishes as a tree ref in Hangar. It is the only way the
producer's output reaches anything: none of a producer's outputs is a build
artifact, so a later step of the same job reads the result through a
`run_inputs` route with `result`, and the producer's other outputs are
scratch. A resumed build finishes a producer whose capture is already sealed
without running it or finding its container.

**Run input**:
A named binding given at admission (a prior run's result, or a sealed
uploaded source), or this run's own published result (a `run_inputs` route
with `result`, produced earlier in the same job), delivered to a template
task's `run_inputs` as a managed read.

### Run admission

**Run admission port**:
Core's published seam for creating a pipeline run inside a caller's
transaction, expressed without database types so consumers cannot reach past
core.

**Admission**:
One request through the port: a template ref, run params, named run inputs,
a principal, a contract key, and a hook that runs inside the transaction
before it commits. Every admission creates or replays a pipeline run.

**Template ref**:
A team-qualified reference to the template to run. The team is part of the
reference, never derived from the principal.

**Principal**:
The caller's already-verified claims, or a build acting for itself, from
which the port derives the role verdict, the recorded creator and the
principal part of the invocation scope: a person's token subject, or a
build's team and calling pipeline.

**Contract key**:
The caller's opaque idempotency identity for one admission: 1-128 bytes of
A-Z a-z 0-9 . _ ~ -, scoped by the server to team, template and principal.
Presenting it again with the same caller intent replays the run it admitted;
with changed intent it is an invocation conflict. The wire name is
`invocation_key`; a build-side caller derives it from its build id and plan
id. The spec calls it the idempotency key.

### Landing queue

**Landing queue**:
A team-owned queue that lands submitted changes on a repository's trunk by
fast-forward once every gate it declares has passed on a candidate. Its state
is core tables; its only outward acts are Runs it admits (ADR-0011).
_Avoid_: merge queue, queue (alone)

**Entry**:
One submitted change in a landing queue: an id, the commit to land, and when
it was submitted. Queued until settled as landed or ejected; both are final.
Its commits are every commit reachable from that commit and not from the
trunk, oldest first, and each lands as its own commit: the queue never
squashes. An entry holding a merge commit, or no commit off the trunk, is
refused; an author who wants one commit squashes before submitting.
_Avoid_: admission, change (alone), request

**Submit ref**:
`refs/queue/submit/<id>`, pushed by the submitter's own git so the compose
step can reach the commit. The API row is the entry; the ref is only
reachability.
_Avoid_: admit ref

**Candidate**:
The tree the compose Run publishes under `candidate/` of its one result,
`composition`, built by replaying each entry's commits, in order, on the
trunk's head, beside a `manifest/` naming base, entries, each commit's
identity, date and message, and the rebuilt head sha. Gates test the
candidate; the land step rebuilds and pushes exactly those commits. It is
never a branch.
_Avoid_: candidate branch, queue-next

**Landing intent**:
The row created when a candidate's compose Run is admitted, keyed by that
Run and completed by its land Run, so a land Run for a candidate is admitted
at most once across restarts. It is composing, then gating while its gates
run (a queue with no gate skips that), then landing, then done.
_Avoid_: queue lease, fence

**Gate**:
A template a landing queue declares, with a wait. Once a candidate's compose
Run succeeds the queue admits one Run of every gate at once, each binding the
composition as its `composition` run input; the candidate lands when every
gate's Run has succeeded. Its row in `landing_gates` is keyed by the
candidate's compose Run and the gate's place in the queue's list.
_Avoid_: check, required status

**Gate verdict**:
What a gate Run's outcome says. Succeeded passes. Failed is red: the entry is
ejected naming the gate and its Run. Errored, aborted, gone, or not finished
within the gate's wait is no verdict: the gate gets one more Run, and a second
no verdict in a row pauses the queue. A composition no longer bindable when a
gate Run is admitted is moved: the entry is composed again.
_Avoid_: flake (for no verdict), result (for the verdict)

**Paused queue**:
A landing queue held on a gate that gave no verdict twice in a row: it admits
and settles nothing until `fly resume-landing-queue` clears it, which gives
that gate a fresh Run and its retry back.
_Avoid_: stopped, disabled

**Queue principal**:
The third form of the run admission port's principal: a landing queue acting
for its team, authorized for that team and no other, recorded as
`landing-queue/<team>/<queue>`. Only `atc/landing` constructs it.
_Avoid_: system principal, service account

**Settle record**:
Why an entry left the queue: landed with its land Run, or ejected naming the
Run and status that ejected it, or the gate and its Run for a red verdict.
`fly landing-queue` prints them, with each in-flight entry's gate Runs and
verdicts.
_Avoid_: event, notification

### Sidecar

**Sidecar**:
A Kubernetes-shaped container declared to run alongside a task step.

**Sidecar source**:
Either a file inside a build artifact listing sidecars, or an inline sidecar
definition.

**Image artifact**:
A container image taken from a prior step's artifact instead of a registry:
a task's `image:` or a sidecar's `image_artifact`.

### Releases

**JetBridge version**:
The fork's own version, held identically in the `VERSION` file, the Go
version declaration and the chart. Nothing syncs them; a test enforces
agreement.

**Concourse version**:
The upstream Concourse release this fork is based on.

**`jb-` tag**:
The release tag form. `-rc` marks a release candidate; once the final tag
exists the floating image tag is left alone.

**`core`**:
The fork's trunk and the branch the release pipeline tracks.
