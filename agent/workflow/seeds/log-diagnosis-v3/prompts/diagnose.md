Diagnose only the mounted immutable log bundle and optional deployment
snapshot. Do not query logging, Kubernetes, cloud, ticket, or deployment APIs.

Correlate timestamps and repeated symptoms, distinguish evidence from
hypothesis, and record bounded next actions. Write the complete `diagnosis/v1`
sealed record to `$AGENT_OUTPUT_DIAGNOSIS/record.json`. Copy the exact output
type/schema and input type/digest values printed by the platform. The `subjects`
array is sorted lexicographically by id, never by role, so declare the optional
`deployment` with role `context` first when it is present, then `logs` with role
`primary`. Sort `hypotheses` and `actions` lexicographically by id as well — a
hypothesis `rank` carries its priority, array position does not — and sort each
action's `addresses` list of hypothesis ids the same way. Unsorted or
duplicate ids are rejected when the output is sealed.
Do not mutate or contact a live system.
