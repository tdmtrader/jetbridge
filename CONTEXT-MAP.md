# Context Map

JetBridge is a Concourse fork with a Kubernetes execution plane and a durable
result plane. Each has its own vocabulary and a checked boundary.

## Contexts

- [Core](./atc/CONTEXT.md): pipelines, jobs, builds, resources, teams and
  auth, pipeline templates and runs, the run admission port, the API, `fly`
  and web surfaces.
- [Agentic](./atc/agent/CONTEXT.md): the MCP surface, MCP grants, and
  composition (a build asking for a child run). A layer over core, wired in
  at one composition root.
- [JetBridge runtime](./atc/worker/jetbridge/CONTEXT.md): step pods, the
  synthetic worker, the artifact daemon with its fail-open durable tier, and
  the brine behavioral contract.
- [Hangar](./hangar/CONTEXT.md): exact immutable trees, strict inputs, and
  the output plane that captures a task's output into one. Includes the
  control-plane coordinator in `atc/hangaroutput`.

## Models

Relationships, lifecycles and store-enforced invariants, one file per
context, under `docs/architecture/`: [core](./docs/architecture/core-model.md),
[agentic](./docs/architecture/agentic-model.md),
[jetbridge runtime](./docs/architecture/jetbridge-runtime-model.md),
[hangar](./docs/architecture/hangar-model.md). Standing decisions live in
[`docs/adr/`](./docs/adr/).

## Relationships

- **Core → Runtime**: core hands the runtime a step to execute and an
  artifact to store; the runtime reports back a synthetic worker and pod
  outcomes. Core owns "build" and "job"; the runtime owns "pod", "volume"
  and "artifact key".
- **Runtime → Hangar**: a step pod's control init takes a source hold and a
  materialization warrant; the artifact daemon hosts strict-input
  materialization and consults the source ledger before destroying a held
  path.
- **Core → Hangar**: only through the coordinator, which passes opaque
  locators and handoff ids. Hangar and the coordinator must not use core's
  product words (run, workflow, ticket, agent, anvil, playbook; for the
  coordinator also build, job, check). A test in `hangar/output` scans every
  Hangar package and the coordinator, on imports, exported fields and wire
  spellings.
- **Agentic → Core**: the agentic context reaches core only through the run
  admission port, the wrapped API and shared value types, never the
  database. Core never imports it.

## Shared kernel

Value types two contexts use and neither owns: the signed artifact
capability the artifact daemon accepts from init containers (`artifactcap`),
and variable interpolation (`vars`). They carry no vocabulary of their own.

## Collisions to watch

- **Receipt** is always qualified: materialization receipt (Hangar strict
  input) or publication receipt (Hangar output plane).
- **Daemon** is always qualified: artifact daemon (runtime) or output daemon
  (Hangar).
- **Lease** and **hold** are always qualified: source hold, read lease,
  operation lease.
- **Reclamation** destroys a pipeline run's payload in core and deletes a
  published object in Hangar. Context makes it clear; do not coin a third
  word.
- **Grant** is an MCP authorization in the agentic context. Hangar's
  capability is a warrant, never a grant.
- **Materialization** is a template resolving into a payload in core and
  the daemon capturing a tree in Hangar. Qualify it when both are near.
- **Scope** is a resource config scope in core, an MCP grant's scope in the
  agentic context, and the namespace component of a tree ref in Hangar.
- **Principal** is a caller's verified claims in core and a cloud-permission
  persona in Hangar.
- **Operation** is an exposed application action in the agentic context and
  a unit of background work in Hangar.
- **Facet** is an activation state machine in Hangar's coordinator and,
  separately, a capability signing domain in its execution control.
- **Run** is a pipeline run in core. A build is never called a run. The
  prototype `run:` step is a step, and is named as such.
- **Transition** is a Hangar handoff step and, separately, a brine step's
  state change.
- **Node** is only ever a Kubernetes node. A plan element is a step; a step
  asking for a child run is a composition call.

## Words that are not in this repo

Track, ticket, workflow and playbook belong to the hearth's process
vocabulary and to the removed agentic layer. They name nothing in this
codebase and must not appear in its glossaries. *Agent* is reserved for the
Agentic context and appears in no other glossary.
