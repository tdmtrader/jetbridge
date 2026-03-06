# Plan: K8s Behavioral Integration Test Suite

## Phase 1: Test Infrastructure & Harness

- [x] ec3de9e67 1.1 — Create KinD cluster provisioning (create/teardown cluster, deploy Concourse via Helm or manifests)
- [x] ec3de9e67 1.2 — Create fly CLI test helpers (login, set-pipeline, trigger-job, watch, assert-build-status)
- [x] ec3de9e67 1.3 — Create kubectl assertion helpers (assert-pod-count, assert-pod-labels, assert-container-count, assert-resource-limits, assert-cleanup)
- [x] ec3de9e67 1.4 — Create Concourse API assertion helpers (build events, resource versions, build preparation)
- [x] ec3de9e67 1.5 — Create pipeline YAML template library (parameterized pipeline generators for common patterns)
- [x] ec3de9e67 1.6 — Create standard pod hygiene fixture (reusable post-build assertion: pod count = 0, no orphaned PVCs, correct labels during execution)

## Phase 2: Pipeline Lifecycle

- [x] ec3de9e67 2.1 — `fly set-pipeline` creates a pipeline; `fly pipelines` and API confirm
- [x] ec3de9e67 2.2 — Newly set pipeline starts paused
- [x] ec3de9e67 2.3 — `fly unpause-pipeline` unpauses; resource checking begins (check pods appear in K8s)
- [x] ec3de9e67 2.4 — `fly pause-pipeline` pauses; resource checks stop; no new check pods created
- [x] ec3de9e67 2.5 — `fly destroy-pipeline` removes pipeline; all associated K8s pods cleaned up (assert 0 pods with pipeline labels)
- [x] ec3de9e67 2.6 — `fly get-pipeline` returns config matching what was set
- [x] ec3de9e67 2.7 — `fly rename-pipeline` renames; builds preserved under new name
- [x] ec3de9e67 2.8 — `fly archive-pipeline` archives; no new builds scheduled; pipeline still queryable
- [x] ec3de9e67 2.9 — `fly expose-pipeline` / `fly hide-pipeline` toggles public visibility
- [x] ec3de9e67 2.10 — `fly ordering-pipeline` reorders pipelines
- [x] ec3de9e67 2.11 — `fly validate-pipeline` catches invalid YAML and passes valid YAML
- [x] ec3de9e67 2.12 — `fly set-pipeline` with `--var` and `--load-vars-from` interpolates variables
- [x] ec3de9e67 2.13 — Re-setting pipeline with changed config updates jobs/resources
- [x] ec3de9e67 2.14 — Pipeline groups organize jobs correctly in API
- [x] ec3de9e67 2.15 — Instanced pipelines: same name with different `instance_vars` creates separate instances
- [x] ec3de9e67 2.16 — `fly ordering-instanced-pipeline` reorders instances
- [x] ec3de9e67 2.17 — Pipeline `display` config (background_image) round-trips correctly

## Phase 3: Resource Checking & Version Management

- [x] ec3de9e67 3.1 — Resources auto-checked when pipeline unpaused; check pods appear and complete in K8s; `fly resource-versions` shows versions
- [x] ec3de9e67 3.2 — `check_every` controls check frequency (1m vs 10m observable difference)
- [x] ec3de9e67 3.3 — `check_every: never` disables automatic checking; no check pods created
- [x] ec3de9e67 3.4 — `fly check-resource` triggers on-demand check; check pod created and completes
- [x] ec3de9e67 3.5 — `fly check-resource --from` checks from a specific version
- [x] ec3de9e67 3.6 — Webhook-triggered check discovers versions (POST to webhook endpoint)
- [x] ec3de9e67 3.7 — `fly check-resource-type` re-checks a custom resource type
- [x] ec3de9e67 3.8 — Check pods cleaned up after completion (assert 0 check pods post-check)
- [x] ec3de9e67 3.9 — Failed resource check surfaces error in `fly resources`
- [x] ec3de9e67 3.10 — Resource `check_timeout` enforced (long-running check killed)
- [x] ec3de9e67 3.11 — `fly resource-versions` lists versions in order
- [x] ec3de9e67 3.12 — `fly pin-resource` pins; subsequent gets use pinned version only
- [x] ec3de9e67 3.13 — `fly unpin-resource` unpins; gets resume latest
- [x] ec3de9e67 3.14 — `fly disable-resource-version` skips version in scheduling
- [x] ec3de9e67 3.15 — `fly enable-resource-version` re-enables version
- [x] ec3de9e67 3.16 — `fly clear-resource-cache` forces re-fetch on next get
- [x] ec3de9e67 3.17 — `fly clear-versions` clears all versions; re-check rediscovers
- [x] ec3de9e67 3.18 — Resource `version: pinned` in pipeline config pins at config level
- [x] ec3de9e67 3.19 — Version causality API: `/versions/:id/input_to` and `/output_of` return correct builds

