# JetBridge 0.3.1

JetBridge 0.3.1 is the first release of the 0.3 line: `VERSION` was declared 0.3.0 on 2026-08-10 but 0.3.0 was never tagged or released, and the previous release, v0.2.246 (2026-08-07), was cut from a lineage that has since diverged, so these notes cover the whole 0.3 line so far, 232 commits on `core` from 3076a54f4e to 9444818926. The line adds pipeline templates with numbered, parameterised runs (`fly run-pipeline`, `fly runs`, a runs page in the web UI); closes a series of filesystem-containment and authentication defects in the artifact daemon; adds an optional durable tier for resource caches ("Hangar") on S3, GCS or a shared filesystem; removes the agentic surface inherited from the 0.2 line (agent review/feedback API and UI, the MCP endpoint, `forge/`, `ci-agent/`); moves the unit-test suites from generated fakes to a real Postgres; and rebuilds the chart and release pipeline so that what is released is what was tested.

## Highlights

- Pipeline templates and numbered runs. A pipeline marked `template: true` declares typed parameters and optional run retention; `fly run-pipeline` materialises an ordinary instanced pipeline at `{run: N}` with a durable run header, run status derived from its builds, and reclamation of the payload under `run_retention` (6ff22356a9, 91128a41e6, ea153c1940).
- Artifact daemon containment. Tar extraction, every request-derived path and every filesystem operation in the daemon now go through `os.Root` handles; six request-boundary escapes (including unauthenticated delete and write outside the store via percent-encoded traversal) are closed and regression-tested (ab1c66c2c5, 36b3c3df2a, c76e4ac4a1).
- Resolve-capability authentication restored. `POST /resolve` and `/resolve-batch` had no authentication on this branch; the daemon now verifies HMAC capabilities bound to key, destination and expiry, the ATC signs them, and the chart wires one Secret to both sides (c8c54faf99, 1e023e7ca4, 56834e1db5).
- Durable resource caches. Resource caches are named by content and can be promoted to S3, GCS or a shared filesystem, restored on a node-local miss, and reclaimed by class and age from inside JetBridge (b49380d2ff, 860e07aa6f, 154ec545f2).
- Agentic surface removed, including an MCP endpoint that was authenticated but never authorised: any holder of a valid token could set, pause or trigger pipelines on any team and abort any build by id (35c6564639, 1ad9946000, 546f96a459).
- Chart and release pipeline made honest. The chart no longer renders flags the binary rejects at startup, the ATC now actually emits Prometheus metrics on a dedicated port, and `release` promotes the exact image the tests ran against instead of rebuilding one (582c85355f, d398db783d, 0952f8b162).

## Pipeline templates and numbered runs

A template is a base pipeline that declares run parameters and optional shell retention. Starting a run materialises an ordinary instanced pipeline at `{run: N}`; while active it uses normal checking, scheduling, build execution and resource semantics, and a small durable run header supplies numbering, lifecycle, history and ownership after the payload is reclaimed. Design: `docs/superpowers/specs/2026-08-19-pipeline-templates-numbered-runs-compact-design.md` (2cb47ce772).

Configuration (6ff22356a9, fb25c8fe42, a3f812b8c6):

- `template: true` on a base pipeline. Template-only fields are refused on ordinary pipelines; a template must have at least one entry job (a job with no `passed:` on any input); an instanced template is refused at save.
- `params`: typed `string`, `number`, `bool` and typed `enum`, each with optional `required`, `default`, `description` and `values`. Names match a charset (no dots), cannot be `run` or `run_id`, and `required` and `default` are mutually exclusive. Defaults and enum members must match the declared type; the numeric domain rejects NaN/Inf.
- `run_retention.keep_last` and `run_retention.ttl_days`, positive and bounded.
- `((run))` and `((run_id))` are reserved and override same-named user input. Declared parameters are excluded from credential evaluation, so `fly set-pipeline --check-creds` no longer reports them as missing credentials and a colliding `-v` value cannot bake a constant over a declaration. A `((param))` in a map key, or in another parameter's name or default, is refused because substitution there is not a value (1abaf88c36).
- Materialisation resolves the params, sets the payload's `template` to false, and clears `trigger: true` on gets without `passed:` so external versions cannot start extra builds inside a run (91128a41e6, 441ab84af2).

Run lifecycle (ea153c1940, 6c0f2f606b, bb0b6ea126, f7cf071454):

