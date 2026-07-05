# Agent review presentation — design

Date: 2026-07-05
Status: approved (pending spec review)

## Goal

Make ci-agent code review results a first-class, evaluable part of the Concourse product. Today a review run produces `review.json` as a build artifact, visible only via `cat` in build logs and a jq-based gate task. The user should get clear review status in the web UI and an easy way to evaluate the agent's findings — feeding the (future) learning loop through the existing verdict taxonomy.

Acceptance scenario (definition of done): a review job runs against commits on the `jetbridge` branch in the live concourse.home pipeline; the user opens the build page, sees the findings panel, submits verdicts, and browses past reviews on a dedicated page.

## Decisions made

- Surfaces: build page panel (primary) + dedicated cross-run reviews page. No GitHub PR comments or fly CLI in v1.
- Evaluation: read + verdict per finding, using the existing six-verdict taxonomy (`accurate`, `false_positive`, `noisy`, `overly_strict`, `partially_correct`, `missed_context`) plus optional note, stored via the existing `/api/v1/agent/feedback` API into the `agent_feedback` table.
- Persistence: reviews are ingested into ATC Postgres (new `agent_reviews` table) via a new POST endpoint. This also delivers durable run history keyed by build.
- Ingestion approach: push from the review task via a new `ci-agent publish` subcommand (approach A), designed so a future native `agent` step type can become the writer without changing the table, API, or UI.
- Verdict UI: all six verdicts inline per finding card as a segmented control (one input, six states; selected segment filled). Squared badges, not pills. No thumbs-triage, no session stepper in v1 (session mode is a natural fast-follow; it would write the same feedback records).

## Architecture

```
review task (pipeline)                         ATC web
┌──────────────────────────┐                  ┌──────────────────────────────┐
│ ci-agent --phase review  │                  │ POST /api/v1/agent/reviews   │
│   → review/review.json   │                  │   (publish token auth)       │
│ ci-agent publish         │ ──── HTTPS ────► │   upsert into agent_reviews  │
│   (BUILD_ID, token, URL) │                  ├──────────────────────────────┤
└──────────────────────────┘                  │ GET /api/v1/builds/:id/      │
                                              │     agent-reviews            │
             web UI (Elm)                     │ GET /api/v1/teams/:team/     │
┌──────────────────────────┐                  │     agent-reviews            │
│ Build page panel         │ ◄──── reads ──── │ (team auth, as today)        │
│ Reviews list page        │                  └──────────────────────────────┘
│ verdicts → existing      │
│ /api/v1/agent/feedback   │
└──────────────────────────┘
```

## Components

### 1. Database: `agent_reviews` table

Migration `1773105504_create_agent_reviews` (up/down), following `1773105502_create_agent_feedback` conventions.

Columns:
- `id SERIAL PRIMARY KEY`
- Build context: `build_id INT NOT NULL`, `build_name TEXT`, `team_name TEXT NOT NULL`, `pipeline_name TEXT NOT NULL`, `job_name TEXT NOT NULL`
- Review identity: `repo TEXT NOT NULL`, `commit_sha TEXT NOT NULL`, `branch TEXT`
- Denormalized listing fields: `score DOUBLE PRECISION`, `max_score DOUBLE PRECISION`, `pass BOOLEAN`, `proven_count INT`, `observation_count INT`, `summary TEXT`, `agent_model TEXT`, `duration_seconds INT`
- Full payload: `review JSONB NOT NULL` (the complete `ReviewOutput`)
- `created_at TIMESTAMPTZ NOT NULL DEFAULT now()`, `updated_at TIMESTAMPTZ`

Unique index on `(build_id, repo, commit_sha)`; ingestion upserts, so re-running a build replaces its row. Listing queries join `agent_feedback` on `(repo, commit_sha)` to compute "evaluated n of m" per review.

DB access via `atc/db/agent_reviews_factory.go`, mirroring `agent_feedback_factory.go`.

### 2. API: `agent/api/reviews` package

Three routes, registered in `atc/routes.go` and `atc/wrappa/api_auth_wrappa.go`:

- `POST /api/v1/agent/reviews` — body `{build_id: int, review: ReviewOutput}`. Auth: static publish token (see below), checked by the handler (not team auth). Validates the ReviewOutput schema and that the build exists; derives team/pipeline/job/build_name from the build row server-side (the client cannot spoof them). 400 malformed, 404 unknown build, 401 bad token. Upsert semantics (idempotent).
- `GET /api/v1/builds/:build_id/agent-reviews` — team-authorized (CheckAuthorizationHandler category, like the feedback routes). Returns full rows including JSONB payload and per-finding feedback (joined from `agent_feedback`) so the panel can show recorded verdicts.
- `GET /api/v1/teams/:team/agent-reviews?pipeline=&repo=&limit=` — team-authorized listing, newest first, denormalized columns + evaluated counts only (no JSONB payload).

Publish token: new web flag `--agent-review-publish-token` (env `CONCOURSE_AGENT_REVIEW_PUBLISH_TOKEN`), set via Helm values secret. Accepted only by the POST route. Rationale: fly/dex tokens expire and would make pipelines flaky; a scoped static token matches the homelab threat model. Upgrading to real team-scoped tokens is a compatible future change.

