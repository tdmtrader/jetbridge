Compare the immutable repositories mounted at `before` and `after`. Review the
actual change for correctness, security, regressions, tests, and maintainability;
do not modify either input and do not contact a live system.

Write exactly one typed candidate beneath the literal directory printed for
`$AGENT_OUTPUT_REVIEW`. It must contain `record.json` conforming to the
`review/v1` sealed-record contract. Copy the exact type and schema printed in
`$AGENT_OUTPUT_REVIEW_RECORD_TYPE` and
`$AGENT_OUTPUT_REVIEW_RECORD_SCHEMA`. Declare two subjects, copying their exact
platform type/digest values: `after` with role `primary` and `before` with role
`context`. The `subjects` array must be sorted lexicographically by id, not by
role, so here it reads `after` then `before`; the `findings` array must be sorted
lexicographically by finding id the same way. Unsorted or duplicate ids are
rejected when the output is sealed. Cite concrete subject files and line ranges
for every finding,
distinguish blocking findings from non-blocking feedback, and explicitly report
when no findings remain. Jetbridge will validate and seal the directory; do not
publish comments or mutate a pull request yourself.
