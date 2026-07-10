# Pipeline Runs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a run-once lifecycle over instanced pipelines — `template: true` pipelines with params schemas, numbered one-shot runs created via `POST .../runs` / `fly run-pipeline`, worst-of aggregate completion status computed by a lifecycle component, and keep_last/ttl retention — as a pure general-CI improvement that provably does not regress reactive pipelines.

**Architecture:** A `pipeline_runs` table plus four new columns on `pipelines` sit under a `db.PipelineRunFactory` (agent_reviews_factory recipe). Run creation interpolates the template's config with params + `{run: N}` (exactly like the `set_pipeline` step), strips `trigger: true`, saves an instanced pipeline (`instance_vars: {run: N}`), inserts the run row, and triggers entry jobs as manual builds; a new `pipeline_run_lifecycler` RunnableComponent (pauser recipe, polling + notify from `build.Finish`) computes completion, reopens on retriggers, and archives per retention via the existing `pipeline.Archive()` machinery.

**Tech Stack:** Go (atc, go-concourse, fly), PostgreSQL migrations + squirrel, Ginkgo/Gomega + counterfeiter, Elm (web UI), topgun k8s_behavioral (Ginkgo + K3s testcontainers).

---

## Context

### Charter summary (workstream `pipeline-runs`, wave 1, size L)

Scope in:
1. `template: true` pipeline flag (no self-scheduling, resource checks disabled, `trigger: true` suppressed for run instances, versions pinned at creation) + params schema (names, string/number/bool types, defaults, required) + validator — resolves spec open item 1.
2. `POST /api/v1/teams/:team/pipelines/:name/runs` + `fly run-pipeline -p <template> -v k=v`; monotonic run numbers as `instance_vars {run: N}`; entry jobs auto-triggered.
3. `pipeline_runs` migration + factory (number, params, aggregate status, timestamps).
4. Run-lifecycle RunnableComponent (polling + notify, never notify-only): worst-of aggregate status, completion detection including in-flight aborts and retriggers.
5. OWNS the parked-run contract: a parked agent step keeps its build `started`, therefore a parked run counts as `running` — the contract platform-mcp's `ask_human` rides.
6. Retention (keep_last / ttl) via existing pipeline-archival machinery; template-page runs list (status, params, duration) in the Elm UI.
7. Unit tests on completion/retention + one topgun behavioral spec proving non-template pipelines are untouched.

Scope OUT (do not implement): workflow-definition rendering (dispatch), ticket dispatch (dispatch), experiment batching UX (process-intel-experiments).

### Prior waves

This is wave 1; `depends_on: []`. Nothing has landed before us and we consume no other workstream's surfaces. Wave-mates (agent-identity, credentials-and-budgets, dev-mcp, workflow-store) run in parallel and touch disjoint files; the routes added here are `:team_name`-scoped and use the ordinary `CheckAuthorizationHandler` tier, NOT the team-less `/api/v1/agent/*` handler agent-identity is building.

### Contract surfaces

**PRODUCES** `pipeline-runs-api-and-lifecycle` — defined in `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`:
- §1.5 "`pipeline_runs` + pipelines template columns" (DDL, run-number allocation, completion contract incl. parked-run rule)
- §2.3 "PipelineRun" (`atc/db/pipeline_run.go` Go types)
- §4.2 route table rows `CreatePipelineRun` / `ListPipelineRuns` / `GetPipelineRun`
- §7 "Pipeline-run params schema and template flag" (YAML keys, `atc/config.go` additions, validation rules, fly commands)

Consumers (later waves): dispatch, platform-mcp-hitl, process-intel-experiments. They call `db.PipelineRunFactory` in-process (`runs:create` scope was removed from the principal vocabulary — contracts decision 22).

**CONSUMES:** no cross-workstream surfaces. Only existing core seams: instanced pipelines / `savePipeline` (`atc/db/team.go:409`), pipeline archival (`atc/db/pipeline.go:705`), the RunnableComponent recipe (`atc/pauser/pipeline_pauser.go`, wiring `atc/atccmd/command.go:1186`), the API route recipe (`atc/routes.go` + `atc/api/handler.go` + `atc/wrappa/api_auth_wrappa.go` + `atc/api/accessor/roles.go`), and the fly command recipe (`fly/commands/fly.go`).

### Design decisions resolving spec open item 1 (recorded in contracts addendum, Task 1)

1. **Base vs instance:** run instances keep `template: true` in their materialized config, so `pipelines.template` is true for both the base template row and its run instances. Base = `template AND instance_vars IS NULL`. The scheduler skips base templates only; lidar's periodic check scan skips ALL `template = true` rows (base and instances).
2. **"Versions pinned at creation" v1 semantics:** implemented as a *frozen check set*, not literal per-resource config pins: one manually-triggered check per resource is enqueued at run creation **by `PipelineRunFactory.CreateRun` itself** (same seam as the `CheckResource` API handler; the API handler is a pass-through — in-process consumers calling the factory get the frozen check set too; amended 2026-07-09, F27), periodic re-checks are disabled, and `trigger: true` is rewritten to `false` at materialization on get steps **without** `passed:` constraints (external-version triggering). Gets **with** `passed:` keep their trigger flag — the scheduler only auto-creates builds for `FirstOccurrence && Trigger` inputs (`atc/scheduler/scheduler.go:90-113`), so preserving passed-chain triggers is what makes "downstream jobs flow through passed: chains as normal" (spec §3) actually happen. Explicit `version:` pins in the template pass through untouched. Documented v1 limitation: if another pipeline shares a resource-config scope and produces new versions mid-run, a not-yet-scheduled job without `passed:` constraints may resolve a newer version.
3. **Completion:** a run completes when its instance pipeline has no job builds in `pending`/`started`, at least one job build exists, and no active unpaused job has `schedule_requested > last_scheduled` (closes the entry-finished-but-downstream-not-yet-created race using existing columns). Aggregate status is worst-of over the latest build per job: `errored > aborted > failed > succeeded`.
4. **Retriggers/late builds:** completion is not final — a completed run whose instance pipeline gains a pending/started job build, **or a job build that completed after the run's `completed_at`** (covers retriggers that start AND finish inside one polling window, when the Finish notify arrives after the build has already left pending/started; amended 2026-07-09, F26), is *reopened* (status back to `running`, `completed_at` cleared) and completes again with a recomputed worst-of. The completed-after predicate is self-terminating: reopen→re-complete stamps a newer `completed_at`. This requires two factory methods beyond contracts §2.3 (`CompletedRunsWithNewActivity`, `RunsToArchive`) and three PipelineRun methods (`Reopen`, `CheckComplete`, `InstancePipeline`) — recorded as a contracts addendum.
5. **Parked-run contract:** a parked step keeps its build `started`; the completion query therefore never completes a parked run. Owned here, proven by an explicit unit spec (Task 9).
6. **Retention YAML carrier:** top-level `run_retention: {keep_last: K, ttl_days: N}` config key mirrored to `pipelines.run_retention` (contracts §1.5 defines the column; §7 omitted the YAML key — addendum documents it).
7. **Reserved param names:** `run` and `run_id` are reserved (amended 2026-07-09, F30). `run` is the instance var / per-template run NUMBER; `run_id` is a second materialization-time static var carrying the globally-unique `pipeline_runs.id`, allocated via `nextval` inside the creation transaction BEFORE materialization (it is NOT part of `instance_vars`). Anything keying cross-template state — metrics, questions, reviews, gateway rows, §8.1 `AGENT_PIPELINE_RUN_ID` — must interpolate `((run_id))`, never `((run))` (run numbers reset per template and collide). The params-schema validator rejects both names.
8. **Direct job triggering** on a base template returns 409 with a message pointing at `fly run-pipeline`.

---

### Task 1: Contracts addendum (early, before any code)

Write the wave-1 implementation decisions into the shared contracts doc so dispatch / platform-mcp / experiments plan against reality.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md:1353` (end of §7, before the `---` separating §8)
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md:1463` (amendment log)

**Steps:**

- [ ] Insert a new subsection at the end of §7 (after the "Fly:" paragraph, before the `---`):

  ```markdown
  ### 7.1 Pipeline-runs implementation addendum (wave 1, owner: pipeline-runs)

  Decisions made while implementing §1.5/§2.3/§7; consumers (dispatch, platform-mcp-hitl,
  process-intel-experiments) should code against these:

  1. **Template column on instances.** Run instances keep `template: true` in their
     materialized config, so `pipelines.template` is true for base templates AND their run
     instances. Base template = `template AND instance_vars IS NULL`. Scheduler skips base
     templates only; lidar's periodic check scan skips all `template = true` rows.
  2. **"Versions pinned at creation" v1.** Implemented as a frozen check set: one
     manually-triggered check per resource enqueued at run creation **by
     `db.PipelineRunFactory.CreateRun` itself** (`CheckFactory.TryCreateCheck`, same seam
     as the `CheckResource` handler; the `POST .../runs` handler is a pass-through, so
     in-process consumers — dispatch, experiments — get the frozen check set too;
     amended 2026-07-09, F27), periodic checks
     disabled, `trigger: true` stripped at materialization from get steps WITHOUT
     `passed:` constraints. Gets WITH `passed:` keep their trigger flag so downstream
     jobs flow through chains as normal (spec §3) — external resource versions can never
     trigger a run-instance build, but passed-chain propagation can. Explicit `version:`
     pins pass through. Known limitation: a shared resource-config scope fed by other
     pipelines can surface newer versions to not-yet-scheduled jobs that lack `passed:`
     constraints.
  3. **Completion + reopen.** Completion additionally requires no active unpaused job with
     `schedule_requested > last_scheduled` (closes the downstream-not-yet-created race).
     Completion is re-entrant: a completed run whose instance pipeline gains a
     pending/started job build — or a job build that COMPLETED after the run's
     `completed_at` (fast-finishing retriggers that never linger in pending/started;
     self-terminating because reopen→re-complete stamps a newer `completed_at`;
     amended 2026-07-09, F26) — is reopened (status `running`, `completed_at` cleared) and
     completes again. §2.3 gains:
     `PipelineRun.Reopen() error`, `PipelineRun.CheckComplete() (PipelineRunStatus, bool, error)`,
     `PipelineRun.InstancePipeline() (Pipeline, bool, error)`, plus getters
     `CreatedAt() time.Time`, `CompletedAt() (time.Time, bool)`, `Archived() bool`;
     `PipelineRunFactory` gains `CompletedRunsWithNewActivity() ([]PipelineRun, error)` and
     `RunsToArchive() ([]PipelineRun, error)`.
  4. **Retention YAML carrier.** Top-level pipeline-config key
     `run_retention: {keep_last: K, ttl_days: N}` (Go: `atc.RunRetentionConfig`), mirrored to
     `pipelines.run_retention` at SaveConfig time. `keep_last` ranks completed, non-archived
     runs per template by number descending.
  5. **Reserved param names.** A params-schema entry named `run` or `run_id` is a config
     validation error (`run_id` added 2026-07-09, F30).
  6. **Template job triggering.** `CreateJobBuild` on a base template returns
     409 Conflict, body: `cannot trigger jobs on a template pipeline; use "fly run-pipeline" to create a run`.
  7. **Wire shapes.** `POST .../runs` body: `{"params": {"name": <value>, ...}}`
     (`atc.CreatePipelineRunRequest`). Response/list element (`atc.PipelineRun`):
     `{"id": int, "number": int, "status": string, "params": object, "created_by": string,
     "created_at": epoch-seconds, "completed_at": epoch-seconds-omitempty, "archived": bool-omitempty}`.
  8. **Entry-job trigger semantics.** Entry jobs (no `passed:` on any input) are triggered as
     manually-triggered builds by `CreateRun` inside the creation call, after the creation
     transaction commits.
  9. **Run-id var (`((run_id))`).** `pipeline_runs.id` is allocated via `nextval` inside the
     creation transaction BEFORE materialization and injected as a second reserved static
     var alongside `((run))` (added 2026-07-09, F30). `((run))` = per-template run NUMBER
     (also the `instance_vars` identity; numbers reset per template). `((run_id))` =
     globally-unique `pipeline_runs.id`; it resolves at materialization only and is NOT
     part of `instance_vars`. Anything keying cross-template state (agent metrics,
     questions, reviews, gateway ledger rows — §8.1 `AGENT_PIPELINE_RUN_ID`) MUST
     interpolate `((run_id))`, never `((run))`. Co-signed with dispatch (renderer sites)
     and harvest.
  ```

- [ ] Append to the §11 amendment log:

  ```markdown
  - 2026-07-08: pipeline-runs wave-1 addendum (§7.1): template-column-true-on-instances rule,
    frozen-check-set pinning, completion reopen semantics + §2.3 interface extensions,
    run_retention YAML key, reserved param name `run`, 409 on template job trigger, wire shapes.
  - 2026-07-09: pipeline-runs design-review fixes F26/F27/F30 in §7.1: reopen detection also
    matches job builds completed after the run's `completed_at` (fast-finish retriggers, F26);
    the frozen-check enqueue lives in `db.PipelineRunFactory.CreateRun`, the runs handler is a
    pass-through (F27); new reserved var `((run_id))` = `pipeline_runs.id` allocated via
    `nextval` before materialization, reserved param names now `run` AND `run_id` (F30,
    co-signed with dispatch + harvest for the §8.1 `AGENT_PIPELINE_RUN_ID` consumers).
  ```

- [ ] Commit:
  ```bash
  git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md
  git commit -m "docs(contracts): pipeline-runs wave-1 addendum (S7.1)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 2: Migrations 1773106030 + 1773106031

**Files:**
- Create: `atc/db/migration/migrations/1773106030_add_template_columns_to_pipelines.up.sql`
- Create: `atc/db/migration/migrations/1773106030_add_template_columns_to_pipelines.down.sql`
- Create: `atc/db/migration/migrations/1773106031_create_pipeline_runs.up.sql`
- Create: `atc/db/migration/migrations/1773106031_create_pipeline_runs.down.sql`
- Test: existing migration suite (`ginkgo ./atc/db/migration/`) applies every embedded migration up

**Steps:**

- [ ] Write `1773106030_add_template_columns_to_pipelines.up.sql` (exact DDL from contracts §1.5):

  ```sql
  ALTER TABLE pipelines ADD COLUMN template BOOLEAN NOT NULL DEFAULT false;
  ALTER TABLE pipelines ADD COLUMN params_schema JSONB;
  ALTER TABLE pipelines ADD COLUMN run_retention JSONB;
  ALTER TABLE pipelines ADD COLUMN last_run_number INTEGER NOT NULL DEFAULT 0;
  ```

- [ ] Write `1773106030_add_template_columns_to_pipelines.down.sql`:

  ```sql
  ALTER TABLE pipelines DROP COLUMN last_run_number;
  ALTER TABLE pipelines DROP COLUMN run_retention;
  ALTER TABLE pipelines DROP COLUMN params_schema;
  ALTER TABLE pipelines DROP COLUMN template;
  ```

- [ ] Write `1773106031_create_pipeline_runs.up.sql` (exact DDL from contracts §1.5):

  ```sql
  CREATE TABLE pipeline_runs (
      id                   SERIAL PRIMARY KEY,
      template_pipeline_id INTEGER NOT NULL REFERENCES pipelines (id) ON DELETE CASCADE,
      instance_pipeline_id INTEGER REFERENCES pipelines (id) ON DELETE SET NULL,
      number               INTEGER NOT NULL,
      params               JSONB NOT NULL DEFAULT '{}',
      status               TEXT NOT NULL DEFAULT 'running'
                           CHECK (status IN ('running','succeeded','failed','errored','aborted')),
      created_by           TEXT NOT NULL DEFAULT '',
      created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
      completed_at         TIMESTAMPTZ,
      archived             BOOLEAN NOT NULL DEFAULT false
  );

  CREATE UNIQUE INDEX pipeline_runs_template_number ON pipeline_runs (template_pipeline_id, number);
  CREATE INDEX pipeline_runs_status ON pipeline_runs (status) WHERE status = 'running';
  ```

- [ ] Write `1773106031_create_pipeline_runs.down.sql`:

  ```sql
  DROP TABLE pipeline_runs;
  ```

- [ ] Run the migration suite (migrations are `go:embed`-discovered from the directory, no registration file):
  ```bash
  ginkgo ./atc/db/migration/
  ```
  Expect: suite green (it runs the full up path; a SQL error in the new files fails it).

- [ ] Commit:
  ```bash
  git add atc/db/migration/migrations/1773106030_* atc/db/migration/migrations/1773106031_*
  git commit -m "feat(db): pipeline_runs table and pipelines template columns (migrations 1773106030-31)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 3: `atc.Config` additions — Template, Params, RunRetention

**Files:**
- Modify: `atc/config.go:20` (Config struct), `atc/config.go:32` (skeletonConfig in `UnmarshalConfig`), `atc/config.go:178` region (new types after `ResourceConfig`)
- Test: `atc/run_config_test.go` (new file, `package atc_test`, joins the existing `atc` Ginkgo suite `atc/atc_suite_test.go`)

**Steps:**

- [ ] Write the failing test in `atc/run_config_test.go`:

  ```go
  package atc_test

  import (
  	"github.com/concourse/concourse/atc"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  )

  var _ = Describe("Template pipeline config", func() {
  	It("round-trips template, params and run_retention through UnmarshalConfig", func() {
  		payload := []byte(`
  template: true
  params:
  - name: commit
    type: string
    required: true
  - name: suite
    type: enum
    values: [unit, integration]
    default: unit
  run_retention:
    keep_last: 5
    ttl_days: 7
  jobs:
  - name: entry
    plan:
    - task: t
      file: task.yml
  `)
  		var config atc.Config
  		err := atc.UnmarshalConfig(payload, &config)
  		Expect(err).ToNot(HaveOccurred())
  		Expect(config.Template).To(BeTrue())
  		Expect(config.Params).To(HaveLen(2))
  		Expect(config.Params[0].Name).To(Equal("commit"))
  		Expect(config.Params[0].Type).To(Equal("string"))
  		Expect(config.Params[0].Required).To(BeTrue())
  		Expect(config.Params[1].Values).To(Equal([]string{"unit", "integration"}))
  		Expect(config.Params[1].Default).To(Equal("unit"))
  		Expect(config.RunRetention.KeepLast).To(Equal(5))
  		Expect(config.RunRetention.TTLDays).To(Equal(7))
  	})
  })
  ```

- [ ] Run and see it fail to compile (`config.Template` undefined):
  ```bash
  ginkgo --focus="Template pipeline config" ./atc/
  ```

- [ ] Add fields to `Config` (atc/config.go:20) — names/tags exactly per contracts §7:

  ```go
  type Config struct {
  	Groups        GroupConfigs     `json:"groups,omitempty"`
  	VarSources    VarSourceConfigs `json:"var_sources,omitempty"`
  	Resources     ResourceConfigs  `json:"resources,omitempty"`
  	ResourceTypes ResourceTypes    `json:"resource_types,omitempty"`
  	Prototypes    Prototypes       `json:"prototypes,omitempty"`
  	Jobs          JobConfigs       `json:"jobs,omitempty"`
  	Display       *DisplayConfig   `json:"display,omitempty"`
  	Template      bool             `json:"template,omitempty"`
  	Params        []ParamSchema    `json:"params,omitempty"`
  	RunRetention  *RunRetentionConfig `json:"run_retention,omitempty"`
  }
  ```

- [ ] Add the same three keys to `skeletonConfig` inside `UnmarshalConfig` (atc/config.go:32) — without this, the keys are silently dropped:

  ```go
  	type skeletonConfig struct {
  		Groups        any `json:"groups,omitempty"`
  		VarSources    any `json:"var_sources,omitempty"`
  		Resources     any `json:"resources,omitempty"`
  		ResourceTypes any `json:"resource_types,omitempty"`
  		Prototypes    any `json:"prototypes,omitempty"`
  		Jobs          any `json:"jobs,omitempty"`
  		Display       any `json:"display,omitempty"`
  		Template      any `json:"template,omitempty"`
  		Params        any `json:"params,omitempty"`
  		RunRetention  any `json:"run_retention,omitempty"`
  	}
  ```

- [ ] Add the new types (after `ResourceConfig`, atc/config.go:191) — `ParamSchema` exactly per contracts §7:

  ```go
  type ParamSchema struct {
  	Name        string   `json:"name"`
  	Type        string   `json:"type"`             // string | number | bool | enum
  	Required    bool     `json:"required,omitempty"`
  	Default     any      `json:"default,omitempty"`
  	Values      []string `json:"values,omitempty"` // enum only
  	Description string   `json:"description,omitempty"`
  }

  type RunRetentionConfig struct {
  	KeepLast int `json:"keep_last,omitempty"`
  	TTLDays  int `json:"ttl_days,omitempty"`
  }
  ```

