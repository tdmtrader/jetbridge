# CI build 587725 red: `agent/harvest` unit tests fail in the container

**Reported by:** pipeline `jetbridge` / job `unit` / build **587725**
**Severity:** blocks the release train — `unit` gates everything downstream
**Branch:** `jetbridge` @ `3f4f161e10`

## Symptom

The `unit` job has gone red on `agent/harvest`. Four of the five
`TestStampTrailer*` specs fail. Nobody can reproduce it: the same commit,
the same command, passes on every developer machine we've tried it on, and
`make test-unit` is green locally.

The specs themselves are not new-broken — they were green when the trailer
stamp landed earlier today (`2f9de88dcb`, reviewed and merged off a green
local run). CI is simply the first place this code path has ever run.

## Job log excerpt

```
--- FAIL: TestStampTrailerAppendsTicketTrailer (0.03s)
    trailer_test.go:49: git commit --amend --no-verify -m implement the thing

        Agent-Ticket: 12: exit status 128: Committer identity unknown

        *** Please tell me who you are.

        Run

          git config --global user.email "you@example.com"
          git config --global user.name "Your Name"

        to set your account's default identity.
        Omit --global to set the identity only in this repository.

        fatal: unable to auto-detect email address (got 'agent@harvest-2f9c1e.(none)')

--- FAIL: TestStampTrailerLeavesTreeIdentical (0.03s)
    trailer_test.go:70: git commit --amend --no-verify -m implement the thing
        [... same exit status 128 ...]

--- FAIL: TestStampTrailerIsIdempotent (0.03s)
    trailer_test.go:82: git commit --amend --no-verify -m implement the thing
        [... same exit status 128 ...]

--- FAIL: TestStampTrailerJoinsAnExistingTrailerBlock (0.04s)
    trailer_test.go:103: git commit --amend --no-verify -m implement the thing
        [... same exit status 128 ...]

--- PASS: TestStampTrailerRejectsNonPositiveTicket (0.00s)
FAIL	github.com/concourse/concourse/agent/harvest	0.41s
```

## Open question: is this only a CI problem?

Harvest also runs in production, inside a pod built from
`deploy/agent-runner/Dockerfile` — the same kind of bare container the test
job runs in.

Before deciding how urgent this is, read `agent/harvest/runner.go` and work
out what a delivery actually looks like when this call fails: does the
delivery still go out, and if it does, what is missing from it and who would
have noticed? Depending on the answer, this red test may be the first visible
sign of something that has been happening for a while, or it may be confined
to CI. State which, with evidence, as part of the fix.

## Acceptance

1. `go test ./agent/harvest/` passes in the `unit` job's container — one with
   `git` installed and nothing else set up in it.
2. It passes on a developer machine for the same reason it passes in the
   container, not because the two are set up differently. We will not take a
   change to the `unit` task, to `deploy/agent-runner/Dockerfile`, or to the
   test scaffolding as the fix: production runs this code in the same kind of
   container CI does, so it has to be the product code that changes.

## Constraints

- Do not weaken the guarantees the existing specs encode: the stamp is
  idempotent, it leaves the tree hash byte-identical, and it joins an
  existing trailer block rather than splitting it.
- Do not change the call ordering contract documented at the call site.
- Harvest feeds delivery-outcome accounting.
  `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` §1.11 is
  the contract for what gets recorded about a delivered commit; everything it
  defines has to come out of a stamped commit exactly as it would today.
- Add regression coverage that would have caught this. It got in past a green
  local run and a review, so a test that would also have been green on those
  machines before the fix is not coverage.
