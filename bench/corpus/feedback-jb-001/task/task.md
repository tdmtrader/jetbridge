# Address the merge-gate review of `agentic-ux-wave-2`

**Type:** feedback loop (respond to review findings)
**Branch:** `agentic-ux-wave-2`
**Target:** `jetbridge` (the mainline; the self-build pipeline deploys it within
minutes of merge)
**Blocking:** yes — the merge is held until this is done

## Context

`agentic-ux-wave-2` implements the U1–U24 findings from the fresh-eyes UX audit
of the agentic surfaces: the ticket page, the ticket queue, the dashboard agent
strip, the `/agent` operator console, agent/harvest steps on the build page, and
the run-status truth that feeds all of them. Its scope, sequencing and explicit
deferrals are in `AGENTIC_UX_WAVE_2_SCOPE.md` at the repo root.

The branch has been through its merge gate: an adversarial review of the whole
wave, whose verdict is **merge after fixes**. The review is in
`task/review-findings.md` — thirteen verified defects (F1–F13) ranked by
operator impact, two minor housekeeping items, and a list of candidates the
reviewer investigated and refuted.

## What is being asked

Produce the change that lets this branch merge: address the review.

- **Fix what the review found, at the level of behavior it specifies.** The
  findings state a failure scenario and what correct behavior would be; they do
  not prescribe an implementation. Where a finding's remedy is under-determined,
  choose the option that survives the failure scenario it names — several of
  these bugs are re-breakable by an obvious-looking fix.
- **Do not fix what it refuted.** The refuted list is refuted; re-litigating it
  costs the merge time it does not have. Two of the entries name behavior that
  is *deliberate* and must survive your change.
- **Do not widen the scope.** This is a merge gate, not a second wave. The
  deferrals in `AGENTIC_UX_WAVE_2_SCOPE.md` stand. Unrelated refactors,
  renames, and drive-by cleanups outside the findings do not belong in this
  change.
- **Say what you changed and why, finding by finding**, including anything you
  chose *not* to change and your reason. One finding explicitly touches a frozen
  state machine outside the wave's own files; call that out rather than slipping
  it in.

## Constraints

- **The Elm bundle is committed and served.** `web/public/elm.min.js` is what
  the web node serves; `hack/build-web.sh` is what produces it. Any change to
  `web/elm/src/**` that is not accompanied by a rebuilt, committed bundle ships
  as a no-op — the mainline has been burned by this before.
- **The `elm-test` suite must be green** (`web/elm/tests/`). Several of these
  fixes change a shared model field or a message constructor, so the fan-out
  reaches modules and tests that the finding does not name. Existing specs that
  encode the *old, wrong* behavior should be corrected, not deleted — and say so
  when you do it.
- **The Go tests must be green.** In particular `agent/api/tickets` guards the
  ticket state machine; its invariants (which states are terminal, which edges
  exist) are load-bearing for the dispatcher and the reconciler.
- **Add regression coverage for the behavioral fixes.** Each fix that changes
  what a pure function or a decoder returns should end up pinned by a spec; a
  fix nobody can re-break silently is worth more than a fix.
- Style, naming and formatting churn is out of scope.

## Deliverable

A change on `agentic-ux-wave-2` that the mainline can take as-is: the code
changes, the tests, the rebuilt bundle, and a finding-by-finding write-up.

Deliver the write-up as the commit message if the tree you are working in is a
git checkout. If it is not, write it to `MERGE_GATE_RESPONSE.md` at the repo
root instead — the merge gate needs the account either way. It must list every
finding you addressed, name anything you deliberately left alone and why, and
flag any change that reaches outside the wave's own files.
