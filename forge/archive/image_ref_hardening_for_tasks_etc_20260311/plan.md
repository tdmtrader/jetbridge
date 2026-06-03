# Implementation Plan: Image Ref Hardening for Tasks, etc.

## Phase 1: On-Demand Image Resolution (replace check pods)

Extend `buildStepDelegate` to resolve image digests in-memory via the OCI
registry API when no cached version exists, eliminating the fallback to
check+get pod execution.

- [x] Write tests for on-demand resolution in `metadataFetchImage`: resolver called when `LatestVersion()` returns not-found, resolved version saved to DB, image URL returned correctly 6f3666ce4
- [x] Inject `imageresolver.Resolver` into `buildStepDelegate` — extend `NewBuildStepDelegateWithFactories()` constructor and the `BuildStepDelegate` creation in `atc/engine/build_step_delegate.go` 6f3666ce4
- [x] Implement on-demand resolve path in `metadataFetchImage()`: when `scope.LatestVersion()` returns `!found`, extract repository+tag from source, call `resolver.Resolve()`, save version via `scope.SaveVersions()`, then continue with normal URL construction 6f3666ce4
- [x] Write tests for auth variants: anonymous (Docker Hub public), basic auth (username/password from source), GCP keychain — verify credentials are extracted from source config and passed to resolver 7d1195cc8
- [x] Implement auth extraction: parse `username`/`password` from evaluated source config, construct `imageresolver.BasicAuth`, pass to `resolver.Resolve()` 7d1195cc8
- [x] Write tests for error handling: resolver network failure, auth failure, invalid image — verify errors propagate as build errors (not silent) 7d1195cc8
- [x] Implement error propagation: on resolve failure, return descriptive error instead of falling back to plan-based fetch 7d1195cc8
- [x] Extend `metadataFetchImage()` beyond `registry-image` type — remove the type guard or map additional OCI-compatible types to the resolve path 7d1195cc8
- [x] Wire resolver into delegate creation in `atc/atccmd/command.go` or wherever `NewBuildStepDelegateWithFactories` is called 6f3666ce4
- [x] Phase 1 Manual Verification 7d1195cc8

---

## Phase 2: Skip Get for image_resource on K8s

Make `taskDelegate.FetchImage()` return the resolved image ref directly without
generating or executing check+get plans when the in-memory path succeeds.

- [x] Write tests for task delegate FetchImage: when resolver succeeds, no plans are generated or executed, ImageSpec with correct ImageURL is returned 0fa0952f3
- [x] Modify `taskDelegate.FetchImage()` to attempt in-memory resolution first (via embedded `BuildStepDelegate.FetchImage` which now uses the enhanced `metadataFetchImage`) — verify no `FetchImagePlan()` call is needed on success 0fa0952f3
- [x] Write tests confirming no check or get events are emitted when in-memory resolution succeeds 0fa0952f3
- [x] Write tests for fallback: when resolver fails (e.g., non-OCI type), fall back to existing plan-based path gracefully 0fa0952f3
- [x] Update `imageSpec()` in `task_step.go` if needed to handle the simplified return path 0fa0952f3
- [x] Phase 2 Manual Verification 0fa0952f3

---

## Phase 3: Sidecar Artifact References [checkpoint: fa03dfb6d] [checkpoint: 7646e526c]

Allow sidecars to reference a prior step's output as their image, enabling
workflows like "build DB image → run as sidecar for tests."

- [x] Write tests for sidecar artifact reference: sidecar with artifact name looks up `ImageRefFor()` from artifact repository, resolved ref used as container image 7646e526c
- [x] Extend `SidecarConfig` or sidecar loading in `task_step.go` to support an artifact name field (e.g., `image_artifact:` or overloading `image:` with artifact lookup) 7646e526c
- [x] Implement artifact lookup in `loadSidecars()` or `ContainerSpec` assembly: check artifact repository for image ref, substitute into sidecar image field 7646e526c
- [x] Write tests for error cases: artifact not found, artifact has no image ref registered 7646e526c
- [x] Update `buildSidecarContainers()` in `jetbridge/container.go` if needed to handle resolved artifact refs 7646e526c
- [x] Phase 3 Manual Verification fa03dfb6d

---

## Phase 4: Sidecar Digest Pinning [checkpoint: fa03dfb6d] [checkpoint: fe9c319b7]

Resolve bare-string sidecar images (e.g., `redis:7`) to pinned digests via
in-memory OCI calls for reproducibility and tracking.

- [x] Write tests for sidecar digest resolution: bare image string `redis:7` resolved to `redis@sha256:...` via resolver before pod creation fe9c319b7
- [x] Implement sidecar image resolution: during `ContainerSpec` assembly or in `buildSidecarContainers()`, call `resolver.Resolve()` for each sidecar image that isn't already digest-pinned or an artifact ref fe9c319b7
- [x] Write tests for auth on sidecar images: anonymous resolution for public images, skip resolution for already-pinned digests (`image@sha256:...`) fe9c319b7
- [x] Inject resolver into the sidecar resolution path (jetbridge container or task step level) fe9c319b7
- [x] Write tests for error handling: unresolvable sidecar image fails the build with clear error fe9c319b7
- [x] Phase 4 Manual Verification fa03dfb6d

---

## Phase 5: Cleanup and Integration Tests [checkpoint: c6bd56e95]

Remove dead code paths and verify end-to-end behavior.

- [x] Remove or gate the plan-based fallback in `buildStepDelegate.FetchImage()` for K8s runtime — the in-memory path should be the only path for image resolution fe9c319b7
- [x] Simplify `FetchImagePlan()` usage — task image resources on K8s should not generate check+get plans fe9c319b7
- [x] Clean up unused plan generation code if no callers remain fe9c319b7
- [x] Write integration test: task with `image_resource:` resolves without pod overhead — verify only the task pod is created (no check/get pods) c6bd56e95
- [x] Write integration test: sidecar with artifact ref from prior build step c6bd56e95
- [x] Write integration test: sidecar with bare string image gets digest-pinned c6bd56e95
- [x] Verify all existing unit tests pass (`make test-unit`) fe9c319b7
- [x] Verify fly integration tests pass (`make test-fly-integration`) fe9c319b7
- [x] Phase 5 Manual Verification c6bd56e95

---
