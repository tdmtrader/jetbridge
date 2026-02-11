# Implementation Plan: Skip Image Resource Download on K8s

## Phase 1: Get Step Short-Circuit for registry-image

Thread the `nativeImageFetch` flag into the get step and short-circuit the physical download for `registry-image` type resources. When enabled, the get step resolves the version from the scheduler (VersionSource) and constructs an ImageURL from source + version, without spawning a resource-type pod or downloading layers.

- [x] Write tests for get step short-circuit when nativeImageFetch is enabled
  - Test: registry-image get step skips container creation and returns ImageURL
  - Test: version from VersionSource (passed constraint) is used correctly
  - Test: GetResult is stored with version metadata for downstream steps
  - Test: non-registry-image types still run the full get
  - Test: nativeImageFetch=false still runs the full get
- [x] Implement get step short-circuit in `atc/exec/get_step.go`
  - Expose `nativeImageFetch` from delegate to get step
  - When enabled and `plan.Type == "registry-image"`: resolve version, construct ImageURL, store result, emit events — skip container/worker entirely
  - Register a lightweight artifact that carries the ImageURL (no volume)

[checkpoint: dd89bc966]

---

## Phase 2: Task Image Resolution from Short-Circuited Get

When a task step uses `image:` referencing a get step that was short-circuited, the task must resolve to an `ImageSpec` with `ImageURL` (no `ImageArtifact`). This may require changes to how the task step reads its image artifact or a new artifact type that carries an image reference.

- [x] Write tests for task step resolving image from short-circuited get
  - Test: task with `image:` from a short-circuited registry-image get produces correct ImageSpec
  - Test: ImageURL contains repository@digest from the get step's resolved version
  - Test: task still works when get step ran the full download (backward compat)
- [x] Implement image reference passthrough from get to task
  - Artifact registered by short-circuited get carries ImageURL metadata
  - Task step's image resolution reads ImageURL from artifact when no volume exists
  - JetBridge uses ImageURL to set pod container image (existing path)

---

## Phase 3: Forced Download via `fetch_artifact` Param

Add a `fetch_artifact` parameter to the get step that forces the full physical download even when the short-circuit would otherwise apply. This supports use cases like Docker-in-Docker, build contexts, and file extraction from images.

- [ ] Write tests for `fetch_artifact` param
  - Test: `params: {fetch_artifact: true}` bypasses the short-circuit
  - Test: full get step runs and artifact volume is available
  - Test: default (no param) uses the short-circuit on K8s
- [ ] Implement `fetch_artifact` param in get step
  - Check for `fetch_artifact` in params before applying short-circuit
  - When set, fall through to existing full-download path
  - Strip `fetch_artifact` from params passed to the resource get script

---

## Phase 4: Custom Resource Type Declaration

Allow custom resource types to declare that they produce registry-compatible images via a `produces: registry-image` field. When set, resources of that type are eligible for the get step short-circuit.

- [ ] Write tests for custom type `produces` declaration
  - Test: custom type with `produces: registry-image` enables short-circuit for its resources
  - Test: custom type without `produces` runs the full get
  - Test: `produces` field is parsed from pipeline config and stored in DB
- [ ] Implement `produces` field on ResourceType
  - Add `Produces` field to `atc.ResourceType`
  - Parse from pipeline YAML config
  - Thread through DB resource type storage
  - Get step checks resource type's `Produces` alongside `plan.Type` for short-circuit eligibility

---

## Phase 5: Integration and Pipeline Verification

End-to-end validation with the live pipeline on concourse.home.

- [ ] Write integration tests
  - Test: registry-image resource passed between jobs, task uses `image:` — no get pod spawned
  - Test: custom type with `produces: registry-image` passed between jobs — no get pod spawned
  - Test: `fetch_artifact: true` on passed resource — get pod spawned, artifact available
- [ ] Verify on concourse.home pipeline
  - Update pipeline to exercise passed image resources
  - Confirm no unnecessary get pods for image resources
  - Confirm tasks run correctly with kubelet-pulled images

---