- Run numbers are monotonic per template and allocated under the template lock in the same transaction that inserts the header, saves the payload and creates a pending build for every entry job; any failure rolls all of it back. Creating a run from a paused or archived template answers 409 and allocates no number.
- Status is `running`, `succeeded`, `failed`, `errored` or `aborted`, derived from the builds of an expected-job set computed once from the materialised graph. The completion predicate runs on every scheduler pass that consumes a request for a run job and is a no-op unless the run is quiescent (6b8f3b5a80, a107ff233a). A manual trigger or rerun is the only terminal-to-running transition.
- A completed run's payload is reclaimed by `atc/gc`'s pipeline-run reclaimer according to the template's current `run_retention`; the header, its builds (stamped with run id, materialised job name and policy key) and their logs survive (f7cf071454). Existing job build-log policies apply across runs of the same logical job via the unmaterialised job key (8bb9d40c4d). Templates with run history cannot be destroyed through the ordinary delete path (`ON DELETE RESTRICT`); archive them instead.
- Task caches for a run are keyed on (team, template pipeline, unmaterialised job name), so every run of a template shares one cache; the identity is propagated opaquely to the worker (7fdfd93cf5, 0d336e062b).
- Run payloads write build events to the team partition rather than a per-pipeline partition, so reclaiming a payload does not drop a table per run (1773105508; 4498155bd4).
- Base templates are excluded from lidar, periodic checks, scheduling and the automatic pipeline pauser; run payloads are excluded from pipeline lists, the dashboard, the sidebar and the cluster-wide resource enumerations, but remain addressable as `tmpl/run:N` (c81a79cc14, e303815195). A payload whose run never terminalises is now auto-paused like any idle pipeline, so lidar stops checking it (7840a35ff6).

Surface:

- `fly run-pipeline -p <template> [-v NAME=STRING] [--json-var NAME=JSON] [--team]` creates a run and prints its number, status and URL (275b05252c, 221d7c15ee). `fly runs -p <template> [-c N] [--json]` lists runs, paging past the server's 500-row cap (275b05252c, aab7cdf8d2, 0aa06883e5).
- `POST`/`GET /api/v1/teams/:team/pipelines/:pipeline/runs` and `GET .../runs/:number`; create requires the member role, list and get the viewer role (f50e6ed8c8, bfcdfed42e, 1aac9644cb). Pipeline list rows carry `run_number` and `run_template_ref` (c81a79cc14).
- Web UI: a runs page at `/teams/:team/pipelines/:pipeline/runs` with a create-run form and pagination, a run page at `.../runs/:number` with live/completed/reclaimed context, and a pause control on templates (7e861d5672, f7a2048f69, 6524613377, f4657e0906).

Consolidation fixes landed on 2026-08-24/25, before the audit: the run-number occupancy probe, the abort-path completion hook and the pauser's template term (6b8f3b5a80); marking a pipeline `template: true` is now a diff and the abandoned-pipeline collector no longer erases templates or payloads (1be5658252); the shared resource/job queries stop carrying run presentation joins on every lidar and scheduler tick (c81a79cc14); the runs list is bounded and fetches a page of payloads in one query (e303815195, df4fab6bcd); `fly get-pipeline` on a payload round-trips into `set-pipeline`, refusals answer JSON, and the UI says "running" like the API (aabe458397).

### Hardening from the audit

The pipeline-templates feature was audited on 2026-09-03; fixes are tagged `(audit <id>)` in the log and group by theme:

- Template and parameter validation: an enum default must be a declared value (ade30ad23b); a dotted var whose root names a declared param is left for runtime var sources instead of failing every run creation (edec39d8d2); manual and webhook checks on a template answer 409 instead of resolving `((param))` placeholders against the credential manager (a2cf36a170); `fly validate-pipeline` and `set-pipeline` run the template declaration checks locally instead of printing "looks good" (0d963a188a); `-v` can set a number or bool enum parameter (7b8c28d053).
- Database and scheduler: a prototype check build on a run payload no longer 500s with a missing event partition (4498155bd4); the per-pass `GetPendingBuilds` behind the unread `NoBuild` flag is gone (a107ff233a); the task-cache upsert trusts its own `RETURNING` (d41e1ddaf8); a run job can be paused after its run terminalises (d962851868); the schema ledger lists every JetBridge-only migration with correct trigger counts (d0342d14a8).
- API and fly ergonomics: run and check refusals reach fly as their reasons rather than a raw JSON blob (401cdd09a8, bb75d433d4); the run server's own 400s answer JSON (343c03ae35); a stored template that fails re-validation at run time answers 409 with its defect instead of a bodiless 500 (c12832960e); `fly runs` pages instead of truncating, gains `--json`, and prints numbers as supplied (aab7cdf8d2, 0aa06883e5).
- RBAC: startup refuses a custom role mapping where `CreatePipelineRun` requires a weaker role than `SaveConfig`, since a run parameter is interpolated verbatim into a saved pipeline config (c562c37752); an action assigned to more than one role is refused and the mapping is loaded once and shared by validation and enforcement (e7a6208967); the cache is keyed on the RBAC path so a later path is not shadowed (9444818926).
- Web: run pages get the sidebar flex layout and the standard top bar with breadcrumbs and login (fc3226e525, 1a42b0da83); the header poll no longer blanks the loaded payload and a single payload 404 no longer latches the run into record-only (c6d77db59b, 2d57757ff0); bool params render as a true/false select and required fields carry `aria-required` (2b52396b04); stale live-region messages, permanent focus rings on pager links and out-of-order page responses are fixed (c3bbc0a991, 981f0ae683, 98f8ec77f6).