- [ ] Run to green, then run the whole atc package:
  ```bash
  ginkgo --focus="Template pipeline config" ./atc/ && ginkgo ./atc/
  ```

- [ ] Commit:
  ```bash
  git add atc/config.go atc/run_config_test.go
  git commit -m "feat(atc): template, params schema and run_retention pipeline config keys" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 4: `atc.ValidateRunParams` — run-time param validation/coercion

**Files:**
- Create: `atc/run_params.go`
- Test: `atc/run_params_test.go`

**Steps:**

- [ ] Write the failing test `atc/run_params_test.go`:

  ```go
  package atc_test

  import (
  	"github.com/concourse/concourse/atc"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  )

  var _ = Describe("ValidateRunParams", func() {
  	schema := []atc.ParamSchema{
  		{Name: "commit", Type: "string", Required: true},
  		{Name: "suite", Type: "enum", Values: []string{"unit", "integration"}, Default: "unit"},
  		{Name: "procs", Type: "number", Default: 2},
  		{Name: "verbose", Type: "bool"},
  	}

  	It("fills defaults and coerces values", func() {
  		out, err := atc.ValidateRunParams(schema, map[string]any{
  			"commit":  "abc123",
  			"procs":   "4",
  			"verbose": "true",
  		})
  		Expect(err).ToNot(HaveOccurred())
  		Expect(out).To(Equal(map[string]any{
  			"commit":  "abc123",
  			"suite":   "unit",
  			"procs":   float64(4),
  			"verbose": true,
  		}))
  	})

  	It("rejects unknown params", func() {
  		_, err := atc.ValidateRunParams(schema, map[string]any{"commit": "x", "bogus": "y"})
  		Expect(err).To(MatchError(ContainSubstring(`unknown param "bogus"`)))
  	})

  	It("rejects missing required params", func() {
  		_, err := atc.ValidateRunParams(schema, map[string]any{})
  		Expect(err).To(MatchError(ContainSubstring(`missing required param "commit"`)))
  	})

  	It("rejects enum values outside the declared set", func() {
  		_, err := atc.ValidateRunParams(schema, map[string]any{"commit": "x", "suite": "smoke"})
  		Expect(err).To(MatchError(ContainSubstring(`not one of`)))
  	})

  	It("rejects type mismatches", func() {
  		_, err := atc.ValidateRunParams(schema, map[string]any{"commit": 42})
  		Expect(err).To(MatchError(ContainSubstring(`expected string`)))
  	})

  	It("omits optional params without defaults", func() {
  		out, err := atc.ValidateRunParams(schema, map[string]any{"commit": "x"})
  		Expect(err).ToNot(HaveOccurred())
  		Expect(out).ToNot(HaveKey("verbose"))
  	})
  })
  ```

- [ ] Run and see compile failure:
  ```bash
  ginkgo --focus="ValidateRunParams" ./atc/
  ```

- [ ] Implement `atc/run_params.go`:

  ```go
  package atc

  import (
  	"fmt"
  	"strconv"
  )

  // ValidateRunParams validates params given for a pipeline run against a
  // template's params schema (shared-contracts §7): unknown names rejected,
  // missing required params rejected, defaults filled server-side, values
  // coerced per JSON type. The returned map is stored on the run row and
  // interpolated into the instanced pipeline.
  func ValidateRunParams(schema []ParamSchema, given map[string]any) (map[string]any, error) {
  	byName := make(map[string]ParamSchema, len(schema))
  	for _, p := range schema {
  		byName[p.Name] = p
  	}

  	for name := range given {
  		if _, ok := byName[name]; !ok {
  			return nil, fmt.Errorf("unknown param %q", name)
  		}
  	}

  	validated := make(map[string]any, len(schema))
  	for _, p := range schema {
  		raw, ok := given[p.Name]
  		if !ok {
  			if p.Default != nil {
  				coerced, err := coerceParam(p, p.Default)
  				if err != nil {
  					return nil, fmt.Errorf("invalid default: %w", err)
  				}
  				validated[p.Name] = coerced
  				continue
  			}
  			if p.Required {
  				return nil, fmt.Errorf("missing required param %q", p.Name)
  			}
  			continue
  		}

  		coerced, err := coerceParam(p, raw)
  		if err != nil {
  			return nil, err
  		}
  		validated[p.Name] = coerced
  	}

  	return validated, nil
  }

  func coerceParam(p ParamSchema, raw any) (any, error) {
  	switch p.Type {
  	case "string":
  		s, ok := raw.(string)
  		if !ok {
  			return nil, fmt.Errorf("param %q: expected string, got %T", p.Name, raw)
  		}
  		return s, nil

  	case "number":
  		switch v := raw.(type) {
  		case int:
  			return float64(v), nil
  		case int64:
  			return float64(v), nil
  		case float64:
  			return v, nil
  		case string:
  			f, err := strconv.ParseFloat(v, 64)
  			if err != nil {
  				return nil, fmt.Errorf("param %q: %q is not a number", p.Name, v)
  			}
  			return f, nil
  		}
  		return nil, fmt.Errorf("param %q: expected number, got %T", p.Name, raw)

  	case "bool":
  		switch v := raw.(type) {
  		case bool:
  			return v, nil
  		case string:
  			b, err := strconv.ParseBool(v)
  			if err != nil {
  				return nil, fmt.Errorf("param %q: %q is not a bool", p.Name, v)
  			}
  			return b, nil
  		}
  		return nil, fmt.Errorf("param %q: expected bool, got %T", p.Name, raw)

  	case "enum":
  		s, ok := raw.(string)
  		if !ok {
  			return nil, fmt.Errorf("param %q: expected one of %v, got %T", p.Name, p.Values, raw)
  		}
  		for _, allowed := range p.Values {
  			if s == allowed {
  				return s, nil
  			}
  		}
  		return nil, fmt.Errorf("param %q: %q is not one of %v", p.Name, s, p.Values)
  	}

  	return nil, fmt.Errorf("param %q: unknown type %q", p.Name, p.Type)
  }
  ```

- [ ] Run to green:
  ```bash
  ginkgo --focus="ValidateRunParams" ./atc/
  ```

- [ ] Commit:
  ```bash
  git add atc/run_params.go atc/run_params_test.go
  git commit -m "feat(atc): ValidateRunParams run-param validation and coercion" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 5: configvalidate rules for the params schema

**Files:**
- Modify: `atc/configvalidate/validate.go:42` (wire into `Validate`), new `validateParamsSchema` function at end of file
- Test: `atc/configvalidate/validate_test.go` (append a Describe block; file exists with the same package/suite)

**Steps:**

- [ ] Append a failing Describe to `atc/configvalidate/validate_test.go` (match the file's existing idiom: it builds an `atc.Config` and asserts on `configvalidate.Validate` `errorMessages`; adapt variable names to the file's existing `config`/`errorMessages` setup if it uses a shared BeforeEach — otherwise use this standalone form):

  ```go
  var _ = Describe("params schema validation", func() {
  	validJob := atc.JobConfig{
  		Name: "entry",
  		PlanSequence: []atc.Step{
  			{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
  		},
  	}

  	validate := func(c atc.Config) []string {
  		_, errorMessages := configvalidate.Validate(c)
  		return errorMessages
  	}

  	It("accepts a valid template with params and retention", func() {
  		errs := validate(atc.Config{
  			Template: true,
  			Params: []atc.ParamSchema{
  				{Name: "commit", Type: "string", Required: true},
  				{Name: "suite", Type: "enum", Values: []string{"a", "b"}, Default: "a"},
  			},
  			RunRetention: &atc.RunRetentionConfig{KeepLast: 5},
  			Jobs:         atc.JobConfigs{validJob},
  		})
  		Expect(errs).To(BeEmpty())
  	})

  	It("rejects params on non-template pipelines", func() {
  		errs := validate(atc.Config{
  			Params: []atc.ParamSchema{{Name: "commit", Type: "string"}},
  			Jobs:   atc.JobConfigs{validJob},
  		})
  		Expect(errs).To(ContainElement(ContainSubstring("params schema is only allowed on template pipelines")))
  	})

  	It("rejects run_retention on non-template pipelines", func() {
  		errs := validate(atc.Config{
  			RunRetention: &atc.RunRetentionConfig{KeepLast: 1},
  			Jobs:         atc.JobConfigs{validJob},
  		})
  		Expect(errs).To(ContainElement(ContainSubstring("run_retention is only allowed on template pipelines")))
  	})

  	It("rejects the reserved names, duplicates, bad types, enums without values, and bad defaults", func() {
  		errs := validate(atc.Config{
  			Template: true,
  			Params: []atc.ParamSchema{
  				{Name: "run", Type: "string"},
  				{Name: "run_id", Type: "string"},
  				{Name: "dup", Type: "string"},
  				{Name: "dup", Type: "string"},
  				{Name: "weird", Type: "list"},
  				{Name: "empty-enum", Type: "enum"},
  				{Name: "bad-default", Type: "number", Default: "not-a-number"},
  			},
  			Jobs: atc.JobConfigs{validJob},
  		})
  		Expect(errs).To(ContainElement(ContainSubstring(`name "run" is reserved`)))
  		Expect(errs).To(ContainElement(ContainSubstring(`name "run_id" is reserved`)))
  		Expect(errs).To(ContainElement(ContainSubstring("duplicate param name")))
  		Expect(errs).To(ContainElement(ContainSubstring(`invalid type "list"`)))
  		Expect(errs).To(ContainElement(ContainSubstring("enum params must declare values")))
  		Expect(errs).To(ContainElement(ContainSubstring("invalid default")))
  	})
  })
  ```

- [ ] Run and see failures (no such validation yet — the valid case passes, the reject cases fail):
  ```bash
  ginkgo --focus="params schema validation" ./atc/configvalidate/
  ```

