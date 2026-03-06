# CGX: K8s Behavioral Test Failures

## Friction Points
- (none yet)

## Good Patterns
- Running tests in focused batches with `--focus` avoids timeout issues and gives clearer per-group results
- The `newMockVersionOrSkip` helper pattern gracefully skips tests when the runtime can't resolve images, preventing false failures from masking real ones

## Anti-Patterns
- (none yet)

## Missing Capabilities
- (none yet)

## Improvement Candidates
- (none yet)