## Artifact daemon and durable storage

Security fixes in the daemon, in the order they landed. All were reproduced against the real handlers before being fixed and are kept as regression tests.

- Tar extraction escaped its destination (security fix). Entry paths were validated as strings and then written through the ambient filesystem, so an archive could create `hatch -> /outside` and then write `hatch/victim` outside the destination while `Fetch` returned nil. Extraction now goes through an `os.Root` at the destination; symlink targets must be relative and internal; hard links are materialised (they were silently dropped); devices, FIFOs and sockets are refused; a refused extraction leaves nothing behind (ab1c66c2c5, dc2afffb02, 923203ec06).
- The request boundary was uncontained (security fix). Go's mux cleans the unescaped path, so `%2e%2e%2f` arrived decoded: `DELETE /artifacts/<..>` deleted outside the root, `PUT /stream-in/<..>` planted files, `POST /register` with an outside `local_path` served arbitrary files (and uploaded them to the shared bucket), `POST /resolve` with an outside `dest` wrote and deleted arbitrarily, and `POST /mirror` tarred an outside tree and shipped it to every peer. Keys and body paths are now validated before any side effect; `.` and the store root are refused; the read path checks symlinks at use; `/resolve-batch` fan-out is bounded to 4 and JSON control bodies to 1 MiB (36b3c3df2a, cc3acf8fd8, 257b68d675).
- Structural names and the registry (security fix). `steps`, `artifacts` and `aliases.json` are refused as artifact keys on every per-artifact verb, compared case-insensitively; registry aliases are validated when used and a poisoned or swapped entry is evicted; `Mirror`'s own join is routed through the same guard. A charset rule that broke legal Unicode job names was reverted (bc52b0a0c0, 9278632e37, 08324eefc1).
- Containment as a property of the handle. The server acquires an `os.Root` for its storage path at construction (`NewServer` now returns an error and `main` creates the path), stream-in runs on a nested `steps/` root, the duplicate stream-in extractor is gone, and the daemon no longer shells out at all: `cp -R` and `chmod -R` are an in-process walk over two handles (ad2969cb7b, b7838f7d1e, c76e4ac4a1).
- Registry values are locations, not paths. `aliases.json` now stores keys relative to the storage root; a legacy file with absolute values is accepted and relativised on load, entries outside the store are refused. This also fixed `RemoveByPath` evicting `build-42` when sweeping `build-4` (8c1ce5184d).
- One computation of a destination (security fix). `resolve()` cleaned its input before walking, so `link/..` collapsed textually and the raw path escaped, reachable from `/resolve` as an unauthenticated delete and write outside the store. Destinations are now derived lexically from the root, the parent is walked component by component with inode re-checks, and non-canonical keys are refused rather than normalised (1bfa2cb43c, 559921ef96). The same change widens promoted directories from 0700 so non-root task containers no longer get EACCES on their inputs, and serialises copies per destination (5 of 8 concurrent resolves to one dest previously failed).
- Absolute symlinks are refused on egress too, from a single tar producer. This is a deliberate build-breaking change; see upgrade notes (65e1f31228).
- Resolve-capability authentication (security fix). `POST /resolve` and `/resolve-batch` are mTLS-exempt by design and, on this branch, had no capability check either, while `artifactDaemon.networkPolicy.enabled` defaults to false. The `artifactcap` package (HMAC over key, dest and expiry) is restored at top level; the daemon verifies and fails closed once a key is configured; the ATC signs every init-container fetch; a malformed key or a TTL below the floor computed from the effective pod scheduling and startup timeouts refuses to start rather than silently not signing (c8c54faf99, 1e023e7ca4, 1164f9db3d). With no key configured the routes stay open and the daemon logs `resolve-unauthenticated` at startup.
- Every refusal is counted and logged through one path (`refusals_total{route,reason}`), including mTLS refusals; a structural test forbids a handler writing a 4xx directly (a8a781c1e2).

Hangar, the durable tier for resource caches (design in `docs/durable-artifact-storage.md`):

