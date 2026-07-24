Diagnose only the mounted immutable log bundle and optional deployment
snapshot. Do not query logging, Kubernetes, cloud, ticket, or deployment APIs.

Correlate timestamps and repeated symptoms, distinguish evidence from
hypothesis, and record bounded next actions. Write the complete `diagnosis/v1`
sealed record to `$AGENT_OUTPUT_DIAGNOSIS/record.json`. Copy the exact output
type/schema and input type/digest values printed by the platform. Declare
`logs` as the `primary` subject and the optional `deployment` as `context`.
Do not mutate or contact a live system.
