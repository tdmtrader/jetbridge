# Implementation Plan: AI Feature Segregation

## Phase 1: Move Agent Schema Package

- [x] Task: Move `atc/agent/schema/` to `agent/schema/` and update package imports faa2abb84
  - Move all files from `atc/agent/schema/` → `agent/schema/`
  - Update import paths in all moved files (`github.com/concourse/concourse/agent/schema`)
  - Verify no `atc/` imports exist in the moved package
  - Run `go test ./agent/schema/...` — all tests pass
- [x] Task: Remove empty `atc/agent/` directory tree faa2abb84
- [x] Task: Phase 1 Manual Verification faa2abb84

## Phase 2: Move Agent Feedback API

- [x] Task: Move `atc/api/agentfeedback/` to `agent/api/feedback/` and update package imports faa2abb84
  - Move all files from `atc/api/agentfeedback/` → `agent/api/feedback/`
  - Update import paths in all moved files
  - Rename package from `agentfeedback` to `feedback`
  - Update `atc/api/handler.go` import: `agentfeedback` → `feedback` from new path
  - Update handler.go references: `agentfeedback.NewHandler` → `feedback.NewHandler`, etc.
  - Verify no `atc/` imports exist in the moved package
  - Run `go test ./agent/api/feedback/...` — all tests pass
- [x] Task: Run full build and vet checks faa2abb84
  - `go build ./cmd/concourse`
  - `go vet ./agent/...`
  - `go vet ./atc/...`
- [x] Task: Phase 2 Manual Verification faa2abb84

## Phase 3: Verify Clean Boundary

- [x] Task: Verify `agent/` has zero imports of `atc/` packages faa2abb84
  - `grep -r "concourse/concourse/atc" agent/` returns no results
- [x] Task: Run unit test suites faa2abb84
  - `go test ./agent/...`
  - `go test ./atc/api/...`
- [x] Task: Phase 3 Manual Verification faa2abb84

---
