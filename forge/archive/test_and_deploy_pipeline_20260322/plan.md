> **Reconciled & closed 2026-06-07.** Delivered in deploy/concourse-pipeline.yml (git-triggered test chain -> push-to-main -> GHCR image build/push -> theborg self-upgrade); all spec requirements met and iterated heavily since. Metadata/checkboxes stale.
>
> Reviewed via a parallel track audit; no further work needed (see closure reason). Original plan preserved below for the record.

# Implementation Plan: Test and Deploy Pipeline

## Phase 1: Pipeline Implementation

- [ ] Task: Write new `deploy/concourse-pipeline.yml` with git-triggered test chain, push-to-main, GHCR build+push, and self-upgrade jobs
- [ ] Task: Set pipeline on theborg via `fly set-pipeline` with github_token variable
- [ ] Task: Phase 1 Manual Verification — trigger pipeline and verify full flow

---
