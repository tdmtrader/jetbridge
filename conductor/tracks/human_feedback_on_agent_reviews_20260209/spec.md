# Spec: Human Feedback on Agent Reviews

**Track ID:** `human_feedback_on_agent_reviews_20260209`
**Type:** feature

## Overview

Builds the feedback loop for improving agent code reviews. After the agent-review step produces findings (ProvenIssues + Observations), humans evaluate them through an interactive conversational UI built in Elm — consistent with the Concourse web stack but clearly separable as its own module. The system captures structured feedback in PostgreSQL, building a dataset of `(code_context, agent_finding, human_verdict)` triples. Future iterations infer feedback implicitly from conversation history and ultimate outcomes (PR merged, fix reverted, etc.).

## Feedback Collection Tiers

| Tier | Source | When | Track |
|------|--------|------|-------|
| **Tier 1 (this track)** | Interactive chat UI — human reviews each finding conversationally | Manual trigger after review | This |
| **Tier 2 (future)** | Conversation history inference — mine agent chat logs for implicit signals | Automatic post-session | Future |
| **Tier 3 (future)** | Outcome inference — PR merged/reverted, fix kept/discarded, CI green/red | Automatic post-merge | Future |

## Architecture

```
┌─────────────────────────────────────┐
│  Elm UI (separable module)          │
│  web/elm/src/AgentFeedback/         │
│                                     │
│  ┌───────────┐  ┌────────────────┐  │
│  │ FindingCard│  │ ChatPanel      │  │
│  │ (context)  │  │ (conversation) │  │
│  └───────────┘  └────────────────┘  │
│  ┌───────────┐  ┌────────────────┐  │
│  │VerdictPicker│ │ SessionSummary│  │
│  └───────────┘  └────────────────┘  │
└──────────────┬──────────────────────┘
               │ HTTP JSON API
┌──────────────▼──────────────────────┐
│  Go API handlers                    │
│  atc/api/agentfeedback/             │
│                                     │
│  GET  /api/v1/agent/reviews/:id     │
│  GET  /api/v1/agent/reviews/:id/    │
│       findings                      │
│  POST /api/v1/agent/feedback        │
│  GET  /api/v1/agent/feedback/       │
│       summary                       │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│  PostgreSQL                         │
│  ci_agent.review_feedback           │
│  ci_agent.review_sessions           │
└─────────────────────────────────────┘
```

## Requirements

1. **Schema types** — `FeedbackRecord` in `ci-agent/schema/` capturing: finding snapshot, verdict, human notes, conversation excerpt, reviewer identity
2. **PostgreSQL storage** — `ci_agent.review_feedback` table with migration; save, query by review, query aggregate stats
3. **Go API handlers** — HTTP endpoints for fetching review findings and submitting feedback, served from the Concourse ATC or as a standalone server
4. **Elm feedback UI** — separable module at `web/elm/src/AgentFeedback/` with:
   - Finding card showing code context, severity, category, agent's reasoning
   - Chat panel for conversational response per finding
   - Verdict picker (auto-suggested from response, human can override)
   - Session summary showing verdicts across all findings
5. **Verdict classifier** — takes the human's natural-language response and suggests a verdict; human confirms or overrides
6. **Feedback session** — ties to a specific review (repo + commit + review timestamp); tracks progress through findings
7. **Aggregation queries** — accuracy rate, false positive rate, by category, by severity; exposed via API

## Verdict Taxonomy

| Verdict | Meaning | Training signal |
|---------|---------|-----------------|
| `accurate` | Real issue, good catch | Positive example for few-shot prompts |
| `false_positive` | Not actually a bug | Negative — agent hallucinated |
| `noisy` | Technically true but not worth flagging | Calibrate severity down |
| `overly_strict` | Style/preference flagged as defect | Tighten category scoping |
| `partially_correct` | Right area, wrong diagnosis | Improve reasoning prompts |
| `missed_context` | Agent lacked context to judge | Improve context passing |

## Feedback Record Schema

```json
{
  "id": "uuid",
  "review_ref": {
    "repo": "https://github.com/org/repo.git",
    "commit": "abc123",
    "review_timestamp": "2026-02-09T17:00:00Z"
  },
  "finding_id": "001",
  "finding_type": "proven_issue",
  "finding_snapshot": { },
  "verdict": "accurate",
  "confidence": 0.95,
  "notes": "Good catch, this would panic in production",
  "conversation_excerpt": [
    {"role": "system", "content": "Finding 001: Nil pointer dereference in config/loader.go:42 ..."},
    {"role": "human", "content": "Yeah this is a real bug. We've seen this panic in staging."},
    {"role": "system", "content": "Classified as: accurate. Agree?"},
    {"role": "human", "content": "yes"}
  ],
  "reviewer": "tdm",
  "source": "interactive",
  "timestamp": "2026-02-10T10:00:00Z"
}
```

## Database Schema

```sql
CREATE SCHEMA IF NOT EXISTS ci_agent;

CREATE TABLE ci_agent.review_feedback (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  repo              TEXT NOT NULL,
  commit_sha        TEXT NOT NULL,
  review_ts         TIMESTAMPTZ NOT NULL,
  finding_id        TEXT NOT NULL,
  finding_type      TEXT NOT NULL,
  finding_snapshot  JSONB NOT NULL,
  verdict           TEXT NOT NULL,
  confidence        REAL,
  notes             TEXT,
  conversation      JSONB,
  reviewer          TEXT,
  source            TEXT NOT NULL DEFAULT 'interactive',
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (repo, commit_sha, finding_id, reviewer)
);

CREATE INDEX idx_feedback_repo_commit ON ci_agent.review_feedback (repo, commit_sha);
CREATE INDEX idx_feedback_verdict ON ci_agent.review_feedback (verdict);
CREATE INDEX idx_feedback_category ON ci_agent.review_feedback ((finding_snapshot->>'category'));
```

## Acceptance Criteria

- [ ] FeedbackRecord Go types with Validate() enforcing required fields and valid verdicts
- [ ] PostgreSQL migration creates ci_agent.review_feedback table
- [ ] API: fetch review findings, submit feedback, query summary stats
- [ ] Elm UI: finding cards with code context, chat panel, verdict picker, session summary
- [ ] Elm module is cleanly separable (own directory, own Main, can build independently)
- [ ] Verdict auto-suggestion from human response text; human can override
- [ ] Conversation excerpt stored with each feedback record
- [ ] Summary stats: accuracy rate, FP rate, by category, by severity
- [ ] Graceful when DATABASE_URL absent (warns, no crash)

## Out of Scope

- Tier 2/3 implicit feedback inference (future tracks)
- Prompt tuning from feedback data (future track)
- Deep integration into main Concourse dashboard navigation (future — this is a standalone page)
- Fine-tuning LLMs directly
- Multi-reviewer consensus / inter-rater agreement
- Rich code diff viewer (show file + line context, not a full diff UI)