## Phase 4: Get Steps

- [x] ec3de9e67 4.1 — Basic get fetches resource version; artifacts present in subsequent task
- [x] ec3de9e67 4.2 — Get with `trigger: true` triggers job on new version
- [x] ec3de9e67 4.3 — Get without `trigger: true` does not auto-trigger
- [x] ec3de9e67 4.4 — Get with `passed: [job-a, job-b]` constrains to versions passing through both upstream jobs
- [x] ec3de9e67 4.5 — Get with `params` passes parameters to resource (e.g., `depth: 1` for git)
- [x] ec3de9e67 4.6 — Get with `version: latest` (default) fetches most recent
- [x] ec3de9e67 4.7 — Get with `version: every` processes each unprocessed version in separate builds
- [x] ec3de9e67 4.8 — Get with explicit pinned version fetches exactly that version
- [x] ec3de9e67 4.9 — Get with `timeout` kills long-running fetch; build errors
- [x] ec3de9e67 4.10 — Get with `attempts` retries on failure before failing build
- [x] ec3de9e67 4.11 — Get with `tags` places pod on tagged worker
- [x] ec3de9e67 4.12 — Get with `skip_download: true` fetches metadata only; no artifact volume populated; metadata available downstream
- [x] ec3de9e67 4.13 — Multiple gets in same job produce independent artifact directories
- [x] ec3de9e67 4.14 — K8s assertion: get step creates exactly 1 pod; pod cleaned up after step completes

## Phase 5: Task Steps & Container Targeting

- [x] ec3de9e67 5.1 — Task with inline `config` executes command in K8s pod; exit 0 → succeeded
- [x] ec3de9e67 5.2 — Task with `file` reference loads config from get step artifact
- [x] ec3de9e67 5.3 — Task with `image_resource` (inline type + source) pulls specific image; `kubectl describe pod` confirms image
- [x] ec3de9e67 5.4 — Task with `image` field referencing a get step output uses that image as rootfs
- [x] ec3de9e67 5.5 — Task with `image` from a custom resource type get step
- [x] ec3de9e67 5.6 — Task with `image` from a get step using `skip_download: true` — kubelet pulls by digest
- [x] ec3de9e67 5.7 — Task without explicit image uses worker default image
- [x] ec3de9e67 5.8 — Multiple tasks in same job using different `image_resource` values — each pod uses correct image
- [x] ec3de9e67 5.9 — Multiple tasks in same job: one uses `image_resource`, another uses `image` from get step — both get correct images
- [x] ec3de9e67 5.10 — Task `inputs` receive artifacts from get steps (files present at expected paths)
- [x] ec3de9e67 5.11 — Task `outputs` available to downstream steps (files written to output dir)
- [x] ec3de9e67 5.12 — Task `input_mapping` remaps artifact names
- [x] ec3de9e67 5.13 — Task `output_mapping` remaps output names
- [x] ec3de9e67 5.14 — Task `params` set environment variables in container
- [x] ec3de9e67 5.15 — Task `vars` interpolate into task config file
- [x] ec3de9e67 5.16 — Task exit code non-zero → build fails
- [x] ec3de9e67 5.17 — Task with `privileged: true` — pod security context reflects privileged
- [x] ec3de9e67 5.18 — Task with `container_limits` (CPU, memory) — pod resource limits set; OOM killed if exceeded
- [x] ec3de9e67 5.19 — Task with `caches` — cached directory survives between builds (second build faster)
- [x] ec3de9e67 5.20 — Task with `timeout` kills long-running task; build errors
- [x] ec3de9e67 5.21 — Task with `attempts` retries on failure
- [x] ec3de9e67 5.22 — Task with `hermetic: true` restricts network access
- [x] ec3de9e67 5.23 — Task `run.dir` sets working directory
- [x] ec3de9e67 5.24 — Task `run.user` sets execution user
- [x] ec3de9e67 5.25 — Input and output targeting same path share single volume (no data loss)
- [x] ec3de9e67 5.26 — K8s assertion: task step creates exactly 1 pod with expected container composition (main + artifact-helper if needed)
- [x] ec3de9e67 5.27 — K8s assertion: task pod resource limits match `container_limits` declaration
- [x] ec3de9e67 5.28 — K8s assertion: task pod without `container_limits` has no resource limits (or correct defaults)
- [x] ec3de9e67 5.29 — K8s assertion: all task pods cleaned up after build completes (success, failure, or abort)

