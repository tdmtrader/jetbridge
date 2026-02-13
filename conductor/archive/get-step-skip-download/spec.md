# Spec: Get Step `skip_download`

## Overview

Add a `skip_download: true` option to get steps that resolves the resource version (via check) but skips downloading artifacts. The resolved version metadata and image ref are registered in the artifact repository, allowing downstream task steps to use the get step's output as a container image via the existing `image:` field.

This replaces the implicit runtime heuristic that currently auto-skips downloads for `registry-image` types on K8s (`get_step.go:188-245`) with an explicit, user-declared intent. The pipeline author controls whether a get downloads bits or just resolves a version.

## Requirements

1. Add `skip_download: bool` field to `GetStep` (`atc/steps.go:295`) and `GetPlan` (`atc/plan.go:210`)
2. Planner copies `skip_download` from `GetStep` to `GetPlan`
3. When `skip_download: true`, `get_step.go` takes the existing skip path: resolve version, create resource cache, register nil artifact + image ref URL, update resource version metadata — but do not spawn a container or run the resource's `get` script
4. The existing `image:` field on task steps (`ImageArtifactName`) already handles nil-artifact + imageRef lookups at `task_step.go:370-380` — no changes needed there
5. Validate at set-pipeline time: `skip_download` is only valid when the resource type is `registry-image` or has `produces: registry-image` (since image ref construction requires a registry URL)
6. Remove the implicit K8s-only auto-skip for registry-image types once `skip_download` is the explicit mechanism — or deprecate it with a warning
7. `fly get-pipeline` round-trips `skip_download: true` correctly

## Acceptance Criteria

- A pipeline with `get: my-image, skip_download: true` followed by `task: build, image: my-image` runs successfully — the task uses the resolved image version without any download
- Checks still run and version tracking still works for `skip_download` resources
- `passed:` constraints work correctly with `skip_download` get steps
- `fly get-pipeline` preserves `skip_download: true`
- Setting `skip_download: true` on a non-registry-image resource produces a validation error at set-pipeline time
- Existing pipelines without `skip_download` continue to work unchanged

## Out of Scope

- Changing `image_resource` on task configs (that's inline and has its own check+get flow)
- Making `skip_download` work for non-image resources (future consideration)
- Removing the `fetch_artifact` param escape hatch (may still be needed for edge cases)