- [ ] Implement `validateParamsSchema` at the end of `atc/configvalidate/validate.go`, collecting one error per problem (returning a single joined error like the other `validateX` helpers do via `compositeErr` — open the file and reuse its existing error-accumulation helper; the file already imports `fmt` and `atc`):

  ```go
  func validateParamsSchema(c atc.Config) error {
  	var errorMessages []string

  	if len(c.Params) > 0 && !c.Template {
  		errorMessages = append(errorMessages, "params schema is only allowed on template pipelines (set template: true)")
  	}
  	if c.RunRetention != nil && !c.Template {
  		errorMessages = append(errorMessages, "run_retention is only allowed on template pipelines (set template: true)")
  	}

  	seen := map[string]bool{}
  	for i, p := range c.Params {
  		identifier := fmt.Sprintf("params[%d] (%q)", i, p.Name)

  		if p.Name == "" {
  			errorMessages = append(errorMessages, identifier+": name is required")
  			continue
  		}
  		if p.Name == "run" {
  			errorMessages = append(errorMessages, identifier+`: name "run" is reserved for the run number`)
  		}
  		if p.Name == "run_id" {
  			// second reserved var: pipeline_runs.id, injected at
  			// materialization (shared-contracts §7.1 item 9; F30 2026-07-09)
  			errorMessages = append(errorMessages, identifier+`: name "run_id" is reserved for the pipeline-run id`)
  		}
  		if seen[p.Name] {
  			errorMessages = append(errorMessages, identifier+": duplicate param name")
  		}
  		seen[p.Name] = true

  		switch p.Type {
  		case "string", "number", "bool":
  			if len(p.Values) > 0 {
  				errorMessages = append(errorMessages, identifier+": values is only allowed for enum params")
  			}
  		case "enum":
  			if len(p.Values) == 0 {
  				errorMessages = append(errorMessages, identifier+": enum params must declare values")
  			}
  		default:
  			errorMessages = append(errorMessages, fmt.Sprintf("%s: invalid type %q (must be string, number, bool or enum)", identifier, p.Type))
  			continue
  		}

  		if p.Default != nil {
  			if _, err := atc.ValidateRunParams([]atc.ParamSchema{p}, nil); err != nil {
  				errorMessages = append(errorMessages, fmt.Sprintf("%s: %s", identifier, err))
  			}
  		}
  	}

  	return compositeErr(errorMessages)
  }
  ```
  (`compositeErr` is the file's existing error-joining helper — used at validate.go:173/214/254.)

- [ ] Wire into `Validate` (atc/configvalidate/validate.go:42, after the `displayErr` block):

  ```go
  	paramsErr := validateParamsSchema(c)
  	if paramsErr != nil {
  		errorMessages = append(errorMessages, formatErr("params", paramsErr))
  	}
  ```

- [ ] Run to green:
  ```bash
  ginkgo ./atc/configvalidate/
  ```

- [ ] Commit:
  ```bash
  git add atc/configvalidate/
  git commit -m "feat(atc): validate template params schema in configvalidate" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 6: `atc.MaterializeRunConfig` + `Config.EntryJobs`

**Files:**
- Create: `atc/run_config.go`
- Test: `atc/run_config_test.go` (extend the file from Task 3)

**Steps:**

- [ ] Append failing specs to `atc/run_config_test.go`:

  ```go
  var _ = Describe("MaterializeRunConfig", func() {
  	template := atc.Config{
  		Template: true,
  		Params:   []atc.ParamSchema{{Name: "ref", Type: "string"}},
  		Resources: atc.ResourceConfigs{
  			{Name: "repo", Type: "git", Source: atc.Source{"branch": "((ref))", "uri": "((repo_uri))"}},
  		},
  		Jobs: atc.JobConfigs{
  			{
  				Name: "entry",
  				PlanSequence: []atc.Step{
  					{Config: &atc.GetStep{Name: "repo", Trigger: true}},
  				},
  			},
  			{
  				Name: "downstream",
  				PlanSequence: []atc.Step{
  					{Config: &atc.GetStep{Name: "repo", Passed: []string{"entry"}, Trigger: true}},
  				},
  			},
  		},
  	}

  	It("resolves params and the run number, strips external triggers, keeps passed-chain triggers and unknown vars", func() {
  		out, err := atc.MaterializeRunConfig(template, 42, 9001, map[string]any{"ref": "abc123"})
  		Expect(err).ToNot(HaveOccurred())

  		Expect(out.Template).To(BeTrue())
  		Expect(out.Resources[0].Source["branch"]).To(Equal("abc123"))
  		// unresolved vars are left for runtime var sources
  		Expect(out.Resources[0].Source["uri"]).To(Equal("((repo_uri))"))

  		// external-version triggering is suppressed: the entry get (no
  		// passed:) loses trigger: true...
  		Expect(out.Jobs[0].Inputs()[0].Trigger).To(BeFalse(), "entry get must not trigger on resource versions")
  		// ...but passed: chains keep flowing — the scheduler only creates
  		// builds for trigger: true inputs, so downstream gets MUST keep it
  		Expect(out.Jobs[1].Inputs()[0].Trigger).To(BeTrue(), "downstream passed: get must keep trigger for chain flow")
  	})

  	It("makes ((run)) available and gives it precedence over params", func() {
  		withRun := template
  		withRun.Resources = atc.ResourceConfigs{
  			{Name: "repo", Type: "git", Source: atc.Source{"tag": "run-((run))"}},
  		}
  		out, err := atc.MaterializeRunConfig(withRun, 7, 9001, map[string]any{"run": "hijack"})
  		Expect(err).ToNot(HaveOccurred())
  		Expect(out.Resources[0].Source["tag"]).To(Equal("run-7"))
  	})

  	// F30 (2026-07-09): ((run_id)) carries the globally-unique
  	// pipeline_runs.id — the value §8.1 AGENT_PIPELINE_RUN_ID is defined as.
  	// ((run)) is only the per-template run NUMBER and collides across
  	// templates; renderers keying cross-template state must use ((run_id)).
  	It("makes ((run_id)) available as the global pipeline_runs.id and gives it precedence over params", func() {
  		withRunID := template
  		withRunID.Resources = atc.ResourceConfigs{
  			{Name: "repo", Type: "git", Source: atc.Source{"tag": "run-((run))-id-((run_id))"}},
  		}
  		out, err := atc.MaterializeRunConfig(withRunID, 7, 9001, map[string]any{"run_id": "hijack"})
  		Expect(err).ToNot(HaveOccurred())
  		Expect(out.Resources[0].Source["tag"]).To(Equal("run-7-id-9001"))
  	})
  })

  var _ = Describe("Config.EntryJobs", func() {
  	It("returns jobs with no passed constraints", func() {
  		config := atc.Config{
  			Jobs: atc.JobConfigs{
  				{Name: "no-inputs", PlanSequence: []atc.Step{
  					{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
  				}},
  				{Name: "entry-get", PlanSequence: []atc.Step{
  					{Config: &atc.GetStep{Name: "repo"}},
  				}},
  				{Name: "downstream", PlanSequence: []atc.Step{
  					{Config: &atc.GetStep{Name: "repo", Passed: []string{"entry-get"}}},
  				}},
  			},
  		}
  		Expect(config.EntryJobs()).To(Equal([]string{"no-inputs", "entry-get"}))
  	})
  })
  ```

- [ ] Run and see compile failure:
  ```bash
  ginkgo --focus="MaterializeRunConfig" ./atc/
  ```

- [ ] Implement `atc/run_config.go`:

  ```go
  package atc

  import (
  	"encoding/json"

  	"github.com/concourse/concourse/vars"
  )

  // MaterializeRunConfig produces the concrete config for a pipeline-run
  // instance: ((param)) references are resolved from the validated params and
  // two reserved vars (which take precedence over params) — ((run)), the
  // per-template run NUMBER (also the instance var), and ((run_id)), the
  // globally-unique pipeline_runs.id allocated before materialization
  // (shared-contracts §7.1 item 9; F30 2026-07-09: run numbers reset per
  // template, so cross-template consumers such as §8.1 AGENT_PIPELINE_RUN_ID
  // must interpolate ((run_id))). Reactive
  // semantics are stripped: get steps WITHOUT passed: constraints have
  // trigger: true rewritten to false (external resource versions never
  // trigger a run-instance build). Gets WITH passed: keep their trigger flag
  // — the scheduler only auto-creates builds for trigger: true inputs, so
  // this is what lets downstream jobs flow through passed: chains as normal
  // (spec §3). Unresolved ((vars)) are left intact for runtime var sources,
  // matching the set_pipeline step (Resolve(false)).
  func MaterializeRunConfig(template Config, runNumber int, runID int, params map[string]any) (Config, error) {
  	payload, err := json.Marshal(template)
  	if err != nil {
  		return Config{}, err
  	}

  	staticVars := []vars.Variables{
  		vars.StaticVariables{"run": runNumber, "run_id": runID},
  		vars.StaticVariables(params),
  	}

  	resolved, err := vars.NewTemplateResolver(payload, staticVars).Resolve(false)
  	if err != nil {
  		return Config{}, err
  	}

  	var config Config
  	err = UnmarshalConfig(resolved, &config)
  	if err != nil {
  		return Config{}, err
  	}

  	for i := range config.Jobs {
  		err = config.Jobs[i].StepConfig().Visit(StepRecursor{
  			OnGet: func(step *GetStep) error {
  				// suppress external-version triggering only; passed:
  				// chains keep their trigger flag so downstream jobs flow
  				if len(step.Passed) == 0 {
  					step.Trigger = false
  				}
  				return nil
  			},
  		})
  		if err != nil {
  			return Config{}, err
  		}
  	}

  	return config, nil
  }

  // EntryJobs returns the names of jobs with no passed: constraints on any
  // input — the jobs auto-triggered when a run is created.
  func (c Config) EntryJobs() []string {
  	var names []string
  	for _, job := range c.Jobs {
  		entry := true
  		for _, input := range job.Inputs() {
  			if len(input.Passed) > 0 {
  				entry = false
  				break
  			}
  		}
  		if entry {
  			names = append(names, job.Name)
  		}
  	}
  	return names
  }
  ```

- [ ] Run to green:
  ```bash
  ginkgo --focus="MaterializeRunConfig|EntryJobs" ./atc/ && ginkgo ./atc/
  ```

- [ ] Commit:
  ```bash
  git add atc/run_config.go atc/run_config_test.go
  git commit -m "feat(atc): MaterializeRunConfig and Config.EntryJobs for pipeline runs" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 7: DB — mirror template columns onto `pipelines`, expose accessors

**Files:**
- Modify: `atc/db/pipeline.go:59` (Pipeline interface), `atc/db/pipeline.go:135` (struct fields), `atc/db/pipeline.go:156` (pipelinesQuery), `atc/db/pipeline.go:189` (accessors), `atc/db/pipeline.go:235` (`Config()` — F19, 2026-07-09)
- Modify: `atc/db/team.go:409` (`savePipeline` insert values map at :469 and update `Set`s at :514), `atc/db/team.go:1333` (`scanPipeline`)
- Test: `atc/db/pipeline_test.go` (append Describe)

**Steps:**

- [ ] Append a failing spec to `atc/db/pipeline_test.go` (the suite provides `defaultTeam`, `dbConn`, `lockFactory` globals):

  ```go
  var _ = Describe("template pipeline columns", func() {
  	It("mirrors template, params schema and run retention from the config", func() {
  		config := atc.Config{
  			Template: true,
  			Params: []atc.ParamSchema{
  				{Name: "greeting", Type: "string", Default: "hello"},
  			},
  			RunRetention: &atc.RunRetentionConfig{KeepLast: 3, TTLDays: 7},
  			Jobs: atc.JobConfigs{
  				{Name: "entry", PlanSequence: []atc.Step{
  					{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
  				}},
  			},
  		}

  		pipeline, _, err := defaultTeam.SavePipeline(
  			atc.PipelineRef{Name: "template-columns-pipeline"}, config, db.ConfigVersion(0), false)
  		Expect(err).ToNot(HaveOccurred())

  		Expect(pipeline.Template()).To(BeTrue())
  		Expect(pipeline.ParamsSchema()).To(Equal(config.Params))
  		Expect(pipeline.RunRetention()).To(Equal(config.RunRetention))
  		Expect(pipeline.LastRunNumber()).To(Equal(0))

  		// F19 (2026-07-09): Config() must reconstruct the three template
  		// fields — Task 8's CreateRun re-saves template.Config() as the run
  		// instance (so a dropped Template flag would save instances with
  		// template=false, breaking lidar exclusion and version pinning), and
  		// the get-pipeline API (atc/api/configserver/get.go:60) serves
  		// Config(), so a fly get-pipeline → set-pipeline round trip would
  		// silently de-template.
  		roundTripped, err := pipeline.Config()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(roundTripped.Template).To(BeTrue())
  		Expect(roundTripped.Params).To(Equal(config.Params))
  		Expect(roundTripped.RunRetention).To(Equal(config.RunRetention))

  		// non-template pipelines default to false/nil
  		Expect(defaultPipeline.Template()).To(BeFalse())
  		Expect(defaultPipeline.ParamsSchema()).To(BeNil())
  		Expect(defaultPipeline.RunRetention()).To(BeNil())

  		defaultConfig, err := defaultPipeline.Config()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(defaultConfig.Template).To(BeFalse())
  		Expect(defaultConfig.Params).To(BeNil())
  		Expect(defaultConfig.RunRetention).To(BeNil())
  	})
  })
  ```

- [ ] Run and see compile failure (`Template` not on interface):
  ```bash
  ginkgo --focus="template pipeline columns" ./atc/db/
  ```

- [ ] Extend the `Pipeline` interface (atc/db/pipeline.go:59, next to `InstanceVars()`):

  ```go
  	Template() bool
  	ParamsSchema() []atc.ParamSchema
  	RunRetention() *atc.RunRetentionConfig
  	LastRunNumber() int
  ```

- [ ] Add struct fields (atc/db/pipeline.go:135 region):

  ```go
  	template      bool
  	paramsSchema  []atc.ParamSchema
  	runRetention  *atc.RunRetentionConfig
  	lastRunNumber int
  ```

- [ ] Append the four columns to `pipelinesQuery` (atc/db/pipeline.go:156, after `p.paused_at`):

  ```go
  		p.paused_at,
  		p.template,
  		p.params_schema,
  		p.run_retention,
  		p.last_run_number`).
  ```

- [ ] Add accessors (atc/db/pipeline.go:189 region):

  ```go
  func (p *pipeline) Template() bool                        { return p.template }
  func (p *pipeline) ParamsSchema() []atc.ParamSchema       { return p.paramsSchema }
  func (p *pipeline) RunRetention() *atc.RunRetentionConfig { return p.runRetention }
  func (p *pipeline) LastRunNumber() int                    { return p.lastRunNumber }
  ```

- [ ] **F19 (2026-07-09):** carry the three fields through `Config()` (atc/db/pipeline.go:235). `Config()` reconstructs `atc.Config` from the row and per-entity tables — without this the three new fields are silently dropped: Task 8's `CreateRun` reads `template.Config()` and re-saves it as the instance, so instances would save `template=false` (lidar exclusion broken, version pinning broken, the Task 8 `instance.Template()` assertion fails), and `fly get-pipeline` → `set-pipeline` (configserver/get.go:60 serves `Config()`) would silently de-template. Set the fields from the already-scanned row in the final struct literal:

  ```go
  	config := atc.Config{
  		Groups:        p.Groups(),
  		VarSources:    p.VarSources(),
  		Resources:     resources.Configs(),
  		ResourceTypes: resourceTypes.Configs(),
  		Prototypes:    prototypes.Configs(),
  		Jobs:          jobConfigs,
  		Display:       p.Display(),
  		Template:      p.template,
  		Params:        p.paramsSchema,
  		RunRetention:  p.runRetention,
  	}
  ```

- [ ] Extend `scanPipeline` (atc/db/team.go:1333): add locals and scan targets at the end of the Scan call, then unmarshal:

  ```go
  	var (
  		// ...existing locals...
  		paramsSchema sql.NullString
  		runRetention sql.NullString
  	)
  	err := scan.Scan(&p.id, &p.name, &groups, &varSources, &display, &nonce, &p.configVersion,
  		&p.teamID, &p.teamName, &p.paused, &p.public, &p.archived, &lastUpdated,
  		&parentJobID, &parentBuildID, &instanceVars, &pausedBy, &pausedAt,
  		&p.template, &paramsSchema, &runRetention, &p.lastRunNumber)
  ```
  and after the existing unmarshal blocks:
  ```go
  	if paramsSchema.Valid {
  		err = json.Unmarshal([]byte(paramsSchema.String), &p.paramsSchema)
  		if err != nil {
  			return err
  		}
  	}

  	if runRetention.Valid {
  		err = json.Unmarshal([]byte(runRetention.String), &p.runRetention)
  		if err != nil {
  			return err
  		}
  	}
  ```

- [ ] Mirror the columns in `savePipeline` (atc/db/team.go:409). Before the `if !existingConfig` branch, marshal once:

  ```go
  	var paramsSchema sql.NullString
  	if config.Params != nil {
  		b, err := json.Marshal(config.Params)
  		if err != nil {
  			return 0, false, err
  		}
  		paramsSchema = sql.NullString{String: string(b), Valid: true}
  	}

  	var runRetention sql.NullString
  	if config.RunRetention != nil {
  		b, err := json.Marshal(config.RunRetention)
  		if err != nil {
  			return 0, false, err
  		}
  		runRetention = sql.NullString{String: string(b), Valid: true}
  	}
  ```
  In the insert `values` map (team.go:469) add:
  ```go
  		"template":      config.Template,
  		"params_schema": paramsSchema,
  		"run_retention": runRetention,
  ```
  In the update branch (team.go:514) add:
  ```go
  		Set("template", config.Template).
  		Set("params_schema", paramsSchema).
  		Set("run_retention", runRetention).
  ```
  (Do NOT touch `last_run_number` in either branch — it is owned by the run-number allocator.)

- [ ] Regenerate the `FakePipeline` counterfeiter fake:
  ```bash
  go generate ./atc/db/...
  ```

- [ ] Run to green (this touches the scan used by every pipeline query — run the full db suite; ~90s, template-DB note in CLAUDE.md applies):
  ```bash
  ginkgo --focus="template pipeline columns" ./atc/db/ && ginkgo ./atc/db/
  ```

- [ ] Commit:
  ```bash
  git add atc/db/pipeline.go atc/db/team.go atc/db/dbfakes/
  git commit -m "feat(db): mirror template/params_schema/run_retention onto pipelines rows and Config()" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 8: DB — `PipelineRun` type + factory: CreateRun / GetRun / ListRuns / RunningRuns

**Files:**
- Create: `atc/db/pipeline_run.go`
- Create: `atc/db/pipeline_run_factory.go`
- Test: `atc/db/pipeline_run_factory_test.go`

**Steps:**

- [ ] Write the failing test `atc/db/pipeline_run_factory_test.go`:

  ```go
  package db_test

  import (
  	"fmt"

  	"github.com/concourse/concourse/atc"
  	"github.com/concourse/concourse/atc/db"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  )

  var _ = Describe("PipelineRunFactory", func() {
  	var (
  		factory  db.PipelineRunFactory
  		template db.Pipeline
  	)

  	templateConfig := atc.Config{
  		Template: true,
  		Params: []atc.ParamSchema{
  			{Name: "greeting", Type: "string", Default: "hello"},
  		},
  		Resources: atc.ResourceConfigs{
  			// marker exercises both reserved vars: ((run)) = per-template
  			// number, ((run_id)) = global pipeline_runs.id (F30, 2026-07-09)
  			{Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "((greeting))", "marker": "run-((run))-id-((run_id))"}},
  		},
  		Jobs: atc.JobConfigs{
  			{
  				Name: "entry",
  				PlanSequence: []atc.Step{
  					{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
  				},
  			},
  			{
  				Name: "downstream",
  				PlanSequence: []atc.Step{
  					{Config: &atc.GetStep{Name: "some-resource", Passed: []string{"entry"}, Trigger: true}},
  				},
  			},
  		},
  	}

  	BeforeEach(func() {
  		// logger and checkFactory are db-suite globals (db_suite_test.go:70/:47);
  		// the CheckFactory is injected so CreateRun itself enqueues the frozen
  		// check set (F27, 2026-07-09)
  		factory = db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)

  		var err error
  		template, _, err = defaultTeam.SavePipeline(
  			atc.PipelineRef{Name: "run-template"}, templateConfig, db.ConfigVersion(0), false)
  		Expect(err).ToNot(HaveOccurred())
  	})

  	It("creates numbered runs with materialized instance pipelines and entry builds", func() {
  		run, err := factory.CreateRun(template.ID(), nil, "some-user")
  		Expect(err).ToNot(HaveOccurred())

  		Expect(run.Number()).To(Equal(1))
  		Expect(run.Status()).To(Equal(db.PipelineRunRunning))
  		Expect(run.CreatedBy()).To(Equal("some-user"))
  		Expect(run.Params()).To(Equal(map[string]any{"greeting": "hello"}))
  		Expect(run.TemplatePipelineID()).To(Equal(template.ID()))

  		instance, found, err := run.InstancePipeline()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeTrue())
  		Expect(instance.InstanceVars()).To(Equal(atc.InstanceVars{"run": float64(1)}))
  		Expect(instance.Template()).To(BeTrue())

  		instanceConfig, err := instance.Config()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(instanceConfig.Resources[0].Source["some"]).To(Equal("hello"))
  		// F30 (2026-07-09): ((run_id)) resolved to the pre-allocated
  		// pipeline_runs.id, ((run)) to the per-template number
  		Expect(instanceConfig.Resources[0].Source["marker"]).To(Equal(fmt.Sprintf("run-1-id-%d", run.ID())))
  		// the downstream get has passed: [entry], so it KEEPS trigger: true
  		// (passed-chain flow); only non-passed gets are stripped
  		Expect(instanceConfig.Jobs[1].Inputs()[0].Trigger).To(BeTrue())

  		entryJob, found, err := instance.Job("entry")
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeTrue())
  		pending, err := entryJob.GetPendingBuilds()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(pending).To(HaveLen(1))

  		downstreamJob, _, err := instance.Job("downstream")
  		Expect(err).ToNot(HaveOccurred())
  		pending, err = downstreamJob.GetPendingBuilds()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(pending).To(BeEmpty())

  		second, err := factory.CreateRun(template.ID(), map[string]any{"greeting": "hi"}, "some-user")
  		Expect(err).ToNot(HaveOccurred())
  		Expect(second.Number()).To(Equal(2))
  	})

  	It("rejects invalid params and non-templates", func() {
  		_, err := factory.CreateRun(template.ID(), map[string]any{"bogus": "x"}, "u")
  		Expect(err).To(MatchError(ContainSubstring(`unknown param "bogus"`)))

  		_, err = factory.CreateRun(defaultPipeline.ID(), nil, "u")
  		Expect(err).To(MatchError(db.ErrNotATemplate))
  	})

  	It("gets and lists runs", func() {
  		one, err := factory.CreateRun(template.ID(), nil, "u")
  		Expect(err).ToNot(HaveOccurred())
  		_, err = factory.CreateRun(template.ID(), nil, "u")
  		Expect(err).ToNot(HaveOccurred())

  		got, found, err := factory.GetRun(template.ID(), 1)
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeTrue())
  		Expect(got.ID()).To(Equal(one.ID()))

  		_, found, err = factory.GetRun(template.ID(), 99)
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeFalse())

  		runs, err := factory.ListRuns(template.ID(), 10)
  		Expect(err).ToNot(HaveOccurred())
  		Expect(runs).To(HaveLen(2))
  		Expect(runs[0].Number()).To(Equal(2)) // newest first

  		running, err := factory.RunningRuns()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(len(running)).To(BeNumerically(">=", 2))
  	})

  	// F27 (2026-07-09): the frozen-check enqueue lives in the FACTORY, not
  	// the API handler — lidar excludes template pipelines, so a run created
  	// by an in-process consumer (dispatch, experiments) whose entry job has
  	// a get step would otherwise pend forever on an empty version set.
  	It("enqueues the frozen check set at creation so get-step entry jobs get versions", func() {
  		getEntryConfig := atc.Config{
  			Template: true,
  			Resources: atc.ResourceConfigs{
  				{Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "source"}},
  			},
  			Jobs: atc.JobConfigs{
  				{Name: "entry-get", PlanSequence: []atc.Step{
  					{Config: &atc.GetStep{Name: "some-resource", Trigger: true}},
  				}},
  			},
  		}
  		getTemplate, _, err := defaultTeam.SavePipeline(
  			atc.PipelineRef{Name: "frozen-check-template"}, getEntryConfig, db.ConfigVersion(0), false)
  		Expect(err).ToNot(HaveOccurred())

  		run, err := factory.CreateRun(getTemplate.ID(), nil, "some-user")
  		Expect(err).ToNot(HaveOccurred())

  		instance, found, err := run.InstancePipeline()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeTrue())

  		resource, found, err := instance.Resource("some-resource")
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeTrue())

  		// exactly one manually-triggered check build persisted for the
  		// instance resource (TryCreateCheck toDB=true writes a builds row
  		// with resource_id set)
  		var checkBuilds int
  		err = dbConn.QueryRow(
  			`SELECT COUNT(*) FROM builds WHERE resource_id = $1`, resource.ID()).
  			Scan(&checkBuilds)
  		Expect(err).ToNot(HaveOccurred())
  		Expect(checkBuilds).To(Equal(1))
  	})
  })
  ```

- [ ] Run and see compile failure:
  ```bash
  ginkgo --focus="PipelineRunFactory" ./atc/db/
  ```

- [ ] Implement `atc/db/pipeline_run.go` (contracts §2.3 + §7.1 addendum extensions; `CheckComplete`/`Reopen`/`Archive` bodies land in Tasks 9–10, stub them here to compile by implementing fully as shown in those tasks or, if implementing strictly sequentially, with working bodies now — the code below is final):

  ```go
  package db

  import (
  	"database/sql"
  	"encoding/json"
  	"errors"
  	"time"

  	sq "github.com/Masterminds/squirrel"
  	"github.com/concourse/concourse/atc/db/lock"
  )

  type PipelineRunStatus string

  const (
  	PipelineRunRunning   PipelineRunStatus = "running"
  	PipelineRunSucceeded PipelineRunStatus = "succeeded"
  	PipelineRunFailed    PipelineRunStatus = "failed"
  	PipelineRunErrored   PipelineRunStatus = "errored"
  	PipelineRunAborted   PipelineRunStatus = "aborted"
  )

  //counterfeiter:generate . PipelineRun
  type PipelineRun interface {
  	ID() int
  	TemplatePipelineID() int
  	InstancePipelineID() (int, bool)
  	Number() int
  	Params() map[string]any
  	Status() PipelineRunStatus
  	CreatedBy() string
  	CreatedAt() time.Time
  	CompletedAt() (time.Time, bool)
  	Archived() bool

  	// InstancePipeline loads the instanced pipeline executing this run.
  	InstancePipeline() (Pipeline, bool, error)

  	// CheckComplete reports whether the run's instance pipeline is quiescent
  	// (no job builds pending or started, at least one job build exists, no
  	// active unpaused job awaiting scheduling) and, if so, the worst-of
  	// aggregate status (errored > aborted > failed > succeeded).
  	CheckComplete() (PipelineRunStatus, bool, error)

  	Finish(status PipelineRunStatus) error
  	Reopen() error
  	Archive() error
  }

  type pipelineRun struct {
  	conn        DbConn
  	lockFactory lock.LockFactory

  	id                 int
  	templatePipelineID int
  	instancePipelineID sql.NullInt64
  	number             int
  	params             map[string]any
  	status             PipelineRunStatus
  	createdBy          string
  	createdAt          time.Time
  	completedAt        sql.NullTime
  	archived           bool
  }

  func newPipelineRun(conn DbConn, lockFactory lock.LockFactory) *pipelineRun {
  	return &pipelineRun{conn: conn, lockFactory: lockFactory}
  }

  func (r *pipelineRun) ID() int                 { return r.id }
  func (r *pipelineRun) TemplatePipelineID() int { return r.templatePipelineID }
  func (r *pipelineRun) Number() int             { return r.number }
  func (r *pipelineRun) Params() map[string]any  { return r.params }
  func (r *pipelineRun) Status() PipelineRunStatus {
  	return r.status
  }
  func (r *pipelineRun) CreatedBy() string    { return r.createdBy }
  func (r *pipelineRun) CreatedAt() time.Time { return r.createdAt }
  func (r *pipelineRun) Archived() bool       { return r.archived }

  func (r *pipelineRun) InstancePipelineID() (int, bool) {
  	if !r.instancePipelineID.Valid {
  		return 0, false
  	}
  	return int(r.instancePipelineID.Int64), true
  }

  func (r *pipelineRun) CompletedAt() (time.Time, bool) {
  	if !r.completedAt.Valid {
  		return time.Time{}, false
  	}
  	return r.completedAt.Time, true
  }

  func (r *pipelineRun) InstancePipeline() (Pipeline, bool, error) {
  	id, ok := r.InstancePipelineID()
  	if !ok {
  		return nil, false, nil
  	}
  	pipeline := newPipeline(r.conn, r.lockFactory)
  	err := scanPipeline(
  		pipeline,
  		pipelinesQuery.Where(sq.Eq{"p.id": id}).RunWith(r.conn).QueryRow(),
  	)
  	if errors.Is(err, sql.ErrNoRows) {
  		return nil, false, nil
  	}
  	if err != nil {
  		return nil, false, err
  	}
  	return pipeline, true, nil
  }

  func (r *pipelineRun) CheckComplete() (PipelineRunStatus, bool, error) {
  	instanceID, ok := r.InstancePipelineID()
  	if !ok {
  		return "", false, nil
  	}

  	var active, total, unscheduled int
  	err := r.conn.QueryRow(`
  		SELECT
  			COUNT(*) FILTER (WHERE b.status IN ('pending','started')),
  			COUNT(*)
  		FROM builds b
  		WHERE b.pipeline_id = $1 AND b.job_id IS NOT NULL`, instanceID).
  		Scan(&active, &total)
  	if err != nil {
  		return "", false, err
  	}

  	err = r.conn.QueryRow(`
  		SELECT COUNT(*)
  		FROM jobs j
  		WHERE j.pipeline_id = $1
  		AND j.active AND NOT j.paused
  		AND j.schedule_requested > j.last_scheduled`, instanceID).
  		Scan(&unscheduled)
  	if err != nil {
  		return "", false, err
  	}

  	if active > 0 || total == 0 || unscheduled > 0 {
  		return "", false, nil
  	}

  	rows, err := r.conn.Query(`
  		SELECT DISTINCT ON (b.job_id) b.status
  		FROM builds b
  		WHERE b.pipeline_id = $1 AND b.job_id IS NOT NULL
  		ORDER BY b.job_id, b.id DESC`, instanceID)
  	if err != nil {
  		return "", false, err
  	}
  	defer Close(rows)

  	worst, worstSeverity := PipelineRunSucceeded, 1
  	for rows.Next() {
  		var status string
  		if err := rows.Scan(&status); err != nil {
  			return "", false, err
  		}
  		mapped, severity := runStatusFromBuildStatus(status)
  		if severity > worstSeverity {
  			worst, worstSeverity = mapped, severity
  		}
  	}

  	return worst, true, nil
  }

  func runStatusFromBuildStatus(status string) (PipelineRunStatus, int) {
  	switch BuildStatus(status) {
  	case BuildStatusErrored:
  		return PipelineRunErrored, 4
  	case BuildStatusAborted:
  		return PipelineRunAborted, 3
  	case BuildStatusFailed:
  		return PipelineRunFailed, 2
  	default:
  		return PipelineRunSucceeded, 1
  	}
  }

  func (r *pipelineRun) Finish(status PipelineRunStatus) error {
  	_, err := psql.Update("pipeline_runs").
  		Set("status", string(status)).
  		Set("completed_at", sq.Expr("now()")).
  		Where(sq.Eq{"id": r.id}).
  		RunWith(r.conn).
  		Exec()
  	if err == nil {
  		r.status = status
  	}
  	return err
  }

  func (r *pipelineRun) Reopen() error {
  	_, err := psql.Update("pipeline_runs").
  		Set("status", string(PipelineRunRunning)).
  		Set("completed_at", nil).
  		Where(sq.Eq{"id": r.id}).
  		RunWith(r.conn).
  		Exec()
  	if err == nil {
  		r.status = PipelineRunRunning
  		r.completedAt = sql.NullTime{}
  	}
  	return err
  }

  func (r *pipelineRun) Archive() error {
  	instance, found, err := r.InstancePipeline()
  	if err != nil {
  		return err
  	}
  	if found {
  		// existing pipeline-archival machinery (soft-archive + GC notify)
  		if err := instance.Archive(); err != nil {
  			return err
  		}
  	}
  	_, err = psql.Update("pipeline_runs").
  		Set("archived", true).
  		Where(sq.Eq{"id": r.id}).
  		RunWith(r.conn).
  		Exec()
  	if err == nil {
  		r.archived = true
  	}
  	return err
  }

  func scanPipelineRun(r *pipelineRun, scan scannable) error {
  	var params sql.NullString
  	var status string
  	err := scan.Scan(&r.id, &r.templatePipelineID, &r.instancePipelineID, &r.number,
  		&params, &status, &r.createdBy, &r.createdAt, &r.completedAt, &r.archived)
  	if err != nil {
  		return err
  	}
  	r.status = PipelineRunStatus(status)
  	if params.Valid {
  		if err := json.Unmarshal([]byte(params.String), &r.params); err != nil {
  			return err
  		}
  	}
  	return nil
  }
  ```

- [ ] Implement `atc/db/pipeline_run_factory.go`:

  ```go
  package db

  import (
  	"context"
  	"database/sql"
  	"encoding/json"
  	"errors"
  	"fmt"

  	sq "github.com/Masterminds/squirrel"
  	"code.cloudfoundry.org/lager/v3"
  	"code.cloudfoundry.org/lager/v3/lagerctx"
  	"github.com/concourse/concourse/atc"
  	"github.com/concourse/concourse/atc/db/lock"
  )

  // ErrNotATemplate is returned when CreateRun is called on a pipeline that is
  // not a base template (template: true, no instance vars).
  var ErrNotATemplate = errors.New("pipeline is not a template")

  // ErrTemplateNotFound is returned when the template pipeline id is unknown.
  var ErrTemplateNotFound = errors.New("template pipeline not found")

  var pipelineRunsQuery = psql.Select(
  	"r.id", "r.template_pipeline_id", "r.instance_pipeline_id", "r.number",
  	"r.params", "r.status", "r.created_by", "r.created_at", "r.completed_at",
  	"r.archived",
  ).From("pipeline_runs r")

  //counterfeiter:generate . PipelineRunFactory
  type PipelineRunFactory interface {
  	// CreateRun validates params against the template's params schema,
  	// allocates the next run number, materializes the instanced pipeline
  	// (instance_vars: {"run": N}), triggers entry jobs, and returns the run.
  	CreateRun(templatePipelineID int, params map[string]any, createdBy string) (PipelineRun, error)
  	GetRun(templatePipelineID, number int) (PipelineRun, bool, error)
  	ListRuns(templatePipelineID int, limit int) ([]PipelineRun, error)
  	RunningRuns() ([]PipelineRun, error)
  	CompletedRunsWithNewActivity() ([]PipelineRun, error)
  	RunsToArchive() ([]PipelineRun, error)
  }

  // NewPipelineRunFactory constructs the factory. The CheckFactory is
  // injected (F27, 2026-07-09) because CreateRun itself enqueues the frozen
  // check set — the runs API handler is a pass-through, so in-process
  // consumers (dispatch, experiments) get identical semantics. The logger is
  // injected (Registrar/Reaper idiom) because CreateRun has no ctx/logger
  // parameter (§2.3 frozen signature) and the check enqueue is best-effort:
  // its failures must be logged, never returned.
  func NewPipelineRunFactory(
  	logger lager.Logger,
  	conn DbConn,
  	lockFactory lock.LockFactory,
  	checkFactory CheckFactory,
  ) PipelineRunFactory {
  	return &pipelineRunFactory{
  		logger:       logger,
  		conn:         conn,
  		lockFactory:  lockFactory,
  		checkFactory: checkFactory,
  	}
  }

  type pipelineRunFactory struct {
  	logger       lager.Logger
  	conn         DbConn
  	lockFactory  lock.LockFactory
  	checkFactory CheckFactory
  }

  func (f *pipelineRunFactory) CreateRun(templatePipelineID int, params map[string]any, createdBy string) (PipelineRun, error) {
  	template, found, err := f.pipelineByID(templatePipelineID)
  	if err != nil {
  		return nil, err
  	}
  	if !found {
  		return nil, ErrTemplateNotFound
  	}
  	if !template.Template() || template.InstanceVars() != nil {
  		return nil, ErrNotATemplate
  	}

  	validated, err := atc.ValidateRunParams(template.ParamsSchema(), params)
  	if err != nil {
  		return nil, err
  	}

  	// Relies on Task 7's Config() carrying Template/Params/RunRetention
  	// (F19, 2026-07-09): the returned config is re-saved as the instance, so
  	// a Config() that dropped Template would save instances with
  	// template=false and break lidar exclusion + version pinning. If the
  	// instance.Template() assertion in the factory test fails, fix
  	// db.pipeline.Config() — do NOT force Template=true here.
  	config, err := template.Config()
  	if err != nil {
  		return nil, err
  	}

  	tx, err := f.conn.Begin()
  	if err != nil {
  		return nil, err
  	}
  	defer Rollback(tx)

  	var number int
  	err = tx.QueryRow(
  		`UPDATE pipelines SET last_run_number = last_run_number + 1 WHERE id = $1 RETURNING last_run_number`,
  		templatePipelineID,
  	).Scan(&number)
  	if err != nil {
  		return nil, err
  	}

  	// F30 (2026-07-09): allocate pipeline_runs.id BEFORE materialization so
  	// ((run_id)) — the globally-unique id that §8.1 AGENT_PIPELINE_RUN_ID is
  	// defined as — can be interpolated into the instance config. nextval
  	// keeps the SERIAL sequence consistent with the explicit-id insert below.
  	var runID int
  	err = tx.QueryRow(
  		`SELECT nextval(pg_get_serial_sequence('pipeline_runs', 'id'))`,
  	).Scan(&runID)
  	if err != nil {
  		return nil, err
  	}

  	instanceConfig, err := atc.MaterializeRunConfig(config, number, runID, validated)
  	if err != nil {
  		return nil, err
  	}

  	nullID := sql.NullInt64{Valid: false}
  	instanceID, _, err := savePipeline(
  		tx,
  		atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": number}},
  		instanceConfig,
  		0,     // fresh instance; run numbers are unique so it never pre-exists
  		false, // run instances start unpaused
  		template.TeamID(),
  		nullID, nullID,
  	)
  	if err != nil {
  		return nil, err
  	}

  	paramsPayload, err := json.Marshal(validated)
  	if err != nil {
  		return nil, err
  	}

  	run := newPipelineRun(f.conn, f.lockFactory)
  	run.id = runID
  	run.templatePipelineID = templatePipelineID
  	run.instancePipelineID = sql.NullInt64{Int64: int64(instanceID), Valid: true}
  	run.number = number
  	run.params = validated
  	run.status = PipelineRunRunning
  	run.createdBy = createdBy

  	// explicit id: pre-allocated above so ((run_id)) is already baked into
  	// the saved instance config (F30)
  	err = psql.Insert("pipeline_runs").
  		Columns("id", "template_pipeline_id", "instance_pipeline_id", "number", "params", "created_by").
  		Values(runID, templatePipelineID, instanceID, number, paramsPayload, createdBy).
  		Suffix("RETURNING created_at").
  		RunWith(tx).
  		QueryRow().
  		Scan(&run.createdAt)
  	if err != nil {
  		return nil, err
  	}

  	err = tx.Commit()
  	if err != nil {
  		return nil, err
  	}

  	// Trigger entry jobs (no passed: upstream) as manually-triggered builds.
  	instance, found, err := f.pipelineByID(instanceID)
  	if err != nil {
  		return nil, err
  	}
  	if found {
  		for _, jobName := range instanceConfig.EntryJobs() {
  			job, jobFound, err := instance.Job(jobName)
  			if err != nil {
  				return nil, err
  			}
  			if !jobFound {
  				continue
  			}
  			_, err = job.CreateBuild(createdBy)
  			if err != nil {
  				return nil, fmt.Errorf("triggering entry job %s: %w", jobName, err)
  			}
  		}

  		// F27 (2026-07-09): the frozen check set is enqueued HERE, by the
  		// factory, per shared-contracts §7.1 item 2 — not in the API handler.
  		f.enqueueInitialChecks(instance)
  	}

  	return run, nil
  }

  // enqueueInitialChecks fires one manually-triggered check per resource of
  // the run's instance pipeline — the frozen-check-set pinning model
  // (shared-contracts §7.1). It lives on the factory (F27, 2026-07-09) so
  // in-process consumers (dispatch, experiments) get the frozen check set
  // too: lidar excludes template pipelines, so a factory-created run whose
  // entry job has a get step would otherwise pend forever on an empty
  // version set (NULL scope → trivially-passing ResourcesChecked → zero
  // versions). Best-effort: failures are logged, never fail run creation
  // (fly check-resource remains available).
  func (f *pipelineRunFactory) enqueueInitialChecks(instance Pipeline) {
  	logger := f.logger.Session("enqueue-initial-checks", lager.Data{
  		"pipeline": instance.Name(), "instance-vars": instance.InstanceVars(),
  	})

  	resourceTypes, err := instance.ResourceTypes()
  	if err != nil {
  		logger.Error("failed-to-load-instance-resource-types", err)
  		return
  	}
  	resources, err := instance.Resources()
  	if err != nil {
  		logger.Error("failed-to-load-instance-resources", err)
  		return
  	}

  	for _, resource := range resources {
  		_, _, err := f.checkFactory.TryCreateCheck(
  			lagerctx.NewContext(context.Background(), logger),
  			resource,
  			resourceTypes,
  			nil,  // from latest
  			true, // manually triggered: skip interval
  			true, // skip interval recursively
  			true, // persist to DB
  		)
  		if err != nil {
  			logger.Error("failed-to-enqueue-initial-check", err, lager.Data{"resource": resource.Name()})
  		}
  	}
  }

  func (f *pipelineRunFactory) GetRun(templatePipelineID, number int) (PipelineRun, bool, error) {
  	run := newPipelineRun(f.conn, f.lockFactory)
  	err := scanPipelineRun(run, pipelineRunsQuery.
  		Where(sq.Eq{"r.template_pipeline_id": templatePipelineID, "r.number": number}).
  		RunWith(f.conn).
  		QueryRow())
  	if errors.Is(err, sql.ErrNoRows) {
  		return nil, false, nil
  	}
  	if err != nil {
  		return nil, false, err
  	}
  	return run, true, nil
  }

  func (f *pipelineRunFactory) ListRuns(templatePipelineID int, limit int) ([]PipelineRun, error) {
  	if limit <= 0 {
  		limit = 100
  	}
  	rows, err := pipelineRunsQuery.
  		Where(sq.Eq{"r.template_pipeline_id": templatePipelineID}).
  		OrderBy("r.number DESC").
  		Limit(uint64(limit)).
  		RunWith(f.conn).
  		Query()
  	if err != nil {
  		return nil, err
  	}
  	return f.scanRuns(rows)
  }

  func (f *pipelineRunFactory) RunningRuns() ([]PipelineRun, error) {
  	rows, err := pipelineRunsQuery.
  		Where(sq.Eq{"r.status": string(PipelineRunRunning), "r.archived": false}).
  		OrderBy("r.id ASC").
  		RunWith(f.conn).
  		Query()
  	if err != nil {
  		return nil, err
  	}
  	return f.scanRuns(rows)
  }

  // CompletedRunsWithNewActivity returns non-running, non-archived runs whose
  // instance pipeline has a pending/started job build — OR a job build that
  // COMPLETED after the run's completed_at (F26, 2026-07-09): the Finish
  // notify (the only wakeup besides the 10s poll) fires after a build leaves
  // pending/started, so a retrigger that starts AND finishes inside one
  // polling gap would otherwise never be observed and the run would keep a
  // stale terminal status forever (plan-creation failures Finish without ever
  // starting — buildstarter.go:225). Self-terminating: reopen clears
  // completed_at (run leaves this query via the status filter) and the
  // re-complete stamps a newer completed_at than every existing end_time.
  func (f *pipelineRunFactory) CompletedRunsWithNewActivity() ([]PipelineRun, error) {
  	rows, err := pipelineRunsQuery.
  		Where(sq.NotEq{"r.status": string(PipelineRunRunning)}).
  		Where(sq.Eq{"r.archived": false}).
  		Where(sq.Expr(`EXISTS (
  			SELECT 1 FROM builds b
  			WHERE b.pipeline_id = r.instance_pipeline_id
  			AND b.job_id IS NOT NULL
  			AND (b.status IN ('pending','started')
  			     OR (b.completed AND r.completed_at IS NOT NULL
  			         AND b.end_time > r.completed_at)))`)).
  		OrderBy("r.id ASC").
  		RunWith(f.conn).
  		Query()
  	if err != nil {
  		return nil, err
  	}
  	return f.scanRuns(rows)
  }

  func (f *pipelineRunFactory) RunsToArchive() ([]PipelineRun, error) {
  	rows, err := f.conn.Query(`
  		WITH candidate AS (
  			SELECT r.id,
  			       r.completed_at,
  			       p.run_retention,
  			       ROW_NUMBER() OVER (PARTITION BY r.template_pipeline_id ORDER BY r.number DESC) AS rank
  			FROM pipeline_runs r
  			JOIN pipelines p ON p.id = r.template_pipeline_id
  			WHERE r.archived = false
  			  AND r.status <> 'running'
  			  AND p.run_retention IS NOT NULL
  		)
  		SELECT id FROM candidate
  		WHERE (run_retention ? 'keep_last' AND rank > (run_retention->>'keep_last')::int)
  		   OR (run_retention ? 'ttl_days' AND completed_at IS NOT NULL
  		       AND completed_at < now() - make_interval(days => (run_retention->>'ttl_days')::int))`)
  	if err != nil {
  		return nil, err
  	}
  	defer Close(rows)

  	var ids []int
  	for rows.Next() {
  		var id int
  		if err := rows.Scan(&id); err != nil {
  			return nil, err
  		}
  		ids = append(ids, id)
  	}
  	if len(ids) == 0 {
  		return nil, nil
  	}

  	runRows, err := pipelineRunsQuery.
  		Where(sq.Eq{"r.id": ids}).
  		OrderBy("r.id ASC").
  		RunWith(f.conn).
  		Query()
  	if err != nil {
  		return nil, err
  	}
  	return f.scanRuns(runRows)
  }

  func (f *pipelineRunFactory) scanRuns(rows *sql.Rows) ([]PipelineRun, error) {
  	defer Close(rows)
  	var runs []PipelineRun
  	for rows.Next() {
  		run := newPipelineRun(f.conn, f.lockFactory)
  		if err := scanPipelineRun(run, rows); err != nil {
  			return nil, err
  		}
  		runs = append(runs, run)
  	}
  	return runs, nil
  }

  func (f *pipelineRunFactory) pipelineByID(id int) (Pipeline, bool, error) {
  	pipeline := newPipeline(f.conn, f.lockFactory)
  	err := scanPipeline(
  		pipeline,
  		pipelinesQuery.Where(sq.Eq{"p.id": id}).RunWith(f.conn).QueryRow(),
  	)
  	if errors.Is(err, sql.ErrNoRows) {
  		return nil, false, nil
  	}
  	if err != nil {
  		return nil, false, err
  	}
  	return pipeline, true, nil
  }
  ```

- [ ] Generate counterfeiter fakes:
  ```bash
  go generate ./atc/db/...
  ```

- [ ] Run to green:
  ```bash
  ginkgo --focus="PipelineRunFactory" ./atc/db/
  ```

- [ ] Commit:
  ```bash
  git add atc/db/pipeline_run.go atc/db/pipeline_run_factory.go atc/db/pipeline_run_factory_test.go atc/db/dbfakes/
  git commit -m "feat(db): PipelineRunFactory with run creation, numbering, materialization, entry-job triggering and frozen-check enqueue" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 9: DB — completion detection specs (worst-of, parked, aborts, retriggers)

The implementation landed in Task 8 (`CheckComplete`, `Reopen`, `CompletedRunsWithNewActivity`); this task pins the completion contract with specs, including the parked-run rule this workstream OWNS.

**Files:**
- Create: `atc/db/pipeline_run_test.go`
- Test: same file

**Steps:**

- [ ] Write `atc/db/pipeline_run_test.go`:

  ```go
  package db_test

  import (
  	"github.com/concourse/concourse/atc"
  	"github.com/concourse/concourse/atc/db"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  )

  var _ = Describe("PipelineRun completion", func() {
  	var (
  		factory  db.PipelineRunFactory
  		run      db.PipelineRun
  		instance db.Pipeline
  	)

  	config := atc.Config{
  		Template: true,
  		Jobs: atc.JobConfigs{
  			{Name: "entry", PlanSequence: []atc.Step{
  				{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
  			}},
  			{Name: "second", PlanSequence: []atc.Step{
  				{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
  			}},
  		},
  	}

  	// the scheduler is not running in this suite; mark all instance jobs as
  	// having been scheduled so the unscheduled-jobs completion guard passes
  	markScheduled := func(pipelineID int) {
  		_, err := dbConn.Exec(
  			`UPDATE jobs SET last_scheduled = schedule_requested WHERE pipeline_id = $1`, pipelineID)
  		Expect(err).ToNot(HaveOccurred())
  	}

  	finishBuild := func(jobName string, status db.BuildStatus) db.Build {
  		job, found, err := instance.Job(jobName)
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeTrue())
  		build, err := job.CreateBuild("test")
  		Expect(err).ToNot(HaveOccurred())
  		Expect(build.Finish(status)).To(Succeed())
  		return build
  	}

  	BeforeEach(func() {
  		factory = db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)

  		template, _, err := defaultTeam.SavePipeline(
  			atc.PipelineRef{Name: "completion-template"}, config, db.ConfigVersion(0), false)
  		Expect(err).ToNot(HaveOccurred())

  		run, err = factory.CreateRun(template.ID(), nil, "test")
  		Expect(err).ToNot(HaveOccurred())

  		var found bool
  		instance, found, err = run.InstancePipeline()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeTrue())
  	})

  	It("does not complete while entry builds are pending", func() {
  		markScheduled(instance.ID())
  		_, complete, err := run.CheckComplete()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(complete).To(BeFalse())
  	})

  	It("does not complete while a build is started — the parked-run contract", func() {
  		// a parked agent step (ask_human / checkpoint) keeps its build in
  		// 'started'; a parked run must therefore stay 'running'
  		instanceID := instance.ID()
  		_, err := dbConn.Exec(
  			`UPDATE builds SET status = 'started' WHERE pipeline_id = $1`, instanceID)
  		Expect(err).ToNot(HaveOccurred())
  		markScheduled(instanceID)

  		_, complete, err := run.CheckComplete()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(complete).To(BeFalse())
  	})

  	It("does not complete while a job still awaits scheduling", func() {
  		// entry build finished but downstream schedule_requested advanced
  		_, err := dbConn.Exec(
  			`UPDATE builds SET status = 'succeeded' WHERE pipeline_id = $1`, instance.ID())
  		Expect(err).ToNot(HaveOccurred())
  		// deliberately do NOT markScheduled: jobs still have
  		// schedule_requested > last_scheduled from savePipeline
  		_, complete, err := run.CheckComplete()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(complete).To(BeFalse())
  	})

  	It("completes with worst-of aggregate status", func() {
  		_, err := dbConn.Exec(
  			`UPDATE builds SET status = 'succeeded' WHERE pipeline_id = $1`, instance.ID())
  		Expect(err).ToNot(HaveOccurred())
  		finishBuild("second", db.BuildStatusFailed)
  		markScheduled(instance.ID())

  		status, complete, err := run.CheckComplete()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(complete).To(BeTrue())
  		Expect(status).To(Equal(db.PipelineRunFailed))

  		// errored beats failed
  		finishBuild("second", db.BuildStatusErrored)
  		markScheduled(instance.ID())
  		status, complete, err = run.CheckComplete()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(complete).To(BeTrue())
  		Expect(status).To(Equal(db.PipelineRunErrored))
  	})

  	It("completes aborted runs as aborted", func() {
  		_, err := dbConn.Exec(
  			`UPDATE builds SET status = 'aborted' WHERE pipeline_id = $1`, instance.ID())
  		Expect(err).ToNot(HaveOccurred())
  		markScheduled(instance.ID())

  		status, complete, err := run.CheckComplete()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(complete).To(BeTrue())
  		Expect(status).To(Equal(db.PipelineRunAborted))
  	})

  	It("surfaces retriggers on completed runs and reopens them", func() {
  		_, err := dbConn.Exec(
  			`UPDATE builds SET status = 'succeeded' WHERE pipeline_id = $1`, instance.ID())
  		Expect(err).ToNot(HaveOccurred())
  		markScheduled(instance.ID())
  		Expect(run.Finish(db.PipelineRunSucceeded)).To(Succeed())

  		// a retrigger creates a new pending build
  		job, _, err := instance.Job("entry")
  		Expect(err).ToNot(HaveOccurred())
  		_, err = job.CreateBuild("test")
  		Expect(err).ToNot(HaveOccurred())

  		reactivated, err := factory.CompletedRunsWithNewActivity()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(reactivated).To(HaveLen(1))
  		Expect(reactivated[0].ID()).To(Equal(run.ID()))

  		Expect(reactivated[0].Reopen()).To(Succeed())
  		Expect(reactivated[0].Status()).To(Equal(db.PipelineRunRunning))
  		_, hasCompletedAt := reactivated[0].CompletedAt()
  		Expect(hasCompletedAt).To(BeFalse())

  		again, err := factory.CompletedRunsWithNewActivity()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(again).To(BeEmpty())
  	})

  	// F26 (2026-07-09): the Finish notify fires only after a build has left
  	// pending/started, so a retrigger that starts AND finishes inside one
  	// 10s polling gap is invisible to the pending/started predicate — the
  	// run would keep a stale terminal status forever. The widened predicate
  	// also matches builds that COMPLETED after the run's completed_at, and
  	// is self-terminating: reopen→re-complete stamps a newer completed_at.
  	It("surfaces fast-finishing retriggers that never linger in pending or started", func() {
  		_, err := dbConn.Exec(
  			`UPDATE builds SET status = 'succeeded' WHERE pipeline_id = $1`, instance.ID())
  		Expect(err).ToNot(HaveOccurred())
  		markScheduled(instance.ID())
  		Expect(run.Finish(db.PipelineRunSucceeded)).To(Succeed())

  		// the retrigger is created AND finished before the lifecycler ever
  		// observes it: by observation time nothing is pending/started, only
  		// a completed build with end_time > the run's completed_at
  		finishBuild("entry", db.BuildStatusFailed)

  		reactivated, err := factory.CompletedRunsWithNewActivity()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(reactivated).To(HaveLen(1))
  		Expect(reactivated[0].ID()).To(Equal(run.ID()))

  		// reopen → recompute → re-finish: the fresh completed_at is newer
  		// than every build end_time, so the run stops matching (no loop)
  		Expect(reactivated[0].Reopen()).To(Succeed())
  		markScheduled(instance.ID())
  		status, complete, err := reactivated[0].CheckComplete()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(complete).To(BeTrue())
  		Expect(status).To(Equal(db.PipelineRunFailed))
  		Expect(reactivated[0].Finish(status)).To(Succeed())

  		again, err := factory.CompletedRunsWithNewActivity()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(again).To(BeEmpty())
  	})
  })
  ```

- [ ] Run — these pass against Task 8's implementation; if any spec fails, fix the implementation (not the spec: the spec IS the contract from §1.5/§7.1):
  ```bash
  ginkgo --focus="PipelineRun completion" ./atc/db/
  ```

- [ ] Commit:
  ```bash
  git add atc/db/pipeline_run_test.go
  git commit -m "test(db): pipeline-run completion contract incl. parked-run, reopen and fast-finish-retrigger semantics" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 10: DB — retention specs (keep_last / ttl_days / Archive)

