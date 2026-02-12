# Spec: Skip Image Resource Download on K8s

**Track ID:** `skip-image-get-k8s`
**Type:** feature

## Overview

On the K8s runtime, when a `registry-image` resource (or custom type producing registry images) is passed between jobs, Concourse currently runs a full `get` step — spawning a pod with the resource type binary, downloading all image layers to a volume, and storing the artifact. The downstream job only needs the image reference (repository + digest) so kubelet can pull natively. This track short-circuits the get step for image-type resources on K8s, resolving just the version metadata instead of performing a physical download.

## Requirements

1. When running on K8s runtime, `get` steps for `registry-image` type resources must skip the physical image download by default and instead store only the version metadata (digest/tag).
2. The version resolution (from scheduler "passed" constraints or check results) must still work correctly — the downstream job gets the exact version that was output by the upstream job.
3. When a task step references the resource via `image:`, the K8s runtime must construct the `ImageURL` from the stored version metadata (repository + digest) rather than requiring an artifact volume.
4. Custom resource types whose get output is a registry image must also be eligible for the short-circuit. A resource-level or type-level declaration (e.g., `produces: registry-image`) enables the optimization for non-base types.
5. A per-resource or per-get-step opt-in parameter (e.g., `params: {fetch_artifact: true}`) must force the full physical download when the image artifact is needed (build context, docker-in-docker, file extraction).
6. The existing `FetchImage` short-circuit (for type image resolution) continues to work as-is.

## Acceptance Criteria

- [ ] `get` steps for `registry-image` resources on K8s skip physical download by default
- [ ] Tasks using `image:` from those get steps work correctly (kubelet pulls the image)
- [ ] Passed constraints still resolve the correct version
- [ ] Custom resource types can opt into the short-circuit via declaration
- [ ] `params: {fetch_artifact: true}` forces full download for cases needing the artifact
- [ ] No regression for non-K8s runtimes
- [ ] Unit and integration tests cover both the short-circuit and forced-download paths

## Out of Scope

- Changing base resource type resolution (already handled by `DefaultResourceTypeImages`)
- Modifying kubelet pull policy or caching behavior
- Optimizing `put` steps
- Multi-arch image resolution
