# Release review: PR #9165 "Switch use of md5 hashing to sha256"

**Requested by:** release manager, 7.14.0
**Repository snapshot:** `concourse/concourse` at the commit the `release/7.14.x`
branch was just cut from
**Change under review:** `task/change-under-review.diff`

## Context

We cut `release/7.14.x` from `master` today. Everything currently on `master` is
therefore queued to ship as **7.14.0**, and 7.14.0 is a **minor** release off the
7.13 line — operators running 7.13.x will upgrade into it in place.

Before we tag an RC I am walking the larger changes that landed on `master` since
7.13 and getting a second opinion on each one. This is one of them.

PR #9165 (merged to `master` on 2025-05-10, contributor `IvanChalukov`, branch
`sha256-switch`) replaces md5 with sha256 as the digest used for resource
versions. It touches `atc/db`, the scheduler algorithm, and ships a database
migration. The diff as merged is attached; it is also already present in the
snapshot you have.

Relevant house rules, so we are working from the same ones:

- We follow semver. Breaking changes ship in a major release only. The next major
  is v8; it has no date and no release branch yet.
- Concourse `web` nodes are rolled, not stopped, during an upgrade.
- Database migrations run at `web` startup, and `concourse migrate
  --target-version` exists for moving a deployment backwards.
- CI is green on this branch, as it was on `master` when the PR merged.

## What I need

Read the change and give me a disposition for 7.14.0:

- **approve** — ships as-is in 7.14.0;
- **approve with changes** — ships in 7.14.0 once specific, named changes are
  made (say exactly what they are);
- **block** — must not ship in 7.14.0 (say what should happen to it instead).

Back the disposition with findings anchored to files and, where you can, to
specific lines or statements. Cover at least:

1. **Correctness** — does the change do what it says, on both the Go side and in
   the migration?
2. **Migration and upgrade safety** — what happens to an existing 7.13.x
   deployment that takes this upgrade, and what happens if it has to go back?
3. **Release-line fit** — whether 7.14.0 is the right release for this change.

Whatever the disposition, I need something I can put in a comment on the PR and
act on this week: if you are recommending anything other than "ship it as-is",
be concrete about what happens to the change instead.

Rank findings by severity. Do not rewrite the change for me — I want the review,
not a patch.

## Deliverable

Write the review to **`REVIEW.md` at the root of the repository snapshot**. Its
first line must be exactly one of:

```
Disposition: approve
Disposition: approve with changes
Disposition: block
```

Everything after that line is yours: the reasoning, the ranked findings, and —
if you are not approving as-is — what you want done with the change instead.
Nothing else in the tree needs to change; `REVIEW.md` is the whole deliverable.
