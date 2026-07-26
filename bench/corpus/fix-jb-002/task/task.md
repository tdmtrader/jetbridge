# Agent review never reaches the build page

## Symptom

Builds that run the `ci-agent` review task finish green, but the build's
**agent-review panel in the web UI stays empty**. The review itself clearly ran —
the task log prints a full review — yet nothing shows up on the build.

This is not every build. Most builds publish their review fine; a minority
silently produce nothing. The affected builds have no failing step, so the
problem is invisible unless you open the task log.

## Evidence from an affected build

The review task (`ci/tasks/ci-agent-review.yml`) dumps the produced artifact and
then tries to publish it. On an affected build the tail of the log reads:

```
=== Review complete ===
Review output:
Here is my review of the change:
{
  "schema_version": "1.0.0",
  "score": 7,
  "findings": [ ... ]
}

=== Publishing review to ATC ===
publish error: review file /tmp/build/.../review/review.json is not valid JSON
WARNING: failed to publish review to ATC (results still available as artifact)
```

A second affected build showed the mirror image: the artifact opened with `{`,
the JSON closed cleanly, and then a trailing sentence followed it.

On a healthy build the same log section shows the artifact starting directly with
`{` (or wrapped in a fenced code block), and the publish step prints
`Published for build <n>`.

Two things are going wrong here and they compound:

1. The artifact the review phase wrote is not parseable as JSON, so the publish
   step rejects it.
2. The publish failure is non-fatal by design — the task prints a `WARNING` and
   the build stays green — so the loss is silent. Nobody notices until someone
   asks why a build has no review.

## Context

- `ci-agent` is a standalone Go module in `ci-agent/` (its own `go.mod`), built
  into the agent runner image and invoked by the review task.
- The review phase drives the Claude CLI and writes the model's answer out as the
  `review.json` artifact; `ci-agent publish` then reads that file and POSTs it to
  the ATC's agent-reviews API.
- Model output is not perfectly stable in shape. Sometimes it is bare JSON,
  sometimes it is fenced in a ```` ```json ```` block, and — as in the log above —
  sometimes the model introduces or signs off its answer in ordinary prose with
  no fence at all. The prompt asks for JSON only, but that is a request, not a
  guarantee, and re-prompting the model is not a fix we control at review time.

## Expected behaviour

A review the model produced should reach the build page even when the model
wraps its JSON answer in surrounding prose. Whatever `ci-agent` hands to
`publish` must be valid JSON.

## Constraints

- Do not weaken the publish-side validation. The payload sent to the ATC must
  still be valid JSON; publish rejecting a genuinely unparseable artifact is
  correct behaviour and should stay.
- Output shapes that already work must keep working — bare JSON and fenced
  ```` ```json ```` blocks both round-trip correctly today, and output with no
  recoverable JSON at all must keep degrading the way it does now rather than
  panicking or inventing a payload.
- `ci-agent` is consumed by other packages in the same module; keep its exported
  API backwards compatible.
- Add regression coverage. The module's tests are Ginkgo-based and run without
  any external services: `cd ci-agent && go test ./...`.
