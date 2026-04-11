# Fly CLI Behavioral Specification

**Track:** `fly_cli_behavioral_spec_20260331`
**Type:** docs
**Status:** active

## Overview

This specification defines the observable behavioral contract for Concourse's `fly` CLI — the primary user interface. The fly CLI has 63 commands covering pipeline management, build execution, resource management, team/auth, worker/container inspection, and utilities. The integration test suite at `fly/integration/` validates each command against a mock ATC server using ghttp.

### Scope

- All 63 fly CLI commands and their integration test coverage
- Command-level behavioral requirements (what the command does, what HTTP requests it makes)
- Missing command test coverage (untested commands)
- Test infrastructure patterns (ghttp mock server, auth setup)

### Out of Scope

- Server-side API handler logic — covered by atc/api/ specs
- Fly command implementation details — only testing observable CLI behavior
- go-concourse client library internals
- Web UI behavior

---

## Section 1: Pipeline Management Commands (16 requirements)

### PM-01: set-pipeline creates/updates pipeline configuration
The `fly set-pipeline` command MUST POST the pipeline configuration to the ATC, support variable interpolation, instance vars, dry-run mode, and credential checking.

### PM-02: get-pipeline retrieves pipeline configuration
The `fly get-pipeline` command MUST GET the pipeline configuration and output it as YAML.

### PM-03: pipelines lists configured pipelines
The `fly pipelines` command MUST list all pipelines for the team, supporting `--all` for cross-team and `--json` for JSON output.

### PM-04: paused-pipelines lists paused pipelines
The `fly paused-pipelines` command MUST list only paused pipelines, filtering from the pipeline list, supporting `--all` and `--json`.

### PM-05: destroy-pipeline removes a pipeline
The `fly destroy-pipeline` command MUST DELETE the pipeline, requiring confirmation unless `--non-interactive` is set.

### PM-06: pause-pipeline pauses a pipeline
The `fly pause-pipeline` command MUST PUT to pause the pipeline.

### PM-07: unpause-pipeline unpauses a pipeline
The `fly unpause-pipeline` command MUST PUT to unpause the pipeline.

### PM-08: archive-pipeline archives a pipeline
The `fly archive-pipeline` command MUST PUT to archive the pipeline, requiring confirmation.

### PM-09: expose-pipeline makes pipeline public
The `fly expose-pipeline` command MUST PUT to expose the pipeline to public view.

### PM-10: hide-pipeline hides pipeline from public
The `fly hide-pipeline` command MUST PUT to hide the pipeline from public view.

### PM-11: rename-pipeline renames a pipeline
The `fly rename-pipeline` command MUST PUT to rename the pipeline.

### PM-12: order-pipelines sets pipeline ordering
The `fly order-pipelines` command MUST PUT the pipeline ordering.

### PM-13: order-instanced-pipelines orders within instance group
The `fly order-instanced-pipelines` command MUST PUT the ordering for instanced pipelines within a group.

### PM-14: validate-pipeline validates config locally
The `fly validate-pipeline` command MUST parse and validate the pipeline YAML without contacting the server.

### PM-15: format-pipeline formats config
The `fly format-pipeline` command MUST parse and re-format the pipeline YAML.

### PM-16: checklist prints checkfile
The `fly checklist` command MUST output a Checkfile format of the pipeline's jobs and resources.

---

## Section 2: Build & Execution Commands (6 requirements)

### BE-01: execute runs one-off build
The `fly execute` command MUST upload task config and inputs, create a one-off build, and stream build output.

### BE-02: builds lists builds
The `fly builds` command MUST list builds with pagination, filtering by pipeline/job, and support `--json`.

### BE-03: abort-build aborts a running build
The `fly abort-build` command MUST PUT to abort the specified build.

### BE-04: trigger-job triggers a job build
The `fly trigger-job` command MUST POST to create a new build for the job, optionally watching output.