- Only resource caches are promoted; step outputs are per-build and stay node-local with a TTL. The tier is fail-open by construction: its methods return bool, a broken store is a miss, and the S3 client caps retries at 2 (acb8c6a5ca).
- Caches are named by content. `resource_caches.durable_key` is a SHA-256 over the parent cache's key, resource type, source hash, version digest and params hash, computed in `FindOrCreateResourceCache` and used as the cache key everywhere; the surrogate `rc-<id>` was unsafe for a permanent copy because the row is hard-deleted and re-minted under a new id, and a restored database would rewind the sequence onto other bytes (5e3f297a2b, b49380d2ff, e9a1202975). Rows predating the column keep `rc-<id>`, are never promoted, and are filled in on the next find.
- Backends: S3-compatible (AWS, MinIO, Ceph, R2, Backblaze), native GCS via Application Default Credentials (no HMAC key to leak), and filesystem on a shared mount. The store interface has `Stat`, `List` and versioned attributes so a future consumer such as an agent snapshot store can use it; no snapshot feature ships in this release (118d28fe8d, 069c8e8634).
- Warm on miss. `FindResourceCache` probes node-locally first and, only on a miss with a content key and a daemon advertising `X-Durable-Tier`, asks one daemon chosen by rendezvous hash of node name to `POST /durable/restore`; a failed warm suppresses the key for 60s; `HEAD /resource-caches/` never consults the store so node affinity survives. Three probe fixes fell out: no fallback to `/resolve` from a probe, endpoint discovery filters on `Ready`, and probe-bound volumes get a daemon client so the peer fallback works (6e49f0501e, 860e07aa6f, f3f1eb6712).
- Retention. Objects are named `<prefix>/<class>/<identity>` (today only `resource-caches`); each daemon walks the store every `--durable-maintenance-interval` (default 15m) and deletes objects older than `--durable-retention CLASS=DURATION`. Anything uncertain is kept: no timestamp, no class prefix, or an unconfigured class. A bucket lifecycle rule still works as a backstop but must include the prefix (e639c75597, 154ec545f2, 3a552a252d).
- Metrics: ATC counters partitioning lookups into local hit, warm hit, warm miss and warm suppressed; daemon gauges for object count, bytes and oldest-object age, which describe the shared store and must be aggregated with `max by()` rather than summed (a210402430).

## Runtime, scheduler and GC fixes

- The reaper deleted every completed step's pod on each sweep, including the `concourse.ci/exit-status` annotation that is the only durable record of a step's result; a resumed build after a web rollout then re-executed steps that had already run. Completed pods are now retained while their build is running, fail-closed (10784a37e3).
- Pod loss (eviction, drain, autoscaler scale-down, node loss) is classified from structured lifecycle state into a typed retryable `InterruptionError` rather than a string match on `Evicted` that errored the build (10784a37e3).
- `fly intercept -j pipeline/job` failed with `empty image for resource type "(unknown)"` because a looked-up container carried empty metadata (f17a4858c5).
- Pinned `image_resource` digests were ignored and the pipeline ran whatever lidar last cached; `engine.finish` double-emitted error events on job builds (0ab40afc64).
- A web restart wiped the in-memory artifact locator, so resumed builds errored on pre-restart artifacts instead of probing peers (44b26a195e).
- A missing task config or `file:` now reports `ErrFileNotFound` with the intended message rather than a raw `open ...: file does not exist` (9ae01bb83a); a get that exits zero without an artifact is refused (c2e41fbd38).
- The build-event stream handler leaked a goroutine and a `LISTEN` per disconnected client on an idle live build (18a1b24801); an event frame with no `data` key crashed the decoder in `fly watch`, `execute` and `trigger-job` (47eac677fa).
- Prometheus series for unknown containers and volumes were never garbage-collected, and every InfluxDB emitter shared one batch buffer (044131865b, 5c5c27ed63).
- Build-log retention: a job declaring `min_success_builds` with no `builds` killed the build-reaper goroutine with an out-of-range index, and a large `--default-days-to-retain-build-logs` overflowed to a negative day count that silently deleted a job's entire history; all three sources are now bounded (a1d68a6b32).
- The OIDC discovery routes are audited as `system`, not `worker`, and a golden test now covers every route so a new one cannot panic the auditor (68369e9c07).
- Documented but not fixed: a field-qualified credential such as `((db.password))` gets no `SecretKeyRef` and its literal value is written into the pod environment, because the tracker keys secret refs on path alone and `kubernetes.Secrets.GetSecretRef` hardcodes key `value`; the two must change together. The current behaviour is pinned by a test (914a3fb235, 6c9601d03e).

## CI, build and deployment

Helm chart (`deploy/chart`):

