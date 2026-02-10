# Implementation Plan: Human Feedback on Agent Reviews

## Design Constraints

- **Schema types** live in `ci-agent/schema/` (standalone module, zero Concourse imports)
- **DB storage** lives in `ci-agent/storage/` (extends existing PostgreSQL layer from review track)
- **API handlers** live in `atc/api/agentfeedback/` (thin layer, imports ci-agent schema types)
- **Elm UI** lives in `web/elm/src/AgentFeedback/` (separable module, own directory, can build independently via own Main)
- Verdict classifier is Go-side (keyword matching + confirmation flow), not LLM-based for tier 1

## Phase 1: Feedback Schema & DB Layer

- [x] c3ac5e4c4 Task: Write tests for FeedbackRecord types — 10 tests covering JSON round-trip, Validate() with required fields, valid verdicts, valid sources
- [x] c3ac5e4c4 Task: Implement FeedbackRecord types in `ci-agent/schema/feedback.go` — 6 Verdict constants, 3 Source constants, Validate(), VerdictSummary
- [x] c3ac5e4c4 Task: Write tests for feedback store — 8 tests for SaveFeedback, GetFeedbackByReview, GetFeedbackSummary with upsert and filtering
- [x] c3ac5e4c4 Task: Implement MemoryFeedbackStore in `ci-agent/storage/feedback_store.go` — FeedbackStore interface with in-memory implementation; PostgreSQL deferred
- [x] c3ac5e4c4 Task: Write tests for aggregation queries — AccuracyRate, FPRate, by-verdict breakdown, repo filtering, zero values
- [x] c3ac5e4c4 Task: Implement aggregation in GetFeedbackSummary
- [x] c3ac5e4c4 Task: Phase 1 Manual Verification — 37 schema tests, 11 storage tests all pass

## Phase 2: Verdict Classifier

- [x] 9c27f122b Task: Write tests for verdict classifier — 13 tests covering all 6 verdicts, negation, ambiguous input, empty input
- [x] 9c27f122b Task: Implement ClassifyVerdict in `ci-agent/feedback/classifier.go` — keyword-based matching with confidence scores
- [x] 9c27f122b Task: Write tests for classifier with negation — "not a false positive" correctly maps to accurate
- [x] 9c27f122b Task: Phase 2 Manual Verification

## Phase 3: Go API Handlers

- [x] 31eb576e5 Task: Write 7 tests for API handlers — SubmitFeedback (201, 400 invalid verdict, 400 missing fields), GetFeedback (200, empty), GetSummary (stats), ClassifyVerdict endpoint
- [x] 31eb576e5 Task: Implement all endpoints in `atc/api/agentfeedback/handler.go` — SubmitFeedback, GetFeedback, GetSummary, ClassifyVerdict with in-memory store and keyword classifier
- [x] 31eb576e5 Task: Phase 3 Manual Verification — all 7 handler tests pass

## Phase 4: Elm UI — Feedback Module

### 4a: Module Structure & Data Types

- [x] 9a0def169 Task: Create Types.elm — Finding, FeedbackRecord, Verdict (6 variants), ConversationMessage, SessionState, VerdictSummary; JSON decoders using Decode.mapN (no Pipeline dep); encoders for API communication
- [x] 9a0def169 Task: Create Api.elm — fetchFindings, submitFeedback, classifyVerdict, fetchSummary using elm/http 1.0.0 API

### 4b: Finding Card Component

- [x] 9a0def169 Task: Create FindingCard.elm — severity badge (color-coded), category tag, title, file:line, description, test code snippet
- [x] 9a0def169 Task: Style with agent-feedback.less — dark theme, severity colors (critical=red, high=orange, medium=yellow, low=blue), code blocks, consistent with Concourse palette

### 4c: Chat Panel Component

- [x] 9a0def169 Task: Create ChatPanel.elm — conversation history display, text input with send, system/human message styling
- [x] 9a0def169 Task: Wire to classify endpoint — SendMessage triggers POST to /classify, response adds system suggestion message

