# CGX: Batch Artifact Resolution

## Session Notes
- Follows from cross_node_artifact_reliability track — that fixed timeouts, this fixes parallelism
- K8s init containers run sequentially, so N init containers = N serial fetches
- User's design: single init container calls batch endpoint, daemon resolves all in parallel
- Keep /resolve for backwards compat
