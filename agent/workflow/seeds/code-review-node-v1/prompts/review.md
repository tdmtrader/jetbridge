Compare the immutable repositories mounted at `before` and `after`. Review the
actual change for correctness, security, regressions, tests, and maintainability.
Do not modify either input and do not contact a live system.

Before using any tool, transcribe the literal output path and all six
type/digest/schema values from the `# Step outputs` and `# Sealed record
authority` sections at the top of this initial prompt. They are part of this
message and are the only valid authority. Never guess or synthesize them.

Use the bundled review skill for the review method. Apply the
`MINIMUM_SEVERITY` threshold supplied by the invoking workflow or direct test.
The accepted severity order is observation, low, medium, high, critical. High
and critical findings must be blocking; observations cannot be blocking.

The inputs may be plain filesystem trees without Git metadata. Compare their
contents directly when Git history or `git diff` is unavailable. Review the
delta rather than treating every pre-existing file as newly authored.

Write exactly one typed candidate beneath the literal directory printed for
`$AGENT_OUTPUT_REVIEW`. It must contain `record.json` conforming to the
`review/v1` sealed-record contract. Copy the exact type and schema printed in
`$AGENT_OUTPUT_REVIEW_RECORD_TYPE` and `$AGENT_OUTPUT_REVIEW_RECORD_SCHEMA`.
Those names are labels in the platform preamble, not process environment
variables: copy the literal values already printed there and do not run `env`,
`printenv`, or shell expansion to rediscover them. If the managed output-builder
tool is unavailable, write `record.json` directly instead of waiting for it.
Declare two subjects, copying their exact platform type/digest values: `after`
with role `primary` and `before` with role `context`.

The `subjects` array must be sorted lexicographically by id, so here it reads
`after` then `before`. The `findings` array must be sorted lexicographically by
finding id. Unsorted or duplicate ids are rejected when the output is sealed.
Cite concrete subject files and line ranges for every finding, distinguish
blocking findings from non-blocking feedback, and explicitly report when no
findings remain. Jetbridge validates and seals the directory; do not publish
comments or mutate a pull request yourself.

The bundled skill contains the complete accepted JSON shape. Follow its
template exactly: all review fields belong beneath `body`, and anchors use
`subject` plus `locator.kind` with no explanatory fields added to the anchor.
