# Adversarial review — agentic UX wave 2, before it merges

Review the `after` repository state against `before`. `before` is the jetbridge
mainline the branch was cut from; `after` is the finished
`agentic-ux-wave-2` branch, immediately before it merges.

This is the last gate on the branch. The next step after this review is a merge
and a web deploy, so the review is deliberately adversarial: assume the work is
wrong until the code says otherwise, and go looking for the ways it breaks in
front of a real operator rather than confirming that it compiles.

## What the branch set out to do

The wave implements findings U1–U24 from a fresh-eyes UX audit of the agentic
surfaces. `AGENTIC_UX_WAVE_2_SCOPE.md` at the root of `after` is the branch's own
scope-and-sequencing note (slice table, deferred items, rationale). The pages it
touches are the agent console (`/agent`), the agent-ticket list and detail pages,
the dashboard's agent strip, and the agent-review/step rendering on the build
page. It is mostly Elm, with a small Go slice on the run-metrics read path.

The branch was built as seven sequential slices, the later ones by sub-agents,
each compile-checking its own slice, with the committed web bundle regenerated
once at the end.

## What to produce

Findings, not fixes. Do not modify either repository state.

For each finding give:

- **where** — file and line range in `after`, precise enough to act on without
  re-deriving it;
- **what breaks** — a concrete scenario: the state the user is in, what they do,
  what they see or lose. "This looks fragile" is not a finding;
- **severity** — one of `critical` / `high` / `medium` / `low`, judged by user
  impact and likelihood, not by how interesting the code is;
- **blocking or not** — does this have to be fixed before the branch merges, or
  is it follow-up work;
- **introduced here or pre-existing** — the branch reuses and moves existing
  components, so not everything that surfaces during the review will be new.
  Attribute each finding.

Rank the findings so the highest-impact one is first. If a section of the diff is
clean, say so — silence on a slice reads the same as not having looked at it.

## Scope and constraints

- `web/public/elm.js` and `web/public/elm.min.js` are generated artifacts,
  rebuilt from `web/elm/src` by `hack/build-web.sh`. They are not reviewable
  source; review the Elm sources and ignore the bundle diff.
- The Elm test suite lives in `web/elm/tests` and runs with `elm-test` from
  `web/elm`. It was green when the branch was cut and is green now, so "the tests
  pass" is not evidence that a behaviour is correct — it may only mean nothing
  covers it.
- The platform's cross-cutting contracts (run and build lifecycle, the agent step
  vocabulary, the ticket state machine) are written down under
  `docs/superpowers/plans/`; the shared-contracts document is the authority for
  what the server-side states actually mean.
- Style, naming and formatting preferences are out of scope unless they cause a
  defect.