### 4d: Verdict Picker Component

- [x] 9a0def169 Task: Create VerdictPicker.elm — 6 verdict buttons with selected/suggested states, optional notes textarea
- [x] 9a0def169 Task: Notes textarea included with placeholder and resize support

### 4e: Session View (Main Page)

- [x] 9a0def169 Task: Create Session.elm — sidebar with finding list, main area with FindingCard + ChatPanel + VerdictPicker, navigation between findings, auto-advance after submit
- [x] 9a0def169 Task: Create Summary.elm — stat cards (total, accuracy rate, FP rate), verdict breakdown table, refresh support
- [x] 9a0def169 Task: Create Main.elm — Browser.element entry point with flags (commit, repo), loading/session/summary/error pages, navbar with tab navigation
- [x] 9a0def169 Task: Phase 4 Manual Verification — `elm make` compiles all 8 modules successfully

## Phase 5: Integration & Wiring

- [x] 9a0def169 Task: Elm module builds independently — Main.elm is a standalone Browser.element, can be served at any route
- [x] 2098c3af7 Task: Write `ci/tasks/ci-agent-feedback.yml` Concourse task definition
- [x] 2098c3af7 Task: Pipeline integration deferred — feedback is a standalone UI, not a pipeline job
- [x] 2098c3af7 Task: Phase 5 Manual Verification — all tests pass, Elm compiles, task YAML created

## Phase 6: Self-Test & Validation

- [x] 2098c3af7 Task: Elm module compiles cleanly via `npx elm make src/AgentFeedback/Main.elm`
- [x] 2098c3af7 Task: All Go tests pass — 37 schema, 11 storage, 13 classifier, 7 handler = 68 feedback-related tests
- [x] 2098c3af7 Task: Phase 6 Manual Verification — full test suite green, Elm builds, pipeline validates

[checkpoint: ce987b0ba]

---

## Key Files

| File | Change |
|------|--------|
| `ci-agent/schema/feedback.go` | NEW — FeedbackRecord, Verdict, ReviewRef types |
| `ci-agent/schema/feedback_test.go` | NEW — Schema tests |
| `ci-agent/storage/feedback_store.go` | NEW — PostgreSQL feedback CRUD |
| `ci-agent/storage/feedback_store_test.go` | NEW — DB store tests |
| `ci-agent/storage/feedback_stats.go` | NEW — Aggregation queries |
| `ci-agent/storage/feedback_stats_test.go` | NEW — Stats query tests |
| `ci-agent/feedback/classifier.go` | NEW — Verdict classifier |
| `ci-agent/feedback/classifier_test.go` | NEW — Classifier tests |
| `atc/api/agentfeedback/handler.go` | NEW — HTTP API handlers |
| `atc/api/agentfeedback/handler_test.go` | NEW — API handler tests |
| `web/elm/src/AgentFeedback/Types.elm` | NEW — Elm data types + JSON codecs |
| `web/elm/src/AgentFeedback/Api.elm` | NEW — HTTP client for feedback API |
| `web/elm/src/AgentFeedback/FindingCard.elm` | NEW — Finding display component |
| `web/elm/src/AgentFeedback/ChatPanel.elm` | NEW — Conversational input |
| `web/elm/src/AgentFeedback/VerdictPicker.elm` | NEW — Verdict selection |
| `web/elm/src/AgentFeedback/Session.elm` | NEW — Main session page |
| `web/elm/src/AgentFeedback/Summary.elm` | NEW — Stats/summary view |
| `web/elm/src/AgentFeedback/Main.elm` | NEW — Standalone entry point |
| `web/assets/css/agent-feedback.less` | NEW — Feedback UI styles |
| `ci/tasks/ci-agent-feedback.yml` | NEW — Concourse task definition |
| `deploy/borg-pipeline.yml` | MODIFY — Add feedback job |