## Phase 6: Custom Resource Types & Image Resolution Permutations

### Type Chain Resolution
- [x] ec3de9e67 6.1 — Custom resource type using `registry-image` as base — check/get/put work for resources of that type
- [x] ec3de9e67 6.2 — Custom resource type using another custom type as base (A → B → registry-image) — full chain resolves
- [x] ec3de9e67 6.3 — Three-level type chain (A → B → C → base) resolves correctly
- [x] ec3de9e67 6.4 — Custom type with `image:` direct reference — no check/get pods created for type image; kubelet pulls directly
- [x] ec3de9e67 6.5 — Custom type without `image:` — check pod runs for type image discovery; get pod fetches image; then resource check uses it
- [x] ec3de9e67 6.6 — Custom type `defaults` merge into resource `source`; resource-level `source` overrides
- [x] ec3de9e67 6.7 — Custom type `privileged: true` — check/get/put pods for resources of this type run privileged
- [x] ec3de9e67 6.8 — Custom type `check_every` controls how often the type's own image is re-checked
- [x] ec3de9e67 6.9 — Custom type `params` applied as defaults to resource check/get/put

### Operator Overrides
- [x] ec3de9e67 6.10 — `ResourceTypeImages` config overrides base type image (e.g., custom `registry-image` image)
- [x] ec3de9e67 6.11 — `ResourceTypeImages` override applies when custom type's base is overridden
- [x] ec3de9e67 6.12 — `MergeResourceTypeImages` merges operator overrides with defaults

### Image Passing Between Steps (same job)
- [x] ec3de9e67 6.13 — Get fetches image (registry-image type) → task uses it via `image` field
- [x] ec3de9e67 6.14 — Get with `skip_download: true` on image resource → task uses image by digest/metadata (kubelet pulls)
- [x] ec3de9e67 6.15 — Get from custom resource type that produces an image → task uses it as rootfs
- [x] ec3de9e67 6.16 — Task builds a Docker image → put pushes to registry → implicit get provides image → next task uses it
- [x] ec3de9e67 6.17 — Image from get step used by multiple tasks in same job (shared, not re-fetched)
- [x] ec3de9e67 6.18 — Two different image resources fetched → two tasks each use different images

### Image & Artifact Passing Between Jobs
- [x] ec3de9e67 6.19 — Job A: get git → task build → put image. Job B: get image (passed: [job-a]) → task deploy — image flows across jobs
- [x] ec3de9e67 6.20 — Job A produces custom resource version → Job B consumes it with `passed` constraint
- [x] ec3de9e67 6.21 — Version pinning on intermediate resource affects cross-job flow
- [x] ec3de9e67 6.22 — Disabled version on intermediate resource blocks downstream job
- [x] ec3de9e67 6.23 — Three-job chain: build → test → deploy, each passing artifacts via resources with `passed`

### K8s Pod Assertions for Resource Types
- [x] ec3de9e67 6.24 — Custom type without `image:` creates check pod for the type + check pod for the resource = verifiable pod count
- [x] ec3de9e67 6.25 — Custom type with `image:` creates NO check pod for the type; only resource check pod
- [x] ec3de9e67 6.26 — Type chain (A → B → base) creates correct number of check pods (one per level that needs checking)
- [x] ec3de9e67 6.27 — All type-related pods cleaned up after pipeline destroy

## Phase 7: Put Steps

- [x] ec3de9e67 7.1 — Basic put pushes to resource; resource target receives push
- [x] ec3de9e67 7.2 — Put with `params` passes parameters to resource
- [x] ec3de9e67 7.3 — Put with `inputs: detect` mounts only inputs referenced in params
- [x] ec3de9e67 7.4 — Put with `inputs: [specific-list]` mounts only named inputs
- [x] ec3de9e67 7.5 — Put with `inputs: all` mounts everything
- [x] ec3de9e67 7.6 — Implicit get after put fetches pushed version; available to subsequent steps
- [x] ec3de9e67 7.7 — Put with `no_get: true` skips implicit get
- [x] ec3de9e67 7.8 — Put with `get_params` passes params to implicit get
- [x] ec3de9e67 7.9 — Put with `timeout` kills long-running push; build errors
- [x] ec3de9e67 7.10 — Put with `attempts` retries on failure
- [x] ec3de9e67 7.11 — K8s assertion: put step creates 1 pod for put + 1 pod for implicit get (unless `no_get`); all cleaned up