- The chart rendered `--kubernetes-cache-pvc` and `--kubernetes-artifact-store-claim` by default, neither of which the binary has, so a bare `helm install` produced a web pod that exited on an unknown flag. Those stanzas, the `cachePvc` and `artifactStorePvc` value blocks, `templates/pvc.yaml` and 15 README rows are gone; a flag-drift test builds both binaries and compares `--help` against what the templates render (582c85355f). `cacheStore` documents only `hostpath` and `emptydir`, the two values the binary accepts (b543629e60).
- The ATC never emitted metrics: it registers its Prometheus emitter only when both bind IP and port are set, and the chart set neither, while the ServiceMonitor scraped port `http` and received the Elm SPA. `metrics.enabled` (default true) and `metrics.port` (default 9391) open the listener and expose a named `metrics` port; `serviceMonitor.enabled` now fails the render without it. Three of the four PrometheusRule expressions named series that do not exist and are corrected (d398db783d, 149e4587f1).
- `artifactDaemon.resolveCapability.existingSecret` is the single switch for capability auth: set, both web and daemon get the flag and mount the Secret; empty, neither does. The chart deliberately does not generate the key (`lookup` is empty under `helm template`, so Argo CD would rotate it on every reconcile); `resolveCapability.ttl` defaults to 2h (56834e1db5, 1e023e7ca4).
- `artifactDaemon.durable.*`: `store` (`""`, `gcs`, `s3`, `filesystem`), `bucket`, `prefix`, `endpoint`, `region`, `path`, `timeout`, `maxBytes`, `maintenanceInterval`, `retention` (class to duration map) and `existingSecret` for S3 credentials as env. `bucket` is required for gcs/s3 and `path` for filesystem; the render fails otherwise (acb8c6a5ca, 154ec545f2).
- Render guards: `secrets.create=true` with `web.replicas > 1` fails, because a per-pod session signing key makes replicas reject each other's tokens (582c85355f).
- Seven chart test suites cover flag drift, cache-store values, durable-store rendering, the resolve-capability Secret, ServiceMonitor port resolution, alert-metric drift against the emitter's own definitions, and the deploy pipeline graph.

Release pipeline (`deploy/concourse-pipeline.yml`):

- The version is declared, not derived: `VERSION`, `versions.go`'s `JetBridgeVersion` and `Chart.yaml`'s `appVersion` must agree, checked by `TestVersionDeclarationsAgree` and again by a read-only acceptance gate ahead of the release task (7f3c16ce7f).
- Tags and images: `tag-rc` force-tags `jb-<VERSION>-rc`; `build-image` stamps the release version into the binary and pushes `jetbridge:<VERSION>-rc-<sha>` (immutable), `:<VERSION>-rc` (floating) and, until `jb-<VERSION>` exists, `:<VERSION>` so the chart's default image resolves between bump and cut; `release` is manually triggered, retags the per-commit image after asserting the image IDs match, tags `jb-<VERSION>` on git, and moves `:latest` only after the tag lands (7f3c16ce7f, 0952f8b162). The previous pipeline shipped the `-rc` candidate binary as the release, could re-release an already-cut version, and printed the SA token into the build log.
- `self-upgrade` sets the per-commit image on every container of `deployment/concourse-web` (including the `migrate-db` and `generate-keys` init containers, which previously kept the old image) and on `daemonset/concourse-artifact-daemon` (previously restarted but never re-imaged), then restarts both. `verify-upgrade` asserts every running pod of both workloads is on this commit's image before checking the API; it previously compared a version string that most commits do not change (0952f8b162).
- The frontend is rebuilt in `build-image` and installed over `web/public` before `go build`; `web/handler.go` embeds whatever bundle is on disk, which is how a stale UI shipped in July (473829956f). Elm tests run in the same task.
- `fly` is cross-compiled for all five published platforms, and the Windows archive is a `.zip` as the download endpoint expects; 0.2 to 0.3 is a MAJOR.MINOR bump that `fly` hard-errors on, and `fly sync` is the in-band remedy (582c85355f, 0952f8b162).
- Every task has a `timeout` under a 6h ceiling, enforced by test; `docker:dind` is pinned to `docker:26-dind` (runc >= 1.2 fails on the 5.4 kernel); `yarn install --immutable` no longer falls back to regenerating the lockfile (bff1f5b944).
- Test runner image: v6 adds PostgreSQL 17 (matching the 17.10 production server), v7 helm, v8 zip, v9 brine, envtest assets and `GOTOOLCHAIN=auto`; `Dockerfile.test-runner` is reconciled with the running image and the dind builders get `--mtu=1450` for the flannel network (01f751b4d2, e16868e48c, f786caf61e). The brine job is not wired in yet.
- The `unit-tests` job previously excluded 19 packages including `atc/db` and everything under `cmd`, so they ran in CI never; the exclusion lists are now held equal by test (41f7ca5dc7).