### 3. Task build identity: `TaskEnv()`

Extend `StepMetadata.TaskEnv()` (`atc/exec/step_metadata.go`) to include `BUILD_ID`, `BUILD_NAME`, `BUILD_TEAM_NAME`, `BUILD_JOB_NAME`, `BUILD_PIPELINE_NAME` (in addition to the existing `ATC_EXTERNAL_URL`). This is a deliberate divergence from upstream Concourse (which withholds build metadata from tasks for hermeticity); this fork accepts it to enable agent result ingestion and future fix-PR linking.

### 4. `ci-agent publish` subcommand

New subcommand in `ci-agent/cmd/ci-agent`:
- Reads `review.json` (path flag, default `review/review.json`), validates it as `schema.ReviewOutput`.
- Resolves `ATC_EXTERNAL_URL`, `BUILD_ID` from env; token from `AGENT_REVIEW_PUBLISH_TOKEN` env.
- POSTs to `/api/v1/agent/reviews`; retries 3x with backoff on 5xx/network errors; exits nonzero on final failure.
- Runs as a trailing step in the review task script, after `review.json` is written, so a publish failure is visible in the task output but does not retroactively alter the review verdict itself. `ci/tasks/ci-agent-review.yml` gains the publish invocation and an `AGENT_REVIEW_PUBLISH_TOKEN` param.

### 5. Web UI (Elm)

Absorb the orphaned `AgentFeedback` module into the main SPA:
- Port `FindingCard` and `VerdictPicker` rendering into main-app views; delete the standalone `Browser.element` app (`AgentFeedback/Main.elm`) and the unused `ChatPanel`. The feedback API stays unchanged.

Build page panel (`web/elm/src/Build/AgentReview.elm` + wiring in `Build.elm`):
- On build page load, fetch `GET /builds/:id/agent-reviews`. 404/empty → no panel rendered.
- Panel sits between the build header and step output. Summary bar always visible when a review exists: robot icon, score badge (green when pass, red when fail), proven/observation counts, "evaluated n of m", expand chevron.
- Expanded: proven issues first (severity-ordered), then a collapsed observations group. Each finding card: squared severity badge (critical/high red, medium amber, low gray), title, `file:line` in mono, description, failing-test evidence in a mono block, then the verdict row.
- Verdict row: segmented control with all six verdicts; selected segment filled (`fill-primary`); optional note field below. Submitting POSTs to `/api/v1/agent/feedback` with the logged-in username as reviewer; response updates the card in place. Failure keeps the segment unselected and shows a retryable inline error — feedback is never silently dropped.
- API load errors render a quiet "couldn't load agent review" line; the build page itself never breaks.

Reviews list page (`web/elm/src/AgentReviews/` + `Routes.elm` entry `/teams/:team/agent-reviews`):
- Filter selects for pipeline and repo; rows newest first.
- Row: score badge, `pipeline / job #build`, `branch @ shortsha · n issues · m obs` in mono, "evaluated n/m", relative time. Row links to the build page (single detail surface).
- Styling in `agent-feedback.less` (renamed/extended as needed), matching the mockups: flat, squared badges, segmented control.

## Rollout

1. Merge to `jetbridge`; self-build pipeline builds and verify-upgrade promotes to concourse.home. Migration runs on web startup.
2. Add `--agent-review-publish-token` to the Helm values secret.
3. Add an `agent-review` job to the primary pipeline: git resource on `jetbridge` → `ci-agent-review.yml` with `REVIEW_DIFF_ONLY=true`, `BASE_REF=main` → publish.
4. Acceptance: push a commit; open the build at concourse.home; see panel; submit verdict; see the run on `/teams/main/agent-reviews`.

## Error handling summary

- Publish: separate from review execution; 3x retry; nonzero exit visible in build output without flipping the review verdict.
- Ingestion: schema-validated, build-validated, idempotent upsert; precise HTTP error codes.
- UI: missing review is a normal state; load errors degrade quietly; verdict submit errors are explicit and retryable.

## Testing

- `atc/db`: migration + `agent_reviews_factory` tests (upsert, listing filters, evaluated-count join), following `agent_feedback_factory` test patterns.
- `agent/api/reviews`: handler tests — token auth, team auth on GETs, validation errors, idempotency, listing.
- `ci-agent`: publish subcommand unit tests against `httptest` fake ATC — env resolution, retry/backoff, exit codes; schema validation of the payload.
- `atc/exec`: `TaskEnv()` coverage for the new build metadata vars.
- Elm: decoder + view tests for panel and list page in `web/elm/tests`.
- Integration: one ATC integration spec exercising POST → GET (build route) → GET (team listing).

## Out of scope (explicitly)

- Native `agent` step type (future track; this design keeps table/API/UI reusable for it).
- Session/stepper evaluation mode (fast-follow; writes the same feedback records).
- GitHub PR comments, fly CLI output, cross-run trend charts, feedback-into-prompt learning loop.