**Files:**
- Modify: `atc/db/pipeline_run_test.go` (append Describe)
- Test: same file

**Steps:**

- [ ] Append the retention Describe:

  ```go
  var _ = Describe("PipelineRun retention", func() {
  	var factory db.PipelineRunFactory

  	makeTemplate := func(name string, retention *atc.RunRetentionConfig) db.Pipeline {
  		config := atc.Config{
  			Template:     true,
  			RunRetention: retention,
  			Jobs: atc.JobConfigs{
  				{Name: "entry", PlanSequence: []atc.Step{
  					{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
  				}},
  			},
  		}
  		template, _, err := defaultTeam.SavePipeline(
  			atc.PipelineRef{Name: name}, config, db.ConfigVersion(0), false)
  		Expect(err).ToNot(HaveOccurred())
  		return template
  	}

  	completedRun := func(template db.Pipeline) db.PipelineRun {
  		run, err := factory.CreateRun(template.ID(), nil, "test")
  		Expect(err).ToNot(HaveOccurred())
  		Expect(run.Finish(db.PipelineRunSucceeded)).To(Succeed())
  		return run
  	}

  	BeforeEach(func() {
  		factory = db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)
  	})

  	It("selects completed runs beyond keep_last, newest kept", func() {
  		template := makeTemplate("retention-keep-last", &atc.RunRetentionConfig{KeepLast: 2})
  		one := completedRun(template)
  		completedRun(template)
  		completedRun(template)

  		toArchive, err := factory.RunsToArchive()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(toArchive).To(HaveLen(1))
  		Expect(toArchive[0].ID()).To(Equal(one.ID()))
  	})

  	It("selects completed runs older than ttl_days", func() {
  		template := makeTemplate("retention-ttl", &atc.RunRetentionConfig{TTLDays: 5})
  		old := completedRun(template)
  		completedRun(template)

  		_, err := dbConn.Exec(
  			`UPDATE pipeline_runs SET completed_at = now() - interval '10 days' WHERE id = $1`, old.ID())
  		Expect(err).ToNot(HaveOccurred())

  		toArchive, err := factory.RunsToArchive()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(toArchive).To(HaveLen(1))
  		Expect(toArchive[0].ID()).To(Equal(old.ID()))
  	})

  	It("never selects running runs or templates without retention", func() {
  		noRetention := makeTemplate("retention-none", nil)
  		completedRun(noRetention)

  		withRetention := makeTemplate("retention-running", &atc.RunRetentionConfig{KeepLast: 0, TTLDays: 1})
  		_, err := factory.CreateRun(withRetention.ID(), nil, "test") // stays running
  		Expect(err).ToNot(HaveOccurred())

  		toArchive, err := factory.RunsToArchive()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(toArchive).To(BeEmpty())
  	})

  	It("Archive archives the instance pipeline and the run row", func() {
  		template := makeTemplate("retention-archive", &atc.RunRetentionConfig{KeepLast: 0})
  		run := completedRun(template)

  		Expect(run.Archive()).To(Succeed())
  		Expect(run.Archived()).To(BeTrue())

  		instance, found, err := run.InstancePipeline()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeTrue())
  		Expect(instance.Archived()).To(BeTrue())

  		// archived runs are never re-selected
  		toArchive, err := factory.RunsToArchive()
  		Expect(err).ToNot(HaveOccurred())
  		Expect(toArchive).To(BeEmpty())
  	})
  })
  ```

  Note the `KeepLast: 0` + `keep_last` JSONB presence subtlety: `RunRetentionConfig{KeepLast: 0}` marshals with `omitempty` to `{}` — so "keep_last: 0" is NOT representable and the "retention-archive" template's run would not be selected by `RunsToArchive`; that spec archives explicitly via `run.Archive()`, which is exactly what it tests. The "retention-running" spec relies on `ttl_days: 1` with a running run.