Removed from the tree: `forge/` (562 files), `ci-agent/` (its own Go module), `ci/`, `deploy/k8s-local.yaml` and `k8s-gke.yaml` (hardcoded non-existent flags), `deploy/agent-pipeline.yml`, `Dockerfile.ci-agent`, `borg-pipeline.yml` and `test-pipeline.yml`, upstream governance (`CODEOWNERS`, `FUNDING.yml`, release docs, EasyCLA and Discord routing in `CONTRIBUTING.md`), four vendored assistant directories, and the never-served 1.4 MB `web/public/elm.js` (e9e705a55a, 3cb4150fee, bff1f5b944). README, TESTING, JETBRIDGE.md, PIPELINE-MIGRATION.md and the chart README no longer describe the PVC-era architecture or flags that do not exist (b543629e60, 2e9d256b2b, e077a8e4ae). `AGENTS.md` carries the lessons harvested from `forge/` (44b26a195e).

## Web UI

- The agent-review UI (three modules, a route, endpoints under `/agent-reviews`) is removed; it was calling routes the ATC no longer served (546f96a459).
- Runs page (`.../pipelines/:pipeline/runs`) with a create-run form, per-type inputs (bool as a true/false select, enum as a select, required fields marked), server-side error display and pagination; run page (`.../runs/:number`) showing the payload's pipeline view with live/completed/reclaimed context, refreshing durations, and a record-only view after reclaim; a pause control on templates (7e861d5672, f7a2048f69, 6524613377, 2b52396b04).
- Pages nested inside a run (build, job, resource, pipeline, causality) get breadcrumbs again via the base template's row plus a run crumb; paused and archived state inside a run is approximated from the template (f9aea562b0).
- Run status renders the wire vocabulary ("running") instead of the build renderer's "started" (aabe458397).
- `make test-elm` exists and runs in CI; two suites had stopped compiling because nothing ran them (546f96a459, 2dbaa02e91).

## fly

- New: `fly run-pipeline` and `fly runs` (see above). `--json-var` refuses dotted parameter names at parse time instead of expanding them into a nested object (221d7c15ee).
- `fly validate-pipeline` and `fly set-pipeline` run the template declaration checks locally; `set-pipeline --check-creds` no longer reports declared params as missing credentials (0d963a188a, a3f812b8c6).
- Run and check refusals (400/409 with a reasons envelope) print as their reasons, one per line (401cdd09a8, bb75d433d4).
- `fly watch`, `fly execute` and `fly trigger-job` no longer crash on an event frame without a `data` key (47eac677fa); `fly intercept -j` works again (f17a4858c5).
- The `fly` binary no longer links a generated test double; `validate-pipeline` used `dbfakes.FakeSigningKeyFactory` in production code (d1354b86fc).
- Version skew: a 0.2 `fly` hard-errors against a 0.3 web (MAJOR.MINOR mismatch); run `fly sync` after upgrading. Archives for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and windows/amd64 are served from the web image (582c85355f).

## API

- Added: `POST`/`GET /api/v1/teams/:team_name/pipelines/:pipeline_name/runs`, `GET .../runs/:number`. Default roles: create member, list and get viewer. `limit` is capped at 500 and returns a `Link` header for the next page (f50e6ed8c8, e303815195, df4fab6bcd).
- Removed: `/api/v1/agent/feedback*`, `/api/v1/agent/reviews*`, `/api/v1/builds/:id/agent-reviews`, `/api/v1/teams/:team/agent-reviews` (35c6564639) and `POST /api/v1/mcp` (1ad9946000). The MCP route was wrapped only in authentication and took the team from the JSON body, so any authenticated user could mutate any team and abort any build by id.
- Status codes and bodies: manual and webhook checks on a template answer 409 (a2cf36a170); run refusals (paused/archived template, invalid params, stored-template defects, payload mutation) answer 400 or 409 with an `atc.SaveConfigResponse`-shaped JSON envelope, `Content-Type: application/json` (aabe458397, 343c03ae35, c12832960e).
- `reclaim_retry_after` on a run is withheld from callers not authorised to see params, so an unauthenticated viewer of an exposed template no longer sees the reclaimer's backoff (b40db3b441).
- Pipeline list rows include `run_number` and `run_template_ref`; run payloads are absent from pipeline, job and resource enumerations (c81a79cc14, e303815195).
- Prometheus metrics are served on the dedicated bind port, never on 8080; with the chart defaults that is port 9391 (d398db783d).

## Database migrations

Head migration is now `1773105509` (159 migration ids: 146 SQL pairs and 13 Go migrations inherited from upstream, plus the JetBridge set). New in this range, all JetBridge-only; the ledger is `docs/migration/schema-delta.md` (e34409c287, b40db3b441, d0342d14a8).

