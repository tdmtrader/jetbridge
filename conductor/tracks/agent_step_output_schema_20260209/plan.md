# Implementation Plan: Agent Step Output Schema

## Design Constraint

This package is a **standalone extension** — a self-contained library with zero Concourse imports. All code lives in `atc/agent/schema/` and depends only on Go stdlib. No existing Concourse files are modified. Future tracks will introduce minimal integration seams where the Concourse runtime reads/writes these schemas.

## Phase 1: Schema Types & Validation

- [x] b6e3d7ffa Task: Create `atc/agent/schema/` package with Results, Artifact, and Status types (zero Concourse imports)
- [x] 6e77af30a Task: Create Event, EventType constants, and event data types in the same package
- [x] ca13e68d7 Task: Write tests for Results.Validate() — required fields, confidence range 0.0-1.0, valid status enum, artifact field requirements
- [x] 0b82d3bf0 Task: Implement Results.Validate()
- [x] 4317cbe18 Task: Write tests for Event.Validate() — required ts/event/data, valid RFC3339 timestamp
- [x] 6f8d62c0a Task: Implement Event.Validate()

## Phase 2: Serialization & NDJSON Utilities

- [x] ff885d891 Task: Write marshal/unmarshal round-trip tests for Results (JSON)
- [x] 0d98952cc Task: Write marshal/unmarshal round-trip tests for Event (single JSON line)
- [x] e9b778e4c Task: Write tests for NDJSON EventWriter — append events to io.Writer, one JSON line per event
- [x] 8b6f5788f Task: Implement EventWriter
- [x] fb2423b4d Task: Write tests for NDJSON EventReader — line-by-line parse from io.Reader, validate each event
- [x] 3015f9704 Task: Implement EventReader
- [x] e09a508ad Task: Phase 2 Manual Verification — confirm package builds with zero Concourse imports (`go vet`, import check) [checkpoint: e09a508ad]

## Phase 3: Documentation

- [x] 3bcd79220 Task: Write schema reference doc (`atc/agent/schema/SCHEMA.md`) with field descriptions, examples, and extensibility conventions
- [x] e6f93739e Task: Verify no regressions — run existing Concourse test suite to confirm zero impact
- [~] Task: Phase 3 Manual Verification

---