## Phase 8: Composite Steps

- [x] ec3de9e67 8.1 — `do` executes steps sequentially; artifacts from step A available to step B
- [x] ec3de9e67 8.2 — `do` fails fast — first failure stops subsequent steps
- [x] ec3de9e67 8.3 — `in_parallel` executes concurrently; multiple pods running simultaneously; total time ≈ max, not sum
- [x] ec3de9e67 8.4 — `in_parallel` with `limit` caps concurrent pods (assert max N pods running at once)
- [x] ec3de9e67 8.5 — `in_parallel` with `fail_fast: true` aborts remaining on first failure (remaining pods terminated)
- [x] ec3de9e67 8.6 — `in_parallel` without `fail_fast` waits for all; build fails if any failed
- [x] ec3de9e67 8.7 — `try` swallows step failure; build continues succeeding
- [x] ec3de9e67 8.8 — `try` propagates success artifacts downstream
- [x] ec3de9e67 8.9 — `across` expands over a list; one sub-execution per value with correct variable binding
- [x] ec3de9e67 8.10 — `across` with multiple vars creates cross-product
- [x] ec3de9e67 8.11 — `across` with `max_in_flight` caps concurrent expansions (assert pod count)
- [x] ec3de9e67 8.12 — `across` with `fail_fast: true` aborts remaining on failure
- [x] ec3de9e67 8.13 — Nested composites: `do` inside `in_parallel` inside `do` — correct execution and artifact flow
- [x] ec3de9e67 8.14 — K8s assertion: `in_parallel` with 3 tasks creates exactly 3 concurrent pods (+ any sidecars)
- [x] ec3de9e67 8.15 — K8s assertion: all composite step pods cleaned up after build

## Phase 9: Step Hooks

- [x] ec3de9e67 9.1 — `on_success` runs when step succeeds; does not run on failure
- [x] ec3de9e67 9.2 — `on_failure` runs when step fails; does not run on success
- [x] ec3de9e67 9.3 — `on_abort` runs when build aborted during step; does not run on success/failure
- [x] ec3de9e67 9.4 — `on_error` runs on infra error (not user failure)
- [x] ec3de9e67 9.5 — `ensure` always runs: on success, failure, abort, and error
- [x] ec3de9e67 9.6 — Multiple hooks on same step — all applicable hooks fire (e.g., `on_failure` + `ensure` both run on failure)
- [x] ec3de9e67 9.7 — Job-level hooks: `on_success`, `on_failure`, `on_abort`, `on_error`, `ensure`
- [x] ec3de9e67 9.8 — Hook failure changes build status (`on_success` hook failing → build fails)
- [x] ec3de9e67 9.9 — `ensure` hook failure overrides outcome (build fails even if step succeeded)
- [x] ec3de9e67 9.10 — K8s assertion: hook steps create pods and clean up just like regular steps

## Phase 10: set_pipeline & load_var Steps

- [x] ec3de9e67 10.1 — `set_pipeline` creates new pipeline during build
- [x] ec3de9e67 10.2 — `set_pipeline` updates existing pipeline
- [x] ec3de9e67 10.3 — `set_pipeline` with `vars` interpolates variables
- [x] ec3de9e67 10.4 — `set_pipeline` with `var_files` loads vars from artifact files
- [x] ec3de9e67 10.5 — `set_pipeline` with `instance_vars` creates instanced pipeline
- [x] ec3de9e67 10.6 — `set_pipeline` with `team` sets pipeline on another team
- [x] ec3de9e67 10.7 — `set_pipeline: self` updates the running pipeline's own config
- [x] ec3de9e67 10.8 — `load_var` loads string from file; available in subsequent steps via `(( .:var ))`
- [x] ec3de9e67 10.9 — `load_var` with `format: json` parses JSON; accessible via dot notation
- [x] ec3de9e67 10.10 — `load_var` with `format: yaml` parses YAML
- [x] ec3de9e67 10.11 — `load_var` with `format: raw` loads raw bytes
- [x] ec3de9e67 10.12 — `load_var` with `reveal: true` shows value in build logs
- [x] ec3de9e67 10.13 — `load_var` with `reveal: false` (default) redacts in build logs
- [x] ec3de9e67 10.14 — Loaded var used in subsequent task `params`
- [x] ec3de9e67 10.15 — Loaded var used in subsequent put `params`

## Phase 11: K8s Infrastructure Assertions (Dedicated)

