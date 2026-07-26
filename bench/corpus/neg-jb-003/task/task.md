# My docs push to `jetbridge` did not trigger a build

**Type:** diagnosis — and a fix, if this turns out to be a defect
**Opened:** 2026-07-19
**Priority:** medium — I need to trust this trigger before I dispatch more loop work

## What happened

About 40 minutes ago I pushed a single docs-only commit to the `jetbridge`
branch on GitHub (`644184e3f0` — one file, under `docs/`). Nothing happened.
`build-and-vet`, the head of the self-release chain on the `cicd` team's
pipeline, has no new build for it, and nothing downstream ran either.

## What I expected

A push to `jetbridge` normally starts the chain: `build-and-vet` →
`unit-tests` / `k8s-runtime-tests` → `tag-rc` → `build-image` →
`self-upgrade` → `release`. That has been working all week for code pushes —
the UX4 merges earlier today each produced a build within a minute or two of
landing.

## What I have already ruled out

- The pipeline is not paused, `build-and-vet` is not paused, nothing is archived.
- The pipeline config stored on the server is current: I diffed
  `fly get-pipeline` against what is checked in on the branch and they are
  identical. This is not a stale-config problem left over from some config
  change that was never re-set.
- GitHub really has the commit on `jetbridge` — I re-checked the remote.
- The `repo` resource's checker is alive: its last-check timestamp keeps
  advancing and no check has errored.
- `concourse-web` logs at `--log-level=info` show **nothing at all** from the
  scheduler around the time of the push — no scheduling lines for this
  pipeline, no job lines, nothing. That silence is the part that bothers me
  most.

## Why I am worried

This fork has been bitten before by silently dropped `NOTIFY`s. The
notification bus does a non-blocking send into a capacity-1 channel and simply
drops the notification when the channel is full, and we had a real incident
where scheduling quietly stopped because of exactly that. The symptom then
looked like this: work not happening, and nothing in the logs to say why. It
was fixed months ago, but my working theory is that we have regressed it — or
that something else between "resource check" and "job gets triggered" is
dropping this version on the floor.

## What I want back

1. **A diagnosis.** What actually happens between my push and the missing
   build? Name the mechanism concretely — file, config stanza, or code path.
2. **My theory, confirmed or refuted**, with the evidence that settles it. If
   it is refuted, tell me what the missing scheduler log lines actually mean.
3. **A fix, if this is a defect.** If it is *not* a defect, say so plainly, and
   tell me what the intended behavior is and why it is that way — I want to
   write it down somewhere durable so nobody re-litigates it in three weeks.

## Constraints

- Diagnose from the repository. There is no cluster access for this and no
  further log capture available; do not present inferred behavior as observed,
  and do not invent log lines or metrics you could not have seen.
- Cite `file:line`, or the file plus the quoted config, for every claim about
  how the system behaves. Distinguish what you verified from what you inferred.
- If you propose any change, state precisely what else it affects and who
  depends on the current behavior.

## How to send it back

Write your answer to a file called `DECISION.md` at the root of the
repository — the diagnosis, your verdict on my theory, and your
recommendation. That file is what I read, so state your conclusion in it
explicitly rather than leaving it implied by whatever else you did or did not
touch.

If your conclusion is that something needs to change, make the change in the
tree as well and describe it in `DECISION.md`. If your conclusion is that
nothing needs to change, leave the tree as you found it and say that in
`DECISION.md`. Either way I want the reasoning, not just the verdict.
