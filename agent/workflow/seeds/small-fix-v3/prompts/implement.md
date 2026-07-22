Read the captured `work-item` and work only from the immutable `repository`
input. Implement the smallest complete fix. Run the focused tests first, then the
repository's appropriate broader suite; do not weaken or delete tests to obtain a
pass and do not contact live systems.

Write a `repository-change/v1` candidate beneath the literal directory printed
for `$AGENT_OUTPUT_CANDIDATE_CHANGE`. Its `change.json` must name `repository` as
`base_input` and describe a structurally valid patch, git tree, or bundle. Write a
strict `validation-report/v1` document at
`$AGENT_OUTPUT_VALIDATION/validation-report.json`, including every executed check
and its real outcome. Jetbridge validates both candidates before the review step.
