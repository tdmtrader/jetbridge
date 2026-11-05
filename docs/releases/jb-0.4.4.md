# JetBridge 0.4.4

JetBridge 0.4.4 follows jb-0.4.3 (ebb2ec2801). A Run producer's output now reaches a later step of the same Run the way it reaches another Run: as a managed read of its published result in Hangar. This replaces two 0.4.3 fixes that made a resumed build depend on the producer's container record and its step directory on the node.

## Changes

- **A later task reads an earlier task's result as a Run input.** A `run_inputs` route with `result: <name>` binds this Run's own result of that name, produced by another task of the same job. The consumer starts once that result is published, and fails if it was discarded or failed.
- **A producer's outputs are not build artifacts.** A task with a `run_result` no longer registers its outputs in the build. A later step that names one as a plain input fails as a missing input on every run. A producer's outputs other than its result are scratch.
- **A resumed build skips a sealed producer without its container.** After a web restart, a producer whose result is already sealed finishes immediately; no worker, container or node directory is involved. The 0.4.3 container retention is removed, and `artifactDaemon.ttl` no longer bounds how late a Run can resume.
- **The landing compose template publishes one result.** `compose` publishes `composition`, holding the tree under `candidate/` and the land step's facts under `manifest/`; the relay task is gone, and the land template takes the one `composition` input.
- **The implement template's `validate` reads `change` as this Run's result.**
- **Tests:** deployment profiles and a 0.3.1 upgrade guard the capabilities 0.4.0 cut.

## Chart (0.2.3 → 0.2.4)

- No value changes; `appVersion` only.

## Operator notes

- No migrations. Upgrade from 0.4.3, 0.4.2, or straight from 0.3.1.
- **Templates must change.** Any template whose later step reads a producer's output as a plain input must route it with `run_inputs` and `result:`. Reinstall the landing templates, compose first and then land; with only one of them updated the queue ejects entries or holds. A candidate composed before the upgrade is composed again.
- **Update `fly`** (`fly sync`): an older `fly` refuses a template that uses `result:`.
- **Do not roll back with Runs in flight.** A web older than 0.4.4 cannot read a Run whose template uses `result:`.
- The 0.4.3 note about raising `artifactDaemon.ttl` for resumed Runs no longer applies.