### BE-05: watch streams build output
The `fly watch` command MUST stream server-sent events from a build and render them.

### BE-06: rerun-build reruns a build
The `fly rerun-build` command MUST POST to rerun a specific build of a job, optionally watching output.

---

## Section 3: Resource Management Commands (8 requirements)

### RM-01: resources lists pipeline resources
The `fly resources` command MUST list resources in a pipeline with their check status.

### RM-02: resource-versions lists resource versions
The `fly resource-versions` command MUST list versions of a resource with pagination.

### RM-03: check-resource triggers resource check
The `fly check-resource` command MUST POST to trigger a check for the resource.

### RM-04: check-resource-type triggers type check
The `fly check-resource-type` command MUST POST to trigger a check for the resource type.

### RM-05: pin-resource pins a version
The `fly pin-resource` command MUST PUT to pin a specific version to a resource.

### RM-06: unpin-resource unpins a resource
The `fly unpin-resource` command MUST PUT to unpin the resource.

### RM-07: enable/disable-resource-version toggles version
The `fly enable-resource-version` and `fly disable-resource-version` commands MUST PUT to toggle version enabled state.

### RM-08: clear-versions clears resource versions
The `fly clear-versions` command MUST DELETE resource versions with confirmation.

---

## Section 4: Team & Auth Commands (8 requirements)

### TA-01: login authenticates with target
The `fly login` command MUST authenticate via password grant or SSO browser flow, store the token in `.flyrc`.

### TA-02: logout releases authentication
The `fly logout` command MUST remove the stored token for the target.

### TA-03: status shows login status
The `fly status` command MUST show whether the user is authenticated with the target.

### TA-04: teams lists teams
The `fly teams` command MUST list all teams.

### TA-05: set-team creates/modifies team
The `fly set-team` command MUST PUT team configuration including auth methods (OIDC, GitHub, etc.).

### TA-06: destroy-team removes a team
The `fly destroy-team` command MUST DELETE the team, requiring confirmation.

### TA-07: active-users lists active users
The `fly active-users` command MUST GET users who have logged in since a given date.

### TA-08: userinfo shows current user
The `fly userinfo` command MUST GET and display the current user's information.

---

## Section 5: Infrastructure Commands (5 requirements)

### IC-01: workers lists registered workers
The `fly workers` command MUST list workers with their state, platform, and resource info.

### IC-02: containers lists active containers
The `fly containers` command MUST list active containers across workers.

### IC-03: volumes lists active volumes
The `fly volumes` command MUST list active volumes across workers.

### IC-04: hijack executes in container
The `fly hijack` command MUST select a container and exec a command in it via WebSocket.

### IC-05: clear-task-cache clears task cache
The `fly clear-task-cache` command MUST DELETE the task cache for a job's step.

---

## Section 6: Utility Commands (6 requirements)

### UC-01: targets lists saved targets
The `fly targets` command MUST list all saved targets from `.flyrc`.

### UC-02: sync downloads matching fly binary
The `fly sync` command MUST download the fly binary matching the target's ATC version.

### UC-03: version prints fly version
The `fly version` command MUST print the current fly binary version.

### UC-04: curl proxies API requests
The `fly curl` command MUST make authenticated HTTP requests to the ATC API.

### UC-05: wall message management
The `fly set-wall`, `fly get-wall`, and `fly clear-wall` commands MUST manage the global wall message.

### UC-06: schedule-job requests scheduler run
The `fly schedule-job` command MUST POST to request the scheduler to run for a specific job.

---

## Section 7: Job Management Commands (3 requirements)

### JM-01: jobs lists pipeline jobs
The `fly jobs` command MUST list jobs in a pipeline.

### JM-02: pause/unpause-job toggles job state
The `fly pause-job` and `fly unpause-job` commands MUST PUT to toggle the job's paused state.

### JM-03: paused-jobs lists paused jobs
The `fly paused-jobs` command MUST list only paused jobs in a pipeline, filtering from the job list.