- [ ] Run; fix any SQL mismatches in `RunsToArchive` until green (the spec is the contract):
  ```bash
  ginkgo --focus="PipelineRun retention" ./atc/db/
  ```

- [ ] Run the full db suite to catch scan regressions:
  ```bash
  ginkgo ./atc/db/
  ```

- [ ] Commit:
  ```bash
  git add atc/db/pipeline_run_test.go atc/db/pipeline_run_factory.go
  git commit -m "test(db): pipeline-run retention (keep_last, ttl_days, archive) specs" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 11: Scheduler skips base templates

**Files:**
- Modify: `atc/db/job_factory.go:95` (`jobsToSchedule` query)
- Test: `atc/db/job_factory_test.go` (append spec)

**Steps:**

- [ ] Append a failing spec to `atc/db/job_factory_test.go` (the file already has a `jobFactory` under test and helpers; use the same `defaultTeam.SavePipeline` idiom as its existing specs):

  ```go
  Describe("template pipelines and scheduling", func() {
  	templateConfig := atc.Config{
  		Template: true,
  		Jobs: atc.JobConfigs{
  			{Name: "template-job", PlanSequence: []atc.Step{
  				{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
  			}},
  		},
  	}

  	It("excludes base template jobs but includes run-instance jobs", func() {
  		_, _, err := defaultTeam.SavePipeline(
  			atc.PipelineRef{Name: "sched-template"}, templateConfig, db.ConfigVersion(0), false)
  		Expect(err).ToNot(HaveOccurred())

  		runFactory := db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)
  		template, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: "sched-template"})
  		Expect(err).ToNot(HaveOccurred())
  		Expect(found).To(BeTrue())
  		_, err = runFactory.CreateRun(template.ID(), nil, "test")
  		Expect(err).ToNot(HaveOccurred())

  		jobs, err := jobFactory.JobsToSchedule()
  		Expect(err).ToNot(HaveOccurred())

  		var names []string
  		for _, job := range jobs {
  			if job.PipelineName() == "sched-template" {
  				names = append(names, fmt.Sprintf("%s/%v", job.Name(), job.PipelineInstanceVars()))
  			}
  		}
  		// only the run instance's job appears, never the base template's
  		Expect(names).To(HaveLen(1))
  		Expect(names[0]).To(ContainSubstring("run"))
  	})
  })
  ```
  (`db.SchedulerJob` embeds the `db.Job` interface — job_factory.go:41 — so `job.Name()`, `job.PipelineName()` and `job.PipelineInstanceVars()` are all available; add `"fmt"` to the test imports if missing.)

- [ ] Run and see failure (both jobs currently scheduled):
  ```bash
  ginkgo --focus="template pipelines and scheduling" ./atc/db/
  ```

- [ ] Add the filter in `jobsToSchedule` (atc/db/job_factory.go:95):

  ```go
  	query := jobsQuery.
  		Where(sq.Expr("j.schedule_requested > j.last_scheduled")).
  		Where(sq.Eq{
  			"j.active": true,
  			"j.paused": false,
  			"p.paused": false,
  		}).
  		// base template pipelines never self-schedule; their run instances do
  		Where(sq.Expr("NOT (p.template AND p.instance_vars IS NULL)"))
  ```

- [ ] Run to green:
  ```bash
  ginkgo --focus="template pipelines and scheduling" ./atc/db/ && ginkgo --focus="Job Factory" ./atc/db/
  ```

- [ ] Commit:
  ```bash
  git add atc/db/job_factory.go atc/db/job_factory_test.go
  git commit -m "feat(db): scheduler skips base template pipelines" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 12: Lidar check scan excludes template pipelines

**Files:**
- Modify: `atc/db/check_factory.go:176` (`Resources()` query)
- Test: `atc/db/check_factory_test.go` (append spec)

**Steps:**

