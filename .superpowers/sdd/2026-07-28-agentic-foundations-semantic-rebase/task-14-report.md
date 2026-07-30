# Task 14 implementation report — direct in-ATC publication

## Status

Implementation checkpoint complete. Independent review is pending.

## Delivered behavior

- `agent/publisher` resolves exact policy authority and opens opaque,
  destination-scoped credentials without allowing authored destinations or
  credentials to select a remote.
- The direct Git adapter publishes from a private `0700` bare scratch
  repository with controlled Git execution, an atomic target/marker update,
  marker recovery, and a `rebase_required` result when the target changes.
- Delivery publication prepares and preflights rebase candidates; the
  repository-merge function replays multi-commit candidates and cleans up a
  conflicted rebase.
- ATC composes the direct snapshot publisher through the existing command
  composition seam. The legacy gateway transport and flags are absent.
- Helm now exposes `agentPublisher`: a human-reviewed policy Secret and a
  distinct credential Secret are mounted read-only only into `concourse-web`.
  The chart allows only the direct Git adapter, validates exact credential
  mappings and AtomicWriter bounds, rejects aliases through other consumers,
  and keeps the publisher mount boundary reserved.
- The final ATC runtime image installs the exact Jammy Git package and runs as
  the chart's non-root identity. It contains no publisher credential paths or
  values.
- Operator documentation describes the direct ATC-owned publication contract,
  exact policy, credential isolation, and direct Git limitations.

## TDD evidence

- The direct publisher Helm render test first failed because the old chart
  rendered none of the required direct flags, mounts, or Secret items. It
  passed after the direct values, validation, web-only mounts, and arguments
  were added.
- The runtime image contract first failed because the final Ubuntu stage did
  not install the exact Git package. It passed after the pinned package and
  non-root runtime user were added.
- The full direct-publisher chart group exposed missing chart README operator
  guidance; it passed after the direct policy, AtomicWriter, fail-closed
  adapter, and credential-manager boundary guidance was added.

## Verification

Passed:

- `go test ./agent/publisher/... -count=1`
- `go test ./agent/functions/repositorymerge ./cmd/function-runner ./agent/workflow/... -count=1`
- `go test ./atc/atccmd ./atc/exec -count=1`
- `go test ./deploy/chart/tests -run '^TestAgentPublisher' -count=1`
- `go test ./deploy -run TestATCRuntimeImageContainsControlledGitWithoutPublisherCredentials -count=1`
- `helm lint deploy/chart`
- `gofmt` on changed Go tests and `git diff --check`

## Residue

Production chart values, flags, mounted resources, implementation transport,
and operator documentation no longer expose the gateway topology. The sole
production-source reference to `agentPublisherGateway` is an explicit Helm
fail-closed tombstone: old values are rejected with “has been removed; use
agentPublisher”, rather than being silently accepted. Focused tests retain the
retired names only to prove their absence/rejection. Stale snapshot-contract
comments now name `agent/publisher/directgit`.

## Commits

- `b2774ce2e2 feat(publisher): resolve direct publication authority`
- `c960867479 feat(publisher): publish direct git refs atomically`
- `b0f2497738 fix(delivery): rebase candidates before publication`
- `14e27b5032 feat(atc): compose direct snapshot publisher`
- Helm/image/docs checkpoint: recorded with this report.

## Fix round 1 — fixed image Git binary

Independent review found that `NewCommandRunner` resolved `git` through the
inherited process `PATH`. Because chart-controlled `web.env` can set `PATH`, a
counterfeit executable could have received the runner's askpass environment.

`NewCommandRunner` now always delegates to `newCommandRunner` with the fixed
image-owned `/usr/bin/git` path. The injected-path `newCommandRunner` seam is
unchanged for controlled runner tests.

RED evidence:

```text
go test ./agent/publisher/directgit -run TestNewCommandRunnerUsesFixedImageGitDespitePATH -count=1
git path = ".../git", want fixed image path /usr/bin/git
counterfeit Git ran
```

GREEN verification:

```text
go test ./agent/publisher/directgit -count=1
ok  github.com/concourse/concourse/agent/publisher/directgit  4.600s
git diff --check
```
