Read the captured `work-item` and work only from the immutable `repository`
input. Implement the smallest complete fix. Run the focused tests first, then the
repository's appropriate broader suite; do not weaken or delete tests to obtain a
pass and do not contact live systems.

Write a `repository-change/v1` candidate beneath the literal directory printed
for `$AGENT_OUTPUT_CANDIDATE_CHANGE`. Write `record.json`, place its payload
beneath `content/`, declare `repository` as the one `base` subject, and copy the
exact platform-provided type, digest, and schema values. Describe a structurally
valid patch, Git tree, or Git bundle. Jetbridge validates and seals the candidate
before the validation step.