| Migration | Purpose | Reversible |
|---|---|---|
| `1773105504` `add_resource_cache_durable_key` | Nullable `resource_caches.durable_key`, the content-derived name the durable tier uses; existing rows stay NULL and are filled on next find (b49380d2ff) | Yes |
| `1773105505` `add_pipeline_template_runs` | Creates `pipeline_runs`; adds `template`, `params`, `run_retention_*`, `last_run_number`, `pipeline_run_id` to `pipelines`; 7 triggers including the deferred constraint that every running run owns exactly one payload (f233f63ddd, 32fcb332ac) | Only while unused |
| `1773105506` `add_pipeline_run_build_identity` | `pipeline_run_id`, `run_job_name`, `run_job_key` on `builds` with a completeness CHECK, an immutability trigger and a partial index; `run_expected`/`run_policy_key` on `jobs` (f233f63ddd) | Only while unused |
| `1773105507` `add_run_task_cache_identity` | Template-scoped task-cache identity so runs of a template share caches (7fdfd93cf5) | Only while unused |
| `1773105508` `skip_run_payload_event_partitions` | Replaces `on_pipeline_insert()` so a run payload gets no per-pipeline build-event partition; events go to the team partition (f7cf071454) | Only while unused |
| `1773105509` `guard_run_payload_deletion` | `BEFORE DELETE` guard on `pipelines` so a payload cannot be deleted outside the reclaim path (f7cf071454) | Only while unused |

- The five template migrations are one-way once the feature is used: every `.down.sql` calls `ensure_pipeline_template_runs_empty()` and raises if any template, run payload or run header exists. Rolling the binary back after a template has been created is a restore-from-backup operation.
- `1773105506` validates a foreign key, verifies a CHECK and builds a non-concurrent index on `builds` in one transaction under `ACCESS EXCLUSIVE`; duration scales with build history and every in-flight build's status write blocks for the whole transaction. Measure on a restored copy before upgrading a busy cluster. No `ANALYZE builds` follows.
- Removed from the tree: `1773105502_create_agent_feedback` and `1773105504_create_agent_reviews` (35c6564639). Note that id `1773105504` is reused by `add_resource_cache_durable_key`; see upgrade notes.
- `docs/migration/migrate-preflight.sh` is pinned to `1773105509` (b49380d2ff, a945121bdb).

## Breaking changes and upgrade notes

Refuses to start:

- `--kubernetes-cache-store` outside `hostpath`/`emptydir` (unchanged in the binary, but the chart previously documented `artifact` and `pvc`) (b543629e60).
- A custom RBAC file (`--config-rbac`) that assigns an action to more than one role, or that gives `CreatePipelineRun` a weaker role than `SaveConfig` (c562c37752, e7a6208967).
- A resolve-capability key that is not exactly 32 bytes, or `--kubernetes-artifact-daemon-resolve-capability-ttl` at or below the floor derived from the effective pod scheduling and startup timeouts plus the init retry budget (1164f9db3d).
- The artifact daemon with `--durable-timeout` >= `--ttl` (6e49f0501e). `NewServer` now fails if the storage root cannot be opened; `main` creates it first (b7838f7d1e).

Refuses to render (Helm):

- `secrets.create=true` with `web.replicas > 1` (582c85355f); `serviceMonitor.enabled` without `metrics.enabled` (d398db783d); `artifactDaemon.durable.store: gcs|s3` without `bucket`, or `filesystem` without `path` (acb8c6a5ca).

Values to change:

- Remove `cachePvc.*` and `artifactStorePvc.*` (including `gcsFuse`); they no longer exist and the flags they rendered never did (582c85355f).
- `metrics.enabled` defaults to true and opens port 9391 on the web pod and Service; set it false if you do not want the listener (d398db783d).
- To enable resolve-capability auth, create a Secret with a 32-byte `resolve.key` (`head -c 32 /dev/urandom`) and set `artifactDaemon.resolveCapability.existingSecret`. On the first rollout an old web that does not sign meets a new daemon that requires, and those resolves 403 until web has rolled; brief and self-healing. Without the Secret the daemon accepts any caller on `/resolve` and logs `resolve-unauthenticated`; `artifactDaemon.networkPolicy.enabled` still defaults to false, so set one or the other (c8c54faf99, 56834e1db5).
- `artifactDaemon.tls` with a generated Secret still rotates its CA on every Argo CD reconcile; set `tls.existingSecret` under GitOps (56834e1db5).

Behaviour changes:

