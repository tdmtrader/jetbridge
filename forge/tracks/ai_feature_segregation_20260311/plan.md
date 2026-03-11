# Implementation Plan: AI Feature Segregation

## Phase 1: Move Agent Schema Package

- [ ] Task: Move `atc/agent/schema/` to `agent/schema/` and update package imports
  - Move all files from `atc/agent/schema/` → `agent/schema/`
  - Update import paths in all moved files (`github.com/concourse/concourse/agent/schema`)
  - Verify no `atc/` imports exist in the moved package
  - Run `go test ./agent/schema/...` — all tests pass
- [ ] Task: Remove empty `atc/agent/` directory tree
- [ ] Task: Phase 1 Manual Verification

## Phase 2: Move Agent Feedback API

- [ ] Task: Move `atc/api/agentfeedback/` to `agent/api/feedback/` and update package imports
  - Move all files from `atc/api/agentfeedback/` → `agent/api/feedback/`
  - Update import paths in all moved files
  - Rename package from `agentfeedback` to `feedback`
  - Update `atc/api/handler.go` import: `agentfeedback` → `feedback` from new path
  - Update handler.go references: `agentfeedback.NewHandler` → `feedback.NewHandler`, etc.
  - Verify no `atc/` imports exist in the moved package
  - Run `go test ./agent/api/feedback/...` — all tests pass
- [ ] Task: Run full build and vet checks
  - `go build ./cmd/concourse`
  - `go vet ./agent/...`
  - `go vet ./atc/...`
- [ ] Task: Phase 2 Manual Verification

## Phase 3: Verify Clean Boundary

- [ ] Task: Verify `agent/` has zero imports of `atc/` packages
  - `grep -r "concourse/concourse/atc" agent/` returns no results
- [ ] Task: Run unit test suites
  - `go test ./agent/...`
  - `go test ./atc/api/...`
- [ ] Task: Phase 3 Manual Verification

---
