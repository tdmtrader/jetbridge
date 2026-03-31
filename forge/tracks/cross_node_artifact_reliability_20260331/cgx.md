# CGX: Cross-Node Artifact Reliability

## Session Notes
- Initial investigation revealed wget -T 5 timeout in init container is fundamentally too short for cross-node resolution
- Large artifacts (1-10+ GB) make this worse since tar streaming takes minutes
- User confirmed: /resolve must remain synchronous (no fire-and-forget to avoid partial file races)
- Scope: fix only (no multi-node test infra in this track)