- Artifacts carrying an absolute symlink are refused on both produce and consume, naming the entry. `python -m venv` (`bin/python -> /usr/local/bin/python3.11`), toolchain `current -> /opt/...` pointers and tarred rootfs trees will fail where they previously delivered a link that only worked when producer and consumer shared an image (65e1f31228, c76e4ac4a1).
- Daemon extraction: hard links are materialised (were dropped), device/FIFO/socket entries and traversing entries fail the extraction (were skipped), non-canonical keys (`a//b`, `a/./b`) and the structural names `steps`, `artifacts`, `aliases.json` are refused on every per-artifact verb (dc2afffb02, ad2969cb7b, 1bfa2cb43c, 08324eefc1). `aliases.json` now stores relative values; the old absolute form is read once and rewritten (8c1ce5184d).
- Completed step pods are retained while their build runs, so more pods linger between sweeps; eviction and node loss retry instead of erroring the build (10784a37e3).
- Build-log GC: bounding `min_success_builds` in the no-max branch reaps builds that a config with `min_success_builds` above its build budget previously kept on deployments without `--max-build-logs-to-retain` (a1d68a6b32).
- Run payloads are hidden from pipeline lists, the dashboard and resource enumerations; a template with run history cannot be deleted, only archived (e303815195, f233f63ddd).
- Clients: template checks answer 409; run and check refusals are JSON where some were `text/plain`; go-concourse returns typed `InvalidPipelineRunError`/`APIRefusalError` for those (a2cf36a170, 343c03ae35, 401cdd09a8, bb75d433d4).

Removed surfaces: agent review/feedback API and UI, the MCP endpoint, `forge/`, `ci-agent/`, `ci/`, the raw k8s manifests, `borg-pipeline.yml`, `test-pipeline.yml`, upstream governance files (35c6564639, 1ad9946000, 3cb4150fee, e9e705a55a).

Database:

- The template migrations are one-way once used and `1773105506` takes `ACCESS EXCLUSIVE` on `builds` for three full scans; back up first and measure on a copy (e34409c287, b40db3b441).
- Databases from the 0.2 line (v0.2.x, head `1773106167`) are ahead of this binary's head and `migrate-preflight.sh` refuses them. Any database that ran a 0.2.x build or a `core` build older than 35c6564639 also has `1773105504` recorded as `create_agent_reviews`, so the migrator would treat `add_resource_cache_durable_key` as already applied and `resource_caches.durable_key` would be missing. Only a database at or below `1773105503` (upstream v8.0.1 plus the first three JetBridge migrations) takes the straightforward path; treat anything else as a migration project, not an upgrade. This was read from the migrator, not tested against a live database.
- The test suites now require PostgreSQL 17 binaries (`initdb`, `postgres`, `psql`) on PATH; CI runs on `concourse-test-runner:v9` (01f751b4d2, 594609e92d).

## Testing

- The generated `dbfakes` package (46k lines) and most other counterfeiter fakes are gone; `atc/api`, `atc/db`, `atc/engine`, `atc/exec`, `atc/gc`, `atc/scheduler`, `atc/worker/jetbridge`, `atc/lidar`, `atc/creds`, `skymarshal/token` and others run against a real Postgres started by `atc/postgresrunner`, which now picks a PID-seeded free port and spreads every id sequence a million apart so a team id cannot pass for a pipeline id (7c6532e227, 44b26a195e, d34e25b55f, 2dbaa02e91, 1c54d011d4). Porting the tests surfaced the digest-pin, engine double-error, intercept, retention and SSM-error defects fixed above.
- Guards are mutation-verified and structural: import-graph tests keep the core/agentic seam and the daemon's request-derivation and symlink-validation rules; the chart is checked against the binaries' `--help`, the emitter's metric names and the Service's port chain; the deploy pipeline graph is checked for unsatisfiable task inputs, unclaimed outputs, force-pushes and missing timeouts (6297a69f28, c943591759, aef2244a63).
- The daemon's containment work carries its own regression suites, including an 88-case differential test of symlink containment against the real filesystem and an 8-way concurrent-fetch race test (bb4f101363, 7b699e9a02).
- `make test-elm` (3094 specs), `make test-unit` (83 suites) and the K8s tiers (testcontainers K3s, CI-only) are the supported paths; plain `go test ./...` is documented as unsupported for database-backed packages. The K8s suites create the resolve-capability Secret before installing so the signed path stays covered (099423acda).

## Known issues

- `self-upgrade` and the redeploy step of `release` use `kubectl set image`, which an Argo CD Application with `selfHeal` reverts to the tag declared in its values within seconds; on such a cluster `verify-upgrade` fails and the workloads stay on the previous image. Until the pipeline writes the tag into the GitOps repo instead, give the Application an `ignoreDifferences` entry for the container images of `deployment/concourse-web` and `daemonset/concourse-artifact-daemon` (`RespectIgnoreDifferences=true` is already required), so the declared tag is a bootstrap value and the pipeline owns the live image. Note that the reverted rollout is a race, not a no-op: the pod on the new image can run its `migrate-db` init container before Argo CD removes it, leaving the database migrated ahead of the binary Argo CD restores.
- A run whose expected job never completes a build stays `running` forever and its header and payload are never reclaimed; closing that needs an explicit abort or a run timeout, which is a policy decision not taken in this release (7840a35ff6).
- A field-qualified credential (`((db.password))`, or any credential whose backend returns a map) is written into the pod environment as a literal rather than a `SecretKeyRef` (914a3fb235, 6c9601d03e).