- [ ] Append a failing spec to `atc/db/check_factory_test.go` (the file has `checkFactory` under test; mirror its existing `Resources` Describe setup):

  ```go
  Context("with template pipelines", func() {
  	It("excludes resources of base templates and run instances, keeps ordinary instanced pipelines", func() {
  		resourceJob := func(template bool) atc.Config {
  			return atc.Config{
  				Template: template,
  				Resources: atc.ResourceConfigs{
  					{Name: "check-res", Type: "some-base-resource-type", Source: atc.Source{"a": "b"}},
  				},
  				Jobs: atc.JobConfigs{
  					{Name: "j", PlanSequence: []atc.Step{
  						{Config: &atc.GetStep{Name: "check-res"}},
  					}},
  				},
  			}
  		}

  		template, _, err := defaultTeam.SavePipeline(
  			atc.PipelineRef{Name: "check-template"}, resourceJob(true), db.ConfigVersion(0), false)
  		Expect(err).ToNot(HaveOccurred())

  		ordinary, _, err := defaultTeam.SavePipeline(
  			atc.PipelineRef{Name: "check-ordinary", InstanceVars: atc.InstanceVars{"branch": "main"}},
  			resourceJob(false), db.ConfigVersion(0), false)
  		Expect(err).ToNot(HaveOccurred())
  		Expect(ordinary.Reload()).To(BeTrue())

  		resources, err := checkFactory.Resources()
  		Expect(err).ToNot(HaveOccurred())

  		var pipelineIDs []int
  		for _, r := range resources {
  			pipelineIDs = append(pipelineIDs, r.PipelineID())
  		}
  		Expect(pipelineIDs).To(ContainElement(ordinary.ID()))
  		Expect(pipelineIDs).ToNot(ContainElement(template.ID()))
  	})
  })
  ```
  (Note: saved pipelines start paused=true via `fly`-style saves in some helpers — this suite saves with `initiallyPaused: false` as shown, matching `Resources()`'s `p.paused = false` filter. If the surrounding Describe unpauses pipelines, follow its idiom.)

- [ ] Run and see failure (template resource currently returned):
  ```bash
  ginkgo --focus="with template pipelines" ./atc/db/
  ```

- [ ] Add the filter (atc/db/check_factory.go:182):

  ```go
  		Where(sq.And{
  			sq.Eq{"p.paused": false},
  			// template pipelines (base AND run instances) never get periodic
  			// checks; run instances get one manually-triggered check at
  			// creation (shared-contracts §7.1)
  			sq.Eq{"p.template": false},
  		}).
  ```

- [ ] Run to green:
  ```bash
  ginkgo --focus="CheckFactory" ./atc/db/
  ```

- [ ] Commit:
  ```bash
  git add atc/db/check_factory.go atc/db/check_factory_test.go
  git commit -m "feat(db): disable periodic resource checks for template pipelines" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 13: Reject direct job triggering on base templates

**Files:**
- Modify: `atc/api/jobserver/create_build.go:38` (after the job-found check, before `DisableManualTrigger`)
- Test: `atc/api/jobs_test.go` (append Context inside the existing `POST /api/v1/.../jobs/:job_name/builds` Describe)

**Steps:**

- [ ] Add a failing Context to the `CreateJobBuild` section of `atc/api/jobs_test.go` (the suite provides `fakePipeline` and issues real HTTP requests; mirror the sibling "when manual triggering is disabled" context):

  ```go
  Context("when the pipeline is a base template", func() {
  	BeforeEach(func() {
  		fakePipeline.TemplateReturns(true)
  		fakePipeline.InstanceVarsReturns(nil)
  	})

  	It("returns 409 with a pointer to run-pipeline", func() {
  		Expect(response.StatusCode).To(Equal(http.StatusConflict))
  		body, err := io.ReadAll(response.Body)
  		Expect(err).ToNot(HaveOccurred())
  		Expect(string(body)).To(ContainSubstring("fly run-pipeline"))
  	})
  })
  ```

- [ ] Run and see failure:
  ```bash
  ginkgo --focus="base template" ./atc/api/
  ```

- [ ] Implement in `atc/api/jobserver/create_build.go` (after the `!found` 404 return at line ~38, before the `DisableManualTrigger` check; add `"fmt"` to imports):

  ```go
  		if pipeline.Template() && pipeline.InstanceVars() == nil {
  			w.WriteHeader(http.StatusConflict)
  			fmt.Fprint(w, `cannot trigger jobs on a template pipeline; use "fly run-pipeline" to create a run`)
  			return
  		}
  ```

- [ ] Run to green:
  ```bash
  ginkgo --focus="base template" ./atc/api/
  ```

- [ ] Commit:
  ```bash
  git add atc/api/jobserver/create_build.go atc/api/jobs_test.go
  git commit -m "feat(api): reject direct job triggering on base template pipelines" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 14: `pipeline_run_lifecycler` RunnableComponent + wiring + notify

**Files:**
- Modify: `atc/component.go:26` (constant)
- Create: `atc/runlifecycle/lifecycler.go`, `atc/runlifecycle/runlifecycle_suite_test.go`, `atc/runlifecycle/lifecycler_test.go`
- Modify: `atc/db/build.go:829` (notify on build finish)
- Modify: `atc/atccmd/command.go:1108` (factory), `atc/atccmd/command.go:1257` (component entry)
- Test: `atc/runlifecycle/lifecycler_test.go`

**Steps:**

- [ ] Add the component constant (atc/component.go:26, after `ComponentSigningKeyLifecycler`):

  ```go
  	ComponentPipelineRunLifecycler      = "pipeline_run_lifecycler"
  ```

- [ ] Write the failing test. `atc/runlifecycle/runlifecycle_suite_test.go`:

  ```go
  package runlifecycle_test

  import (
  	"testing"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  )

  func TestRunLifecycle(t *testing.T) {
  	RegisterFailHandler(Fail)
  	RunSpecs(t, "RunLifecycle Suite")
  }
  ```

  `atc/runlifecycle/lifecycler_test.go`:

  ```go
  package runlifecycle_test

  import (
  	"context"
  	"errors"

  	"github.com/concourse/concourse/atc/db"
  	"github.com/concourse/concourse/atc/db/dbfakes"
  	"github.com/concourse/concourse/atc/runlifecycle"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  )

  var _ = Describe("Lifecycler", func() {
  	var (
  		factory    *dbfakes.FakePipelineRunFactory
  		lifecycler *runlifecycle.Lifecycler
  	)

  	BeforeEach(func() {
  		factory = new(dbfakes.FakePipelineRunFactory)
  		lifecycler = runlifecycle.NewLifecycler(factory)
  	})

  	It("finishes complete runs with their aggregate status", func() {
  		complete := new(dbfakes.FakePipelineRun)
  		complete.CheckCompleteReturns(db.PipelineRunFailed, true, nil)
  		incomplete := new(dbfakes.FakePipelineRun)
  		incomplete.CheckCompleteReturns("", false, nil)
  		factory.RunningRunsReturns([]db.PipelineRun{complete, incomplete}, nil)

  		Expect(lifecycler.Run(context.Background())).To(Succeed())

  		Expect(complete.FinishCallCount()).To(Equal(1))
  		Expect(complete.FinishArgsForCall(0)).To(Equal(db.PipelineRunFailed))
  		Expect(incomplete.FinishCallCount()).To(Equal(0))
  	})

  	It("reopens completed runs with new activity", func() {
  		retriggered := new(dbfakes.FakePipelineRun)
  		factory.CompletedRunsWithNewActivityReturns([]db.PipelineRun{retriggered}, nil)

  		Expect(lifecycler.Run(context.Background())).To(Succeed())
  		Expect(retriggered.ReopenCallCount()).To(Equal(1))
  	})

  	It("archives expired runs", func() {
  		expired := new(dbfakes.FakePipelineRun)
  		factory.RunsToArchiveReturns([]db.PipelineRun{expired}, nil)

  		Expect(lifecycler.Run(context.Background())).To(Succeed())
  		Expect(expired.ArchiveCallCount()).To(Equal(1))
  	})

  	It("continues past per-run errors", func() {
  		bad := new(dbfakes.FakePipelineRun)
  		bad.CheckCompleteReturns("", false, errors.New("boom"))
  		good := new(dbfakes.FakePipelineRun)
  		good.CheckCompleteReturns(db.PipelineRunSucceeded, true, nil)
  		factory.RunningRunsReturns([]db.PipelineRun{bad, good}, nil)

  		Expect(lifecycler.Run(context.Background())).To(Succeed())
  		Expect(good.FinishCallCount()).To(Equal(1))
  	})
  })
  ```

- [ ] Run and see compile failure:
  ```bash
  ginkgo ./atc/runlifecycle/
  ```

- [ ] Implement `atc/runlifecycle/lifecycler.go` (pauser recipe: `Run(ctx) error`, context logger):

  ```go
  package runlifecycle

  import (
  	"context"

  	"code.cloudfoundry.org/lager/v3"
  	"code.cloudfoundry.org/lager/v3/lagerctx"
  	"github.com/concourse/concourse/atc/db"
  )

  // Lifecycler is the pipeline_run_lifecycler RunnableComponent: it completes
  // quiescent runs with a worst-of aggregate status, reopens completed runs
  // that gained new builds (retriggers), and archives runs past their
  // template's retention policy via the existing pipeline-archival machinery.
  type Lifecycler struct {
  	runFactory db.PipelineRunFactory
  }

  func NewLifecycler(runFactory db.PipelineRunFactory) *Lifecycler {
  	return &Lifecycler{runFactory: runFactory}
  }

  func (l *Lifecycler) Run(ctx context.Context) error {
  	logger := lagerctx.FromContext(ctx).Session("pipeline-run-lifecycler")

  	running, err := l.runFactory.RunningRuns()
  	if err != nil {
  		logger.Error("failed-to-list-running-runs", err)
  		return err
  	}

  	for _, run := range running {
  		status, complete, err := run.CheckComplete()
  		if err != nil {
  			logger.Error("failed-to-check-run-completion", err, lager.Data{"run-id": run.ID()})
  			continue
  		}
  		if !complete {
  			continue
  		}
  		if err := run.Finish(status); err != nil {
  			logger.Error("failed-to-finish-run", err, lager.Data{"run-id": run.ID()})
  			continue
  		}
  		logger.Info("run-completed", lager.Data{"run-id": run.ID(), "status": string(status)})
  	}

  	reactivated, err := l.runFactory.CompletedRunsWithNewActivity()
  	if err != nil {
  		logger.Error("failed-to-list-reactivated-runs", err)
  		return err
  	}
  	for _, run := range reactivated {
  		if err := run.Reopen(); err != nil {
  			logger.Error("failed-to-reopen-run", err, lager.Data{"run-id": run.ID()})
  			continue
  		}
  		logger.Info("run-reopened", lager.Data{"run-id": run.ID()})
  	}

  	expired, err := l.runFactory.RunsToArchive()
  	if err != nil {
  		logger.Error("failed-to-list-expired-runs", err)
  		return err
  	}
  	for _, run := range expired {
  		if err := run.Archive(); err != nil {
  			logger.Error("failed-to-archive-run", err, lager.Data{"run-id": run.ID()})
  			continue
  		}
  		logger.Info("run-archived", lager.Data{"run-id": run.ID()})
  	}

  	return nil
  }
  ```

- [ ] Run to green:
  ```bash
  ginkgo ./atc/runlifecycle/
  ```

- [ ] Wake the component on build completion — never notify-only, this is IN ADDITION to polling (atc/db/build.go:829, alongside the existing `Notify` calls at the end of `Finish`):

  ```go
  	b.conn.Bus().Notify(atc.ComponentPipelineRunLifecycler)
  ```

- [ ] Wire the component in `atc/atccmd/command.go`. At :1108 (with the other backend factories; `logger` is a `backendComponents` parameter at :1079 and `dbCheckFactory` is constructed just above at :1103 — the CheckFactory injection is F27, 2026-07-09):

  ```go
  	dbPipelineRunFactory := db.NewPipelineRunFactory(logger, dbConn, lockFactory, dbCheckFactory)
  ```
  In the `components` slice (after the `ComponentSigningKeyLifecycler` entry at :1257; the default 10s polling interval applies — polling + notify, never notify-only):

  ```go
  		{
  			Component: atc.Component{
  				Name: atc.ComponentPipelineRunLifecycler,
  			},
  			Runnable: runlifecycle.NewLifecycler(dbPipelineRunFactory),
  		},
  ```
  Add the import `"github.com/concourse/concourse/atc/runlifecycle"`.

- [ ] Verify everything compiles and db suite is green (build.go changed):
  ```bash
  go build ./atc/... && ginkgo --focus="Build" ./atc/db/
  ```

- [ ] Commit:
  ```bash
  git add atc/component.go atc/runlifecycle/ atc/db/build.go atc/atccmd/command.go
  git commit -m "feat(atc): pipeline_run_lifecycler component (completion, reopen, retention)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 15: Routes, auth wrappa, reject-archived wrappa, roles

**Files:**
- Modify: `atc/routes.go:127` (constants) and `atc/routes.go:260` (route entries)
- Modify: `atc/wrappa/api_auth_wrappa.go:176` (authorized case)
- Modify: `atc/wrappa/reject_archived_wrappa.go:26` (RejectArchived case) and `:137` (as-is case)
- Modify: `atc/api/accessor/roles.go:120` (DefaultRoles entries)
- Test: `atc/wrappa/api_auth_wrappa_test.go` ("handles each route" — panics on unhandled routes)

**Steps:**

- [ ] Add route name constants (atc/routes.go, near the pipeline constants at :72):

  ```go
  	CreatePipelineRun = "CreatePipelineRun"
  	ListPipelineRuns  = "ListPipelineRuns"
  	GetPipelineRun    = "GetPipelineRun"
  ```

- [ ] Add route entries (atc/routes.go route table, near the other `pipelines/:pipeline_name` entries at :173) — paths exactly per contracts §4.2:

  ```go
  	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs", Method: "POST", Name: CreatePipelineRun},
  	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs", Method: "GET", Name: ListPipelineRuns},
  	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs/:run_number", Method: "GET", Name: GetPipelineRun},
  ```

- [ ] Run the wrappa suite and watch BOTH exhaustive switches panic ("you missed a spot" / "how do archived pipelines affect your endpoint?"):
  ```bash
  ginkgo ./atc/wrappa/
  ```

- [ ] Add all three to the `authorized` case in `atc/wrappa/api_auth_wrappa.go` (the case block ending at `atc.ListDeprecatedScopes` at :176):

  ```go
  			atc.CreatePipelineRun,
  			atc.ListPipelineRuns,
  			atc.GetPipelineRun,
  ```

- [ ] In `atc/wrappa/reject_archived_wrappa.go`: add `atc.CreatePipelineRun` to the RejectArchived case (:26 block, after `atc.RerunJobBuild`) — no runs on archived templates — and `atc.ListPipelineRuns, atc.GetPipelineRun` to the leave-as-is case (:137 block, before `atc.MCPEndpoint`).

- [ ] Add DefaultRoles entries (atc/api/accessor/roles.go:120, after the agent block) — tiers exactly per contracts §4.2 (`authorized member` / `authorized viewer`):

  ```go
  	atc.CreatePipelineRun: MemberRole,
  	atc.ListPipelineRuns:  ViewerRole,
  	atc.GetPipelineRun:    ViewerRole,
  ```

- [ ] Run to green (the API suite fails until Task 16 wires handlers — that is expected; wrappa and accessor must pass now):
  ```bash
  ginkgo ./atc/wrappa/ ./atc/api/accessor/
  ```

- [ ] Commit:
  ```bash
  git add atc/routes.go atc/wrappa/ atc/api/accessor/roles.go
  git commit -m "feat(api): pipeline-run routes with authorized member/viewer tiers" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 16: runserver handlers, presenter, API wiring

**Files:**
- Create: `atc/pipeline_run.go` (wire types), `atc/api/present/pipeline_run.go`, `atc/api/runserver/server.go`, `atc/api/runserver/runs.go`
- Modify: `atc/api/handler.go:46` (NewHandler signature — add `dbPipelineRunFactory db.PipelineRunFactory` after `dbCheckFactory`), `atc/api/handler.go:109` (construct server), route map near `:198`
- Modify: `atc/api/api_suite_test.go:182` (pass `fakePipelineRunFactory`), `atc/atccmd/command.go:2181` + `:2256` (param + call site), `atc/atccmd/command.go:932` call site (construct + pass factory)
- Test: `atc/api/runs_test.go`

**Steps:**

- [ ] Add wire types in `atc/pipeline_run.go` (shapes per contracts §7.1):

  ```go
  package atc

  type PipelineRun struct {
  	ID          int            `json:"id"`
  	Number      int            `json:"number"`
  	Status      string         `json:"status"`
  	Params      map[string]any `json:"params,omitempty"`
  	CreatedBy   string         `json:"created_by,omitempty"`
  	CreatedAt   int64          `json:"created_at"`
  	CompletedAt int64          `json:"completed_at,omitempty"`
  	Archived    bool           `json:"archived,omitempty"`
  }

  type CreatePipelineRunRequest struct {
  	Params map[string]any `json:"params,omitempty"`
  }
  ```

- [ ] Write the failing API test `atc/api/runs_test.go` (follows `pipelines_test.go` idiom — the suite exposes `client`, `server`, `fakeAccess`, `dbTeamFactory`, `dbTeam`, `fakePipeline`; add `fakePipelineRunFactory` to the suite in the next step):

  ```go
  package api_test

  import (
  	"bytes"
  	"encoding/json"
  	"io"
  	"net/http"

  	"github.com/concourse/concourse/atc"
  	"github.com/concourse/concourse/atc/db"
  	"github.com/concourse/concourse/atc/db/dbfakes"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  )

  var _ = Describe("Pipeline Runs API", func() {
  	var response *http.Response

  	BeforeEach(func() {
  		fakeAccess.IsAuthenticatedReturns(true)
  		fakeAccess.IsAuthorizedReturns(true)
  		dbTeamFactory.FindTeamReturns(dbTeam, true, nil)
  		dbTeam.PipelineReturns(fakePipeline, true, nil)
  		fakePipeline.IDReturns(17)
  		fakePipeline.TemplateReturns(true)
  		fakePipeline.InstanceVarsReturns(nil)
  		fakePipeline.ParamsSchemaReturns([]atc.ParamSchema{
  			{Name: "greeting", Type: "string", Default: "hello"},
  		})
  	})

  	Describe("POST /api/v1/teams/a-team/pipelines/a-pipeline/runs", func() {
  		post := func(body string) *http.Response {
  			req, err := http.NewRequest("POST",
  				server.URL+"/api/v1/teams/a-team/pipelines/a-pipeline/runs",
  				bytes.NewBufferString(body))
  			Expect(err).NotTo(HaveOccurred())
  			resp, err := client.Do(req)
  			Expect(err).NotTo(HaveOccurred())
  			return resp
  		}

  		It("creates a run and returns 201 with the run body", func() {
  			fakeRun := new(dbfakes.FakePipelineRun)
  			fakeRun.IDReturns(3)
  			fakeRun.NumberReturns(42)
  			fakeRun.StatusReturns(db.PipelineRunRunning)
  			fakeRun.ParamsReturns(map[string]any{"greeting": "hi"})
  			fakePipelineRunFactory.CreateRunReturns(fakeRun, nil)

  			response = post(`{"params":{"greeting":"hi"}}`)
  			Expect(response.StatusCode).To(Equal(http.StatusCreated))

  			templateID, params, _ := fakePipelineRunFactory.CreateRunArgsForCall(0)
  			Expect(templateID).To(Equal(17))
  			Expect(params).To(Equal(map[string]any{"greeting": "hi"}))

  			var run atc.PipelineRun
  			Expect(json.NewDecoder(response.Body).Decode(&run)).To(Succeed())
  			Expect(run.Number).To(Equal(42))
  			Expect(run.Status).To(Equal("running"))
  		})

  		It("rejects unknown params with 400", func() {
  			response = post(`{"params":{"bogus":"x"}}`)
  			Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
  			body, _ := io.ReadAll(response.Body)
  			Expect(string(body)).To(ContainSubstring(`unknown param "bogus"`))
  			Expect(fakePipelineRunFactory.CreateRunCallCount()).To(Equal(0))
  		})

  		It("rejects non-template pipelines with 409", func() {
  			fakePipeline.TemplateReturns(false)
  			response = post(`{}`)
  			Expect(response.StatusCode).To(Equal(http.StatusConflict))
  		})
  	})

  	Describe("GET /api/v1/teams/a-team/pipelines/a-pipeline/runs", func() {
  		It("lists runs newest first", func() {
  			fakeRun := new(dbfakes.FakePipelineRun)
  			fakeRun.NumberReturns(2)
  			fakeRun.StatusReturns(db.PipelineRunSucceeded)
  			fakePipelineRunFactory.ListRunsReturns([]db.PipelineRun{fakeRun}, nil)

  			resp, err := client.Get(server.URL + "/api/v1/teams/a-team/pipelines/a-pipeline/runs")
  			Expect(err).NotTo(HaveOccurred())
  			Expect(resp.StatusCode).To(Equal(http.StatusOK))

  			var runs []atc.PipelineRun
  			Expect(json.NewDecoder(resp.Body).Decode(&runs)).To(Succeed())
  			Expect(runs).To(HaveLen(1))
  			Expect(runs[0].Number).To(Equal(2))
  		})
  	})

  	Describe("GET /api/v1/teams/a-team/pipelines/a-pipeline/runs/:run_number", func() {
  		It("returns the run or 404", func() {
  			fakeRun := new(dbfakes.FakePipelineRun)
  			fakeRun.NumberReturns(7)
  			fakePipelineRunFactory.GetRunReturns(fakeRun, true, nil)

  			resp, err := client.Get(server.URL + "/api/v1/teams/a-team/pipelines/a-pipeline/runs/7")
  			Expect(err).NotTo(HaveOccurred())
  			Expect(resp.StatusCode).To(Equal(http.StatusOK))

  			fakePipelineRunFactory.GetRunReturns(nil, false, nil)
  			resp, err = client.Get(server.URL + "/api/v1/teams/a-team/pipelines/a-pipeline/runs/8")
  			Expect(err).NotTo(HaveOccurred())
  			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
  		})
  	})
  })
  ```

- [ ] Run and see compile failure (`fakePipelineRunFactory` undefined):
  ```bash
  ginkgo --focus="Pipeline Runs API" ./atc/api/
  ```

- [ ] Implement the presenter `atc/api/present/pipeline_run.go`:

  ```go
  package present

  import (
  	"github.com/concourse/concourse/atc"
  	"github.com/concourse/concourse/atc/db"
  )

  func PipelineRun(run db.PipelineRun) atc.PipelineRun {
  	presented := atc.PipelineRun{
  		ID:        run.ID(),
  		Number:    run.Number(),
  		Status:    string(run.Status()),
  		Params:    run.Params(),
  		CreatedBy: run.CreatedBy(),
  		CreatedAt: run.CreatedAt().Unix(),
  		Archived:  run.Archived(),
  	}
  	if completedAt, ok := run.CompletedAt(); ok {
  		presented.CompletedAt = completedAt.Unix()
  	}
  	return presented
  }

  func PipelineRuns(runs []db.PipelineRun) []atc.PipelineRun {
  	presented := []atc.PipelineRun{}
  	for _, run := range runs {
  		presented = append(presented, PipelineRun(run))
  	}
  	return presented
  }
  ```

- [ ] Implement `atc/api/runserver/server.go` (no CheckFactory here — the frozen-check enqueue lives in `db.PipelineRunFactory.CreateRun` per F27, 2026-07-09; the handler is a pass-through):

  ```go
  package runserver

  import (
  	"code.cloudfoundry.org/lager/v3"
  	"github.com/concourse/concourse/atc/db"
  )

  type Server struct {
  	logger     lager.Logger
  	runFactory db.PipelineRunFactory
  }

  func NewServer(
  	logger lager.Logger,
  	runFactory db.PipelineRunFactory,
  ) *Server {
  	return &Server{
  		logger:     logger,
  		runFactory: runFactory,
  	}
  }
  ```

- [ ] Implement `atc/api/runserver/runs.go`:

  ```go
  package runserver

  import (
  	"encoding/json"
  	"errors"
  	"fmt"
  	"io"
  	"net/http"
  	"strconv"

  	"code.cloudfoundry.org/lager/v3"

  	"github.com/concourse/concourse/atc"
  	"github.com/concourse/concourse/atc/api/accessor"
  	"github.com/concourse/concourse/atc/api/present"
  	"github.com/concourse/concourse/atc/db"
  )

  func (s *Server) CreateRun(pipeline db.Pipeline) http.Handler {
  	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		logger := s.logger.Session("create-pipeline-run", lager.Data{"pipeline": pipeline.Name()})
  		w.Header().Set("Content-Type", "application/json")

  		if !pipeline.Template() || pipeline.InstanceVars() != nil {
  			w.WriteHeader(http.StatusConflict)
  			fmt.Fprint(w, `only template pipelines can be run; set "template: true"`)
  			return
  		}

  		var req atc.CreatePipelineRunRequest
  		err := json.NewDecoder(r.Body).Decode(&req)
  		if err != nil && !errors.Is(err, io.EOF) {
  			w.WriteHeader(http.StatusBadRequest)
  			fmt.Fprint(w, "malformed request body")
  			return
  		}

  		validated, err := atc.ValidateRunParams(pipeline.ParamsSchema(), req.Params)
  		if err != nil {
  			w.WriteHeader(http.StatusBadRequest)
  			fmt.Fprint(w, err.Error())
  			return
  		}

  		acc := accessor.GetAccessor(r)
  		// pass-through: CreateRun itself triggers entry jobs AND enqueues
  		// the frozen check set (shared-contracts §7.1 items 2/8; F27,
  		// 2026-07-09 — previously the enqueue lived here, which left
  		// factory-created runs without their frozen checks)
  		run, err := s.runFactory.CreateRun(pipeline.ID(), validated, acc.UserInfo().DisplayUserId)
  		if err != nil {
  			logger.Error("failed-to-create-run", err)
  			w.WriteHeader(http.StatusInternalServerError)
  			return
  		}

  		w.WriteHeader(http.StatusCreated)
  		err = json.NewEncoder(w).Encode(present.PipelineRun(run))
  		if err != nil {
  			logger.Error("failed-to-encode-run", err)
  		}
  	})
  }

  func (s *Server) ListRuns(pipeline db.Pipeline) http.Handler {
  	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		logger := s.logger.Session("list-pipeline-runs")
  		w.Header().Set("Content-Type", "application/json")

  		limit := 100
  		if l := r.URL.Query().Get("limit"); l != "" {
  			parsed, err := strconv.Atoi(l)
  			if err != nil {
  				w.WriteHeader(http.StatusBadRequest)
  				return
  			}
  			limit = parsed
  		}

  		runs, err := s.runFactory.ListRuns(pipeline.ID(), limit)
  		if err != nil {
  			logger.Error("failed-to-list-runs", err)
  			w.WriteHeader(http.StatusInternalServerError)
  			return
  		}

  		err = json.NewEncoder(w).Encode(present.PipelineRuns(runs))
  		if err != nil {
  			logger.Error("failed-to-encode-runs", err)
  		}
  	})
  }

  func (s *Server) GetRun(pipeline db.Pipeline) http.Handler {
  	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		logger := s.logger.Session("get-pipeline-run")
  		w.Header().Set("Content-Type", "application/json")

  		number, err := strconv.Atoi(r.FormValue(":run_number"))
  		if err != nil {
  			w.WriteHeader(http.StatusBadRequest)
  			return
  		}

  		run, found, err := s.runFactory.GetRun(pipeline.ID(), number)
  		if err != nil {
  			logger.Error("failed-to-get-run", err)
  			w.WriteHeader(http.StatusInternalServerError)
  			return
  		}
  		if !found {
  			w.WriteHeader(http.StatusNotFound)
  			return
  		}

  		err = json.NewEncoder(w).Encode(present.PipelineRun(run))
  		if err != nil {
  			logger.Error("failed-to-encode-run", err)
  		}
  	})
  }
  ```

- [ ] Wire into `atc/api/handler.go`: add param `dbPipelineRunFactory db.PipelineRunFactory` to `NewHandler` (after `dbCheckFactory` at :46); construct the server next to `pipelineServer` (:109 — no CheckFactory: the factory owns the frozen-check enqueue, F27 2026-07-09):

  ```go
  	runServer := runserver.NewServer(logger, dbPipelineRunFactory)
  ```
  and add route entries next to the pipeline entries (:198):

  ```go
  	atc.CreatePipelineRun: pipelineHandlerFactory.HandlerFor(runServer.CreateRun),
  	atc.ListPipelineRuns:  pipelineHandlerFactory.HandlerFor(runServer.ListRuns),
  	atc.GetPipelineRun:    pipelineHandlerFactory.HandlerFor(runServer.GetRun),
  ```
  Import `"github.com/concourse/concourse/atc/api/runserver"`.

- [ ] Update callers:
  - `atc/api/api_suite_test.go`: declare `fakePipelineRunFactory *dbfakes.FakePipelineRunFactory` with the other suite vars (:49 block), initialize it in the BeforeEach (`fakePipelineRunFactory = new(dbfakes.FakePipelineRunFactory)`) and pass it in the `api.NewHandler(...)` call (:182) after `dbCheckFactory`.
  - `atc/atccmd/command.go`: add `dbPipelineRunFactory db.PipelineRunFactory` parameter to `constructAPIHandler` (:2181, after `dbCheckFactory`), pass it through to `api.NewHandler` (:2256), and at the `constructAPIHandler` call site inside `constructAPIMembers` (:932) construct and pass `db.NewPipelineRunFactory(logger, dbConn, lockFactory, dbCheckFactory)` (`logger` is the `constructAPIMembers` parameter at :846; `dbCheckFactory` is constructed at :902 — F27, 2026-07-09).

- [ ] Run to green:
  ```bash
  go build ./atc/... && ginkgo --focus="Pipeline Runs API" ./atc/api/ && ginkgo ./atc/api/
  ```

- [ ] Commit:
  ```bash
  git add atc/pipeline_run.go atc/api/
  git commit -m "feat(api): pipeline-run create/list/get handlers (pass-through to PipelineRunFactory)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 17: go-concourse client methods

**Files:**
- Create: `go-concourse/concourse/pipeline_runs.go`
- Modify: `go-concourse/concourse/team.go:11` (Team interface)
- Test: `go-concourse/concourse/pipeline_runs_test.go`

**Steps:**

- [ ] Write the failing test `go-concourse/concourse/pipeline_runs_test.go` (ghttp idiom of the sibling `pipelines_test.go` — the suite provides `atcServer` and `team`):

  ```go
  package concourse_test

  import (
  	"net/http"

  	"github.com/concourse/concourse/atc"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  	"github.com/onsi/gomega/ghttp"
  )

  var _ = Describe("Pipeline Runs", func() {
  	Describe("CreatePipelineRun", func() {
  		It("POSTs params and returns the created run", func() {
  			expectedURL := "/api/v1/teams/some-team/pipelines/regression-suite/runs"
  			atcServer.AppendHandlers(
  				ghttp.CombineHandlers(
  					ghttp.VerifyRequest("POST", expectedURL),
  					ghttp.VerifyJSON(`{"params":{"ref":"abc"}}`),
  					ghttp.RespondWithJSONEncoded(http.StatusCreated,
  						atc.PipelineRun{ID: 1, Number: 42, Status: "running"}),
  				),
  			)

  			run, err := team.CreatePipelineRun(
  				atc.PipelineRef{Name: "regression-suite"},
  				map[string]any{"ref": "abc"},
  			)
  			Expect(err).NotTo(HaveOccurred())
  			Expect(run.Number).To(Equal(42))
  		})
  	})

  	Describe("ListPipelineRuns", func() {
  		It("GETs the runs list", func() {
  			expectedURL := "/api/v1/teams/some-team/pipelines/regression-suite/runs"
  			atcServer.AppendHandlers(
  				ghttp.CombineHandlers(
  					ghttp.VerifyRequest("GET", expectedURL),
  					ghttp.RespondWithJSONEncoded(http.StatusOK,
  						[]atc.PipelineRun{{Number: 2, Status: "succeeded"}, {Number: 1, Status: "failed"}}),
  				),
  			)

  			runs, err := team.ListPipelineRuns(atc.PipelineRef{Name: "regression-suite"})
  			Expect(err).NotTo(HaveOccurred())
  			Expect(runs).To(HaveLen(2))
  			Expect(runs[0].Number).To(Equal(2))
  		})
  	})

  	Describe("GetPipelineRun", func() {
  		It("GETs a single run and reports absence", func() {
  			expectedURL := "/api/v1/teams/some-team/pipelines/regression-suite/runs/7"
  			atcServer.AppendHandlers(
  				ghttp.CombineHandlers(
  					ghttp.VerifyRequest("GET", expectedURL),
  					ghttp.RespondWithJSONEncoded(http.StatusOK, atc.PipelineRun{Number: 7}),
  				),
  			)

  			run, found, err := team.GetPipelineRun(atc.PipelineRef{Name: "regression-suite"}, 7)
  			Expect(err).NotTo(HaveOccurred())
  			Expect(found).To(BeTrue())
  			Expect(run.Number).To(Equal(7))

  			atcServer.AppendHandlers(
  				ghttp.RespondWith(http.StatusNotFound, ""),
  			)
  			_, found, err = team.GetPipelineRun(atc.PipelineRef{Name: "regression-suite"}, 8)
  			Expect(err).NotTo(HaveOccurred())
  			Expect(found).To(BeFalse())
  		})
  	})
  })
  ```

- [ ] Run and see compile failure:
  ```bash
  ginkgo --focus="Pipeline Runs" ./go-concourse/concourse/
  ```

- [ ] Add methods to the `Team` interface (go-concourse/concourse/team.go:11, near the pipeline methods):

  ```go
  	CreatePipelineRun(pipelineRef atc.PipelineRef, params map[string]any) (atc.PipelineRun, error)
  	ListPipelineRuns(pipelineRef atc.PipelineRef) ([]atc.PipelineRun, error)
  	GetPipelineRun(pipelineRef atc.PipelineRef, number int) (atc.PipelineRun, bool, error)
  ```

- [ ] Implement `go-concourse/concourse/pipeline_runs.go`:

  ```go
  package concourse

  import (
  	"bytes"
  	"encoding/json"
  	"net/http"
  	"strconv"

  	"github.com/concourse/concourse/atc"
  	"github.com/concourse/concourse/go-concourse/concourse/internal"
  	"github.com/tedsuo/rata"
  )

  func (team *team) CreatePipelineRun(pipelineRef atc.PipelineRef, params map[string]any) (atc.PipelineRun, error) {
  	payload, err := json.Marshal(atc.CreatePipelineRunRequest{Params: params})
  	if err != nil {
  		return atc.PipelineRun{}, err
  	}

  	var run atc.PipelineRun
  	err = team.connection.Send(internal.Request{
  		RequestName: atc.CreatePipelineRun,
  		Params: rata.Params{
  			"team_name":     team.Name(),
  			"pipeline_name": pipelineRef.Name,
  		},
  		Body:   bytes.NewBuffer(payload),
  		Header: http.Header{"Content-Type": []string{"application/json"}},
  	}, &internal.Response{
  		Result: &run,
  	})
  	return run, err
  }

  func (team *team) ListPipelineRuns(pipelineRef atc.PipelineRef) ([]atc.PipelineRun, error) {
  	var runs []atc.PipelineRun
  	err := team.connection.Send(internal.Request{
  		RequestName: atc.ListPipelineRuns,
  		Params: rata.Params{
  			"team_name":     team.Name(),
  			"pipeline_name": pipelineRef.Name,
  		},
  	}, &internal.Response{
  		Result: &runs,
  	})
  	return runs, err
  }

  func (team *team) GetPipelineRun(pipelineRef atc.PipelineRef, number int) (atc.PipelineRun, bool, error) {
  	var run atc.PipelineRun
  	err := team.connection.Send(internal.Request{
  		RequestName: atc.GetPipelineRun,
  		Params: rata.Params{
  			"team_name":     team.Name(),
  			"pipeline_name": pipelineRef.Name,
  			"run_number":    strconv.Itoa(number),
  		},
  	}, &internal.Response{
  		Result: &run,
  	})

  	switch err.(type) {
  	case nil:
  		return run, true, nil
  	case internal.ResourceNotFoundError:
  		return atc.PipelineRun{}, false, nil
  	default:
  		return atc.PipelineRun{}, false, err
  	}
  }
  ```

- [ ] Regenerate the `FakeTeam` counterfeiter fake and run to green:
  ```bash
  go generate ./go-concourse/... && ginkgo ./go-concourse/concourse/
  ```

- [ ] Commit:
  ```bash
  git add go-concourse/
  git commit -m "feat(go-concourse): CreatePipelineRun/ListPipelineRuns/GetPipelineRun client methods" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 18: `fly run-pipeline`

**Files:**
- Create: `fly/commands/run_pipeline.go`
- Modify: `fly/commands/fly.go:55` (command registration, near ArchivePipeline)
- Test: `fly/integration/run_pipeline_test.go`

**Steps:**

- [ ] Write the failing integration test `fly/integration/run_pipeline_test.go` (mock-ATC ghttp idiom of `archive_pipeline_test.go`; suite globals `flyPath`, `targetName`, `atcServer`):

  ```go
  package integration_test

  import (
  	"net/http"
  	"os/exec"

  	"github.com/concourse/concourse/atc"
  	"github.com/tedsuo/rata"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  	"github.com/onsi/gomega/gbytes"
  	"github.com/onsi/gomega/gexec"
  	"github.com/onsi/gomega/ghttp"
  )

  var _ = Describe("run-pipeline", func() {
  	It("POSTs params and prints the created run", func() {
  		path, err := atc.Routes.CreatePathForRoute(atc.CreatePipelineRun,
  			rata.Params{"team_name": "main", "pipeline_name": "regression-suite"})
  		Expect(err).NotTo(HaveOccurred())

  		atcServer.AppendHandlers(
  			ghttp.CombineHandlers(
  				ghttp.VerifyRequest("POST", path),
  				ghttp.VerifyJSON(`{"params":{"ref":"abc","procs":2}}`),
  				ghttp.RespondWithJSONEncoded(http.StatusCreated,
  					atc.PipelineRun{ID: 9, Number: 42, Status: "running"}),
  			),
  		)

  		flyCmd := exec.Command(flyPath, "-t", targetName, "run-pipeline",
  			"-p", "regression-suite", "-v", "ref=abc", "-y", "procs=2")
  		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
  		Expect(err).NotTo(HaveOccurred())
  		<-sess.Exited
  		Expect(sess.ExitCode()).To(Equal(0))
  		Expect(sess.Out).To(gbytes.Say(`created run regression-suite#42`))
  	})

  	It("fails politely when the server rejects params", func() {
  		path, err := atc.Routes.CreatePathForRoute(atc.CreatePipelineRun,
  			rata.Params{"team_name": "main", "pipeline_name": "regression-suite"})
  		Expect(err).NotTo(HaveOccurred())

  		atcServer.AppendHandlers(
  			ghttp.CombineHandlers(
  				ghttp.VerifyRequest("POST", path),
  				ghttp.RespondWith(http.StatusBadRequest, `unknown param "bogus"`),
  			),
  		)

  		flyCmd := exec.Command(flyPath, "-t", targetName, "run-pipeline",
  			"-p", "regression-suite", "-v", "bogus=x")
  		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
  		Expect(err).NotTo(HaveOccurred())
  		<-sess.Exited
  		Expect(sess.ExitCode()).NotTo(Equal(0))
  	})
  })
  ```

- [ ] Run and see failure (unknown command):
  ```bash
  make test-fly-integration
  ```
  (or targeted: `ginkgo --focus="run-pipeline" ./fly/integration/`)

- [ ] Implement `fly/commands/run_pipeline.go`:

  ```go
  package commands

  import (
  	"fmt"

  	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
  	"github.com/concourse/concourse/fly/rc"
  )

  type RunPipelineCommand struct {
  	Pipeline flaghelpers.PipelineFlag           `short:"p" long:"pipeline" required:"true" description:"Template pipeline to run"`
  	Var      []flaghelpers.VariablePairFlag     `short:"v" long:"var"      unquote:"false" value-name:"[NAME=STRING]" description:"Param value (string)"`
  	YAMLVar  []flaghelpers.YAMLVariablePairFlag `short:"y" long:"yaml-var" unquote:"false" value-name:"[NAME=YAML]"   description:"Param value (typed YAML)"`
  	Team     flaghelpers.TeamFlag               `long:"team" description:"Name of the team, if different from the target default"`
  }

  func (command *RunPipelineCommand) Validate() error {
  	_, err := command.Pipeline.Validate()
  	return err
  }

  func (command *RunPipelineCommand) Execute(args []string) error {
  	err := command.Validate()
  	if err != nil {
  		return err
  	}

  	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
  	if err != nil {
  		return err
  	}

  	err = target.Validate()
  	if err != nil {
  		return err
  	}

  	team, err := command.Team.LoadTeam(target)
  	if err != nil {
  		return err
  	}

  	params := map[string]any{}
  	for _, pair := range command.Var {
  		params[pair.Ref.Path] = pair.Value
  	}
  	for _, pair := range command.YAMLVar {
  		params[pair.Ref.Path] = pair.Value
  	}

  	ref := command.Pipeline.Ref()
  	run, err := team.CreatePipelineRun(ref, params)
  	if err != nil {
  		return err
  	}

  	fmt.Printf("created run %s#%d\n", ref.Name, run.Number)
  	fmt.Printf("watch it at %s/teams/%s/pipelines/%s?vars.run=%d\n",
  		target.URL(), team.Name(), ref.Name, run.Number)

  	return nil
  }
  ```

- [ ] Register in `fly/commands/fly.go` (alphabetical block near `ArchivePipeline` at :55). NOTE: alias `rp` is already taken by `rename-pipeline` (fly.go:59), so use `runp`:

  ```go
  	RunPipeline RunPipelineCommand `command:"run-pipeline" alias:"runp" description:"Create a one-shot run of a template pipeline"`
  ```

- [ ] Run to green:
  ```bash
  make test-fly-integration
  ```

- [ ] Commit:
  ```bash
  git add fly/commands/run_pipeline.go fly/commands/fly.go fly/integration/run_pipeline_test.go
  git commit -m "feat(fly): run-pipeline command" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 19: `fly runs`

