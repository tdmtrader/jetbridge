Compare the immutable repositories mounted at `before` and `after`. Review the
actual change for correctness, security, regressions, tests, and maintainability;
do not modify either input and do not contact a live system.

Write exactly one typed candidate beneath the literal directory printed for
`$AGENT_OUTPUT_REVIEW`. It must contain `review.json` conforming to the
`review/v1` contract. Cite concrete files and line ranges for every finding,
distinguish blocking findings from non-blocking feedback, and explicitly report
when no findings remain. Jetbridge will validate and seal the directory; do not
publish comments or mutate a pull request yourself.