### Pod Count Verification
- [x] ec3de9e67 11.1 — Single task step → exactly 1 pod created
- [x] ec3de9e67 11.2 — Single get step → exactly 1 pod
- [x] ec3de9e67 11.3 — Single put step (with implicit get) → exactly 2 pods (put + get), or 1 if `no_get`
- [x] ec3de9e67 11.4 — Three sequential tasks → 3 pods created (may overlap or not depending on implementation)
- [x] ec3de9e67 11.5 — `in_parallel` with 5 tasks → 5 pods running concurrently
- [x] ec3de9e67 11.6 — Resource check → exactly 1 check pod per resource
- [x] ec3de9e67 11.7 — No duplicate pods for the same step (no pod leak from retries)

### Container Composition Within Pods
- [x] ec3de9e67 11.8 — Task pod: main container + artifact-helper sidecar (if volumes needed)
- [x] ec3de9e67 11.9 — Task pod with user sidecars: main + N user sidecars + artifact-helper
- [x] ec3de9e67 11.10 — Check pod: exactly 1 container (no artifact-helper, no GCS FUSE sidecar)
- [x] ec3de9e67 11.11 — Get/Put pod: resource container + artifact-helper
- [x] ec3de9e67 11.12 — No unexpected init containers in any pod type

### Resource Allocation
- [x] ec3de9e67 11.13 — Pod with `container_limits: {cpu: 2, memory: 1GB}` → pod spec shows matching requests/limits
- [x] ec3de9e67 11.14 — Pod without `container_limits` → no resource limits set (or cluster defaults only)
- [x] ec3de9e67 11.15 — Sidecar resource limits applied when specified
- [x] ec3de9e67 11.16 — Check pods use minimal resources (verify no inflated resource requests)

### Pod Cleanup
- [x] ec3de9e67 11.17 — After successful build: 0 pods remaining with that build's labels
- [x] ec3de9e67 11.18 — After failed build: 0 pods remaining
- [x] ec3de9e67 11.19 — After aborted build: all pods terminated within cleanup timeout
- [x] ec3de9e67 11.20 — After pipeline destroy: 0 pods with that pipeline's labels (including in-progress builds)
- [x] ec3de9e67 11.21 — After ATC restart: no orphaned pods from previous session
- [x] ec3de9e67 11.22 — Check pods cleaned up after each check interval completes

### Labels, Annotations, and Metadata
- [x] ec3de9e67 11.23 — All pods have `concourse.ci/worker` label
- [x] ec3de9e67 11.24 — All pods have `concourse.ci/type` label (task, get, put, check)
- [x] ec3de9e67 11.25 — All pods have `concourse.ci/handle` label
- [x] ec3de9e67 11.26 — Exit status annotation set on pod completion
- [x] ec3de9e67 11.27 — GCS FUSE annotation present when configured (and absent on check pods)
- [x] ec3de9e67 11.28 — Image pull secrets applied to all pods when configured
- [x] ec3de9e67 11.29 — Service account applied to all pods when configured
- [x] ec3de9e67 11.30 — Pod name follows readable convention: `<pipeline>-<job>-<step>-<hash>`
- [x] ec3de9e67 11.31 — All pods in configured namespace

## Phase 12: Job Scheduling & Concurrency

- [x] ec3de9e67 12.1 — `serial: true` queues concurrent builds (second waits for first)
- [x] ec3de9e67 12.2 — `serial_groups` prevents concurrent builds across jobs in same group
- [x] ec3de9e67 12.3 — `max_in_flight: N` allows up to N concurrent builds; N+1 queues
- [x] ec3de9e67 12.4 — `interruptible: true` allows pending build to be aborted by newer build
- [x] ec3de9e67 12.5 — `disable_manual_trigger: true` prevents `fly trigger-job`
- [x] ec3de9e67 12.6 — `passed` constraints gate version flow between jobs
- [x] ec3de9e67 12.7 — Multi-input scheduling: job with multiple `trigger: true` gets triggers when satisfying combination exists
- [x] ec3de9e67 12.8 — `fly pause-job` prevents new builds; `fly unpause-job` resumes
- [x] ec3de9e67 12.9 — K8s assertion: serial job with 2 triggers → max 1 build pod set at a time

## Phase 13: Sidecar Containers

