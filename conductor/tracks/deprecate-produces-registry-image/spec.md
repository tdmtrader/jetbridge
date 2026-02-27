# Spec: Deprecate `produces: registry-image` Syntax on Resource Types

## Overview

Resource types currently support a `produces` field (e.g. `produces: registry-image`) that
tells Concourse the resource type produces a registry-compatible image. When set, Concourse
skips the physical artifact download and instead constructs a `docker://` image URL from
the version metadata, allowing kubelet (on K8s) or Garden (on legacy) to pull the image
directly.

This field is being deprecated in favor of the `image:` field on resource types, which was
introduced in the "Direct Image References for Resource Types" track. The `image:` field
provides a cleaner, more explicit mechanism: pipeline authors specify the container image
reference directly (e.g. `image: concourse/git-resource:latest`), and Concourse passes it
straight to the pod spec without any check/get cycle.

### Why Deprecate `produces`

1. **K8s + mock-backed types break** -- When a custom resource type uses `produces: registry-image`
   but is backed by a mock or test source, the K8s worker constructs a `docker://` URL from
   version metadata that references a non-existent image. Kubelet then fails to pull it,
   causing pod `ImagePullBackOff` errors.

2. **Implicit vs explicit** -- `produces` is an implicit hint that changes internal plan
   construction behavior. The `image:` field is explicit: the pipeline author states the
   exact image reference, no inference required.

3. **Redundant code paths** -- `produces` adds branching in `imageURLFromSource()`,
   `FetchImage()`, `metadataFetchImage()`, the get step, the check step, the put step,
   the step validator, and the planner. The `image:` field short-circuits all of this
   with a single `ImageRef` check in `TypeImage`.

4. **Confusing semantics** -- `produces` is set on the resource type definition but takes
   effect on the *image resolution* of resources that *use* that type. This indirection is
   hard to reason about and poorly documented.

## Current `produces` Usage in the Codebase

The `produces` field flows through these locations:

| File | Usage |
|------|-------|
| `atc/config.go:203` | `Produces` field on `ResourceType` struct |
| `atc/plan.go:240` | `Produces` field on `GetPlan` struct |
| `atc/config.go:382-384` | Wired from `ResourceType.Produces` into `GetPlan.Produces` during image plan construction |
| `atc/builds/planner.go:144-145` | Same wiring during resource plan construction |
| `atc/engine/build_step_delegate.go:359-362` | `imageURLFromSource()` checks `produces` to construct docker:// URLs |
| `atc/engine/build_step_delegate.go:397` | `metadataFetchImage()` checks `produces` for registry-image behavior |
| `atc/engine/build_step_delegate.go:436` | `FetchImage()` checks `produces` for URL construction |
| `atc/exec/get_step.go:193` | Get step checks `produces` for skip-download registry-image behavior |
| `atc/exec/get_step.go:587` | `resolveImageFromGetPlan()` checks `produces` for URL construction |
| `atc/step_validator.go:160` | Step validator checks `produces` to determine if a type is an image type |

## Migration Path

### Before (deprecated)

```yaml
resource_types:
- name: custom-registry
  type: registry-image
  source:
    repository: my-org/custom-registry-resource
  produces: registry-image
```

### After (preferred)

```yaml
resource_types:
- name: custom-registry
  image: my-org/custom-registry-resource:latest
```

The `image:` field completely bypasses the check/get cycle. No check pods are spawned,
no get steps execute, and kubelet pulls the image directly from the reference.

For types that genuinely need the check/get cycle (e.g. a resource type whose image
comes from a private registry requiring Concourse credential management), keep the
`type:` + `source:` pattern -- just remove the `produces` field.

## Deprecation Strategy

1. **Phase 1: Deprecation warnings** -- When `produces` is set on a resource type,
   log a deprecation warning during config validation and plan construction. The warning
   should recommend using `image:` instead and provide the migration example.

2. **Phase 2: Internal code updates** -- Refactor internal code to prefer the `image:`
   field path. Where `produces` is still needed for backward compatibility, funnel it
   through the same code path as `image:`.

3. **Phase 3: Test migration** -- Update all test cases that use `produces: registry-image`
   to use `image:` instead. Remove test-only reliance on the `produces` code path.

4. **Phase 4: Documentation** -- Update pipeline configuration documentation to recommend
   `image:` and mark `produces` as deprecated.

## Acceptance Criteria

- [ ] Using `produces` in a pipeline config triggers a visible deprecation warning in logs.
- [ ] All existing pipelines using `produces` continue to function (backward compatible).
- [ ] All internal tests use `image:` instead of `produces: registry-image`.
- [ ] The `produces` field is not removed from the struct (backward compat), but is
      documented as deprecated.
- [ ] No new code uses `produces` -- all new image resolution goes through `image:`.

## Out of Scope

- Removing the `produces` field from the `ResourceType` or `GetPlan` structs entirely
  (that would be a breaking change requiring a major version bump).
- Modifying the `image:` field implementation itself (already complete in the
  resource-type-image-refs track).
- Changes to base resource type configuration (`--kubernetes-base-resource-type` flag).
- Changes to the Garden/containerd runtime (legacy, already removed in JetBridge).
- Automated pipeline migration tooling (users migrate manually).
