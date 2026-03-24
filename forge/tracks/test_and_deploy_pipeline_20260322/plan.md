# Implementation Plan: Test and Deploy Pipeline

## Phase 1: Pipeline Implementation

- [ ] Task: Write new `deploy/concourse-pipeline.yml` with git-triggered test chain, push-to-main, GHCR build+push, and self-upgrade jobs
- [ ] Task: Set pipeline on theborg via `fly set-pipeline` with github_token variable
- [ ] Task: Phase 1 Manual Verification — trigger pipeline and verify full flow

---
