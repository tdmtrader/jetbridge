# CGX: Slim Check Pods

## Frustrations & Friction

_(Capture friction points during implementation)_

## Good Patterns to Encode

- Investigating pod container counts revealed unnecessary resource waste on every check cycle
- The artifact-helper sidecar gate (`ArtifactStoreClaim != ""`) was too broad — step type matters

## Anti-Patterns to Prevent

- Adding sidecars unconditionally to all pod types without considering whether the step needs them

## Missing Capabilities

_(Capture during implementation)_

## Improvement Candidates

_(Capture during implementation)_
