Compare the immutable repositories mounted at `before` and `after`. Review the
actual change for correctness, security, regressions, tests, and maintainability.
Do not modify either input and do not contact a live system.

Use the bundled review skill for the review method. Apply the
`MINIMUM_SEVERITY` threshold supplied by the invoking workflow or direct test.

Write exactly one typed candidate beneath the literal directory printed for
`$AGENT_OUTPUT_REVIEW`. It must contain `record.json` conforming to the
`review/v1` sealed-record contract. Copy the exact type and schema printed in
`$AGENT_OUTPUT_REVIEW_RECORD_TYPE` and `$AGENT_OUTPUT_REVIEW_RECORD_SCHEMA`.
Declare two subjects, copying their exact platform type/digest values: `after`
with role `primary` and `before` with role `context`.

The `subjects` array must be sorted lexicographically by id, so here it reads
`after` then `before`. The `findings` array must be sorted lexicographically by
finding id. Unsorted or duplicate ids are rejected when the output is sealed.
Cite concrete subject files and line ranges for every finding, distinguish
blocking findings from non-blocking feedback, and explicitly report when no
findings remain. Jetbridge validates and seals the directory; do not publish
comments or mutate a pull request yourself.