**Files:**
- Create: `fly/commands/runs.go`
- Modify: `fly/commands/fly.go:55` (registration)
- Test: `fly/integration/runs_test.go`

**Steps:**

- [ ] Write the failing test `fly/integration/runs_test.go`:

  ```go
  package integration_test

  import (
  	"net/http"
  	"os/exec"

  	"github.com/concourse/concourse/atc"
  	"github.com/tedsuo/rata"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  	"github.com/onsi/gomega/gbytes"
  	"github.com/onsi/gomega/gexec"
  	"github.com/onsi/gomega/ghttp"
  )

  var _ = Describe("runs", func() {
  	It("lists runs with number, status, params and duration", func() {
  		path, err := atc.Routes.CreatePathForRoute(atc.ListPipelineRuns,
  			rata.Params{"team_name": "main", "pipeline_name": "regression-suite"})
  		Expect(err).NotTo(HaveOccurred())

  		atcServer.AppendHandlers(
  			ghttp.CombineHandlers(
  				ghttp.VerifyRequest("GET", path),
  				ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.PipelineRun{
  					{Number: 2, Status: "running", Params: map[string]any{"ref": "def"}, CreatedAt: 1751500000},
  					{Number: 1, Status: "succeeded", Params: map[string]any{"ref": "abc"}, CreatedAt: 1751400000, CompletedAt: 1751400300},
  				}),
  			),
  		)

  		flyCmd := exec.Command(flyPath, "-t", targetName, "runs", "-p", "regression-suite")
  		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
  		Expect(err).NotTo(HaveOccurred())
  		<-sess.Exited
  		Expect(sess.ExitCode()).To(Equal(0))
  		Expect(sess.Out).To(gbytes.Say(`2\s+running\s+ref=def`))
  		Expect(sess.Out).To(gbytes.Say(`1\s+succeeded\s+ref=abc\s+5m0s`))
  	})
  })
  ```

- [ ] Run and see failure:
  ```bash
  make test-fly-integration
  ```

- [ ] Implement `fly/commands/runs.go`:

  ```go
  package commands

  import (
  	"fmt"
  	"os"
  	"sort"
  	"strings"
  	"time"

  	"github.com/concourse/concourse/atc"
  	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
  	"github.com/concourse/concourse/fly/rc"
  	"github.com/concourse/concourse/fly/ui"
  	"github.com/fatih/color"
  )

  type RunsCommand struct {
  	Pipeline flaghelpers.PipelineFlag `short:"p" long:"pipeline" required:"true" description:"Template pipeline whose runs to list"`
  	Team     flaghelpers.TeamFlag     `long:"team" description:"Name of the team, if different from the target default"`
  }

  func (command *RunsCommand) Execute(args []string) error {
  	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
  	if err != nil {
  		return err
  	}

  	err = target.Validate()
  	if err != nil {
  		return err
  	}

  	team, err := command.Team.LoadTeam(target)
  	if err != nil {
  		return err
  	}

  	runs, err := team.ListPipelineRuns(command.Pipeline.Ref())
  	if err != nil {
  		return err
  	}

  	table := ui.Table{Headers: ui.TableRow{
  		{Contents: "number", Color: color.New(color.Bold)},
  		{Contents: "status", Color: color.New(color.Bold)},
  		{Contents: "params", Color: color.New(color.Bold)},
  		{Contents: "duration", Color: color.New(color.Bold)},
  	}}

  	for _, run := range runs {
  		table.Data = append(table.Data, ui.TableRow{
  			{Contents: fmt.Sprintf("%d", run.Number)},
  			{Contents: run.Status},
  			{Contents: formatRunParams(run.Params)},
  			{Contents: formatRunDuration(run)},
  		})
  	}

  	return table.Render(os.Stdout, Fly.PrintTableHeaders)
  }

  func formatRunParams(params map[string]any) string {
  	if len(params) == 0 {
  		return "n/a"
  	}
  	keys := make([]string, 0, len(params))
  	for k := range params {
  		keys = append(keys, k)
  	}
  	sort.Strings(keys)
  	pairs := make([]string, 0, len(keys))
  	for _, k := range keys {
  		pairs = append(pairs, fmt.Sprintf("%s=%v", k, params[k]))
  	}
  	return strings.Join(pairs, ",")
  }

  func formatRunDuration(run atc.PipelineRun) string {
  	if run.CompletedAt == 0 {
  		return "n/a"
  	}
  	return time.Duration((run.CompletedAt-run.CreatedAt)*int64(time.Second)).String()
  }
  ```

- [ ] Register in `fly/commands/fly.go`:

  ```go
  	Runs RunsCommand `command:"runs" description:"List runs of a template pipeline"`
  ```

- [ ] Run to green:
  ```bash
  make test-fly-integration
  ```

- [ ] Commit:
  ```bash
  git add fly/commands/runs.go fly/commands/fly.go fly/integration/runs_test.go
  git commit -m "feat(fly): runs command listing template pipeline runs" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 20: Elm data layer — template flag + PipelineRun type + fetch plumbing

**Files:**
- Modify: `atc/pipeline.go` (wire `Template` field), `atc/api/present/pipeline.go:9`
- Modify: `web/elm/src/Concourse.elm:1147` (Pipeline alias), `:1192` (decodePipeline), append PipelineRun type + decoder
- Modify: `web/elm/src/Api/Endpoints.elm:42` (PipelineEndpoint) and the `pipelineEndpoint` case at `:210`
- Modify: `web/elm/src/Message/Effects.elm:128` (Effect type) and runEffect case at `:240`
- Modify: `web/elm/src/Message/Callback.elm:30`
- Modify: `web/elm/tests/Data.elm:180` (pipeline helper) and any other `Concourse.Pipeline` record literals the compiler flags
- Test: `web/elm/tests/ApiEndpointsTests.elm` (endpoint path), compile of the full Elm app

**Steps:**

- [ ] Expose `template` in the pipeline API JSON. In `atc/pipeline.go` add to the `Pipeline` struct:

  ```go
  	Template      bool           `json:"template,omitempty"`
  ```
  In `atc/api/present/pipeline.go:9` add:
  ```go
  		Template:      savedPipeline.Template(),
  ```
  Verify: `go build ./atc/... && ginkgo ./atc/api/`

- [ ] Add a failing endpoint test in `web/elm/tests/ApiEndpointsTests.elm` (mirror the existing `PipelineJobsList` case in that file):

  ```elm
  , test "PipelineRunsList" <|
      \_ ->
          PipelineRunsList
              |> Pipeline pipelineId
              |> Endpoints.toString []
              |> Expect.equal "/api/v1/teams/team/pipelines/pipeline/runs"
  ```

- [ ] Run and see failure:
  ```bash
  cd web/elm && elm-test tests/ApiEndpointsTests.elm
  ```

- [ ] Implement the data layer:

  `web/elm/src/Api/Endpoints.elm` — add to `PipelineEndpoint` (:42):
  ```elm
      | PipelineRunsList
  ```
  and to `pipelineEndpoint` (:210):
  ```elm
          PipelineRunsList ->
              [ "runs" ]
  ```

  `web/elm/src/Concourse.elm` — add `template : Bool` as the LAST field of the `Pipeline` alias (:1147):
  ```elm
      , backgroundImage : Maybe String
      , backgroundFilter : Maybe String
      , template : Bool
      }
  ```
  and the matching final decoder line in `decodePipeline` (:1192):
  ```elm
          |> andMap (defaultTo False <| Json.Decode.field "template" Json.Decode.bool)
  ```
  Append the run type + decoder (near `decodePipeline`), and export `PipelineRun` and `decodePipelineRun` from the module's `exposing` list:
  ```elm
  type alias PipelineRun =
      { id : Int
      , number : Int
      , status : String
      , params : Dict String JsonValue
      , createdAt : Time.Posix
      , completedAt : Maybe Time.Posix
      }


  decodePipelineRun : Json.Decode.Decoder PipelineRun
  decodePipelineRun =
      Json.Decode.succeed PipelineRun
          |> andMap (Json.Decode.field "id" Json.Decode.int)
          |> andMap (Json.Decode.field "number" Json.Decode.int)
          |> andMap (Json.Decode.field "status" Json.Decode.string)
          |> andMap (defaultTo Dict.empty <| Json.Decode.field "params" (Json.Decode.dict decodeJsonValue))
          |> andMap (Json.Decode.field "created_at" (Json.Decode.map dateFromSeconds Json.Decode.int))
          |> andMap (Json.Decode.maybe (Json.Decode.field "completed_at" (Json.Decode.map dateFromSeconds Json.Decode.int)))
  ```

  `web/elm/src/Message/Effects.elm` — add to the `Effect` type (:128):
  ```elm
      | FetchPipelineRuns Concourse.PipelineIdentifier
  ```
  and to `runEffect` (mirroring the `FetchJobs` case at :240):
  ```elm
          FetchPipelineRuns id ->
              Api.get
                  (Endpoints.PipelineRunsList |> Endpoints.Pipeline id)
                  |> Api.expectJson (Json.Decode.list Concourse.decodePipelineRun)
                  |> Api.request
                  |> Task.attempt PipelineRunsFetched
  ```

  `web/elm/src/Message/Callback.elm` — add (:30, next to `JobsFetched`):
  ```elm
      | PipelineRunsFetched (Fetched (List Concourse.PipelineRun))
  ```

  `web/elm/tests/Data.elm` — add `, template = False` to the `pipeline` helper record (:180). Then find every other `Concourse.Pipeline` record literal and add the field:
  ```bash
  cd web/elm && elm make src/Main.elm --output=/dev/null; elm-test 2>&1 | head -50
  ```
  Fix each compile error the same way (`template = False`) until both compile.

- [ ] Run to green:
  ```bash
  cd web/elm && elm-test tests/ApiEndpointsTests.elm && elm-test
  ```

- [ ] Commit:
  ```bash
  git add atc/pipeline.go atc/api/present/pipeline.go web/elm/
  git commit -m "feat(web): pipeline template flag and PipelineRun data layer in Elm" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 21: Elm template-page runs list + bundle rebuild

