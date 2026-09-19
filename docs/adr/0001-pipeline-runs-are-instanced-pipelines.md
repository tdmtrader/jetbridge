---
status: accepted
date: 2026-08-14
---

# Pipeline runs are instanced pipelines with a durable header

A pipeline run needed a numbered, parameterized execution of a template with
history that outlives the work. We chose to materialize each run as an
ordinary instanced pipeline (the payload) that the existing scheduler,
lidar and build machinery run unchanged, and to record the run itself as a
separate durable header (number, params, status, creator, config hash) that
survives when the payload is reclaimed. The alternative, a run engine beside
the scheduler, would have duplicated build admission, scheduling and
retention for one more kind of thing.

## Consequences

- A completed run pauses its payload; `paused` is what every hot-loop
  component already checks, so completion adds no new flag to those loops.
- Reclamation destroys the payload but keeps the header and the builds,
  which become detached: kept with their logs, belonging to no job or
  pipeline. Build history must therefore carry its own cross-run job
  identity (the run job key).
- A run is a pipeline to everything that does not know about runs. The
  admission gate is what stops a template from building and a payload from
  taking one-off builds.