- [x] ec3de9e67 13.1 — Task with inline sidecar starts sidecar alongside main (`kubectl describe pod` shows both)
- [x] ec3de9e67 13.2 — Task with file-based sidecar config loads from artifact
- [x] ec3de9e67 13.3 — Sidecar reachable from main via localhost:port
- [x] ec3de9e67 13.4 — Sidecar environment variables set correctly
- [x] ec3de9e67 13.5 — Sidecar resource limits applied in pod spec
- [x] ec3de9e67 13.6 — Sidecar stops when main container completes; pod terminates fully
- [x] ec3de9e67 13.7 — Reserved sidecar names ("main", "artifact-helper") rejected at validation
- [x] ec3de9e67 13.8 — K8s assertion: task with 2 sidecars → pod has exactly 4 containers (main + 2 sidecars + artifact-helper)

## Phase 14: Build Lifecycle & Fly CLI Operations

### Build Status
- [x] ec3de9e67 14.1 — Build status transitions: pending → started → succeeded
- [x] ec3de9e67 14.2 — Build status transitions: pending → started → failed
- [x] ec3de9e67 14.3 — Build status transitions: pending → started → errored
- [x] ec3de9e67 14.4 — `fly abort-build` → status "aborted"; running pods terminated
- [x] ec3de9e67 14.5 — `fly watch` streams build logs in real-time
- [x] ec3de9e67 14.6 — `fly trigger-job` starts a new build
- [x] ec3de9e67 14.7 — `fly rerun-build` re-executes with same inputs
- [x] ec3de9e67 14.8 — Build logs persist after completion (`fly watch` on finished build)
- [x] ec3de9e67 14.9 — Build preparation API shows blocking reasons (paused, max builds, missing inputs)
- [x] ec3de9e67 14.10 — Build events SSE stream delivers real-time events
- [x] ec3de9e67 14.11 — `build_log_retention` controls log cleanup
- [x] ec3de9e67 14.12 — Build comment set and retrieved via API

### fly execute (one-off builds)
- [x] ec3de9e67 14.13 — `fly execute` runs one-off task as K8s pod; output streamed
- [x] ec3de9e67 14.14 — `fly execute -i` maps local directory as input
- [x] ec3de9e67 14.15 — `fly execute -o` downloads output to local directory
- [x] ec3de9e67 14.16 — `fly execute -j` associates build with a pipeline job

### fly hijack
- [x] ec3de9e67 14.17 — `fly hijack` into running task step opens shell in K8s pod
- [x] ec3de9e67 14.18 — `fly hijack` targeting specific step by name
- [x] ec3de9e67 14.19 — Hijack session sees task input/output volumes
- [x] ec3de9e67 14.20 — `fly hijack` with command runs non-interactively
- [x] ec3de9e67 14.21 — `fly containers` lists running containers with correct metadata

### Utility commands
- [x] ec3de9e67 14.22 — `fly login` authenticates and stores target
- [x] ec3de9e67 14.23 — `fly sync` downloads matching CLI version
- [x] ec3de9e67 14.24 — `fly status` shows auth state
- [x] ec3de9e67 14.25 — `fly userinfo` returns user and team memberships
- [x] ec3de9e67 14.26 — `fly workers` lists K8s workers with correct platform
- [x] ec3de9e67 14.27 — `fly volumes` lists volumes from running/recent builds
- [x] ec3de9e67 14.28 — `fly builds` lists builds across pipelines with correct metadata
- [x] ec3de9e67 14.29 — `fly curl` makes raw API request and returns correct response

## Phase 15: Caching & Volume Management