**Files:**
- Modify: `web/elm/src/Pipeline/Pipeline.elm:66` (Model), `:91` (init), `:176` (PipelineFetched handler), handleCallback, `:578` (`viewSubPage`)
- Test: `web/elm/tests/PipelineTests.elm`
- Modify: `web/public/*` (rebuilt bundle, committed — the build-image pipeline embeds committed assets via go:embed and never runs the frontend build)

**Steps:**

- [ ] Add a failing view test to `web/elm/tests/PipelineTests.elm` (uses the file's existing `Common.init` + `Application.handleCallback` idiom and `Data.pipeline`):

  ```elm
  , test "template pipeline shows its runs list" <|
      \_ ->
          Common.init "/teams/team/pipelines/pipeline"
              |> Application.handleCallback
                  (Callback.PipelineFetched
                      (Ok
                          (Data.pipeline "team" 1
                              |> Data.withName "pipeline"
                              |> (\p -> { p | template = True })
                          )
                      )
                  )
              |> Tuple.first
              |> Application.handleCallback
                  (Callback.PipelineRunsFetched
                      (Ok
                          [ { id = 1
                            , number = 42
                            , status = "succeeded"
                            , params = Dict.empty
                            , createdAt = Time.millisToPosix 0
                            , completedAt = Just (Time.millisToPosix 300000)
                            }
                          ]
                      )
                  )
              |> Tuple.first
              |> Common.queryView
              |> Query.find [ class "pipeline-runs" ]
              |> Query.has [ text "#42", text "succeeded" ]
  , test "non-template pipeline shows no runs list" <|
      \_ ->
          Common.init "/teams/team/pipelines/pipeline"
              |> Application.handleCallback
                  (Callback.PipelineFetched (Ok (Data.pipeline "team" 1 |> Data.withName "pipeline")))
              |> Tuple.first
              |> Common.queryView
              |> Query.hasNot [ class "pipeline-runs" ]
  ```
  (Add `import Dict` if the file lacks it; `Data.withName` exists in Data.elm — if not, set the name via record update.)

- [ ] Run and see failure:
  ```bash
  cd web/elm && elm-test tests/PipelineTests.elm
  ```

- [ ] Implement in `web/elm/src/Pipeline/Pipeline.elm`:

  Model (:66) — add:
  ```elm
          , runs : Maybe (List Concourse.PipelineRun)
  ```
  init (:91 region) — add `runs = Nothing` to the initial model record.

  `PipelineFetched (Ok pipeline)` handler (:176) — fetch runs for templates alongside the existing job/resource fetches:
  ```elm
              , effects
                  ++ [ FetchJobs locator
                     , FetchResources locator
                     ]
                  ++ (if pipeline.template then
                          [ FetchPipelineRuns locator ]

                      else
                          []
                     )
  ```
  (The page refetches the pipeline on its existing clock subscription, so runs refresh on the same cadence.)

  handleCallback — add cases next to `JobsFetched`:
  ```elm
          PipelineRunsFetched (Ok runs) ->
              ( { model | runs = Just runs }, effects )

          PipelineRunsFetched (Err _) ->
              ( model, effects )
  ```

  `viewSubPage` (:578) — insert `viewRuns model` between `viewGroupsBar session model` and the `pipeline-content` div:
  ```elm
          [ viewGroupsBar session model
          , viewRuns model
          , Html.div
              [ class "pipeline-content" ]
  ```
  and add the view helpers at the bottom of the file:
  ```elm
  viewRuns : Model -> Html Message
  viewRuns model =
      case ( model.pipeline, model.runs ) of
          ( RemoteData.Success pipeline, Just runs ) ->
              if pipeline.template then
                  Html.div
                      [ class "pipeline-runs"
                      , style "padding" "10px"
                      , style "background" Colors.frame
                      , style "color" Colors.text
                      , style "font-size" "14px"
                      ]
                      (Html.div [ style "font-weight" "700", style "margin-bottom" "5px" ]
                          [ Html.text "runs" ]
                          :: List.map viewRun runs
                      )

              else
                  Html.text ""

          _ ->
              Html.text ""


  viewRun : Concourse.PipelineRun -> Html Message
  viewRun run =
      Html.div
          [ class "pipeline-run-row"
          , style "display" "flex"
          , style "gap" "12px"
          , style "padding" "2px 0"
          ]
          [ Html.span [ style "min-width" "50px" ] [ Html.text ("#" ++ String.fromInt run.number) ]
          , Html.span [ class ("run-status-" ++ run.status), style "min-width" "80px" ]
              [ Html.text run.status ]
          , Html.span [ style "flex-grow" "1" ] [ Html.text (runParamsSummary run.params) ]
          , Html.span [] [ Html.text (runDurationSummary run) ]
          ]


  runParamsSummary : Dict String Concourse.JsonValue -> String
  runParamsSummary params =
      params
          |> Dict.toList
          |> List.map (\( k, v ) -> k ++ "=" ++ jsonValueToString v)
          |> String.join ", "


  jsonValueToString : Concourse.JsonValue -> String
  jsonValueToString v =
      Json.Encode.encode 0 (Concourse.encodeJsonValue v)
          |> String.replace "\"" ""


  runDurationSummary : Concourse.PipelineRun -> String
  runDurationSummary run =
      case run.completedAt of
          Just completedAt ->
              let
                  seconds =
                      (Time.posixToMillis completedAt - Time.posixToMillis run.createdAt) // 1000
              in
              String.fromInt (seconds // 60) ++ "m" ++ String.fromInt (modBy 60 seconds) ++ "s"

          Nothing ->
              "running"
  ```
  Add the imports the file is missing (`Dict`, `Json.Encode`, `Time`; `Colors` and `RemoteData` are already imported). If `Concourse.encodeJsonValue` is not exposed, expose it in `Concourse.elm`'s `exposing` list (the function exists — `encodeInstanceVars` uses it).

- [ ] Run to green:
  ```bash
  cd web/elm && elm-test tests/PipelineTests.elm && elm-test
  ```

- [ ] Rebuild and commit the frontend bundle (go:embed serves committed assets — see commit 6f16d19ab5 precedent):
  ```bash
  yarn run build
  ```

- [ ] Commit:
  ```bash
  git add web/elm/ web/public/
  git commit -m "feat(web): runs list on template pipeline page" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

### Task 22: Topgun behavioral spec — non-template pipelines untouched + template run flow

The charter's non-negotiable regression proof. Runs in `topgun/k8s_behavioral/` (K3s testcontainers; CANNOT run on this machine's Colima — verify compile locally, execute via the `k8s-e2e` CI pipeline or a Docker-capable host).

**Files:**
- Create: `topgun/k8s_behavioral/pipeline_runs_test.go`
- Test: same file (compile locally; execute in CI)

**Steps:**

- [ ] Write `topgun/k8s_behavioral/pipeline_runs_test.go` using the suite's existing helpers (`writePipelineFile`, `setPipeline`, `fly.Run`, `fly.GetVersions`, `pipelineName` — exactly the idioms in `pipeline_lifecycle_test.go`):

  ```go
  package behavioral_test

  import (
  	"time"

  	. "github.com/onsi/ginkgo/v2"
  	. "github.com/onsi/gomega"
  	"github.com/onsi/gomega/gbytes"
  )

  var _ = Describe("Pipeline Runs", func() {
  	// The regression proof: a plain reactive pipeline behaves exactly as
  	// before the template-pipeline changes (checks run, trigger: true fires).
  	It("leaves non-template pipelines' reactive semantics untouched", func() {
  		pipelineFile := writePipelineFile("runs-regression.yml", `
  resources:
  - name: reactive-res
    type: mock
    source:
      create_files:
        data.txt: "reactive"

  jobs:
  - name: reactive-job
    plan:
    - get: reactive-res
      trigger: true
    - task: noop
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["still reactive"]
  `)
  		setPipeline(pipelineFile)
  		fly.Run("unpause-pipeline", "-p", pipelineName)

  		By("verifying resource checking still happens")
  		Eventually(func() int {
  			return len(fly.GetVersions(pipelineName, "reactive-res"))
  		}, 3*time.Minute, time.Second).Should(BeNumerically(">", 0),
  			"non-template pipelines must keep periodic resource checks")

  		By("verifying trigger: true still auto-triggers a build")
  		Eventually(func() *gbytes.Buffer {
  			sess := fly.Start("builds", "-p", pipelineName)
  			<-sess.Exited
  			return sess.Out
  		}, 3*time.Minute, 2*time.Second).Should(gbytes.Say("reactive-job"),
  			"non-template pipelines must keep trigger: true semantics")
  	})

  	It("runs template pipelines once via run-pipeline, flowing passed: chains", func() {
  		pipelineFile := writePipelineFile("runs-template.yml", `
  template: true
  params:
  - name: greeting
    type: string
    default: hello

  resources:
  - name: seed
    type: mock
    source:
      create_files:
        data.txt: "seed"

  jobs:
  - name: entry
    plan:
    - get: seed
      trigger: true
    - task: say
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["((greeting)) from run ((run))"]
  - name: chained
    plan:
    - get: seed
      passed: [entry]
      trigger: true
    - task: done
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["chain flowed"]
  `)
  		setPipeline(pipelineFile)
  		fly.Run("unpause-pipeline", "-p", pipelineName)

  		By("rejecting direct job triggering on the template")
  		sess := fly.Start("trigger-job", "-j", pipelineName+"/entry")
  		<-sess.Exited
  		Expect(sess.ExitCode()).NotTo(Equal(0))

  		By("creating a run")
  		sess = fly.Start("run-pipeline", "-p", pipelineName, "-v", "greeting=hi")
  		<-sess.Exited
  		Expect(sess.ExitCode()).To(Equal(0))
  		Expect(sess.Out).To(gbytes.Say("#1"))

  		By("the downstream job flows through the passed: chain")
  		Eventually(func() *gbytes.Buffer {
  			sess := fly.Start("builds", "-p", pipelineName+"/run:1")
  			<-sess.Exited
  			return sess.Out
  		}, 5*time.Minute, 3*time.Second).Should(gbytes.Say("chained"),
  			"downstream jobs with passed: must auto-trigger inside a run")

  		By("waiting for the run to complete successfully")
  		Eventually(func() *gbytes.Buffer {
  			sess := fly.Start("runs", "-p", pipelineName)
  			<-sess.Exited
  			return sess.Out
  		}, 5*time.Minute, 3*time.Second).Should(gbytes.Say(`1\s+succeeded`),
  			"both jobs should run and the lifecycler should complete the run")
  	})
  })
  ```
  (All helpers verified in the suite: `writePipelineFile`, `setPipeline`, `fly.Run`, `fly.Start` — returns a gexec session, see pipeline_lifecycle_test.go:240 — `fly.GetVersions`, `fly.GetPipelines`.)

- [ ] Verify it compiles locally:
  ```bash
  go vet ./topgun/k8s_behavioral/
  ```

- [ ] Execute on CI (concourse.home `k8s-e2e` pipeline, `k8s-behavioral-tests` job) or a Docker-capable host:
  ```bash
  cd topgun/k8s_behavioral && ginkgo --procs=2 -v --timeout=1h --focus="Pipeline Runs" .
  ```

- [ ] Commit:
  ```bash
  git add topgun/k8s_behavioral/pipeline_runs_test.go
  git commit -m "test(topgun): behavioral proof that template pipelines don't regress reactive ones" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
  ```

---

## Execution notes

**Full workstream test suite** (PostgreSQL must be running — `pg_isready`):

```bash
ginkgo ./atc/ ./atc/configvalidate/ ./atc/wrappa/ ./atc/api/accessor/ ./atc/runlifecycle/
ginkgo ./atc/db/migration/
ginkgo ./atc/db/          # ~1007+ specs, ~90s; template-DB conflict note in CLAUDE.md
ginkgo ./atc/api/
ginkgo ./go-concourse/concourse/
make test-fly-integration # ~30s, mock ATC (mock version 0.1.0 — unchanged here)
cd web/elm && elm-test
make test-unit            # final sweep: all 79 suites, ~3 min
```

Never use `--race` (parallel compilation failures per CLAUDE.md). Do not reduce `worker_cache_test.go` timeouts.

**Behavioral tier:** `topgun/k8s_behavioral` cannot run on this machine (K3s-in-Docker-in-Colima namespace errors). Compile-check with `go vet ./topgun/k8s_behavioral/`, then run via the `k8s-e2e` pipeline on concourse.home (job `k8s-behavioral-tests`) or any Docker-capable host with `ginkgo --procs=2 --focus="Pipeline Runs" ./topgun/k8s_behavioral/`. Expect ~3/117 unrelated GC-timing flakes suite-wide.

**Live verification on theborg (optional but recommended before calling the workstream done):** deploy the built image to a throwaway namespace (NOT `cicd`/`concourse`), `fly login` per the theborg pattern in memory, set a small `template: true` pipeline, and verify `fly run-pipeline` → entry job → `fly runs` shows `succeeded`, while an existing reactive pipeline keeps checking. The riskiest production surface is the two query changes (`JobsToSchedule`, `CheckFactory.Resources`) — watch a live cicd pipeline trigger normally after deploy.

**Rollback notes for the risky diffs:**
- `atc/db/job_factory.go` scheduler filter and `atc/db/check_factory.go` check filter are each a single `Where` clause — revert by deleting the clause; behavior returns to pre-workstream exactly (both are provably no-ops for `template = false` rows, which is every existing row since the column defaults to false).
- `atc/db/team.go` savePipeline/scanPipeline and `atc/db/pipeline.go` pipelinesQuery changes are additive columns; migration `1773106030` has a clean down.
- `pipeline_runs` table (`1773106031`) has a clean `DROP TABLE` down and nothing else references it outside the new factory/component/handlers.
- The lifecycler is an independent component; disabling it (removing its `RunnableComponent` entry) stops completion/retention without affecting run creation or any existing component.
- `build.Finish` gained one non-blocking `Notify` — removable independently; polling covers the loss (never notify-only, per the fork's lossy-NOTIFY lesson).

**Known v1 limitations (documented in contracts §7.1):** frozen-check-set pinning can see newer versions from shared scopes for non-`passed:` inputs of late jobs; `keep_last: 0` is not representable (JSON omitempty); a run whose entry-job build creation fails midway stays `running` until TTL retention or manual archive.

---

## Design-review amendments

- **2026-07-09 (F19 — `pipeline.Config()` dropped Template/Params/RunRetention; BLOCKER):** `atc/db/pipeline.go` `Config()` (:235) reconstructs only the 7 legacy fields, so Task 8's `CreateRun` — which reads `template.Config()` and re-saves it as the instance — would have saved instances with `template=false`, breaking lidar exclusion (Task 12) and frozen-check version pinning, and failing the plan's own `instance.Template()` assertion mid-Task-8; `fly get-pipeline` → `set-pipeline` (configserver/get.go:60 serves `Config()`) would silently de-template. **Change:** Task 7 gains an explicit step setting `Template`/`Params`/`RunRetention` from the already-scanned row in `Config()`'s final struct literal, and the Task 7 spec gains a save→`Config()` round-trip assertion block (template AND non-template default cases). Task 8's `CreateRun` needs no change; a warning comment above `template.Config()` tells implementers to fix `Config()` — never to force `Template=true` in the factory — if the `instance.Template()` assertion fails. No contract change (contracts §7 already defines the three `atc.Config` keys).
- **2026-07-09 (F26 — reopen detection missed fast-finishing builds):** `CompletedRunsWithNewActivity` matched only `pending`/`started` builds, but the `build.Finish` notify (the only wakeup besides the 10s poll) fires after a build leaves those states — a retrigger that started AND finished inside one polling gap (plan-creation failures Finish without ever starting, buildstarter.go:225) was never observed, leaving the run's terminal status stale forever (violating design decision 4). **Change:** the Task 8 EXISTS predicate is widened with `OR (b.completed AND r.completed_at IS NOT NULL AND b.end_time > r.completed_at)` — self-terminating, since reopen→re-complete stamps a `completed_at` newer than every build `end_time`. Task 9 gains the spec "surfaces fast-finishing retriggers that never linger in pending or started" (finish a retrigger build immediately after `run.Finish`, assert the run surfaces, then reopen→re-complete and assert it stops surfacing). Design decision 4 and §7.1 item 3 (Task 1) amended to state the completed-after-`completed_at` predicate.
- **2026-07-09 (F27 — frozen-check enqueue lived only in the API handler; latent, contract hygiene):** `enqueueInitialChecks` was implemented on the runserver handler while §7.1 promised checks "at run creation" and later-wave consumers (dispatch, experiments — contracts decision 22) call `db.PipelineRunFactory` in-process; with lidar excluding template pipelines, a factory-created run whose entry job has a get step would pend forever (NULL scope → trivially-passing `ResourcesChecked` → zero versions). Latent today (no planned consumer renders get-step entry jobs) but a §7.1 contract mismatch. **Change:** the enqueue moved into `pipelineRunFactory.CreateRun` (Task 8): `NewPipelineRunFactory` now injects a `CheckFactory` plus a `lager.Logger` (Registrar/Reaper idiom — the §2.3 `CreateRun` signature is frozen without ctx/logger, and the enqueue is best-effort: failures logged, never returned). The Task 16 handler is reduced to a pass-through (`runserver.NewServer(logger, runFactory)`, no CheckFactory, `enqueueInitialChecks` deleted from `runs.go`); all constructor call sites updated (Tasks 8/9/10/11 tests use the db-suite `logger`/`checkFactory` globals; Task 14 wires `dbCheckFactory` from command.go:1103, Task 16 from :902). New Task 8 factory DB spec "enqueues the frozen check set at creation so get-step entry jobs get versions" (template with a get-step entry job; asserts exactly one persisted check build for the instance resource). §7.1 item 2 (Task 1) amended; frozen interface `PipelineRunFactory`/`CreateRun` shapes unchanged.
- **2026-07-09 (F30-part — `((run_id))` reserved var + id allocation before materialization; co-signed with dispatch + harvest):** §8.1 defines `AGENT_PIPELINE_RUN_ID` as `pipeline_runs.id`, but the only run-scoped var was `((run))` — the per-template run NUMBER, which resets per template and collides across tickets (metrics/questions/reviews/gateway rows would key on colliding numbers while tickets/secrets/principals use the id). **Change (this plan's part):** `CreateRun` (Task 8) allocates `pipeline_runs.id` inside the creation transaction via `SELECT nextval(pg_get_serial_sequence('pipeline_runs','id'))` BEFORE materialization and inserts the row with the explicit id (`RETURNING created_at` only); `atc.MaterializeRunConfig` (Task 6) gains a `runID int` parameter and injects `run_id` as a second reserved static var alongside `run` (precedence over params); the params-schema validator (Task 5) reserves `run_id` next to `run` (new reject assertion). Tests: Task 6 gains the `((run_id))` resolution/precedence spec; Task 8's factory spec interpolates a `run-((run))-id-((run_id))` marker source and asserts it equals `run-1-id-<run.ID()>`. Design decision 7, §7.1 items 5 + new item 9, and the §11 log (Task 1) record the var. The renderer-site switch to `((run_id))` and the harvest `AGENT_PIPELINE_RUN_ID` env fallback are dispatch's (plan 11) and harvest's (plan 09) parts of F30.
