# Platform Blocking-Bug Audit Report

**Audit date:** 2026-08-01
**Branch:** `codex/agentic-platform-rebase`
**Audited base:** `fbb44c8333`
**Remediated head:** `aa1740baa6`

## Audit contract

This pass reported only reproduced behavior that contradicted an existing
ownership, confidentiality, or release-safety contract, or that could block
safe platform use. Feature gaps, possible enhancements, cleanup, and general
product shortcomings were excluded.

The audit covered these independent surfaces:

- control-plane persistence and authorization, with emphasis on snapshot
  ownership and lifecycle transitions;
- agent runtime and data-plane boundaries, including path containment,
  provider executable identity, output construction, and Hangar integration;
- deployment and release paths, including parameterized credentials, shell
  tracing, Git publication, and promotion ordering.

The runtime/data-plane review did not reproduce an additional blocking defect.
Four blocking bugs were reproduced elsewhere; all four are fixed below.

## Confirmed and resolved bugs

### AUDIT-001 — equal-digest reseal revived another team's expired snapshot

**Impact:** data-integrity and isolation violation. Once snapshot manifests
became directly team-owned, `CommitSealBatch` still revived expired rows by
digest alone. Resealing a digest for one team could therefore make another
team's independently owned expired content usable again.

**Proof before correction:** a two-team, equal-digest database regression set
both manifests to `expired`, resealed only for the default team, and observed
the other team's exact row transition to `available`.

**Correction:** the revival update in `atc/db/agent_snapshots_factory.go` now
requires both the digest and `commit.Context.TeamID`. Digest-global location
storage and same-team sibling revival remain unchanged.

**Commit:** `5cb494b7b9` (`fix(db): scope snapshot revival to owning team`)

### AUDIT-002 — authenticated Git commands exposed credentials through xtrace

**Impact:** credential disclosure. The RC tag task in
`deploy/concourse-pipeline.yml` and the Borg promotion task in
`deploy/borg-pipeline.yml` started their shells with `-x`, then expanded
`GITHUB_TOKEN` inside authenticated remote URLs. The expanded token could be
written to build logs.

**Proof before correction:** a parsed, flow-sensitive pipeline regression
reported exactly `tag-rc/create-rc-tag` and
`promote-to-main/push-to-main` while xtrace was active.

**Correction:** only those two credential-bearing tasks changed from `sh -exc`
to `sh -ec`. Existing explicit `set +x`/`set -x` guards remain supported, and
the regression now rejects token, password, or secret parameter expansion
whenever tracing is active.

**Commit:** `79ce939984` (`fix(deploy): stop tracing Git credentials`)

### AUDIT-003 — release could overwrite a concurrent legitimate `main`

**Impact:** repository history and release data loss. The final release task
used `git push --force origin HEAD:refs/heads/main`; a concurrent update to
`main` could be silently replaced.

**Proof before correction:** the release-pipeline regression found the force
push. The race was reproduced with a real bare Git remote and sibling release
and concurrent-main histories.

**Correction:** `deploy/push-jetbridge-main.sh` fetches `origin/main`, captures
the exact fetched commit from `FETCH_HEAD`, requires it to be an ancestor of
the release commit, and performs an ordinary push. The ordinary server-side
push check also rejects a change that lands after the fetch. Release ordering
still places publication after the final tag push and before deployment.

**Commit:** `d202e5acd8` (`fix(deploy): publish main without force`)

### AUDIT-004 — Git-backed deployment tests failed on clean CI hosts

**Impact:** adoption and verification blocker. The real-Git release and GitOps
writeback fixtures initialized bare remotes without naming their default
branch. They passed on the audit workstation only because Apple's system Git
configuration supplies `init.defaultBranch=main`; a clean Linux Git setup
advertised an unborn `master` while the fixtures seeded only `main`.

**Proof before correction:** with global and system Git configuration disabled,
the focused release test failed its fast-forward proof and the full deployment
package failed every home-infra Git fixture before exercising its intended
behavior.

**Correction:** both bare remotes now initialize with an explicit `main` branch.
No production helper or acceptance assertion changed.

**Commit:** `aa1740baa6` (`fix(deploy): make git fixtures branch deterministic`)

## Verification

- Serial PostgreSQL regression: 3 snapshot lifecycle/ownership specs passed,
  0 failed, 1455 skipped. It was run outside the sandbox because PostgreSQL's
  `initdb` requires a shared-memory operation denied by the sandbox.
- Full deployment package: `go test ./deploy -count=1` passed.
- Clean-Git deployment package: the same full package passed with global and
  system Git configuration disabled.
- Shell syntax: both `deploy/push-jetbridge-main.sh` and
  `deploy/write-agent-runner-home-infra.sh` passed `sh -n`.
- Chart validation: `helm lint deploy/chart` linted 1 chart with 0 failures.
- Each task received an independent blocking-only review and was approved.
- The final independent review of `fbb44c8333..aa1740baa6` found no Critical,
  Important, or Minor issue and approved the complete four-fix delta.
- `git diff --check` passed before final documentation assembly.

## Remaining blocker outside this audit's code changes

The existing first-user remediation track's Task 6 remains unable to begin its
rollout and node-level acceptance pass until the already verified agent-runner
digest is activated through the separate `home-infra` GitOps boundary. That is
an external-state/authority gate, not a newly reproduced defect in this code
audit. No direct Kubernetes, home-infra, release, push, or pipeline mutation was
performed here.

There are no new implementation discussion points: each reproduced code bug
had a clear correction above the requested confidence threshold.

## Workspace preservation

The audit and fixes were performed only in the linked semantic-rebase worktree.
The three pre-existing dirty evidence files were neither staged nor committed:

- `.superpowers/sdd/2026-07-28-agentic-foundations-semantic-rebase/progress.md`
- `.superpowers/sdd/2026-08-01-jetbridge-first-user-blocker-remediation/progress.md`
- `JETBRIDGE_FIRST_USER_FINDINGS.md`