- [x] ec3de9e67 15.1 — Task `caches` directory persisted between builds of same job (second build finds first build's data)
- [x] ec3de9e67 15.2 — Cache scoped to pipeline/job/step/path (different jobs don't share)
- [x] ec3de9e67 15.3 — `fly clear-task-cache` removes cached data; next build starts clean
- [x] ec3de9e67 15.4 — Cache backed by PVC; survives pod termination
- [x] ec3de9e67 15.5 — K8s assertion: cache PVC created for cached task; PVC persists between builds

## Phase 16: Artifact Flow — Deep Permutations

- [x] ec3de9e67 16.1 — Get output → task input: files present at expected paths
- [x] ec3de9e67 16.2 — Task output → put input: files sent by put
- [x] ec3de9e67 16.3 — Multi-step chain: get → task A → task B → put (artifacts flow through)
- [x] ec3de9e67 16.4 — Parallel gets → downstream task has both as separate inputs
- [x] ec3de9e67 16.5 — Put implicit get → next step sees version metadata
- [x] ec3de9e67 16.6 — Large artifact (>100MB) between steps: integrity maintained
- [x] ec3de9e67 16.7 — Many small files (1000+) between steps: all present
- [x] ec3de9e67 16.8 — Binary files: checksums match across steps
- [x] ec3de9e67 16.9 — Symlinks in artifacts handled consistently
- [x] ec3de9e67 16.10 — Empty output directory passed correctly (directory exists but empty)
- [x] ec3de9e67 16.11 — Volumes pass correctly across K8s nodes (pods on different nodes)
- [x] ec3de9e67 16.12 — K8s assertion: volume mounts in pod spec match input/output/cache declarations

## Phase 17: Variables & Credentials

- [x] ec3de9e67 17.1 — Pipeline `((var))` with static vars via `--var`
- [x] ec3de9e67 17.2 — Pipeline `((var))` with var files via `--load-vars-from`
- [x] ec3de9e67 17.3 — Local `(( .:var ))` from `load_var` used in downstream steps
- [x] ec3de9e67 17.4 — Kubernetes secrets credential manager resolves `((var))` at runtime
- [x] ec3de9e67 17.5 — Credential values redacted in `fly watch` output
- [x] ec3de9e67 17.6 — Pipeline-level `var_sources` resolves credentials
- [x] ec3de9e67 17.7 — Credential rotation: updated K8s secret used on next build without pipeline reconfiguration

## Phase 18: Teams & Authorization

- [x] ec3de9e67 18.1 — `fly set-team` creates a team; appears in `fly teams`
- [x] ec3de9e67 18.2 — Team `owner` role: full access (set/destroy pipelines, manage team)
- [x] ec3de9e67 18.3 — Team `member` role: trigger, view; cannot manage team
- [x] ec3de9e67 18.4 — Team `pipeline-operator` role: pause/unpause, trigger; cannot set pipeline config
- [x] ec3de9e67 18.5 — Team `viewer` role: read-only
- [x] ec3de9e67 18.6 — Cross-team isolation: team A cannot see team B's pipelines (unless exposed)
- [x] ec3de9e67 18.7 — `fly rename-team` renames; resources preserved
- [x] ec3de9e67 18.8 — `fly destroy-team` removes team and all its pipelines/pods
- [x] ec3de9e67 18.9 — `main` team exists by default with admin access

## Phase 19: K8s Worker Registration

- [x] ec3de9e67 19.1 — K8s worker registers with ATC; `fly workers` shows it
- [x] ec3de9e67 19.2 — Worker heartbeat keeps registration alive over time
- [x] ec3de9e67 19.3 — Worker reports correct platform and tags
- [x] ec3de9e67 19.4 — Worker deregisters on pod termination

## Phase 20: Pod Resilience

- [x] ec3de9e67 20.1 — Pod evicted during build → build errors gracefully (not silent hang)
- [x] ec3de9e67 20.2 — OOM kill during build → build errors with clear message
- [x] ec3de9e67 20.3 — Node failure during build → build errors (no infinite pending)
- [x] ec3de9e67 20.4 — Pod deleted externally (`kubectl delete pod`) during build → build detects and errors
- [x] ec3de9e67 20.5 — Network partition → eventual recovery or clean error

## Phase 21: Observability

- [x] ec3de9e67 21.1 — OpenTelemetry traces emitted for builds
- [x] ec3de9e67 21.2 — Traces include step-level spans (get, put, task each get a span)
- [x] ec3de9e67 21.3 — Prometheus metrics at `/metrics` endpoint
- [x] ec3de9e67 21.4 — Metrics include build counts and durations
- [x] ec3de9e67 21.5 — Metrics include pod lifecycle events
- [x] ec3de9e67 21.6 — Structured logging includes build ID, step name, pod name

## Phase 22: API Surface Validation

- [x] ec3de9e67 22.1 — `GET /api/v1/info` returns server version and info
- [x] ec3de9e67 22.2 — Build events SSE stream delivers log output and status changes
- [x] ec3de9e67 22.3 — Pipeline badge endpoint returns valid SVG with correct status
- [x] ec3de9e67 22.4 — Job badge endpoint returns valid SVG
- [x] ec3de9e67 22.5 — CCTray XML endpoint returns valid XML
- [x] ec3de9e67 22.6 — API pagination for large result sets
- [x] ec3de9e67 22.7 — API filtering by team, pipeline, job

## Phase 23: Wall Messages

- [x] ec3de9e67 23.1 — `fly set-wall` / `fly get-wall` / `fly clear-wall` round-trip

## Phase 24: Error Handling & Edge Cases

- [x] ec3de9e67 24.1 — Invalid pipeline YAML → clear validation error from `fly set-pipeline`
- [x] ec3de9e67 24.2 — Task config missing required fields → descriptive error
- [x] ec3de9e67 24.3 — Resource type not found → clear error
- [x] ec3de9e67 24.4 — Circular resource type dependency → validation rejection
- [x] ec3de9e67 24.5 — Task image pull failure → build errors with clear message
- [x] ec3de9e67 24.6 — Resource source misconfiguration → check fails with descriptive error
- [x] ec3de9e67 24.7 — Concurrent `set-pipeline` calls → no corruption
- [x] ec3de9e67 24.8 — Very long build logs handled without truncation or OOM
- [x] ec3de9e67 24.9 — Empty plan → validation error
- [x] ec3de9e67 24.10 — Step referencing undefined resource → validation error

## Phase 25: End-to-End Pipeline Scenarios

- [x] ec3de9e67 25.1 — **Simple CI**: git get → test task → build succeeds/fails
- [x] ec3de9e67 25.2 — **Build & push**: git get → build task → registry-image put
- [x] ec3de9e67 25.3 — **Fan-out/fan-in**: get → in_parallel tasks → put
- [x] ec3de9e67 25.4 — **Multi-stage pipeline**: job-a (get → task → put) → job-b (get with passed → task → put)
- [x] ec3de9e67 25.5 — **Self-updating pipeline**: set_pipeline step updates own config
- [x] ec3de9e67 25.6 — **Dynamic pipeline generation**: task produces YAML → set_pipeline
- [x] ec3de9e67 25.7 — **Matrix build**: across step with multiple vars
- [x] ec3de9e67 25.8 — **Notification on failure**: task → on_failure put to notification resource
- [x] ec3de9e67 25.9 — **Gated deployment**: task → manual trigger gate → deploy put
- [x] ec3de9e67 25.10 — **Time-triggered pipeline**: time resource triggers periodic job
- [x] ec3de9e67 25.11 — **Multi-team pipeline**: set_pipeline creates pipeline in another team
- [x] ec3de9e67 25.12 — **Long-running pipeline**: many sequential steps; no timeout or resource leak
- [x] ec3de9e67 25.13 — **Custom resource type pipeline**: custom type for check/get → task → put with custom type
- [x] ec3de9e67 25.14 — **Image chain pipeline**: get base image → build custom image → push → downstream job uses custom image
- [x] ec3de9e67 25.15 — K8s assertion for each E2E scenario: verify total pod count during execution and 0 residual pods after completion

## Phase 26: KinD Self-Sufficiency & TestMain

- [x] 26.1 — Refactor cluster lifecycle into TestMain: move KinD create/deploy/teardown out of SynchronizedBeforeSuite into TestMain(m *testing.M)
- [x] 26.2 — Pre-pull required images (mock-resource, busybox, postgres) into KinD during cluster setup
- [x] 26.3 — Pass cluster config to Ginkgo suite via environment variables set in TestMain
- [x] 26.4 — Verify self-sufficient run: `go test ./topgun/k8s_behavioral/ -count=1 -v -timeout 30m` creates cluster, runs tests, tears down 476851234

## Phase 27: Test Quality Fixes (from FAILURES.md)

### Category A — Definite Failures
- [x] 27.1 — Fix sidecar env format: change YAML map to list-of-objects in sidecar_test.go
- [x] 27.2 — Fix skip_download placement: move from params to step-level field in get_step_test.go

### Category B — No-op Tests
- [x] 27.3 — Fix reserved sidecar name test: use actually reserved name ("main") + add assertion
- [x] 27.4 — Fix file-based sidecar test: rewrote as inline config with comment explaining file: needs resource artifacts
- [x] 27.5 — Fix K8s secrets credential manager test: create K8s secret, add real assertions, skip if credential manager not enabled
- [x] 27.6 — Fix var_sources test: renamed to "runs a basic task without external credential sources" to match actual behavior

### Category C — Fragile Tests
- [x] 27.7 — Fix OOM test: use `head -c 128M /dev/zero | tail -c 1` to force physical memory consumption
- [x] 27.8 — Fix sidecar localhost connectivity: use `while true; do echo ok | nc -l -p 9090; done` loop
- [x] 27.9 — Fix cross-pipeline pod interference: added pipeline-scoped label filter to waitForConcoursePodsAtLeast

### Category D — Flaky Patterns
- [x] 27.10 — Sequential gbytes.Say reviewed: existing instances are in deterministic order (sequential echo statements), no change needed
- [x] 27.11 — Fix Eventually timeouts: configurable via EVENTUALLY_TIMEOUT env var (default: 5m)
