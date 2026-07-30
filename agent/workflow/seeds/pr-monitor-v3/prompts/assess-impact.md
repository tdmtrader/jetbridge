Compare the final rebased `candidate` and fresh `validation` with
`accepted-candidate` and `accepted-validation`. Use `pull-request` and the
authorized `response` only as supporting context. Do not contact the forge,
publish anything, or modify the candidate.

Write one `publish-impact/v1` sealed-record candidate to the literal directory
printed for `$AGENT_OUTPUT_PUBLISH_IMPACT`. It must contain `record.json`.
Copy the exact output type and schema and exact subject type/digest values
supplied by the platform. Describe only evidence present in the declared
inputs: baseline and candidate digests, lexicographically sorted unique file
changes, exact changed-line totals, conflict-resolution evidence, sorted
validation differences, deterministic rule results, and a bounded semantic
assessment. An agent assessment may require reapproval but must never waive a
deterministic rule or platform invariant. Keep reasons sorted and make
`reapproval_required` consistent with all rule results and any escalation.
The platform will independently reopen the accepted evidence and recompute
the complete decision at both approval and publication boundaries.
