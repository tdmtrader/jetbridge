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
One element of a build plan (get, put, task, set_pipeline, the prototype
`run` step, and the composition steps around them).
_Avoid_: node

**Resource**:
A versioned external thing a pipeline reads with `get` and writes with
`put`.

**Resource type**:
The image and protocol that knows how to check, get and put one kind of
resource.

**Version**:
One discovered state of a resource, found by a check.

**Check**:
A build that asks a resource for new versions. Checks are scheduled by lidar.

**Resource config scope**:
The shared version history for one resource config, so identical resources
across pipelines share checks and versions.

**Resource cache**:
The fetched content of one resource version with given params. It is
addressed by content, which is what makes it eligible for durable storage.

**Task cache**:
A directory a task declares as persisting between builds of the same job.
_Avoid_: cache (alone; a resource cache is a different thing)

**Team**:
The ownership and authorization boundary for pipelines, builds and workers.

**Instanced pipeline**:
A pipeline identified by its name plus instance vars, one of several
instances of the same name.

**Paused pipeline**:
A pipeline the scheduler, lidar and the pauser all skip. Background
components skip a pipeline for exactly two reasons: it is paused, or it is a
template.

**Archived pipeline**:
A paused pipeline whose job and resource configs have been cleared. Archiving
is irreversible.

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
typed parameters and retention and exists only to be run.
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
once every expected job has a finished build.

**Run lock**:
The row lock on a run header that serializes build admission, completion and
reclamation for that run.

**Build admission**:
The gate deciding whether a build may be created at all on a template or a
payload: templates never build, payloads take no one-off builds, and a
terminal run refuses everything.

**Run retention**:
The template-declared policy (`keep_last`, `ttl_days`) that decides when a
completed run's payload may be destroyed.
_Avoid_: shell retention

**Reclamation**:
Destroying an eligible completed run's payload while keeping its run header
and build logs.
_Avoid_: shell-destroy, log rehome

**Reclaimed run**:
A run whose payload is gone. Only its header and detached builds remain.

**Detached build**:
A build of a reclaimed run, kept with its logs but belonging to no job or
pipeline.

**Cache scope**:
A template's choice of whether its payloads share one growing task cache
(`template`) or get ephemeral ones (`none`, the default).

### Run admission

**Run admission port**:
Core's published seam for creating a pipeline run inside a caller's
transaction, expressed without database types so consumers cannot reach past
core.

**Admission**:
One request through the port: a template ref, run params, a principal, a
contract key, optionally the run that caused it, and a hook that runs inside
the transaction before it commits.

**Template ref**:
A team-qualified reference to the template to run. The team is part of the
reference, never derived from the principal.

**Principal**:
The caller's already-verified claims, from which the port derives the role
verdict and the recorded creator.

**Contract key**:
The caller's opaque idempotency identity for one admission. The port only
requires that it is present.

### Sidecar

**Sidecar**:
A Kubernetes-shaped container declared to run alongside a task step.

**Sidecar source**:
Either a file inside a build artifact listing sidecars, or an inline sidecar
definition.

**Image artifact**:
A sidecar image taken from a prior step's artifact instead of a registry.

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
