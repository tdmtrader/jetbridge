Diagnose only the immutable log bundle mounted at `logs` and, when present, the
deployment snapshot mounted at `deployment`. Do not query logging, Kubernetes,
cloud, ticket, or deployment APIs and do not mutate a live system.

Before using any tool, transcribe the literal path and every applicable
type/digest/schema values from the `# Step outputs` and `# Sealed record
authority` sections at the top of this initial prompt. They are part of this
message and are the only valid authority. Never guess or synthesize them.

Use the bundled diagnosis skill to correlate the evidence, rank hypotheses,
calibrate confidence, seek counterevidence, and recommend bounded next actions.

Write exactly one typed candidate beneath the literal directory printed for
`$AGENT_OUTPUT_DIAGNOSIS`. It must contain `record.json` conforming to the
`diagnosis/v1` sealed-record contract. Copy the exact type and schema printed in
`$AGENT_OUTPUT_DIAGNOSIS_RECORD_TYPE` and
`$AGENT_OUTPUT_DIAGNOSIS_RECORD_SCHEMA`.
Those names are labels in the platform preamble, not process environment
variables: copy the literal values already printed there and do not run `env`,
`printenv`, or shell expansion to rediscover them. If the managed output-builder
tool is unavailable, write `record.json` directly instead of waiting for it.

Declare `logs` as the primary subject using its exact platform type and digest.
If `deployment` is mounted, declare it as a context subject using its exact
platform type and digest. Sort subjects lexicographically by id, so an included
`deployment` precedes `logs`. Sort `hypotheses` and `actions` lexicographically
by id; use contiguous ranks starting at 1 to express hypothesis priority. Sort
each action's `addresses` ids lexicographically. Unsorted or duplicate ids are
rejected when the output is sealed.

Every evidence and counterevidence anchor must name a declared subject and use
a valid locator. For log text, use `log-lines` with a canonical relative path
and positive inclusive start/end line numbers. A conclusion of `identified`
requires evidence on the rank-1 hypothesis; use `suspected` when evidence is
meaningful but not conclusive, and `inconclusive` when the bundle cannot support
a bounded diagnosis. Jetbridge validates and seals the directory.

The bundled skill contains the complete accepted JSON shape. Follow its
template exactly: all diagnosis fields belong beneath `body`, confidence is a
score object, anchors use `subject` plus `locator.kind`, and no explanatory
fields may be added to an anchor.
