# Credential Vault, Rate-Limit Probe, Cost Ledger, and Budget Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Vault per-user Anthropic tokens encrypted at rest, empirically answer the shared-rate-limit question on theborg before anything is designed around it, land the append-only cost ledger fed by the existing agent-review job, and ship the budget Go library that owns all budget arithmetic.

**Architecture:** Three new packages (`agent/credentials`, `agent/budget`, `agent/api/costs`) follow the existing `agent/api/reviews` layering — pure stdlib domain packages implemented by `atc/db` factories (squirrel + counterfeiter) and wired into the API via `atc/api/handler.go`, `atc/routes.go`, `atc/wrappa/api_auth_wrappa.go`. Two K8s helpers (ephemeral per-run secret attacher, long-lived platform-credential syncer) live in `agent/credentials` over `kubernetes.Interface`. `fly agent auth` / `fly agent costs` ride new go-concourse client methods; ci-agent gains a `costs.json` artifact and a fire-and-forget cost publish.

**Tech Stack:** Go 1.25, PostgreSQL migrations (`atc/db/migration`, embedded via `go:embed`), squirrel, counterfeiter, Ginkgo/Gomega (atc, go-concourse, fly/integration), plain `go test` (agent/*, ci-agent), client-go + `fake.NewSimpleClientset()`, live theborg tests behind `//go:build live`.

---

## Context

**Charter (workstreams.json id `credentials-and-budgets`, size L, wave 1, depends_on []):**
- `agent_user_credentials` migration + factory: encrypted at rest via Concourse's existing DB encryption machinery, expiry timestamps, Jira-user mapping seam column from day one.
- `fly agent auth` walking `claude setup-token` (headless `CLAUDE_CODE_OAUTH_TOKEN`); expiry-horizon nagging via API field + `fly status`.
- Ephemeral K8s secret attach/cleanup helper for runs (consumed later by dispatch and gateway), including cleanup on abort/error paths.
- FIRST DELIVERABLE: live theborg rate-limit probe + written decision memo.
- `agent_cost_ledger` migration + factory with NULLABLE ticket join key, fed immediately from the existing agent-review job's ci-agent cost parsing; rollups per ticket/run/user/day + dashboard view; ledger writes fire-and-forget.
- Budget model as single owner: per-ticket budget (default per workflow definition, overridable), global daily cap, per-step budget-slice arithmetic, exposed as a Go library.
- Platform-credential policy: which credential funds platform-initiated LLM work and how the ledger attributes it.

**Scope OUT (do not implement):** budget enforcement call-sites (dispatch admission, gateway cutoff), per-repo git deploy credentials (harvest-step), agent principal tokens (agent-identity).

**Prior waves:** none — this is wave 1. Wave-mates (agent-identity, pipeline-runs, dev-mcp, workflow-store) run in parallel; their surfaces do NOT exist yet. Two touch-points with agent-identity are handled by an explicit contract addendum (Task 1): the `principal(costs:write)` tier for `SubmitAgentCostRecord` (interim: static publish token, exactly like `SubmitAgentReview` today), and the shared `fly agent` command struct.

**Contract surfaces this plan PRODUCES** (sections of `00-shared-contracts.md`):
- §1.3 `agent_user_credentials`, §1.4 `agent_cost_ledger`, §1.13 Platform credential policy
- §2.6 UserCredential (`agent/credentials/types.go`), §2.7 Budget library (`agent/budget/budget.go`)
- §4.2 routes `SetAgentUserCredential`, `GetAgentUserCredentialStatus`, `DeleteAgentUserCredential`, `GetAgentCostRollup`, `SubmitAgentCostRecord`
- §8.2 ephemeral run secret `agent-run-<run-id>` + long-lived `agent-platform-credential` secret + the run-secret safety-net reaper (`RunSecretReaper`, Task 15a — per-run secret cleanup is OWNED here per final-review F22; dispatch's in-process `Cleanup` is only the first line of defense)

**Contract surfaces this plan CONSUMES:**
- §1.2/§4.1 (agent-identity): `principal(costs:write)` — deferred via addendum; NOT consumed in code this wave.
- Conventions section: migration block 1773106020–29, money as `NUMERIC(12,6)`/`float64`, cross-aggregate refs as plain columns, factory recipe (`atc/db/agent_reviews_factory.go`).

**Verified code seams (line anchors current on branch `jetbridge`):**
- `atc/db/agent_reviews_factory.go` — factory recipe; `agent/api/reviews/types.go:123` Store interface; `agent/api/reviews/handler.go:69-115` static-token handler recipe.
- `atc/db/encryption/strategy.go:11` `Strategy` interface; `atc/db/open.go:24,63` `EncryptionStrategy()` on `DbConn`; `atc/db/migration/encryption.go:11-20` `encryptedColumns` rotation list (vault column MUST be added here).
- `atc/db/migration/migration.go:153` `go:embed migrations`; `atc/db/migration/legacy_upgrade_test.go:37` `jetbridgeHeadMigration = 1773105504` (bump with each migration).
- `atc/routes.go:121-129` (constants), `:254-262` (agent routes); `atc/wrappa/api_auth_wrappa.go:80-86` (authenticated), `:112-113` (SubmitAgentReview pass-through), `:169-176` (authorized); `atc/api/accessor/roles.go:108-115`.
- `atc/api/handler.go:47-93` NewHandler signature, `:122-139` server construction, `:269-276` handlers map; `atc/api/api_suite_test.go:182-228` second call site.
- `atc/atccmd/command.go:218` publish-token flag, `:2256-2300` NewHandler call, `:1268-1323` K8s component block (registrar/reaper `RunnableComponent` recipe), `atc/component.go:23-24` component constants.
- `ci-agent/llm/result.go` `CallResult`/`ParseCLIEnvelope` (cost parsing); `ci-agent/phaserunner/runner.go:104-120` LLM call site (usage currently discarded), `:186-199` results/step-results writing; `ci-agent/publish/publish.go` + `ci-agent/cmd/ci-agent/publish.go`; `ci/tasks/ci-agent-review.yml:57-71` publish step.
- `go-concourse/concourse/client.go:15-40` Client interface; `internal.Request{RequestName, Params, Query, Body}` (`internal/connection.go:29-36`); `wall.go` POST recipe.
- `fly/commands/fly.go:7-97` command registry; `fly/commands/status.go:12-33`; `fly/commands/curl.go` target/token recipe; `fly/integration/userinfo_test.go` ghttp spec recipe.
- `atc/worker/jetbridge/live_test.go:27-61` `kubeClient` live-test pattern; `live_secret_env_test.go` SecretKeyRef pattern.
- Users: `atc/db/migration/migrations/1563997651_users_table.up.sql` (id serial, sub UNIQUE, username, connector, last_login); rows created at login by `skymarshal/token/access_token.go:110`.

---

### Task 1: Wave-start contract addendum

The contracts doc has four points that need cross-workstream agreement recorded BEFORE parallel implementation diverges. Write them into the amendment log now.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md:1463` (append to `## 11. Amendment log`)

**Steps:**

- [ ] Append to the end of `## 11. Amendment log` in `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`:

```markdown
- 2026-07-08 (credentials-and-budgets wave-1 planning addendum; affects: agent-identity, dispatch, gateway-mcp, workflow-store, agent-step):
  - **SubmitAgentCostRecord interim auth:** until agent-identity's `principal(costs:write)` tier lands, `POST /api/v1/agent/costs` ships with the same handler-validated static publish token as `SubmitAgentReview` (wrappa pass-through case, `Authorization: Bearer <--agent-review-publish-token>`). agent-identity flips both routes to principal auth in its cutover task; no OTHER route may adopt the static token.
  - **SecretAttacher labels:** `credentials.SecretAttacher.Attach` (§2.6) has no ticket parameter, so the per-run secret is created with only the `concourse/agent-run: "<run-id>"` label. Dispatch adds the `concourse/ticket` label itself when it has one — §8.2's label list is a target state satisfied jointly, and the reaper sweep keys off `concourse/agent-run` alone.
  - **`group_by=workflow` rollups (§4.2 `GetAgentCostRollup`):** `agent_cost_ledger` has no workflow column; workflow attribution rides `metadata->>'workflow'`. Writers that know their workflow (agent-step ingest, gateway metering) MUST set `{"workflow": "<name>@<version>"}` in `metadata`.
  - **`fly agent` command family:** `fly/commands/agent.go` `AgentCommand` struct is created by credentials-and-budgets with `Auth`/`Costs` subcommand fields; wave-mates (workflow-store) and later workstreams add their own fields (`Workflows`, `Tickets`, …) to the same struct — additive merges only.
  - **Migration deploy ordering:** the migrator is version-pointer based (`atc/db/migration/migration.go` `Migrate`: `currentVersion < m.Version`), so a deployed DB whose head is 1773106021+ will NEVER later apply a lower-numbered migration. Wave-1 branches MUST merge to `jetbridge` in migration-number order (identity 1773106010s → credentials 1773106020s → pipeline-runs 1773106030s → workflow-store 1773106040s) before any theborg deploy picks them up.
  - **Key rotation:** `agent_user_credentials.encrypted_token` is added to `atc/db/migration/encryption.go` `encryptedColumns` so `concourse web --encryption-key` rotation re-encrypts the vault (validates the §1.3 encryption decision — the rotation list is hardcoded and would otherwise silently skip the vault).
  - **Third credentials migration:** 1773106022 (within the allocated 1773106020–29 block) seeds the §1.13 `agent-platform` service user and creates the `agent_cost_daily_rollup` SQL view (the "dashboard view" deliverable).
  - **§1.13 ticket-budget arithmetic:** `budget.Ledger.SpentForTicket` (and therefore `Checker.TicketRemaining`/`StepSlice`) EXCLUDES `source = 'harvest_judge'` rows — per §1.13 the judge must never be starved by an agent that burned the ticket budget; judge spend is capped separately by the workflow's `judge_usd`. The global daily cap (`SpentSince`) includes ALL sources, platform spend included.
  - **Platform-credential provisioning (§1.13/§8.2):** the `agent-platform` service user never logs in, so `PUT /api/v1/agent/user-credentials` accepts an optional body field `"user": "platform"` (the ONLY non-self value), allowed for admin tokens only, which vaults the credential onto the service user's row; `GET`/`DELETE /api/v1/agent/user-credentials[/:kind]?user=platform` mirror it. Surfaced as `fly agent auth --platform [--delete]` (admin). All other access remains strictly self-scoped.
- 2026-07-09 (credentials-and-budgets final-review F22 addendum; affects: pipeline-runs, dispatch, agent-identity, gateway-mcp):
  - **Run-secret cleanup ownership (§8.2's "reaper safety-net GC"):** OWNED by credentials-and-budgets. `credentials.RunSecretReaper` (plan 02 Task 15a) IS the safety-net GC that §8.2, plan 03, and plan 11 reference: a polling `RunnableComponent` beside the platform syncer that lists worker-namespace secrets by the `concourse/agent-run` label, deletes any whose run is complete or absent (narrow `RunActive(runID)` seam; production impl `atc/db.NewAgentRunChecker` over `pipeline_runs` — absent row OR absent table = inactive), and best-effort revokes the per-run principal `agent-run-<run-id>` in the same pass. Attribution rewording: dispatch's in-process `SecretAttacher.Cleanup` on abort/error paths (plan 11) is the FIRST line of defense only; plan 03's lifecycler stays deliberately pure (no attacher/clientset — do NOT plumb one in). A 5-minute creation-grace window protects the dispatch `CreateRun`→`Attach` ordering from sweep races. The `PrincipalRevoker` binding ships nil until agent-identity's store lands (its cutover task binds it) — safe interim because per-run principals carry `expires_at`, unlike the secret.
```

- [ ] Commit:

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "docs(agent): wave-1 credentials-and-budgets contract addendum"
```

---

### Task 2: Rate-limit probe live-test harness + decision-memo template

The probe answers: does headless `claude -p` usage under a `CLAUDE_CODE_OAUTH_TOKEN` share the token owner's interactive rate-limit window? Build the harness now; Task 3 runs it. Plain Go test, `//go:build live`, per the jetbridge live-test pattern (CLAUDE.md).

**Decisiveness (final-review F35, 2026-07-09):** `/status` displays usage at coarse granularity, so a burst of tiny "Reply with exactly: ok" calls can be invisible even when the window IS shared — a null `/status` delta from such a burst can never support a NOT-SHARED conclusion. The harness therefore (a) defaults `PROBE_PROMPT` to a ~800-word generation so the burst is sized ABOVE display granularity, (b) sums the captured CLI envelopes' usage into a `PROBE_TOTAL` line (the envelope side of the comparison), and (c) the memo's verdict is calibrated against an interactive burst that visibly moved `/status` (threshold T), with an asymmetric rule: any visible delta ⇒ SHARED; NOT SHARED only above 2×T; anything else ⇒ INCONCLUSIVE, which downstream consumers MUST treat as SHARED.

**Files:**
- Create: `agent/credentials/probe_usage.go`
- Create: `agent/credentials/probe_usage_test.go`
- Create: `agent/credentials/live_rate_limit_probe_test.go`
- Create: `docs/superpowers/plans/agentic-platform/notes/2026-07-rate-limit-probe.md`

**Steps:**

- [ ] Write the failing unit test `agent/credentials/probe_usage_test.go` (plain `go test`, NOT live-gated — the summing must be verifiable without a cluster):

```go
package credentials_test

import (
	"testing"

	"github.com/concourse/concourse/agent/credentials"
)

func TestSumProbeUsageSumsEnvelopes(t *testing.T) {
	logs := `PROBE_START calls=2 sleep=2s
PROBE_RESULT call=1 wall_ms=5000 {"type":"result","is_error":false,"total_cost_usd":0.031,"usage":{"input_tokens":12,"output_tokens":820}}
PROBE_RESULT call=2 wall_ms=6100 {"type":"result","is_error":true,"cost_usd":0.009,"usage":{"input_tokens":11,"output_tokens":240}}
PROBE_DONE`

	totals := credentials.SumProbeUsage(logs)
	if totals.Envelopes != 2 {
		t.Fatalf("envelopes: %d", totals.Envelopes)
	}
	if totals.InputTokens != 23 || totals.OutputTokens != 1060 {
		t.Fatalf("tokens: in=%d out=%d", totals.InputTokens, totals.OutputTokens)
	}
	// older CLI envelopes say cost_usd, newer say total_cost_usd — both count
	if totals.CostUSD < 0.0399 || totals.CostUSD > 0.0401 {
		t.Fatalf("cost: %f", totals.CostUSD)
	}
	if totals.Errors != 1 {
		t.Fatalf("errors: %d", totals.Errors)
	}
}

func TestSumProbeUsageSkipsUnparseableLines(t *testing.T) {
	logs := `PROBE_RESULT call=1 wall_ms=100 bash: claude: command not found
PROBE_RESULT call=2 wall_ms=100 {"not":"a CLI envelope"}
random line {"type":"result","usage":{"output_tokens":99}}`

	totals := credentials.SumProbeUsage(logs)
	if totals.Envelopes != 0 {
		t.Fatalf("nothing should parse, got %d envelopes", totals.Envelopes)
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/credentials/
```

Expected failure: compile error `undefined: credentials.SumProbeUsage`.

- [ ] Write `agent/credentials/probe_usage.go`:

```go
package credentials

import (
	"encoding/json"
	"strings"
)

// ProbeTotals sums the usage reported by the CLI envelopes the rate-limit
// probe captures. F35: `/status` displays usage at coarse granularity, so
// the decision memo compares this envelope-side total against a calibrated
// interactive burst — a probe burst too small to move /status can never
// support a NOT-SHARED conclusion.
type ProbeTotals struct {
	Envelopes    int
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Errors       int
}

type probeEnvelope struct {
	Type         string  `json:"type"`
	IsError      bool    `json:"is_error"`
	CostUSD      float64 `json:"cost_usd"`       // older CLI versions
	TotalCostUSD float64 `json:"total_cost_usd"` // newer CLI versions
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// SumProbeUsage extracts the JSON envelope from each PROBE_RESULT line of
// the probe pod's logs and sums usage into a PROBE_TOTAL. Lines without a
// parseable envelope are skipped — they still show up in the raw log, but
// contribute nothing here, so Envelopes < calls flags a degraded run.
func SumProbeUsage(logs string) ProbeTotals {
	var totals ProbeTotals
	for _, line := range strings.Split(logs, "\n") {
		if !strings.HasPrefix(line, "PROBE_RESULT ") {
			continue
		}
		brace := strings.Index(line, "{")
		if brace < 0 {
			continue
		}
		var env probeEnvelope
		if err := json.Unmarshal([]byte(line[brace:]), &env); err != nil || env.Type == "" {
			continue
		}
		totals.Envelopes++
		totals.InputTokens += env.Usage.InputTokens
		totals.OutputTokens += env.Usage.OutputTokens
		cost := env.TotalCostUSD
		if cost == 0 {
			cost = env.CostUSD
		}
		totals.CostUSD += cost
		if env.IsError {
			totals.Errors++
		}
	}
	return totals
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/credentials/
```

Expected: `ok`.

- [ ] Write `agent/credentials/live_rate_limit_probe_test.go`. It is compile-gated behind `live` so it cannot break normal builds; it refuses live namespaces:

```go
//go:build live

package credentials_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/credentials"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// TestLiveRateLimitProbe runs a burst of headless claude CLI calls inside a
// pod on a throwaway namespace, using a real CLAUDE_CODE_OAUTH_TOKEN. The
// human operator watches the token owner's interactive session usage
// (`claude` → /status) before and after, and records findings in
// docs/superpowers/plans/agentic-platform/notes/2026-07-rate-limit-probe.md.
//
// F35: the default prompt is a ~800-word generation (NOT a one-token "ok")
// so the burst is sized above /status display granularity, and the captured
// envelopes' usage is summed into a PROBE_TOTAL line the memo compares
// against the calibrated interactive threshold T.
//
// Required env:
//   CLAUDE_CODE_OAUTH_TOKEN  - token from `claude setup-token`
//   K8S_TEST_NAMESPACE       - THROWAWAY namespace (not cicd/concourse/default)
// Optional env:
//   PROBE_CALLS (default 5), PROBE_SLEEP_SECONDS (default 5),
//   PROBE_IMAGE (default node:22-bookworm), PROBE_MODEL (default: CLI default),
//   PROBE_PROMPT (default: ~800-word essay generation)
func TestLiveRateLimitProbe(t *testing.T) {
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if token == "" {
		t.Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
	}
	ns := os.Getenv("K8S_TEST_NAMESPACE")
	if ns == "" {
		t.Skip("K8S_TEST_NAMESPACE not set")
	}
	for _, forbidden := range []string{"cicd", "concourse", "default", "kube-system"} {
		if ns == forbidden {
			t.Fatalf("refusing to run the probe in live namespace %q — create a throwaway namespace", ns)
		}
	}

	clientset := probeClient(t)
	ctx := context.Background()

	calls := envIntOr(t, "PROBE_CALLS", 5)
	sleepSec := envIntOr(t, "PROBE_SLEEP_SECONDS", 5)
	image := os.Getenv("PROBE_IMAGE")
	if image == "" {
		image = "node:22-bookworm"
	}
	modelFlag := ""
	if m := os.Getenv("PROBE_MODEL"); m != "" {
		modelFlag = "--model " + m
	}
	// F35: default to a generation large enough to register on /status —
	// one-token replies sit below its display granularity.
	prompt := os.Getenv("PROBE_PROMPT")
	if prompt == "" {
		prompt = "Write an 800-word essay on the history of container orchestration. Output only the essay."
	}

	stamp := time.Now().Format("150405")
	secretName := "rate-limit-probe-token-" + stamp
	podName := "rate-limit-probe-" + stamp

	_, err := clientset.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
		StringData: map[string]string{"anthropic-token": token},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating probe secret: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Secrets(ns).Delete(context.Background(), secretName, metav1.DeleteOptions{})
	})

	script := fmt.Sprintf(`set -e
npm install -g @anthropic-ai/claude-code >/dev/null 2>&1
echo "PROBE_START calls=%d sleep=%ds"
for i in $(seq 1 %d); do
  start=$(date +%%s%%3N)
  out=$(claude -p "$PROBE_PROMPT" --output-format json %s 2>&1) || true
  end=$(date +%%s%%3N)
  echo "PROBE_RESULT call=$i wall_ms=$((end-start)) $out"
  sleep %d
done
echo PROBE_DONE`, calls, sleepSec, calls, modelFlag, sleepSec)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels:    map[string]string{"app": "agent-rate-limit-probe"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   image,
				Command: []string{"bash", "-c", script},
				Env: []corev1.EnvVar{{
					Name:  "PROBE_PROMPT",
					Value: prompt,
				}, {
					Name: "CLAUDE_CODE_OAUTH_TOKEN",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
							Key:                  "anthropic-token",
						},
					},
				}},
			}},
		},
	}
	_, err = clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating probe pod: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(ns).Delete(context.Background(), podName, metav1.DeleteOptions{})
	})

	deadline := time.Now().Add(20 * time.Minute)
	for {
		p, err := clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting probe pod: %v", err)
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe pod did not complete within 20m (phase %s)", p.Status.Phase)
		}
		time.Sleep(10 * time.Second)
	}

	logReq := clientset.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{})
	stream, err := logReq.Stream(ctx)
	if err != nil {
		t.Fatalf("streaming probe logs: %v", err)
	}
	defer stream.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		t.Fatalf("reading probe logs: %v", err)
	}
	logs := buf.String()
	t.Logf("=== probe output ===\n%s", logs)

	if !strings.Contains(logs, "PROBE_DONE") {
		t.Fatalf("probe did not reach PROBE_DONE — inspect output above")
	}
	limited := 0
	for _, line := range strings.Split(logs, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate_limit") || strings.Contains(lower, "429") {
			limited++
			t.Logf("LIMIT SIGNAL: %s", line)
		}
	}

	// F35: sum the captured envelopes — the memo compares this against the
	// calibrated interactive threshold T. A run with no parseable envelopes
	// cannot support ANY conclusion, so it fails loudly.
	totals := credentials.SumProbeUsage(logs)
	if totals.Envelopes == 0 {
		t.Fatalf("no parseable CLI envelopes in %d calls — PROBE_TOTAL cannot be computed; the memo may not conclude anything from this run", calls)
	}
	t.Logf("PROBE_TOTAL calls=%d envelopes=%d input_tokens=%d output_tokens=%d cost_usd=%.4f errors=%d",
		calls, totals.Envelopes, totals.InputTokens, totals.OutputTokens, totals.CostUSD, totals.Errors)
	t.Logf("probe complete: %d calls, %d rate-limit signals — record PROBE_TOTAL and findings in the decision memo", calls, limited)
}

func probeClient(t *testing.T) kubernetes.Interface {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = home + "/.kube/config"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("building kube config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("creating clientset: %v", err)
	}
	return clientset
}

func envIntOr(t *testing.T, name string, def int) int {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		t.Fatalf("invalid %s=%q", name, v)
	}
	return n
}
```

- [ ] Verify it compiles under the live tag without running (no cluster locally):

```bash
go vet -tags live ./agent/credentials/
```

Expected: exits 0 (the package holds `probe_usage.go` plus this live-gated file; vet compiles both).

- [ ] Write the memo template `docs/superpowers/plans/agentic-platform/notes/2026-07-rate-limit-probe.md`:

```markdown
# Decision memo: do headless claude calls share the owner's interactive rate-limit window?

- **Status:** PROBE NOT YET RUN (template — filled by Task 3)
- **Date run:** _
- **Probe:** `agent/credentials/live_rate_limit_probe_test.go` on theborg, throwaway namespace
- **Charter question (spec §11 / open item 7):** whether headless usage via `CLAUDE_CODE_OAUTH_TOKEN` counts against the token owner's interactive (subscription) rate-limit window.

## Method

1. Owner opens an interactive `claude` session and records `/status` usage BEFORE.
2. **Calibrate /status granularity (F35):** in the interactive session, generate output (long essays) until `/status` FIRST visibly moves. Record the approximate volume (output tokens / cost, from the interactive transcript) that produced that first visible movement — this is the calibrated visibility threshold **T**. Without T, no NOT-SHARED verdict is possible.
3. Run the probe: N sequential headless `claude -p` calls of `PROBE_PROMPT` (default ~800-word generations — sized ABOVE display granularity, per F35) in a pod on theborg with the owner's setup-token. Record the test's `PROBE_TOTAL` line (envelope-summed input/output tokens, cost, errors).
4. Owner records `/status` usage AFTER; compare deltas; note any 429/limit signals in probe output.
5. If `PROBE_TOTAL` < 2×T, escalate: rerun with `PROBE_CALLS=20` (and/or a longer `PROBE_PROMPT`) until `PROBE_TOTAL` ≥ 2×T or limit signals appear.

## Raw results

- Calibrated visibility threshold **T** (interactive volume that first moved `/status`): _

| run | calls | prompt | wall-time per call (ms) | PROBE_TOTAL tokens in/out | PROBE_TOTAL cost (usd) | limit signals | interactive usage before | interactive usage after |
|---|---|---|---|---|---|---|---|---|
| 1 | _ | _ | _ | _ | _ | _ | _ | _ |

## Findings

- Shared window: SHARED / NOT SHARED / PARTIAL / INCONCLUSIVE — _evidence_
- **Decisiveness rule (F35 — asymmetric, calibrated against T):**
  - ANY visible `/status` delta attributable to the probe ⇒ **SHARED** (a small burst can only under-report sharing, never fake it).
  - **NOT SHARED** may be concluded ONLY when `PROBE_TOTAL` ≥ 2×T AND `/status` did not move.
  - Anything else ⇒ **INCONCLUSIVE**. Record it as such; downstream consumers MUST treat INCONCLUSIVE as SHARED (fail-safe: throttle as if shared).
- Headless failure mode when limited: _error string, envelope `is_error`, exit code_

## Decision (feeds budget defaults and batch sizing)

- **Per-ticket budget default (workflow definitions, §6 `budget.ticket_usd`):** $_
- **Global daily cap recommendation for theborg (`--agent-daily-budget-usd`):** $_
- **Dispatch/experiment concurrency guidance:** _max parallel runs per user token; whether platform credential must be a separate account_
- **Gateway cutoff sizing:** _slice sizing rule of thumb given observed per-call cost_

## Consequences

- If SHARED: dispatch (wave 4) must throttle per-user concurrency; experiments (wave 5) batch under the daily cap with per-user serialization; document in workflow defaults.
- If NOT SHARED (only concludable above the calibrated 2×T threshold): budgets are purely cost-control; concurrency limited only by the daily cap.
- If INCONCLUSIVE: identical operational posture to SHARED — dispatch/experiments throttle as if shared, and the memo notes what a future decisive rerun would need (bigger burst, longer prompt, fresh calibration).
```

- [ ] Commit:

```bash
git add agent/credentials/probe_usage.go agent/credentials/probe_usage_test.go \
        agent/credentials/live_rate_limit_probe_test.go \
        docs/superpowers/plans/agentic-platform/notes/2026-07-rate-limit-probe.md
git commit -m "feat(agent): live rate-limit probe harness + decision memo template"
```

---

### Task 3: Run the probe on theborg and write the decision memo

FIRST DELIVERABLE of the workstream (charter). Human-in-the-loop task — needs the operator's Claude subscription token and eyes on an interactive session.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/notes/2026-07-rate-limit-probe.md` (fill all `_` placeholders in the template)

**Steps:**

- [ ] Create a throwaway namespace on theborg (kube-context `theborg` → https://theborg.home:6443; NOT `cicd`/`concourse`, no pod-security label):

```bash
kubectl --context theborg create namespace agent-probe-$(date +%m%d)
```

- [ ] Record interactive usage BEFORE: operator runs `claude` locally (the account whose setup-token is used), runs `/status`, screenshots/copies the usage block into the memo's Raw results table.

- [ ] **Calibrate the /status visibility threshold T (F35):** still in the interactive session, ask for long generations (e.g. "write a 2000-word essay …") and re-check `/status` after each until it FIRST visibly moves. Record the approximate output-token/cost volume that produced that first movement as T in the memo's Raw results. No NOT-SHARED verdict is permitted without T.

- [ ] Run the probe (operator supplies the token; 5 calls of the default ~800-word `PROBE_PROMPT` first):

```bash
KUBECONFIG=~/.kube/config \
K8S_TEST_NAMESPACE=agent-probe-$(date +%m%d) \
CLAUDE_CODE_OAUTH_TOKEN=<paste from `claude setup-token`> \
go test -tags live -run '^TestLiveRateLimitProbe$' -v -count=1 -timeout 30m ./agent/credentials/
```

Expected: test PASSES, prints `PROBE_RESULT` lines with per-call wall-times and JSON envelopes, a `PROBE_TOTAL` summary line (envelope-summed tokens/cost — fails loudly if no envelope parsed), and ends with `PROBE_DONE`.

- [ ] Record interactive usage AFTER (same `/status` check). Apply the memo's decisiveness rule (F35): any visible delta ⇒ SHARED; if no delta and `PROBE_TOTAL` < 2×T, the run is not decisive — escalate with `PROBE_CALLS=20 PROBE_SLEEP_SECONDS=2` (and/or a longer `PROBE_PROMPT`) until `PROBE_TOTAL` ≥ 2×T or a delta/limit signal appears.

- [ ] Fill in ALL sections of `docs/superpowers/plans/agentic-platform/notes/2026-07-rate-limit-probe.md`: raw results (including T and every run's `PROBE_TOTAL`), findings (SHARED / NOT SHARED / PARTIAL / INCONCLUSIVE per the calibrated rule — INCONCLUSIVE is a legitimate verdict and downstream treats it as SHARED), decision numbers (per-ticket default, daily cap recommendation, concurrency guidance), consequences. Flip status line to `PROBE RUN — DECISION RECORDED`.

- [ ] Delete the throwaway namespace:

```bash
kubectl --context theborg delete namespace agent-probe-$(date +%m%d)
```

- [ ] Commit:

```bash
git add docs/superpowers/plans/agentic-platform/notes/2026-07-rate-limit-probe.md
git commit -m "docs(agent): rate-limit probe results and budget-default decisions"
```

---

### Task 4: `agent/credentials` domain types + memory backend

Contract §2.6, EXACT interface shapes. Pure stdlib (no atc imports — `atc/db` imports this package, and `atc/api/accessor` imports `atc/db`, so importing accessor here would cycle).

**Files:**
- Create: `agent/credentials/types.go`
- Create: `agent/credentials/memory.go`
- Test: `agent/credentials/types_test.go`

**Steps:**

- [ ] Write the failing test `agent/credentials/types_test.go`:

```go
package credentials_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/credentials"
)

func TestValidKind(t *testing.T) {
	if !credentials.ValidKind("anthropic_oauth") || !credentials.ValidKind("anthropic_api_key") {
		t.Fatal("contract kinds must validate")
	}
	if credentials.ValidKind("openai") || credentials.ValidKind("") {
		t.Fatal("unknown kinds must not validate")
	}
}

func TestCredentialJSONNeverCarriesToken(t *testing.T) {
	c := credentials.Credential{UserID: 1, UserName: "alice", Kind: "anthropic_oauth", Token: "sk-secret"}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-secret") {
		t.Fatalf("token leaked into JSON: %s", data)
	}
}

func TestPutRequestValidate(t *testing.T) {
	ok := credentials.PutRequest{Kind: "anthropic_oauth", Token: "tok"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	platform := credentials.PutRequest{Kind: "anthropic_oauth", Token: "tok", User: credentials.PlatformUserName}
	if err := platform.Validate(); err != nil {
		t.Fatalf("platform-user request rejected: %v", err)
	}
	for _, bad := range []credentials.PutRequest{
		{Kind: "", Token: "tok"},
		{Kind: "openai", Token: "tok"},
		{Kind: "anthropic_oauth", Token: ""},
		{Kind: "anthropic_oauth", Token: "tok", User: "someone-else"},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid request accepted: %+v", bad)
		}
	}
}

func TestMemoryBackendRoundTrip(t *testing.T) {
	m := credentials.NewMemoryBackend()
	m.AddUser("sub-1", 7, "alice")

	id, name, found, err := m.UserBySub("sub-1")
	if err != nil || !found || id != 7 || name != "alice" {
		t.Fatalf("UserBySub: %d %q %v %v", id, name, found, err)
	}

	exp := time.Now().Add(time.Hour)
	if err := m.Put(7, "alice", "anthropic_oauth", "sk-tok", exp); err != nil {
		t.Fatal(err)
	}

	status, err := m.Status(7)
	if err != nil || len(status) != 1 {
		t.Fatalf("Status: %v %v", status, err)
	}
	if status[0].Token != "" {
		t.Fatal("Status must not carry tokens")
	}
	if status[0].ExpiresAt != exp.Unix() {
		t.Fatalf("ExpiresAt: got %d want %d", status[0].ExpiresAt, exp.Unix())
	}

	cred, found, err := m.Resolve(7, "anthropic_oauth")
	if err != nil || !found || cred.Token != "sk-tok" {
		t.Fatalf("Resolve: %+v %v %v", cred, found, err)
	}

	expiring, err := m.ExpiringWithin(2 * time.Hour)
	if err != nil || len(expiring) != 1 {
		t.Fatalf("ExpiringWithin(2h): %v %v", expiring, err)
	}
	expiring, err = m.ExpiringWithin(time.Minute)
	if err != nil || len(expiring) != 0 {
		t.Fatalf("ExpiringWithin(1m): %v %v", expiring, err)
	}

	if err := m.SetJiraAccountID(7, "jira-123"); err != nil {
		t.Fatal(err)
	}
	status, _ = m.Status(7)
	if status[0].JiraAccountID != "jira-123" {
		t.Fatal("jira seam not persisted")
	}

	if err := m.Delete(7, "anthropic_oauth"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := m.Resolve(7, "anthropic_oauth"); found {
		t.Fatal("credential survived Delete")
	}
}
```

- [ ] Run it to verify it fails:

```bash
go test ./agent/credentials/
```

Expected failure: `undefined: credentials.ValidKind` (and the other symbols) — compile error.

- [ ] Write `agent/credentials/types.go` (§2.6 verbatim, plus additive helpers):

```go
// Package credentials owns the per-user Anthropic credential vault: the
// domain types and Store contract (implemented by atc/db), the HTTP
// handler seam, and the K8s secret helpers (ephemeral per-run secret and
// long-lived platform-credential secret) that dispatch and the gateway
// consume. Contract: docs/superpowers/plans/agentic-platform/
// 00-shared-contracts.md §1.3, §2.6, §8.2, §1.13.
package credentials

import (
	"context"
	"fmt"
	"time"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

const (
	KindAnthropicOAuth  = "anthropic_oauth"
	KindAnthropicAPIKey = "anthropic_api_key"
)

// The §1.13 platform service user, seeded by migration 1773106022. It
// never logs in; admins vault its credential via `fly agent auth
// --platform` (PutRequest.User = PlatformUserName). Its credential funds
// platform-initiated LLM work (harvest judge, retrospective, calibration).
const (
	PlatformUserSub  = "agent-platform"
	PlatformUserName = "platform"
)

// ValidKind reports whether kind is accepted by the
// agent_user_credentials CHECK constraint.
func ValidKind(kind string) bool {
	return kind == KindAnthropicOAuth || kind == KindAnthropicAPIKey
}

// Credential never carries the decrypted token in API responses;
// Token is populated only by Store.Resolve for dispatch/secret-attach.
type Credential struct {
	UserID         int    `json:"user_id"`
	UserName       string `json:"user_name"`
	Kind           string `json:"kind"` // anthropic_oauth | anthropic_api_key
	ExpiresAt      int64  `json:"expires_at,omitempty"`
	LastVerifiedAt int64  `json:"last_verified_at,omitempty"`
	JiraAccountID  string `json:"jira_account_id,omitempty"`

	Token string `json:"-"` // decrypted; in-memory only
}

//counterfeiter:generate . Store
type Store interface {
	Put(userID int, userName, kind, token string, expiresAt time.Time) error
	Status(userID int) ([]Credential, error)                    // no tokens
	Resolve(userID int, kind string) (*Credential, bool, error) // decrypts
	ExpiringWithin(d time.Duration) ([]Credential, error)       // nag list
	Delete(userID int, kind string) error
}

// SecretAttacher is the ephemeral K8s secret helper (§8.2). Implemented once
// here; dispatch and the gateway use it, nobody re-implements secret lifecycle.
//counterfeiter:generate . SecretAttacher
type SecretAttacher interface {
	// Attach creates secret agent-run-<runID> in the worker namespace with
	// the §8.2 keys and returns its name. Idempotent per runID.
	Attach(ctx context.Context, runID int, cred *Credential, principalToken string) (secretName string, err error)
	// Cleanup deletes the secret. Called by the pipeline-run lifecycle
	// component on run completion (and best-effort by dispatch on error).
	Cleanup(ctx context.Context, runID int) error
}

// Backend is what the HTTP handler and the platform-credential syncer
// need: the vault Store plus user resolution from token claims. The
// atc/db factory implements it. Additive to the frozen §2.6 Store.
type Backend interface {
	Store
	// UserBySub resolves a users row by its OIDC sub claim (users.sub is
	// UNIQUE; rows are created at login by skymarshal).
	UserBySub(sub string) (userID int, userName string, found bool, err error)
	// SetJiraAccountID records the phase-2 Jira mapping seam value on all
	// of the user's credential rows.
	SetJiraAccountID(userID int, jiraAccountID string) error
}

// PutRequest is the parsed PUT /api/v1/agent/user-credentials body.
type PutRequest struct {
	Kind          string `json:"kind"`
	Token         string `json:"token"`
	ExpiresAt     int64  `json:"expires_at,omitempty"` // unix seconds; 0 = unknown
	JiraAccountID string `json:"jira_account_id,omitempty"`
	// User is empty for the normal self-scoped write. The ONLY other value
	// is PlatformUserName ("platform"), accepted from admins only: it
	// vaults the credential onto the §1.13 service user's row (the service
	// user never logs in, so no self-scoped path can reach it).
	User string `json:"user,omitempty"`
}

func (r *PutRequest) Validate() error {
	if !ValidKind(r.Kind) {
		return fmt.Errorf("kind must be %s or %s", KindAnthropicOAuth, KindAnthropicAPIKey)
	}
	if r.Token == "" {
		return fmt.Errorf("token is required")
	}
	if r.User != "" && r.User != PlatformUserName {
		return fmt.Errorf("user must be omitted or %q", PlatformUserName)
	}
	return nil
}
```

- [ ] Write `agent/credentials/memory.go` (test double for handler tests and the api suite, mirroring `reviews.NewMemoryStore`):

```go
package credentials

import (
	"sync"
	"time"
)

// MemoryBackend is an in-memory Backend for tests.
type MemoryBackend struct {
	mu    sync.Mutex
	users map[string]memUser // sub -> user
	creds map[int]map[string]memCred
}

type memUser struct {
	id   int
	name string
}

type memCred struct {
	token          string
	expiresAt      time.Time
	lastVerifiedAt time.Time
	jiraAccountID  string
	userName       string
}

func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		users: map[string]memUser{},
		creds: map[int]map[string]memCred{},
	}
}

// AddUser registers a fake users row (login-created in production).
func (m *MemoryBackend) AddUser(sub string, id int, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[sub] = memUser{id: id, name: name}
}

func (m *MemoryBackend) UserBySub(sub string) (int, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[sub]
	return u.id, u.name, ok, nil
}

func (m *MemoryBackend) Put(userID int, userName, kind, token string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.creds[userID] == nil {
		m.creds[userID] = map[string]memCred{}
	}
	prev := m.creds[userID][kind]
	m.creds[userID][kind] = memCred{
		token: token, expiresAt: expiresAt, userName: userName,
		jiraAccountID: prev.jiraAccountID,
	}
	return nil
}

func (m *MemoryBackend) Status(userID int) ([]Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Credential{}
	for kind, c := range m.creds[userID] {
		out = append(out, m.toCredential(userID, kind, c, false))
	}
	return out, nil
}

func (m *MemoryBackend) Resolve(userID int, kind string) (*Credential, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.creds[userID][kind]
	if !ok {
		return nil, false, nil
	}
	cred := m.toCredential(userID, kind, c, true)
	return &cred, true, nil
}

func (m *MemoryBackend) ExpiringWithin(d time.Duration) ([]Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(d)
	out := []Credential{}
	for userID, kinds := range m.creds {
		for kind, c := range kinds {
			if !c.expiresAt.IsZero() && c.expiresAt.Before(cutoff) {
				out = append(out, m.toCredential(userID, kind, c, false))
			}
		}
	}
	return out, nil
}

func (m *MemoryBackend) Delete(userID int, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.creds[userID], kind)
	return nil
}

func (m *MemoryBackend) SetJiraAccountID(userID int, jiraAccountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for kind, c := range m.creds[userID] {
		c.jiraAccountID = jiraAccountID
		m.creds[userID][kind] = c
	}
	return nil
}

func (m *MemoryBackend) toCredential(userID int, kind string, c memCred, withToken bool) Credential {
	cred := Credential{
		UserID:        userID,
		UserName:      c.userName,
		Kind:          kind,
		JiraAccountID: c.jiraAccountID,
	}
	if !c.expiresAt.IsZero() {
		cred.ExpiresAt = c.expiresAt.Unix()
	}
	if !c.lastVerifiedAt.IsZero() {
		cred.LastVerifiedAt = c.lastVerifiedAt.Unix()
	}
	if withToken {
		cred.Token = c.token
	}
	return cred
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/credentials/
```

Expected: `ok  github.com/concourse/concourse/agent/credentials`.

- [ ] Commit:

```bash
git add agent/credentials/types.go agent/credentials/memory.go agent/credentials/types_test.go
git commit -m "feat(agent): credentials domain types, Store/SecretAttacher contracts, memory backend"
```

---

### Task 5: Migration 1773106020 — `agent_user_credentials` (+ rotation list, head bump)

SQL is §1.3 of the contracts doc, verbatim. Also adds the vault column to the hardcoded key-rotation list (`encryptedColumns`) and bumps the legacy-upgrade head constant.

**Files:**
- Create: `atc/db/migration/migrations/1773106020_create_agent_user_credentials.up.sql`
- Create: `atc/db/migration/migrations/1773106020_create_agent_user_credentials.down.sql`
- Modify: `atc/db/migration/encryption.go:11-20` (`encryptedColumns` list)
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration`)
- Test: `atc/db/migration/create_agent_user_credentials_test.go`

**Steps:**

- [ ] Write the failing migration test `atc/db/migration/create_agent_user_credentials_test.go` (harness precedent: `add_job_tags_test.go` `postgresRunner.OpenDBAtVersion`):

```go
package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Create agent user credentials", func() {
	const postMigrationVersion = 1773106020

	It("creates the vault table with FK cascade, kind check, and (user_id, kind) uniqueness", func() {
		db := postgresRunner.OpenDBAtVersion(postMigrationVersion)
		defer db.Close()

		_, err := db.Exec(`INSERT INTO users(sub, username, connector) VALUES('cred-mig-sub','alice','local')`)
		Expect(err).NotTo(HaveOccurred())
		var userID int
		Expect(db.QueryRow(`SELECT id FROM users WHERE sub='cred-mig-sub'`).Scan(&userID)).To(Succeed())

		_, err = db.Exec(`INSERT INTO agent_user_credentials(user_id, user_name, kind, encrypted_token)
			VALUES($1, 'alice', 'anthropic_oauth', 'ciphertext')`, userID)
		Expect(err).NotTo(HaveOccurred())

		By("rejecting a duplicate (user_id, kind)")
		_, err = db.Exec(`INSERT INTO agent_user_credentials(user_id, user_name, kind, encrypted_token)
			VALUES($1, 'alice', 'anthropic_oauth', 'other')`, userID)
		Expect(err).To(HaveOccurred())

		By("rejecting an unknown kind via CHECK")
		_, err = db.Exec(`INSERT INTO agent_user_credentials(user_id, user_name, kind, encrypted_token)
			VALUES($1, 'alice', 'openai', 'x')`, userID)
		Expect(err).To(HaveOccurred())

		By("allowing NULL nonce (encryption disabled) and NULL expiry")
		var nonce, expires any
		Expect(db.QueryRow(`SELECT nonce, expires_at FROM agent_user_credentials WHERE user_id=$1`, userID).
			Scan(&nonce, &expires)).To(Succeed())
		Expect(nonce).To(BeNil())
		Expect(expires).To(BeNil())

		By("cascading on user deletion")
		_, err = db.Exec(`DELETE FROM users WHERE id=$1`, userID)
		Expect(err).NotTo(HaveOccurred())
		var n int
		Expect(db.QueryRow(`SELECT COUNT(*) FROM agent_user_credentials`).Scan(&n)).To(Succeed())
		Expect(n).To(Equal(0))
	})
})
```

- [ ] Run to verify it fails (migration file does not exist, so migrating to 1773106020 errors):

```bash
ginkgo --focus="Create agent user credentials" ./atc/db/migration/
```

Expected failure: migrating to version 1773106020 fails (no such migration).

- [ ] Write `atc/db/migration/migrations/1773106020_create_agent_user_credentials.up.sql` (§1.3 verbatim):

```sql
CREATE TABLE agent_user_credentials (
    id               SERIAL PRIMARY KEY,
    user_id          INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_name        TEXT NOT NULL,
    kind             TEXT NOT NULL DEFAULT 'anthropic_oauth'
                     CHECK (kind IN ('anthropic_oauth', 'anthropic_api_key')),
    encrypted_token  TEXT NOT NULL,
    nonce            TEXT,
    expires_at       TIMESTAMPTZ,
    last_verified_at TIMESTAMPTZ,
    jira_account_id  TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_user_credentials_user_kind ON agent_user_credentials (user_id, kind);
```

- [ ] Write `atc/db/migration/migrations/1773106020_create_agent_user_credentials.down.sql`:

```sql
DROP TABLE agent_user_credentials;
```

- [ ] In `atc/db/migration/encryption.go`, extend `encryptedColumns` (line 11) so key rotation covers the vault:

```go
var encryptedColumns = []encryptedColumn{
	{"teams", "legacy_auth", "id"},
	{"resources", "config", "id"},
	{"jobs", "config", "id"},
	{"resource_types", "config", "id"},
	{"prototypes", "config", "id"},
	{"builds", "private_plan", "id"},
	{"cert_cache", "cert", "domain"},
	{"pipelines", "var_sources", "id"},
	{"agent_user_credentials", "encrypted_token", "id"},
}
```

- [ ] In `atc/db/migration/legacy_upgrade_test.go` line 37, bump the head constant:

```go
// JetBridge HEAD (last migration)
const jetbridgeHeadMigration = 1773106020
```

- [ ] Run to verify pass:

```bash
ginkgo --focus="Create agent user credentials" ./atc/db/migration/
```

Expected: 1 spec passing.

- [ ] Commit:

```bash
git add atc/db/migration/migrations/1773106020_create_agent_user_credentials.up.sql \
        atc/db/migration/migrations/1773106020_create_agent_user_credentials.down.sql \
        atc/db/migration/encryption.go atc/db/migration/legacy_upgrade_test.go \
        atc/db/migration/create_agent_user_credentials_test.go
git commit -m "feat(db): agent_user_credentials vault table with key-rotation coverage"
```

---

### Task 6: `atc/db` AgentUserCredentialsFactory (Store + Backend implementation)

Factory recipe from `atc/db/agent_reviews_factory.go`: interface embeds the domain contract, squirrel queries, counterfeiter directive, encryption via `f.conn.EncryptionStrategy()` (`atc/db/open.go:24`).

**Files:**
- Create: `atc/db/agent_user_credentials_factory.go`
- Test: `atc/db/agent_user_credentials_factory_test.go`

**Steps:**

- [ ] Write the failing Ginkgo test `atc/db/agent_user_credentials_factory_test.go`:

```go
package db_test

import (
	"database/sql"
	"time"

	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentUserCredentialsFactory", func() {
	var factory db.AgentUserCredentialsFactory

	BeforeEach(func() {
		factory = db.NewAgentUserCredentialsFactory(dbConn)
	})

	createUser := func(sub, name string) int {
		Expect(db.NewUserFactory(dbConn).CreateOrUpdateUser(name, "local", sub)).To(Succeed())
		var id int
		Expect(dbConn.QueryRow(`SELECT id FROM users WHERE sub = $1`, sub).Scan(&id)).To(Succeed())
		return id
	}

	It("resolves users by sub", func() {
		id := createUser("cred-sub-a", "alice")
		gotID, gotName, found, err := factory.UserBySub("cred-sub-a")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(gotID).To(Equal(id))
		Expect(gotName).To(Equal("alice"))

		_, _, found, err = factory.UserBySub("cred-sub-missing")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("round-trips a credential encrypted with the connection strategy", func() {
		id := createUser("cred-sub-b", "bob")
		exp := time.Now().Add(365 * 24 * time.Hour).Truncate(time.Second)
		Expect(factory.Put(id, "bob", "anthropic_oauth", "sk-live-token", exp)).To(Succeed())

		By("never returning the token from Status")
		status, err := factory.Status(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(status).To(HaveLen(1))
		Expect(status[0].Token).To(BeEmpty())
		Expect(status[0].Kind).To(Equal("anthropic_oauth"))
		Expect(status[0].UserName).To(Equal("bob"))
		Expect(status[0].ExpiresAt).To(Equal(exp.Unix()))

		By("decrypting via Resolve")
		cred, found, err := factory.Resolve(id, "anthropic_oauth")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(cred.Token).To(Equal("sk-live-token"))

		By("storing ciphertext consistent with the connection strategy")
		var enc string
		var nonce sql.NullString
		Expect(dbConn.QueryRow(
			`SELECT encrypted_token, nonce FROM agent_user_credentials WHERE user_id = $1 AND kind = 'anthropic_oauth'`, id,
		).Scan(&enc, &nonce)).To(Succeed())
		var noncePtr *string
		if nonce.Valid {
			noncePtr = &nonce.String
		}
		plain, err := dbConn.EncryptionStrategy().Decrypt(enc, noncePtr)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(plain)).To(Equal("sk-live-token"))
	})

	It("upserts on (user_id, kind)", func() {
		id := createUser("cred-sub-c", "carol")
		Expect(factory.Put(id, "carol", "anthropic_oauth", "tok-1", time.Time{})).To(Succeed())
		Expect(factory.Put(id, "carol", "anthropic_oauth", "tok-2", time.Time{})).To(Succeed())

		cred, found, err := factory.Resolve(id, "anthropic_oauth")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(cred.Token).To(Equal("tok-2"))
		Expect(cred.ExpiresAt).To(BeZero())

		status, _ := factory.Status(id)
		Expect(status).To(HaveLen(1))
	})

	It("lists credentials expiring within a horizon", func() {
		idSoon := createUser("cred-sub-d", "dana")
		idLater := createUser("cred-sub-e", "erin")
		Expect(factory.Put(idSoon, "dana", "anthropic_oauth", "t", time.Now().Add(24*time.Hour))).To(Succeed())
		Expect(factory.Put(idLater, "erin", "anthropic_oauth", "t", time.Now().Add(90*24*time.Hour))).To(Succeed())

		expiring, err := factory.ExpiringWithin(30 * 24 * time.Hour)
		Expect(err).ToNot(HaveOccurred())
		names := []string{}
		for _, c := range expiring {
			names = append(names, c.UserName)
		}
		Expect(names).To(ContainElement("dana"))
		Expect(names).ToNot(ContainElement("erin"))
	})

	It("deletes by kind and records the jira seam", func() {
		id := createUser("cred-sub-f", "finn")
		Expect(factory.Put(id, "finn", "anthropic_api_key", "key", time.Time{})).To(Succeed())
		Expect(factory.SetJiraAccountID(id, "acct-9")).To(Succeed())

		status, _ := factory.Status(id)
		Expect(status[0].JiraAccountID).To(Equal("acct-9"))

		Expect(factory.Delete(id, "anthropic_api_key")).To(Succeed())
		_, found, err := factory.Resolve(id, "anthropic_api_key")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
```

- [ ] Run to verify it fails:

```bash
ginkgo --focus="AgentUserCredentialsFactory" ./atc/db/
```

Expected failure: compile error `undefined: db.AgentUserCredentialsFactory`.

- [ ] Write `atc/db/agent_user_credentials_factory.go`:

```go
package db

import (
	"database/sql"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/credentials"
)

//counterfeiter:generate . AgentUserCredentialsFactory
type AgentUserCredentialsFactory interface {
	credentials.Backend
}

func NewAgentUserCredentialsFactory(conn DbConn) AgentUserCredentialsFactory {
	return &agentUserCredentialsFactory{conn: conn}
}

type agentUserCredentialsFactory struct {
	conn DbConn
}

func (f *agentUserCredentialsFactory) UserBySub(sub string) (int, string, bool, error) {
	var (
		id   int
		name string
	)
	err := f.conn.QueryRow(`SELECT id, username FROM users WHERE sub = $1`, sub).Scan(&id, &name)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return id, name, true, nil
}

func (f *agentUserCredentialsFactory) Put(userID int, userName, kind, token string, expiresAt time.Time) error {
	encrypted, nonce, err := f.conn.EncryptionStrategy().Encrypt([]byte(token))
	if err != nil {
		return err
	}
	var expires any
	if !expiresAt.IsZero() {
		expires = expiresAt
	}
	_, err = psql.Insert("agent_user_credentials").
		Columns("user_id", "user_name", "kind", "encrypted_token", "nonce", "expires_at").
		Values(userID, userName, kind, encrypted, nonce, expires).
		Suffix(`ON CONFLICT (user_id, kind) DO UPDATE SET
			user_name = EXCLUDED.user_name,
			encrypted_token = EXCLUDED.encrypted_token,
			nonce = EXCLUDED.nonce,
			expires_at = EXCLUDED.expires_at,
			updated_at = now()`).
		RunWith(f.conn).
		Exec()
	return err
}

const credentialColumns = `user_id, user_name, kind,
	COALESCE(EXTRACT(EPOCH FROM expires_at)::bigint, 0),
	COALESCE(EXTRACT(EPOCH FROM last_verified_at)::bigint, 0),
	jira_account_id`

func scanCredential(scan func(...any) error, withSecret bool) (credentials.Credential, string, *string, error) {
	var (
		cred  credentials.Credential
		enc   string
		nonce sql.NullString
	)
	dest := []any{
		&cred.UserID, &cred.UserName, &cred.Kind,
		&cred.ExpiresAt, &cred.LastVerifiedAt, &cred.JiraAccountID,
	}
	if withSecret {
		dest = append(dest, &enc, &nonce)
	}
	if err := scan(dest...); err != nil {
		return credentials.Credential{}, "", nil, err
	}
	var noncePtr *string
	if nonce.Valid {
		noncePtr = &nonce.String
	}
	return cred, enc, noncePtr, nil
}

func (f *agentUserCredentialsFactory) Status(userID int) ([]credentials.Credential, error) {
	rows, err := f.conn.Query(
		`SELECT `+credentialColumns+` FROM agent_user_credentials WHERE user_id = $1 ORDER BY kind`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []credentials.Credential{}
	for rows.Next() {
		cred, _, _, err := scanCredential(rows.Scan, false)
		if err != nil {
			return nil, err
		}
		out = append(out, cred)
	}
	return out, rows.Err()
}

func (f *agentUserCredentialsFactory) Resolve(userID int, kind string) (*credentials.Credential, bool, error) {
	row := f.conn.QueryRow(
		`SELECT `+credentialColumns+`, encrypted_token, nonce
		 FROM agent_user_credentials WHERE user_id = $1 AND kind = $2`,
		userID, kind,
	)
	cred, enc, nonce, err := scanCredential(row.Scan, true)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	plain, err := f.conn.EncryptionStrategy().Decrypt(enc, nonce)
	if err != nil {
		return nil, false, err
	}
	cred.Token = string(plain)
	return &cred, true, nil
}

func (f *agentUserCredentialsFactory) ExpiringWithin(d time.Duration) ([]credentials.Credential, error) {
	rows, err := f.conn.Query(
		`SELECT `+credentialColumns+`
		 FROM agent_user_credentials
		 WHERE expires_at IS NOT NULL AND expires_at < $1
		 ORDER BY expires_at ASC`,
		time.Now().Add(d),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []credentials.Credential{}
	for rows.Next() {
		cred, _, _, err := scanCredential(rows.Scan, false)
		if err != nil {
			return nil, err
		}
		out = append(out, cred)
	}
	return out, rows.Err()
}

func (f *agentUserCredentialsFactory) Delete(userID int, kind string) error {
	_, err := psql.Delete("agent_user_credentials").
		Where(sq.Eq{"user_id": userID, "kind": kind}).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *agentUserCredentialsFactory) SetJiraAccountID(userID int, jiraAccountID string) error {
	_, err := psql.Update("agent_user_credentials").
		Set("jira_account_id", jiraAccountID).
		Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"user_id": userID}).
		RunWith(f.conn).
		Exec()
	return err
}
```

- [ ] Run to verify pass:

```bash
ginkgo --focus="AgentUserCredentialsFactory" ./atc/db/
```

Expected: 5 specs passing.

- [ ] Regenerate counterfeiter fakes (adds `dbfakes.FakeAgentUserCredentialsFactory` and `credentialsfakes`):

```bash
go generate ./atc/db/ ./agent/credentials/
go build ./atc/... ./agent/...
```

- [ ] Commit:

```bash
git add atc/db/agent_user_credentials_factory.go atc/db/agent_user_credentials_factory_test.go \
        atc/db/dbfakes/ agent/credentials/credentialsfakes/
git commit -m "feat(db): AgentUserCredentialsFactory with encryption-at-rest via conn strategy"
```

---

### Task 7: `agent/budget` library — the single source of budget truth

Contract §2.7 `Checker` interface EXACT. Adds the `Ledger` persistence seam (implemented by `atc/db` in Task 9), `TicketBudgets` seam (real implementation arrives with ticket-core/dispatch; `NoTicketBudgets` for wave 1), source constants matching the §1.4 CHECK, and a `MemoryLedger` test double.

**Files:**
- Create: `agent/budget/budget.go`
- Create: `agent/budget/checker.go`
- Create: `agent/budget/memory.go`
- Test: `agent/budget/checker_test.go`

**Steps:**

- [ ] Write the failing test `agent/budget/checker_test.go`:

```go
package budget_test

import (
	"testing"
	"time"

	"github.com/concourse/concourse/agent/budget"
)

type fixedBudgets struct {
	budgets map[int]float64
}

func (f fixedBudgets) BudgetUSD(ticketID int) (float64, bool, error) {
	b, ok := f.budgets[ticketID]
	return b, ok, nil
}

func newChecker(t *testing.T, dailyCap float64, budgets map[int]float64, entries []budget.LedgerEntry) budget.Checker {
	t.Helper()
	ledger := budget.NewMemoryLedger()
	for _, e := range entries {
		if err := ledger.Insert(e); err != nil {
			t.Fatal(err)
		}
	}
	fixedNow := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	return budget.NewChecker(ledger, fixedBudgets{budgets}, budget.Config{
		GlobalDailyCapUSD: dailyCap,
		Location:          time.UTC,
		Now:               func() time.Time { return fixedNow },
	})
}

func ticketEntry(ticketID int, cost float64, at time.Time) budget.LedgerEntry {
	tid := ticketID
	return budget.LedgerEntry{TicketID: &tid, Source: budget.SourceAgentStep, CostUSD: cost, OccurredAt: at}
}

func TestTicketRemaining(t *testing.T) {
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 0, map[int]float64{7: 10.0}, []budget.LedgerEntry{
		ticketEntry(7, 4.0, at),
		ticketEntry(8, 99.0, at),
	})

	r, err := c.TicketRemaining(7)
	if err != nil {
		t.Fatal(err)
	}
	if r.LimitUSD != 10.0 || r.SpentUSD != 4.0 || r.RemainingUSD != 6.0 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}

	r, err = c.TicketRemaining(99) // unknown ticket -> uncapped
	if err != nil {
		t.Fatal(err)
	}
	if r.LimitUSD != 0 || r.Exhausted {
		t.Fatalf("unknown ticket must be uncapped, got %+v", r)
	}
}

func TestTicketExhausted(t *testing.T) {
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 0, map[int]float64{7: 3.0}, []budget.LedgerEntry{ticketEntry(7, 3.5, at)})
	r, err := c.TicketRemaining(7)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Exhausted || r.RemainingUSD != -0.5 {
		t.Fatalf("got %+v", r)
	}
}

func TestTicketRemainingExcludesHarvestJudgeSpend(t *testing.T) {
	// §1.13: judge spend is capped separately (workflow judge_usd) and must
	// never deplete the ticket budget — the judge runs precisely when the
	// agent may have burned everything.
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	tid := 7
	c := newChecker(t, 0, map[int]float64{7: 10.0}, []budget.LedgerEntry{
		ticketEntry(7, 4.0, at),
		{TicketID: &tid, Source: budget.SourceHarvestJudge, CostUSD: 3.0, OccurredAt: at},
	})
	r, err := c.TicketRemaining(7)
	if err != nil {
		t.Fatal(err)
	}
	if r.SpentUSD != 4.0 || r.RemainingUSD != 6.0 {
		t.Fatalf("judge spend leaked into the ticket budget: %+v", r)
	}

	// The daily window still counts ALL sources, judge included.
	capped := newChecker(t, 50, nil, []budget.LedgerEntry{
		{TicketID: &tid, Source: budget.SourceHarvestJudge, CostUSD: 3.0, OccurredAt: at},
		{Source: budget.SourceCIAgent, CostUSD: 2.0, OccurredAt: at},
	})
	daily, err := capped.GlobalDailyRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if daily.SpentUSD != 5.0 {
		t.Fatalf("daily cap must include judge spend: %+v", daily)
	}
}

func TestGlobalDailyRemaining(t *testing.T) {
	today := time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 7, 7, 23, 0, 0, 0, time.UTC)
	c := newChecker(t, 50, nil, []budget.LedgerEntry{
		{Source: budget.SourceCIAgent, CostUSD: 12.5, OccurredAt: today},
		{Source: budget.SourceCIAgent, CostUSD: 100, OccurredAt: yesterday}, // before local midnight
	})
	r, err := c.GlobalDailyRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if r.LimitUSD != 50 || r.SpentUSD != 12.5 || r.RemainingUSD != 37.5 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}

	uncapped := newChecker(t, 0, nil, []budget.LedgerEntry{{Source: budget.SourceCIAgent, CostUSD: 12.5, OccurredAt: today}})
	r, err = uncapped.GlobalDailyRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if r.LimitUSD != 0 || r.Exhausted || r.SpentUSD != 12.5 {
		t.Fatalf("cap 0 must be uncapped, got %+v", r)
	}
}

func TestStepSlice(t *testing.T) {
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 0, map[int]float64{7: 10.0}, []budget.LedgerEntry{ticketEntry(7, 8.0, at)})

	// slice smaller than ticket remaining -> slice wins
	r, err := c.StepSlice(7, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if r.RemainingUSD != 1.0 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}

	// slice larger than ticket remaining -> ticket remaining wins
	r, _ = c.StepSlice(7, 5.0)
	if r.RemainingUSD != 2.0 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}

	// zero slice -> inherit ticket remaining
	r, _ = c.StepSlice(7, 0)
	if r.LimitUSD != 10.0 || r.RemainingUSD != 2.0 {
		t.Fatalf("got %+v", r)
	}

	// uncapped ticket + explicit slice -> slice is the cap
	r, _ = c.StepSlice(42, 2.5)
	if r.LimitUSD != 2.5 || r.RemainingUSD != 2.5 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}
}

func TestStepSliceExhaustedTicket(t *testing.T) {
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 0, map[int]float64{7: 3.0}, []budget.LedgerEntry{ticketEntry(7, 3.0, at)})
	r, err := c.StepSlice(7, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Exhausted {
		t.Fatalf("expected exhausted slice, got %+v", r)
	}
}

func TestRecordValidates(t *testing.T) {
	c := newChecker(t, 0, nil, nil)
	if err := c.Record(budget.LedgerEntry{Source: "made-up", CostUSD: 1}); err == nil {
		t.Fatal("invalid source accepted")
	}
	if err := c.Record(budget.LedgerEntry{Source: budget.SourceProbe, CostUSD: -1}); err == nil {
		t.Fatal("negative cost accepted")
	}
	if err := c.Record(budget.LedgerEntry{Source: budget.SourceProbe, CostUSD: 0.25}); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
}

func TestMemoryLedgerRollup(t *testing.T) {
	ledger := budget.NewMemoryLedger()
	day1 := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	entries := []budget.LedgerEntry{
		{Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 1, Turns: 2, OccurredAt: day1},
		{Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 2, Turns: 3, OccurredAt: day2},
		{Source: budget.SourceCIAgent, UserName: "bob", CostUSD: 4, Turns: 1, OccurredAt: day2,
			Metadata: []byte(`{"workflow":"review@1"}`)},
	}
	for _, e := range entries {
		if err := ledger.Insert(e); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := ledger.Rollup(budget.GroupByDay, day1.Add(-time.Hour), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != "2026-07-07" || rows[1].CostUSD != 6 {
		t.Fatalf("day rollup: %+v", rows)
	}

	rows, _ = ledger.Rollup(budget.GroupByUser, day1.Add(-time.Hour), time.Time{})
	if len(rows) != 2 {
		t.Fatalf("user rollup: %+v", rows)
	}

	rows, _ = ledger.Rollup(budget.GroupByWorkflow, day1.Add(-time.Hour), time.Time{})
	found := false
	for _, r := range rows {
		if r.Key == "review@1" && r.CostUSD == 4 {
			found = true
		}
	}
	if !found {
		t.Fatalf("workflow rollup missing metadata key: %+v", rows)
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/budget/
```

Expected failure: compile error `undefined: budget.NewMemoryLedger` etc.

- [ ] Write `agent/budget/budget.go` (§2.7 verbatim interfaces + persistence/rollup seams):

```go
// Package budget is the single source of budget truth (contract §2.7).
// All budget arithmetic — per-ticket remaining, global daily cap, per-step
// slices — lives here and nowhere else. Consumers: dispatch (admission),
// agent-step (slice env computation), gateway (mid-flight cutoff),
// scorecards/delivery-outcomes (rollups).
//
// Sharing rule (no double counting): the dispatcher admits a run using
// TicketRemaining/GlobalDailyRemaining at dispatch time and computes each
// step's AGENT_BUDGET_SLICE_USD via StepSlice against spend already in the
// ledger. The gateway then enforces ONLY its own step's slice against the
// spend it meters for that step; it never re-checks ticket or daily budgets
// mid-flight. Every dollar enters the ledger exactly once (Record,
// append-only), so the next dispatch-time computation sees gateway-metered
// spend without any reconciliation.
package budget

import (
	"encoding/json"
	"time"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Ledger source values — must match the agent_cost_ledger CHECK constraint (§1.4).
const (
	SourceAgentStep     = "agent_step"
	SourceGateway       = "gateway"
	SourceHarvestJudge  = "harvest_judge"
	SourceRetrospective = "retrospective"
	SourceCIAgent       = "ci_agent"
	SourceProbe         = "probe"
)

func ValidSource(s string) bool {
	switch s {
	case SourceAgentStep, SourceGateway, SourceHarvestJudge,
		SourceRetrospective, SourceCIAgent, SourceProbe:
		return true
	}
	return false
}

// Rollup dimensions for GetAgentCostRollup (?group_by=).
const (
	GroupByUser     = "user"
	GroupByTicket   = "ticket"
	GroupByDay      = "day"
	GroupByWorkflow = "workflow" // reads metadata->>'workflow' (see contract addendum)
)

func ValidGroupBy(g string) bool {
	switch g {
	case GroupByUser, GroupByTicket, GroupByDay, GroupByWorkflow:
		return true
	}
	return false
}

// LedgerEntry mirrors agent_cost_ledger columns (§1.4). Zero OccurredAt
// means "let the DB default to now()". Nil UserID/TicketID/PipelineRunID
// map to NULL (cross-aggregate join keys, not FKs).
type LedgerEntry struct {
	OccurredAt          time.Time       `json:"occurred_at,omitempty"`
	UserID              *int            `json:"user_id,omitempty"`
	UserName            string          `json:"user_name,omitempty"`
	TicketID            *int            `json:"ticket_id,omitempty"`
	PipelineRunID       *int            `json:"pipeline_run_id,omitempty"`
	BuildID             int             `json:"build_id,omitempty"`
	StepName            string          `json:"step_name,omitempty"`
	Source              string          `json:"source"`
	Provider            string          `json:"provider,omitempty"`
	Model               string          `json:"model,omitempty"`
	InputTokens         int64           `json:"input_tokens,omitempty"`
	OutputTokens        int64           `json:"output_tokens,omitempty"`
	CacheReadTokens     int64           `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64           `json:"cache_creation_tokens,omitempty"`
	Turns               int             `json:"turns,omitempty"`
	CostUSD             float64         `json:"cost_usd"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

// Remaining reports budget headroom. LimitUSD == 0 means UNCAPPED (the
// same "0 = uncapped" convention as AgentStep.BudgetSliceUSD, §2.8);
// RemainingUSD is meaningless when uncapped.
type Remaining struct {
	LimitUSD     float64 `json:"limit_usd"`
	SpentUSD     float64 `json:"spent_usd"`
	RemainingUSD float64 `json:"remaining_usd"`
	Exhausted    bool    `json:"exhausted"`
}

// Checker is consulted by the dispatcher (admission), the agent step
// (slice env computation) and the gateway (mid-flight cutoff). All
// arithmetic — including "how much is left" — lives here and nowhere else.
//counterfeiter:generate . Checker
type Checker interface {
	// TicketRemaining = ticket budget − SUM(ledger cost for ticket_id),
	// where ticket budget = tickets.budget_usd ?? workflow default.
	TicketRemaining(ticketID int) (Remaining, error)
	// GlobalDailyRemaining = daily cap − SUM(ledger cost since local midnight).
	GlobalDailyRemaining() (Remaining, error)
	// StepSlice resolves an agent step's budget slice: min(step slice from
	// the workflow definition, TicketRemaining). Zero/negative = do not start.
	StepSlice(ticketID int, sliceUSD float64) (Remaining, error)
	// Record appends a ledger row (append-only).
	Record(entry LedgerEntry) error
}

// Ledger is the persistence seam implemented by
// atc/db.NewAgentCostLedgerFactory. Rollups are queries, never
// materialized mutations; rows are append-only.
//counterfeiter:generate . Ledger
type Ledger interface {
	Insert(entry LedgerEntry) error
	// SpentForTicket sums the ticket's spend EXCLUDING harvest_judge rows:
	// per contracts §1.13 the judge must never be starved by an agent that
	// burned the ticket budget (judge spend is capped separately by the
	// workflow's judge_usd).
	SpentForTicket(ticketID int) (float64, error)
	// SpentSince sums ALL sources (the global daily cap includes platform
	// and judge spend, §1.13).
	SpentSince(since time.Time) (float64, error)
	// Rollup groups by a GroupBy* dimension over [since, until); zero
	// until means unbounded.
	Rollup(groupBy string, since, until time.Time) ([]RollupRow, error)
}

// RollupRow is one aggregated line of GetAgentCostRollup.
type RollupRow struct {
	Key          string  `json:"key"`
	Entries      int     `json:"entries"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Turns        int64   `json:"turns"`
	CostUSD      float64 `json:"cost_usd"`
}

// TicketBudgets resolves "ticket budget = tickets.budget_usd ?? workflow
// default". Wave 1 has no tickets table, so NoTicketBudgets stands in;
// ticket-core/dispatch supply the real implementation without this
// package changing.
//counterfeiter:generate . TicketBudgets
type TicketBudgets interface {
	BudgetUSD(ticketID int) (float64, bool, error)
}

// NoTicketBudgets reports every ticket as having no configured budget
// (uncapped). Wave-1 wiring only.
type NoTicketBudgets struct{}

func (NoTicketBudgets) BudgetUSD(int) (float64, bool, error) { return 0, false, nil }

// Config tunes a Checker. GlobalDailyCapUSD comes from the web flag
// --agent-daily-budget-usd (0 = unlimited). Location defines "local
// midnight" for the daily window (nil = time.Local).
type Config struct {
	GlobalDailyCapUSD float64
	Location          *time.Location
	Now               func() time.Time
}
```

- [ ] Write `agent/budget/checker.go`:

```go
package budget

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrInvalidEntry marks Record validation failures so HTTP handlers can
// map them to 400 rather than 500.
var ErrInvalidEntry = errors.New("invalid ledger entry")

func NewChecker(ledger Ledger, budgets TicketBudgets, cfg Config) Checker {
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &checker{ledger: ledger, budgets: budgets, cfg: cfg}
}

type checker struct {
	ledger  Ledger
	budgets TicketBudgets
	cfg     Config
}

func (c *checker) TicketRemaining(ticketID int) (Remaining, error) {
	spent, err := c.ledger.SpentForTicket(ticketID)
	if err != nil {
		return Remaining{}, err
	}
	limit, found, err := c.budgets.BudgetUSD(ticketID)
	if err != nil {
		return Remaining{}, err
	}
	if !found || limit <= 0 {
		return Remaining{SpentUSD: spent}, nil // uncapped
	}
	remaining := limit - spent
	return Remaining{
		LimitUSD:     limit,
		SpentUSD:     spent,
		RemainingUSD: remaining,
		Exhausted:    remaining <= 0,
	}, nil
}

func (c *checker) GlobalDailyRemaining() (Remaining, error) {
	now := c.cfg.Now().In(c.cfg.Location)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.cfg.Location)
	spent, err := c.ledger.SpentSince(midnight)
	if err != nil {
		return Remaining{}, err
	}
	if c.cfg.GlobalDailyCapUSD <= 0 {
		return Remaining{SpentUSD: spent}, nil // uncapped
	}
	remaining := c.cfg.GlobalDailyCapUSD - spent
	return Remaining{
		LimitUSD:     c.cfg.GlobalDailyCapUSD,
		SpentUSD:     spent,
		RemainingUSD: remaining,
		Exhausted:    remaining <= 0,
	}, nil
}

func (c *checker) StepSlice(ticketID int, sliceUSD float64) (Remaining, error) {
	ticket, err := c.TicketRemaining(ticketID)
	if err != nil {
		return Remaining{}, err
	}
	if sliceUSD <= 0 {
		// No per-step slice declared: the step inherits whatever the
		// ticket has left (possibly uncapped).
		return ticket, nil
	}
	if ticket.LimitUSD == 0 {
		// Uncapped ticket: the slice itself is the only cap.
		return Remaining{LimitUSD: sliceUSD, RemainingUSD: sliceUSD}, nil
	}
	allowed := math.Min(sliceUSD, ticket.RemainingUSD)
	return Remaining{
		LimitUSD:     sliceUSD,
		SpentUSD:     ticket.SpentUSD,
		RemainingUSD: allowed,
		Exhausted:    allowed <= 0,
	}, nil
}

func (c *checker) Record(entry LedgerEntry) error {
	if !ValidSource(entry.Source) {
		return fmt.Errorf("source %q: %w", entry.Source, ErrInvalidEntry)
	}
	if entry.CostUSD < 0 {
		return fmt.Errorf("negative cost_usd: %w", ErrInvalidEntry)
	}
	return c.ledger.Insert(entry)
}
```

- [ ] Write `agent/budget/memory.go`:

```go
package budget

import (
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"
)

// MemoryLedger is an in-memory Ledger for tests and the api suite.
type MemoryLedger struct {
	mu      sync.Mutex
	entries []LedgerEntry
}

func NewMemoryLedger() *MemoryLedger {
	return &MemoryLedger{}
}

func (m *MemoryLedger) Insert(entry LedgerEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = time.Now()
	}
	m.entries = append(m.entries, entry)
	return nil
}

func (m *MemoryLedger) SpentForTicket(ticketID int) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum float64
	for _, e := range m.entries {
		// harvest_judge spend never depletes the ticket budget (§1.13).
		if e.TicketID != nil && *e.TicketID == ticketID && e.Source != SourceHarvestJudge {
			sum += e.CostUSD
		}
	}
	return sum, nil
}

func (m *MemoryLedger) SpentSince(since time.Time) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum float64
	for _, e := range m.entries {
		if !e.OccurredAt.Before(since) {
			sum += e.CostUSD
		}
	}
	return sum, nil
}

func (m *MemoryLedger) Rollup(groupBy string, since, until time.Time) ([]RollupRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byKey := map[string]*RollupRow{}
	for _, e := range m.entries {
		if e.OccurredAt.Before(since) {
			continue
		}
		if !until.IsZero() && !e.OccurredAt.Before(until) {
			continue
		}
		var key string
		switch groupBy {
		case GroupByUser:
			key = e.UserName
		case GroupByTicket:
			if e.TicketID != nil {
				key = strconv.Itoa(*e.TicketID)
			}
		case GroupByWorkflow:
			var meta struct {
				Workflow string `json:"workflow"`
			}
			if len(e.Metadata) > 0 {
				_ = json.Unmarshal(e.Metadata, &meta)
			}
			key = meta.Workflow
		default: // GroupByDay
			key = e.OccurredAt.UTC().Format("2006-01-02")
		}
		row := byKey[key]
		if row == nil {
			row = &RollupRow{Key: key}
			byKey[key] = row
		}
		row.Entries++
		row.InputTokens += e.InputTokens
		row.OutputTokens += e.OutputTokens
		row.Turns += int64(e.Turns)
		row.CostUSD += e.CostUSD
	}
	out := []RollupRow{}
	for _, row := range byKey {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
```

- [ ] Run to verify pass, then generate fakes:

```bash
go test ./agent/budget/
go generate ./agent/budget/
go build ./agent/...
```

Expected: tests `ok`; `agent/budget/budgetfakes/` created.

- [ ] Commit:

```bash
git add agent/budget/
git commit -m "feat(agent): budget library - checker arithmetic, ledger seam, memory ledger"
```

---

### Task 8: Migration 1773106021 — `agent_cost_ledger`

SQL is §1.4 verbatim: append-only, NULLABLE `ticket_id` join key (no FK — tickets land in wave 2), CHECK-constrained `source`.

**Files:**
- Create: `atc/db/migration/migrations/1773106021_create_agent_cost_ledger.up.sql`
- Create: `atc/db/migration/migrations/1773106021_create_agent_cost_ledger.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37`
- Test: `atc/db/migration/create_agent_cost_ledger_test.go`

**Steps:**

- [ ] Write the failing migration test `atc/db/migration/create_agent_cost_ledger_test.go`:

```go
package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Create agent cost ledger", func() {
	const postMigrationVersion = 1773106021

	It("creates the append-only ledger with nullable join keys and source check", func() {
		db := postgresRunner.OpenDBAtVersion(postMigrationVersion)
		defer db.Close()

		By("accepting a minimal row with NULL user/ticket/run join keys")
		_, err := db.Exec(`INSERT INTO agent_cost_ledger(source, cost_usd) VALUES('ci_agent', 0.123456)`)
		Expect(err).NotTo(HaveOccurred())

		By("accepting every contract source value")
		for _, source := range []string{"agent_step", "gateway", "harvest_judge", "retrospective", "ci_agent", "probe"} {
			_, err := db.Exec(`INSERT INTO agent_cost_ledger(source, cost_usd) VALUES($1, 0)`, source)
			Expect(err).NotTo(HaveOccurred(), source)
		}

		By("rejecting unknown sources via CHECK")
		_, err = db.Exec(`INSERT INTO agent_cost_ledger(source, cost_usd) VALUES('slack', 0)`)
		Expect(err).To(HaveOccurred())

		By("accepting a ticket-scoped row before agent_tickets exists (plain column, no FK)")
		_, err = db.Exec(`INSERT INTO agent_cost_ledger(source, ticket_id, cost_usd) VALUES('agent_step', 42, 1.5)`)
		Expect(err).NotTo(HaveOccurred())

		By("preserving NUMERIC(12,6) precision")
		var cost string
		Expect(db.QueryRow(`SELECT cost_usd::text FROM agent_cost_ledger WHERE cost_usd = 0.123456`).
			Scan(&cost)).To(Succeed())
		Expect(cost).To(Equal("0.123456"))
	})
})
```

- [ ] Run to verify it fails:

```bash
ginkgo --focus="Create agent cost ledger" ./atc/db/migration/
```

Expected failure: migrating to version 1773106021 fails (no such migration).

- [ ] Write `atc/db/migration/migrations/1773106021_create_agent_cost_ledger.up.sql` (§1.4 verbatim):

```sql
CREATE TABLE agent_cost_ledger (
    id                    BIGSERIAL PRIMARY KEY,
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id               INTEGER,
    user_name             TEXT NOT NULL DEFAULT '',
    ticket_id             INTEGER,
    pipeline_run_id       INTEGER,
    build_id              INTEGER NOT NULL DEFAULT 0,
    step_name             TEXT NOT NULL DEFAULT '',
    source                TEXT NOT NULL
                          CHECK (source IN ('agent_step','gateway','harvest_judge','retrospective','ci_agent','probe')),
    provider              TEXT NOT NULL DEFAULT 'anthropic',
    model                 TEXT NOT NULL DEFAULT '',
    input_tokens          BIGINT NOT NULL DEFAULT 0,
    output_tokens         BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    turns                 INTEGER NOT NULL DEFAULT 0,
    cost_usd              NUMERIC(12,6) NOT NULL DEFAULT 0,
    metadata              JSONB
);

CREATE INDEX agent_cost_ledger_user_day ON agent_cost_ledger (user_id, occurred_at DESC);
CREATE INDEX agent_cost_ledger_ticket   ON agent_cost_ledger (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_cost_ledger_day      ON agent_cost_ledger (occurred_at DESC);
```

- [ ] Write `atc/db/migration/migrations/1773106021_create_agent_cost_ledger.down.sql`:

```sql
DROP TABLE agent_cost_ledger;
```

- [ ] Bump `atc/db/migration/legacy_upgrade_test.go:37`:

```go
const jetbridgeHeadMigration = 1773106021
```

- [ ] Run to verify pass:

```bash
ginkgo --focus="Create agent cost ledger" ./atc/db/migration/
```

Expected: 1 spec passing.

- [ ] Commit:

```bash
git add atc/db/migration/migrations/1773106021_create_agent_cost_ledger.up.sql \
        atc/db/migration/migrations/1773106021_create_agent_cost_ledger.down.sql \
        atc/db/migration/legacy_upgrade_test.go atc/db/migration/create_agent_cost_ledger_test.go
git commit -m "feat(db): agent_cost_ledger append-only spend table"
```

---

### Task 9: `atc/db` AgentCostLedgerFactory implementing `budget.Ledger`

**Files:**
- Create: `atc/db/agent_cost_ledger_factory.go`
- Test: `atc/db/agent_cost_ledger_factory_test.go`

**Steps:**

- [ ] Write the failing Ginkgo test `atc/db/agent_cost_ledger_factory_test.go`:

```go
package db_test

import (
	"encoding/json"
	"time"

	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentCostLedgerFactory", func() {
	var ledger db.AgentCostLedgerFactory

	BeforeEach(func() {
		ledger = db.NewAgentCostLedgerFactory(dbConn)
		_, err := dbConn.Exec(`DELETE FROM agent_cost_ledger`)
		Expect(err).ToNot(HaveOccurred())
	})

	intPtr := func(i int) *int { return &i }

	It("inserts and sums per ticket, excluding harvest_judge spend (§1.13)", func() {
		at := time.Now()
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceAgentStep, TicketID: intPtr(7), CostUSD: 1.25,
			InputTokens: 100, OutputTokens: 50, Turns: 3, Model: "claude-sonnet-5",
			UserName: "alice", OccurredAt: at,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceGateway, TicketID: intPtr(7), CostUSD: 0.75, OccurredAt: at,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceHarvestJudge, TicketID: intPtr(7), CostUSD: 0.5, OccurredAt: at, // excluded from ticket sums
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceCIAgent, CostUSD: 9.0, OccurredAt: at, // no ticket
		})).To(Succeed())

		spent, err := ledger.SpentForTicket(7)
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeNumerically("~", 2.0, 1e-9))

		By("still counting judge spend toward time-window sums (daily cap)")
		windowSpent, err := ledger.SpentSince(at.Add(-time.Minute))
		Expect(err).ToNot(HaveOccurred())
		Expect(windowSpent).To(BeNumerically("~", 11.5, 1e-9))

		spent, err = ledger.SpentForTicket(999)
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeZero())
	})

	It("defaults occurred_at to now and sums since a cutoff", func() {
		old := time.Now().Add(-48 * time.Hour)
		Expect(ledger.Insert(budget.LedgerEntry{Source: budget.SourceProbe, CostUSD: 5, OccurredAt: old})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{Source: budget.SourceProbe, CostUSD: 2})).To(Succeed()) // zero -> now()

		spent, err := ledger.SpentSince(time.Now().Add(-time.Hour))
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeNumerically("~", 2.0, 1e-9))

		spent, err = ledger.SpentSince(time.Now().Add(-72 * time.Hour))
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeNumerically("~", 7.0, 1e-9))
	})

	It("rolls up by day, user, ticket, and workflow metadata", func() {
		day1 := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
		day2 := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 1, Turns: 2,
			InputTokens: 10, OccurredAt: day1,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 2, OccurredAt: day2,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceAgentStep, UserName: "bob", TicketID: intPtr(42), CostUSD: 4, OccurredAt: day2,
			Metadata: json.RawMessage(`{"workflow":"review@1"}`),
		})).To(Succeed())

		since := day1.Add(-time.Hour)

		rows, err := ledger.Rollup(budget.GroupByDay, since, time.Time{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0].Key).To(Equal("2026-07-07"))
		Expect(rows[0].Entries).To(Equal(1))
		Expect(rows[0].InputTokens).To(Equal(int64(10)))
		Expect(rows[1].CostUSD).To(BeNumerically("~", 6.0, 1e-9))

		rows, err = ledger.Rollup(budget.GroupByUser, since, time.Time{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2)) // alice, bob

		rows, err = ledger.Rollup(budget.GroupByTicket, since, time.Time{})
		Expect(err).ToNot(HaveOccurred())
		keys := []string{}
		for _, r := range rows {
			keys = append(keys, r.Key)
		}
		Expect(keys).To(ContainElement("42"))

		rows, err = ledger.Rollup(budget.GroupByWorkflow, since, time.Time{})
		Expect(err).ToNot(HaveOccurred())
		found := false
		for _, r := range rows {
			if r.Key == "review@1" {
				found = true
				Expect(r.CostUSD).To(BeNumerically("~", 4.0, 1e-9))
			}
		}
		Expect(found).To(BeTrue())

		By("bounding with until")
		rows, err = ledger.Rollup(budget.GroupByDay, since, day2.Add(-time.Hour))
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))

		By("rejecting unknown group_by")
		_, err = ledger.Rollup("nonsense", since, time.Time{})
		Expect(err).To(HaveOccurred())
	})
})
```

Note: the suite runs in parallel; the `DELETE FROM agent_cost_ledger` in BeforeEach keeps these specs self-consistent because only this file touches the table.

- [ ] Run to verify it fails:

```bash
ginkgo --focus="AgentCostLedgerFactory" ./atc/db/
```

Expected failure: compile error `undefined: db.AgentCostLedgerFactory`.

- [ ] Write `atc/db/agent_cost_ledger_factory.go`:

```go
package db

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/budget"
)

//counterfeiter:generate . AgentCostLedgerFactory
type AgentCostLedgerFactory interface {
	budget.Ledger
}

func NewAgentCostLedgerFactory(conn DbConn) AgentCostLedgerFactory {
	return &agentCostLedgerFactory{conn: conn}
}

type agentCostLedgerFactory struct {
	conn DbConn
}

func (f *agentCostLedgerFactory) Insert(entry budget.LedgerEntry) error {
	var occurred any = sq.Expr("now()")
	if !entry.OccurredAt.IsZero() {
		occurred = entry.OccurredAt
	}
	provider := entry.Provider
	if provider == "" {
		provider = "anthropic"
	}
	var metadata any
	if len(entry.Metadata) > 0 {
		metadata = []byte(entry.Metadata)
	}
	_, err := psql.Insert("agent_cost_ledger").
		Columns(
			"occurred_at", "user_id", "user_name", "ticket_id", "pipeline_run_id",
			"build_id", "step_name", "source", "provider", "model",
			"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
			"turns", "cost_usd", "metadata",
		).
		Values(
			occurred, entry.UserID, entry.UserName, entry.TicketID, entry.PipelineRunID,
			entry.BuildID, entry.StepName, entry.Source, provider, entry.Model,
			entry.InputTokens, entry.OutputTokens, entry.CacheReadTokens, entry.CacheCreationTokens,
			entry.Turns, entry.CostUSD, metadata,
		).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *agentCostLedgerFactory) SpentForTicket(ticketID int) (float64, error) {
	// harvest_judge spend never depletes the ticket budget (§1.13); the
	// judge is capped separately by the workflow's judge_usd.
	var spent float64
	err := f.conn.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0)::float8 FROM agent_cost_ledger
		 WHERE ticket_id = $1 AND source <> 'harvest_judge'`,
		ticketID,
	).Scan(&spent)
	return spent, err
}

func (f *agentCostLedgerFactory) SpentSince(since time.Time) (float64, error) {
	var spent float64
	err := f.conn.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0)::float8 FROM agent_cost_ledger WHERE occurred_at >= $1`,
		since,
	).Scan(&spent)
	return spent, err
}

func (f *agentCostLedgerFactory) Rollup(groupBy string, since, until time.Time) ([]budget.RollupRow, error) {
	var keyExpr string
	switch groupBy {
	case budget.GroupByUser:
		keyExpr = `COALESCE(user_name, '')`
	case budget.GroupByTicket:
		keyExpr = `COALESCE(ticket_id::text, '')`
	case budget.GroupByWorkflow:
		// Contract addendum: workflow attribution rides metadata->>'workflow'.
		keyExpr = `COALESCE(metadata->>'workflow', '')`
	case budget.GroupByDay:
		keyExpr = `to_char((occurred_at AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD')`
	default:
		return nil, fmt.Errorf("unsupported group_by %q", groupBy)
	}

	query := `SELECT ` + keyExpr + ` AS key,
		COUNT(*)::int,
		COALESCE(SUM(input_tokens), 0)::bigint,
		COALESCE(SUM(output_tokens), 0)::bigint,
		COALESCE(SUM(turns), 0)::bigint,
		COALESCE(SUM(cost_usd), 0)::float8
		FROM agent_cost_ledger
		WHERE occurred_at >= $1`
	args := []any{since}
	if !until.IsZero() {
		args = append(args, until)
		query += ` AND occurred_at < $2`
	}
	query += ` GROUP BY 1 ORDER BY 1`

	rows, err := f.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []budget.RollupRow{}
	for rows.Next() {
		var row budget.RollupRow
		if err := rows.Scan(&row.Key, &row.Entries, &row.InputTokens, &row.OutputTokens, &row.Turns, &row.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
```

- [ ] Run to verify pass, regenerate dbfakes:

```bash
ginkgo --focus="AgentCostLedgerFactory" ./atc/db/
go generate ./atc/db/
```

Expected: 3 specs passing.

- [ ] Commit:

```bash
git add atc/db/agent_cost_ledger_factory.go atc/db/agent_cost_ledger_factory_test.go atc/db/dbfakes/
git commit -m "feat(db): AgentCostLedgerFactory implementing budget.Ledger with rollups"
```

---

### Task 10: Migration 1773106022 — platform service user seed + dashboard view

Implements §1.13's service user (`sub='agent-platform'`, `connector='local'`, `username='platform'`) and the "dashboard view" deliverable as a SQL view over the ledger (queryable from Grafana/psql without app code).

**Files:**
- Create: `atc/db/migration/migrations/1773106022_seed_agent_platform_user_and_cost_view.up.sql`
- Create: `atc/db/migration/migrations/1773106022_seed_agent_platform_user_and_cost_view.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37`
- Test: `atc/db/migration/seed_agent_platform_user_and_cost_view_test.go`

**Steps:**

- [ ] Write the failing migration test `atc/db/migration/seed_agent_platform_user_and_cost_view_test.go`:

```go
package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Seed agent platform user and cost view", func() {
	const postMigrationVersion = 1773106022

	It("seeds the platform service user and creates the daily rollup view", func() {
		db := postgresRunner.OpenDBAtVersion(postMigrationVersion)
		defer db.Close()

		By("seeding the §1.13 service user")
		var username, connector string
		Expect(db.QueryRow(`SELECT username, connector FROM users WHERE sub = 'agent-platform'`).
			Scan(&username, &connector)).To(Succeed())
		Expect(username).To(Equal("platform"))
		Expect(connector).To(Equal("local"))

		By("aggregating ledger rows per day/user/source in the view")
		_, err := db.Exec(`INSERT INTO agent_cost_ledger(source, user_name, cost_usd, turns, occurred_at)
			VALUES ('ci_agent', 'alice', 1.5, 2, '2026-07-08T10:00:00Z'),
			       ('ci_agent', 'alice', 0.5, 1, '2026-07-08T11:00:00Z')`)
		Expect(err).NotTo(HaveOccurred())

		var entries, turns int
		var cost float64
		Expect(db.QueryRow(`SELECT entries, turns, cost_usd::float8 FROM agent_cost_daily_rollup
			WHERE day = '2026-07-08' AND user_name = 'alice' AND source = 'ci_agent'`).
			Scan(&entries, &turns, &cost)).To(Succeed())
		Expect(entries).To(Equal(2))
		Expect(turns).To(Equal(3))
		Expect(cost).To(BeNumerically("~", 2.0, 1e-9))
	})
})
```

- [ ] Run to verify it fails:

```bash
ginkgo --focus="Seed agent platform user and cost view" ./atc/db/migration/
```

Expected failure: migrating to 1773106022 fails (no such migration).

- [ ] Write `atc/db/migration/migrations/1773106022_seed_agent_platform_user_and_cost_view.up.sql`:

```sql
-- §1.13: dedicated service user that owns the platform Anthropic
-- credential funding platform-initiated LLM work (harvest judge,
-- retrospective agent, calibration jobs).
INSERT INTO users (username, connector, sub)
VALUES ('platform', 'local', 'agent-platform')
ON CONFLICT (sub) DO NOTHING;

-- Dashboard view over the append-only ledger: per UTC-day, user, source.
CREATE VIEW agent_cost_daily_rollup AS
SELECT
    (occurred_at AT TIME ZONE 'UTC')::date AS day,
    COALESCE(user_name, '')                AS user_name,
    source,
    COUNT(*)::int                          AS entries,
    SUM(input_tokens)                      AS input_tokens,
    SUM(output_tokens)                     AS output_tokens,
    SUM(turns)::int                        AS turns,
    SUM(cost_usd)                          AS cost_usd
FROM agent_cost_ledger
GROUP BY 1, 2, 3;
```

- [ ] Write `atc/db/migration/migrations/1773106022_seed_agent_platform_user_and_cost_view.down.sql`:

```sql
DROP VIEW agent_cost_daily_rollup;
DELETE FROM users WHERE sub = 'agent-platform' AND connector = 'local';
```

- [ ] Bump `atc/db/migration/legacy_upgrade_test.go:37`:

```go
const jetbridgeHeadMigration = 1773106022
```

- [ ] Run to verify pass:

```bash
ginkgo --focus="Seed agent platform user and cost view" ./atc/db/migration/
```

Expected: 1 spec passing.

- [ ] Commit:

```bash
git add atc/db/migration/migrations/1773106022_seed_agent_platform_user_and_cost_view.up.sql \
        atc/db/migration/migrations/1773106022_seed_agent_platform_user_and_cost_view.down.sql \
        atc/db/migration/legacy_upgrade_test.go \
        atc/db/migration/seed_agent_platform_user_and_cost_view_test.go
git commit -m "feat(db): seed agent-platform service user and cost dashboard view"
```

---

### Task 11: `agent/api/costs` HTTP handler (submit + rollup)

`POST /api/v1/agent/costs` (interim static-token auth, exactly the `reviews.SubmitReview` recipe at `agent/api/reviews/handler.go:69-79`) and `GET /api/v1/agent/costs` rollup with the daily-budget summary. Pure stdlib + `agent/budget` — no atc imports (layering rule).

**Files:**
- Create: `agent/api/costs/handler.go`
- Test: `agent/api/costs/handler_test.go`

**Steps:**

- [ ] Write the failing test `agent/api/costs/handler_test.go` (plain Go + httptest, the reviews-handler style):

```go
package costs_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/agent/budget"
)

func newHandler() (*costs.Handler, *budget.MemoryLedger) {
	ledger := budget.NewMemoryLedger()
	checker := budget.NewChecker(ledger, budget.NoTicketBudgets{}, budget.Config{
		GlobalDailyCapUSD: 50,
		Location:          time.UTC,
	})
	return costs.NewHandler(ledger, checker, "publish-secret"), ledger
}

func submit(t *testing.T, h *costs.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agent/costs", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.SubmitRecord(rec, req)
	return rec
}

func TestSubmitRequiresToken(t *testing.T) {
	h, _ := newHandler()
	if rec := submit(t, h, "", `{"source":"ci_agent","cost_usd":1}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d", rec.Code)
	}
	if rec := submit(t, h, "wrong", `{"source":"ci_agent","cost_usd":1}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", rec.Code)
	}
}

func TestSubmitDisabledWithoutConfiguredToken(t *testing.T) {
	ledger := budget.NewMemoryLedger()
	checker := budget.NewChecker(ledger, budget.NoTicketBudgets{}, budget.Config{Location: time.UTC})
	h := costs.NewHandler(ledger, checker, "")
	if rec := submit(t, h, "anything", `{"source":"ci_agent","cost_usd":1}`); rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestSubmitRecordsEntry(t *testing.T) {
	h, ledger := newHandler()
	rec := submit(t, h, "publish-secret",
		`{"source":"ci_agent","cost_usd":0.42,"user_name":"alice","build_id":1234,"step_name":"review/analyze","model":"claude-sonnet-5","input_tokens":100,"output_tokens":50,"turns":4}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	spent, _ := ledger.SpentSince(time.Now().Add(-time.Minute))
	if spent != 0.42 {
		t.Fatalf("ledger spent = %v", spent)
	}
}

func TestSubmitRejectsInvalidEntries(t *testing.T) {
	h, _ := newHandler()
	if rec := submit(t, h, "publish-secret", `{"source":"slack","cost_usd":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad source: got %d", rec.Code)
	}
	if rec := submit(t, h, "publish-secret", `{"source":"ci_agent","cost_usd":-1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative cost: got %d", rec.Code)
	}
	if rec := submit(t, h, "publish-secret", `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: got %d", rec.Code)
	}
}

func TestGetRollup(t *testing.T) {
	h, ledger := newHandler()
	now := time.Now().UTC()
	_ = ledger.Insert(budget.LedgerEntry{Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 2, OccurredAt: now})
	_ = ledger.Insert(budget.LedgerEntry{Source: budget.SourceCIAgent, UserName: "bob", CostUSD: 3, OccurredAt: now})

	req := httptest.NewRequest("GET", "/api/v1/agent/costs?group_by=user", nil)
	rec := httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var resp costs.RollupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.GroupBy != "user" || len(resp.Rows) != 2 {
		t.Fatalf("resp: %+v", resp)
	}
	if resp.Summary.CapUSD != 50 || resp.Summary.SpentUSD != 5 || resp.Summary.RemainingUSD != 45 {
		t.Fatalf("summary: %+v", resp.Summary)
	}
}

func TestGetRollupDefaultsAndValidation(t *testing.T) {
	h, _ := newHandler()

	req := httptest.NewRequest("GET", "/api/v1/agent/costs", nil)
	rec := httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default group_by: got %d", rec.Code)
	}
	var resp costs.RollupResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.GroupBy != "day" || resp.Rows == nil {
		t.Fatalf("resp: %+v", resp)
	}

	req = httptest.NewRequest("GET", "/api/v1/agent/costs?group_by=nonsense", nil)
	rec = httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad group_by: got %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/v1/agent/costs?since=garbage", nil)
	rec = httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad since: got %d", rec.Code)
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/api/costs/
```

Expected failure: compile error `undefined: costs.NewHandler`.

- [ ] Write `agent/api/costs/handler.go`:

```go
// Package costs serves the agent cost-ledger API: POST /api/v1/agent/costs
// (SubmitAgentCostRecord — interim static-token auth per the wave-1
// contract addendum; agent-identity flips it to principal(costs:write))
// and GET /api/v1/agent/costs (GetAgentCostRollup).
package costs

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/budget"
)

type Handler struct {
	ledger       budget.Ledger
	checker      budget.Checker
	publishToken string
}

func NewHandler(ledger budget.Ledger, checker budget.Checker, publishToken string) *Handler {
	return &Handler{ledger: ledger, checker: checker, publishToken: publishToken}
}

// DailySummary reports today's spend against --agent-daily-budget-usd.
// CapUSD == 0 means uncapped.
type DailySummary struct {
	CapUSD       float64 `json:"daily_cap_usd"`
	SpentUSD     float64 `json:"daily_spent_usd"`
	RemainingUSD float64 `json:"daily_remaining_usd"`
	Exhausted    bool    `json:"daily_exhausted"`
}

// RollupResponse is the GET /api/v1/agent/costs body.
type RollupResponse struct {
	GroupBy string             `json:"group_by"`
	Summary DailySummary       `json:"summary"`
	Rows    []budget.RollupRow `json:"rows"`
}

// SubmitRecord handles POST /api/v1/agent/costs. Auth recipe mirrors
// reviews.Handler.SubmitReview: a static bearer token validated in the
// handler (the route is a wrappa pass-through until agent-identity's
// principal tier lands).
func (h *Handler) SubmitRecord(w http.ResponseWriter, r *http.Request) {
	if h.publishToken == "" {
		http.Error(w, "agent cost recording is not enabled", http.StatusForbidden)
		return
	}
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(h.publishToken)) != 1 {
		http.Error(w, "invalid publish token", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "request body exceeds 1MB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var entry budget.LedgerEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := h.checker.Record(entry); err != nil {
		if errors.Is(err, budget.ErrInvalidEntry) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "recorded"})
}

// GetRollup handles GET /api/v1/agent/costs
// (?group_by=user|ticket|day|workflow&since=&until=).
func (h *Handler) GetRollup(w http.ResponseWriter, r *http.Request) {
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = budget.GroupByDay
	}
	if !budget.ValidGroupBy(groupBy) {
		http.Error(w, fmt.Sprintf("group_by must be one of user|ticket|day|workflow, got %q", groupBy), http.StatusBadRequest)
		return
	}

	since, err := parseTimeParam(r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, "invalid since: "+err.Error(), http.StatusBadRequest)
		return
	}
	if since.IsZero() {
		since = time.Now().Add(-30 * 24 * time.Hour)
	}
	until, err := parseTimeParam(r.URL.Query().Get("until"))
	if err != nil {
		http.Error(w, "invalid until: "+err.Error(), http.StatusBadRequest)
		return
	}

	rows, err := h.ledger.Rollup(groupBy, since, until)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []budget.RollupRow{}
	}

	resp := RollupResponse{GroupBy: groupBy, Rows: rows}
	// Degrade: the summary must never block the rollup.
	if daily, err := h.checker.GlobalDailyRemaining(); err == nil {
		resp.Summary = DailySummary{
			CapUSD:       daily.LimitUSD,
			SpentUSD:     daily.SpentUSD,
			RemainingUSD: daily.RemainingUSD,
			Exhausted:    daily.Exhausted,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// parseTimeParam accepts RFC3339 or YYYY-MM-DD; empty means zero time.
func parseTimeParam(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("want RFC3339 or YYYY-MM-DD, got %q", v)
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/api/costs/
```

Expected: `ok`.

- [ ] Commit:

```bash
git add agent/api/costs/
git commit -m "feat(agent): costs API handler - submit record + rollup with daily summary"
```

---

### Task 12: `agent/credentials` HTTP handler

PUT/GET/DELETE `/api/v1/agent/user-credentials`. "Self only": the handler resolves the caller's `users` row from token claims via an injected `ClaimsFunc` (the `reviews.BuildLookupFunc` seam pattern — keeps this package free of `atc/api/accessor`, which would cycle through `atc/db`). One deliberate exception (contract addendum): admins may target the §1.13 `agent-platform` service user (`"user":"platform"` / `?user=platform`), because that user never logs in and no self-scoped path could otherwise vault the platform credential.

**Files:**
- Create: `agent/credentials/handler.go`
- Test: `agent/credentials/handler_test.go`

**Steps:**

- [ ] Write the failing test `agent/credentials/handler_test.go`:

```go
package credentials_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/credentials"
)

func newCredHandler() (*credentials.Handler, *credentials.MemoryBackend) {
	backend := credentials.NewMemoryBackend()
	backend.AddUser("sub-alice", 7, "alice")
	backend.AddUser(credentials.PlatformUserSub, 99, "platform")
	claims := func(r *http.Request) (string, string, bool, bool) {
		sub := r.Header.Get("X-Test-Sub")
		isAdmin := r.Header.Get("X-Test-Admin") == "true"
		return sub, "alice", isAdmin, sub != ""
	}
	return credentials.NewHandler(backend, claims), backend
}

func TestSetStoresCredentialForSelf(t *testing.T) {
	h, backend := newCredHandler()
	exp := time.Now().Add(365 * 24 * time.Hour).Unix()
	body := `{"kind":"anthropic_oauth","token":"sk-tok","expires_at":` + jsonInt(exp) + `,"jira_account_id":"acct-1"}`
	req := httptest.NewRequest("PUT", "/api/v1/agent/user-credentials", strings.NewReader(body))
	req.Header.Set("X-Test-Sub", "sub-alice")
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	cred, found, _ := backend.Resolve(7, "anthropic_oauth")
	if !found || cred.Token != "sk-tok" || cred.ExpiresAt != exp {
		t.Fatalf("stored: %+v found=%v", cred, found)
	}
	status, _ := backend.Status(7)
	if status[0].JiraAccountID != "acct-1" {
		t.Fatalf("jira seam not stored: %+v", status[0])
	}
}

func TestSetRejectsUnknownUserAndBadBodies(t *testing.T) {
	h, _ := newCredHandler()

	req := httptest.NewRequest("PUT", "/api/v1/agent/user-credentials",
		strings.NewReader(`{"kind":"anthropic_oauth","token":"t"}`))
	rec := httptest.NewRecorder()
	h.Set(rec, req) // no claims header -> unauthenticated
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no claims: got %d", rec.Code)
	}

	req = httptest.NewRequest("PUT", "/api/v1/agent/user-credentials",
		strings.NewReader(`{"kind":"anthropic_oauth","token":"t"}`))
	req.Header.Set("X-Test-Sub", "sub-never-logged-in")
	rec = httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user: got %d", rec.Code)
	}

	for _, body := range []string{`{"kind":"openai","token":"t"}`, `{"kind":"anthropic_oauth","token":""}`, `nope`} {
		req = httptest.NewRequest("PUT", "/api/v1/agent/user-credentials", strings.NewReader(body))
		req.Header.Set("X-Test-Sub", "sub-alice")
		rec = httptest.NewRecorder()
		h.Set(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: got %d", body, rec.Code)
		}
	}
}

func TestStatusReturnsOnlySelfWithoutTokens(t *testing.T) {
	h, backend := newCredHandler()
	backend.AddUser("sub-bob", 8, "bob")
	_ = backend.Put(7, "alice", "anthropic_oauth", "sk-a", time.Now().Add(time.Hour))
	_ = backend.Put(8, "bob", "anthropic_oauth", "sk-b", time.Time{})

	req := httptest.NewRequest("GET", "/api/v1/agent/user-credentials", nil)
	req.Header.Set("X-Test-Sub", "sub-alice")
	rec := httptest.NewRecorder()
	h.Status(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sk-a") || strings.Contains(rec.Body.String(), "sk-b") {
		t.Fatalf("token leaked: %s", rec.Body.String())
	}
	var creds []credentials.Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &creds); err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 || creds[0].UserID != 7 {
		t.Fatalf("got %+v", creds)
	}
}

func TestPlatformCredentialIsAdminOnly(t *testing.T) {
	h, backend := newCredHandler()
	body := `{"kind":"anthropic_oauth","token":"sk-platform","user":"platform"}`

	req := httptest.NewRequest("PUT", "/api/v1/agent/user-credentials", strings.NewReader(body))
	req.Header.Set("X-Test-Sub", "sub-alice") // authenticated, NOT admin
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin platform write: got %d", rec.Code)
	}

	req = httptest.NewRequest("PUT", "/api/v1/agent/user-credentials", strings.NewReader(body))
	req.Header.Set("X-Test-Sub", "sub-alice")
	req.Header.Set("X-Test-Admin", "true")
	rec = httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin platform write: got %d: %s", rec.Code, rec.Body.String())
	}

	// The credential lands on the §1.13 service user's row, not the admin's.
	cred, found, _ := backend.Resolve(99, "anthropic_oauth")
	if !found || cred.Token != "sk-platform" {
		t.Fatalf("platform credential row: %+v found=%v", cred, found)
	}
	if _, found, _ := backend.Resolve(7, "anthropic_oauth"); found {
		t.Fatal("platform write leaked onto the admin's own row")
	}

	// Admin delete via ?user=platform.
	req = httptest.NewRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth?user=platform", nil)
	req.Header.Set("X-Test-Sub", "sub-alice")
	req.Header.Set("X-Test-Admin", "true")
	req.Form = map[string][]string{":kind": {"anthropic_oauth"}}
	rec = httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin platform delete: got %d", rec.Code)
	}
	if _, found, _ := backend.Resolve(99, "anthropic_oauth"); found {
		t.Fatal("platform credential survived delete")
	}
}

func TestDeleteByKind(t *testing.T) {
	h, backend := newCredHandler()
	_ = backend.Put(7, "alice", "anthropic_oauth", "sk-a", time.Time{})

	req := httptest.NewRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth", nil)
	req.Header.Set("X-Test-Sub", "sub-alice")
	req.Form = map[string][]string{":kind": {"anthropic_oauth"}}
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d", rec.Code)
	}
	if _, found, _ := backend.Resolve(7, "anthropic_oauth"); found {
		t.Fatal("credential survived delete")
	}

	req = httptest.NewRequest("DELETE", "/api/v1/agent/user-credentials/openai", nil)
	req.Header.Set("X-Test-Sub", "sub-alice")
	req.Form = map[string][]string{":kind": {"openai"}}
	rec = httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad kind: got %d", rec.Code)
	}
}

func jsonInt(i int64) string {
	b, _ := json.Marshal(i)
	return string(b)
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/credentials/
```

Expected failure: compile error `undefined: credentials.NewHandler`.

- [ ] Write `agent/credentials/handler.go`:

```go
package credentials

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// ClaimsFunc extracts the caller's identity from the request. Wired in
// atc/api/handler.go from accessor claims (sub + preferred username +
// admin); injected as a function to keep this package free of atc imports.
type ClaimsFunc func(r *http.Request) (sub string, userName string, isAdmin bool, ok bool)

// Handler serves the self-scoped credential vault API.
type Handler struct {
	backend Backend
	claims  ClaimsFunc
}

func NewHandler(backend Backend, claims ClaimsFunc) *Handler {
	return &Handler{backend: backend, claims: claims}
}

// resolveTarget resolves the users row a request operates on: the caller's
// own row, or — for admins that requested PlatformUserName — the §1.13
// service user's row (the only permitted non-self target).
func (h *Handler) resolveTarget(w http.ResponseWriter, r *http.Request, requested string) (int, string, bool) {
	sub, claimName, isAdmin, ok := h.claims(r)
	if !ok || sub == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return 0, "", false
	}
	switch requested {
	case "":
		// self-scoped
	case PlatformUserName:
		if !isAdmin {
			http.Error(w, "only admins may manage the platform credential", http.StatusForbidden)
			return 0, "", false
		}
		sub, claimName = PlatformUserSub, PlatformUserName
	default:
		http.Error(w, `user must be omitted or "platform"`, http.StatusBadRequest)
		return 0, "", false
	}
	userID, userName, found, err := h.backend.UserBySub(sub)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return 0, "", false
	}
	if !found {
		http.Error(w, "no user record for this token; log in to this Concourse first", http.StatusNotFound)
		return 0, "", false
	}
	if userName == "" {
		userName = claimName
	}
	return userID, userName, true
}

// Set handles PUT /api/v1/agent/user-credentials (self only; admins may
// pass "user":"platform" to vault the §1.13 platform credential).
func (h *Handler) Set(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "request body exceeds 1MB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req PutRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	userID, userName, ok := h.resolveTarget(w, r, req.User)
	if !ok {
		return
	}

	var expiresAt time.Time
	if req.ExpiresAt > 0 {
		expiresAt = time.Unix(req.ExpiresAt, 0)
	}
	if err := h.backend.Put(userID, userName, req.Kind, req.Token, expiresAt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.JiraAccountID != "" {
		if err := h.backend.SetJiraAccountID(userID, req.JiraAccountID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "saved",
		"kind":       req.Kind,
		"expires_at": req.ExpiresAt,
	})
}

// Status handles GET /api/v1/agent/user-credentials (self only; no
// tokens; admins may pass ?user=platform).
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := h.resolveTarget(w, r, r.URL.Query().Get("user"))
	if !ok {
		return
	}
	creds, err := h.backend.Status(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if creds == nil {
		creds = []Credential{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(creds)
}

// Delete handles DELETE /api/v1/agent/user-credentials/:kind (self only;
// admins may pass ?user=platform).
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := h.resolveTarget(w, r, r.URL.Query().Get("user"))
	if !ok {
		return
	}
	kind := r.FormValue(":kind")
	if !ValidKind(kind) {
		http.Error(w, "unknown credential kind", http.StatusBadRequest)
		return
	}
	if err := h.backend.Delete(userID, kind); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/credentials/
```

Expected: `ok`.

- [ ] Commit:

```bash
git add agent/credentials/handler.go agent/credentials/handler_test.go
git commit -m "feat(agent): self-scoped credential vault HTTP handler"
```

---

### Task 13: Routes, wrappa, roles, API + web-command wiring

Registers the five contract routes and threads the new stores/flag through both `api.NewHandler` call sites. The wrappa exhaustive-switch test is the failing-test lever: adding routes without wrappa entries panics `you missed a spot`.

**Files:**
- Modify: `atc/routes.go:125` (constants after `GetAgentReviewFindings`), `atc/routes.go:258` (route entries after the agent feedback block)
- Modify: `atc/wrappa/api_auth_wrappa.go:85` (authenticated case), `:112-113` (pass-through case), `:176` (authorized case)
- Modify: `atc/api/accessor/roles.go:115` (after `GetBuildAgentReviews`)
- Modify: `atc/api/handler.go:12` (imports), `:92` (params), `:139` (servers), `:276` (handlers map)
- Modify: `atc/api/api_suite_test.go:227` (new args)
- Modify: `atc/atccmd/command.go:218` (flag), `:2299` (NewHandler args)

**Steps:**

- [ ] Add route name constants in `atc/routes.go` after line 125 (`GetAgentReviewFindings`):

```go
	SetAgentUserCredential       = "SetAgentUserCredential"
	GetAgentUserCredentialStatus = "GetAgentUserCredentialStatus"
	DeleteAgentUserCredential    = "DeleteAgentUserCredential"
	GetAgentCostRollup           = "GetAgentCostRollup"
	SubmitAgentCostRecord        = "SubmitAgentCostRecord"
```

- [ ] Add route entries in `atc/routes.go` after line 258 (`GetAgentReviewFindings` entry):

```go
	{Path: "/api/v1/agent/user-credentials", Method: "PUT", Name: SetAgentUserCredential},
	{Path: "/api/v1/agent/user-credentials", Method: "GET", Name: GetAgentUserCredentialStatus},
	{Path: "/api/v1/agent/user-credentials/:kind", Method: "DELETE", Name: DeleteAgentUserCredential},
	{Path: "/api/v1/agent/costs", Method: "GET", Name: GetAgentCostRollup},
	{Path: "/api/v1/agent/costs", Method: "POST", Name: SubmitAgentCostRecord},
```

- [ ] Run the wrappa suite to verify the deliberate failure:

```bash
ginkgo ./atc/wrappa/
```

Expected failure: panic `you missed a spot: "SetAgentUserCredential"` (or whichever new route hits the default case first).

- [ ] Add wrappa entries in `atc/wrappa/api_auth_wrappa.go`. To the `authenticated` case list (ends line 85, before `atc.MCPEndpoint`):

```go
			atc.SetAgentUserCredential,
			atc.GetAgentUserCredentialStatus,
			atc.DeleteAgentUserCredential,
```

To the pass-through case (line 112) — extend the case and its comment:

```go
		// unauthenticated at the Concourse-token layer: publishers authenticate
		// with a static bearer token that the handler itself validates
		// (agent/api/reviews.Handler.SubmitReview, agent/api/costs.Handler.
		// SubmitRecord), not a Concourse user session/JWT. agent-identity
		// flips both routes to principal auth in its cutover (contract
		// addendum 2026-07-08).
		case atc.SubmitAgentReview,
			atc.SubmitAgentCostRecord:
			// no-op: pass straight through to the handler
```

To the `authorized` case list (after `atc.ListTeamAgentReviews`, line 174):

```go
			atc.GetAgentCostRollup,
```

- [ ] Add the role entry in `atc/api/accessor/roles.go` after `atc.GetBuildAgentReviews` (line 114; note the file comment — team-less paths are effectively admin-only until agent-identity's `CheckAgentAuthorizationHandler` lands, per contracts §4.2/decision 21):

```go
	atc.GetAgentCostRollup: ViewerRole,
```

- [ ] Run to verify the wrappa suite now passes:

```bash
ginkgo ./atc/wrappa/
```

Expected: all specs pass.

- [ ] Wire handlers in `atc/api/handler.go`. Add imports (line 11-12 block):

```go
	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/atc/api/accessor"
```

Append parameters to `NewHandler` after `agentReviewPublishToken string` (line 92):

```go
	credentialsBackend credentials.Backend,
	costLedger budget.Ledger,
	agentDailyBudgetUSD float64,
```

Construct the servers after the `reviewsServer` block (line 139):

```go
	credentialsServer := credentials.NewHandler(credentialsBackend, func(r *http.Request) (string, string, bool, bool) {
		acc := accessor.GetAccessor(r)
		claims := acc.Claims()
		name := claims.PreferredUsername
		if name == "" {
			name = claims.UserName
		}
		return claims.Sub, name, acc.IsAdmin(), claims.Sub != ""
	})
	costChecker := budget.NewChecker(costLedger, budget.NoTicketBudgets{}, budget.Config{
		GlobalDailyCapUSD: agentDailyBudgetUSD,
	})
	costsServer := costs.NewHandler(costLedger, costChecker, agentReviewPublishToken)
```

Add handlers-map entries next to the agent review entries (line 276):

```go
		atc.SetAgentUserCredential:       http.HandlerFunc(credentialsServer.Set),
		atc.GetAgentUserCredentialStatus: http.HandlerFunc(credentialsServer.Status),
		atc.DeleteAgentUserCredential:    http.HandlerFunc(credentialsServer.Delete),
		atc.GetAgentCostRollup:           http.HandlerFunc(costsServer.GetRollup),
		atc.SubmitAgentCostRecord:        http.HandlerFunc(costsServer.SubmitRecord),
```

- [ ] Update the test call site `atc/api/api_suite_test.go` (after `"test-agent-review-publish-token"`, line 227) and add the two imports (`agent/budget`, `agent/credentials`):

```go
		credentials.NewMemoryBackend(),
		budget.NewMemoryLedger(),
		0,
```

- [ ] Update the web command `atc/atccmd/command.go`. Flag after line 218:

```go
	AgentDailyBudgetUSD float64 `long:"agent-daily-budget-usd" default:"0" description:"Global daily agent LLM spend cap in USD across all agent work, enforced by dispatch admission and reported by the cost rollup API. 0 disables the cap."`
```

NewHandler args after `cmd.AgentReviewPublishToken` (line 2299):

```go
		db.NewAgentUserCredentialsFactory(dbConn),
		db.NewAgentCostLedgerFactory(dbConn),
		cmd.AgentDailyBudgetUSD,
```

- [ ] Verify compile + run the api suite:

```bash
go build ./atc/...
ginkgo ./atc/api/
```

Expected: build clean; api suite green (rata router constructs — every route has a handler and a wrappa entry).

- [ ] Run the wider unit slices touched by this task:

```bash
ginkgo ./atc/wrappa/ ./atc/api/accessor/
```

Expected: green.

- [ ] Commit:

```bash
git add atc/routes.go atc/wrappa/api_auth_wrappa.go atc/api/accessor/roles.go \
        atc/api/handler.go atc/api/api_suite_test.go atc/atccmd/command.go
git commit -m "feat(atc): wire agent credential + cost routes, --agent-daily-budget-usd flag"
```

---

### Task 14: Ephemeral K8s run-secret attacher

Implements §2.6 `SecretAttacher` / §8.2: secret `agent-run-<run-id>`, keys `anthropic-token` + `principal-token`, label `concourse/agent-run` (ticket label is dispatch's job per the addendum). Idempotent Attach; Cleanup tolerant of not-found so abort/error paths can call it unconditionally.

**Files:**
- Create: `agent/credentials/secret_attacher.go`
- Test: `agent/credentials/secret_attacher_test.go`

**Steps:**

- [ ] Write the failing test `agent/credentials/secret_attacher_test.go` (fake clientset per repo convention):

```go
package credentials_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/credentials"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAttachCreatesLabeledSecret(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	attacher := credentials.NewK8sSecretAttacher(clientset, "concourse-workers")

	cred := &credentials.Credential{UserID: 7, UserName: "alice", Kind: "anthropic_oauth", Token: "sk-tok"}
	name, err := attacher.Attach(context.Background(), 42, cred, "cap1.9.secret")
	if err != nil {
		t.Fatal(err)
	}
	if name != "agent-run-42" {
		t.Fatalf("secret name: %q", name)
	}

	secret, err := clientset.CoreV1().Secrets("concourse-workers").Get(context.Background(), "agent-run-42", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.StringData["anthropic-token"] != "sk-tok" {
		t.Fatalf("anthropic-token: %q", secret.StringData["anthropic-token"])
	}
	if secret.StringData["principal-token"] != "cap1.9.secret" {
		t.Fatalf("principal-token: %q", secret.StringData["principal-token"])
	}
	if secret.Labels["concourse/agent-run"] != "42" {
		t.Fatalf("labels: %v", secret.Labels)
	}
}

func TestAttachIsIdempotentPerRun(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	attacher := credentials.NewK8sSecretAttacher(clientset, "ns")
	cred := &credentials.Credential{Token: "tok-1"}

	if _, err := attacher.Attach(context.Background(), 7, cred, "p-1"); err != nil {
		t.Fatal(err)
	}
	cred.Token = "tok-2"
	name, err := attacher.Attach(context.Background(), 7, cred, "p-2")
	if err != nil {
		t.Fatalf("second attach must update, not fail: %v", err)
	}
	if name != "agent-run-7" {
		t.Fatalf("name: %q", name)
	}
	secret, _ := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-7", metav1.GetOptions{})
	if secret.StringData["anthropic-token"] != "tok-2" {
		t.Fatalf("attach did not refresh token: %q", secret.StringData["anthropic-token"])
	}
}

func TestAttachValidatesInput(t *testing.T) {
	attacher := credentials.NewK8sSecretAttacher(fake.NewSimpleClientset(), "ns")
	if _, err := attacher.Attach(context.Background(), 1, nil, "p"); err == nil {
		t.Fatal("nil credential accepted")
	}
	if _, err := attacher.Attach(context.Background(), 1, &credentials.Credential{}, "p"); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestCleanupDeletesAndTolerangesMissing(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	attacher := credentials.NewK8sSecretAttacher(clientset, "ns")
	cred := &credentials.Credential{Token: "tok"}

	if _, err := attacher.Attach(context.Background(), 5, cred, "p"); err != nil {
		t.Fatal(err)
	}
	if err := attacher.Cleanup(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-5", metav1.GetOptions{}); err == nil {
		t.Fatal("secret survived cleanup")
	}

	// abort/error paths call Cleanup unconditionally — not-found is fine
	if err := attacher.Cleanup(context.Background(), 5); err != nil {
		t.Fatalf("second cleanup must be a no-op: %v", err)
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/credentials/
```

Expected failure: compile error `undefined: credentials.NewK8sSecretAttacher`.

- [ ] Write `agent/credentials/secret_attacher.go`:

```go
package credentials

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// §8.2 secret naming and keys — the injection contract every consumer
// (dispatch, gateway, agent-step exec) reads.
const (
	SecretKeyAnthropicToken = "anthropic-token"
	SecretKeyPrincipalToken = "principal-token"
	RunLabel                = "concourse/agent-run"

	// PlatformSecretName is the long-lived platform credential secret
	// (§8.2/§1.13), maintained by PlatformSecretSyncer — never per-run.
	PlatformSecretName = "agent-platform-credential"
)

// RunSecretName returns the §8.2 per-run secret name.
func RunSecretName(runID int) string {
	return fmt.Sprintf("agent-run-%d", runID)
}

// K8sSecretAttacher implements SecretAttacher against a worker namespace.
type K8sSecretAttacher struct {
	client    kubernetes.Interface
	namespace string
}

func NewK8sSecretAttacher(client kubernetes.Interface, namespace string) *K8sSecretAttacher {
	return &K8sSecretAttacher{client: client, namespace: namespace}
}

func (a *K8sSecretAttacher) Attach(ctx context.Context, runID int, cred *Credential, principalToken string) (string, error) {
	if cred == nil || cred.Token == "" {
		return "", fmt.Errorf("attach run %d: credential with a decrypted token is required", runID)
	}
	name := RunSecretName(runID)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: a.namespace,
			Labels: map[string]string{
				RunLabel: strconv.Itoa(runID),
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			SecretKeyAnthropicToken: cred.Token,
			SecretKeyPrincipalToken: principalToken,
		},
	}

	_, err := a.client.CoreV1().Secrets(a.namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Idempotent per runID: refresh contents on re-attach.
		_, err = a.client.CoreV1().Secrets(a.namespace).Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("attach run %d: %w", runID, err)
	}
	return name, nil
}

func (a *K8sSecretAttacher) Cleanup(ctx context.Context, runID int) error {
	err := a.client.CoreV1().Secrets(a.namespace).Delete(ctx, RunSecretName(runID), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

var _ SecretAttacher = (*K8sSecretAttacher)(nil)
```

- [ ] Run to verify pass:

```bash
go test ./agent/credentials/
```

Expected: `ok`.

- [ ] Commit:

```bash
git add agent/credentials/secret_attacher.go agent/credentials/secret_attacher_test.go
git commit -m "feat(agent): ephemeral agent-run K8s secret attacher with idempotent lifecycle"
```

---

### Task 15: Platform-credential secret syncer (RunnableComponent)

§8.2 long-lived secret: keeps `agent-platform-credential` in sync with the `agent-platform` service user's vault row. Sync is **bidirectional** (§8.2, amended 2026-07-09 in `00-shared-contracts.md`): credential vaulted → create/update the secret; credential absent → delete any existing secret so a revoked token can never be mounted. Polling component (never notify-only — fork lesson), wired next to the K8s registrar/reaper in `atc/atccmd/command.go`.

**Files:**
- Create: `agent/credentials/platform_syncer.go`
- Modify: `atc/component.go:24` (new constant after `ComponentK8sWorkerReaper`)
- Modify: `atc/atccmd/command.go:1322` (append component inside the `cmd.Kubernetes.Namespace != ""` block, after the reaper)
- Test: `agent/credentials/platform_syncer_test.go`

**Steps:**

- [ ] Write the failing test `agent/credentials/platform_syncer_test.go`:

```go
package credentials_test

import (
	"context"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newSyncerFixture(withCred bool) (*credentials.PlatformSecretSyncer, *credentials.MemoryBackend, *fake.Clientset) {
	backend := credentials.NewMemoryBackend()
	backend.AddUser(credentials.PlatformUserSub, 99, "platform")
	if withCred {
		_ = backend.Put(99, "platform", credentials.KindAnthropicOAuth, "sk-platform", time.Now().Add(time.Hour))
	}
	clientset := fake.NewSimpleClientset()
	syncer := credentials.NewPlatformSecretSyncer(
		lagertest.NewTestLogger("syncer"), backend, clientset, "concourse-workers",
	)
	return syncer, backend, clientset
}

func TestSyncerCreatesPlatformSecret(t *testing.T) {
	syncer, _, clientset := newSyncerFixture(true)
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	secret, err := clientset.CoreV1().Secrets("concourse-workers").
		Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.StringData["anthropic-token"] != "sk-platform" {
		t.Fatalf("token: %q", secret.StringData["anthropic-token"])
	}
}

func TestSyncerRefreshesChangedToken(t *testing.T) {
	syncer, backend, clientset := newSyncerFixture(true)
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = backend.Put(99, "platform", credentials.KindAnthropicOAuth, "sk-rotated", time.Now().Add(time.Hour))
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	secret, _ := clientset.CoreV1().Secrets("concourse-workers").
		Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{})
	if secret.StringData["anthropic-token"] != "sk-rotated" {
		t.Fatalf("token not refreshed: %q", secret.StringData["anthropic-token"])
	}
}

func TestSyncerNoopsWithoutPlatformCredential(t *testing.T) {
	syncer, _, clientset := newSyncerFixture(false)
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatalf("missing credential must not error the component: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets("concourse-workers").
		Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{}); err == nil {
		t.Fatal("secret must not exist when the vault has no platform credential")
	}
}

func TestSyncerDeletesSecretWhenCredentialUnvaulted(t *testing.T) {
	// Seed the platform secret as if a prior sync (with a vaulted credential)
	// had created it, then unvault the credential. Bidirectional sync (§8.2)
	// requires the syncer to DELETE the now-stale secret so no pod can mount a
	// revoked token.
	syncer, _, clientset := newSyncerFixture(false)
	seed := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentials.PlatformSecretName,
			Namespace: "concourse-workers",
			Labels:    map[string]string{"concourse/agent-platform-credential": "true"},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"anthropic-token": "sk-stale"},
	}
	if _, err := clientset.CoreV1().Secrets("concourse-workers").
		Create(context.Background(), seed, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := syncer.Run(context.Background()); err != nil {
		t.Fatalf("unvaulted credential must not error the component: %v", err)
	}

	if _, err := clientset.CoreV1().Secrets("concourse-workers").
		Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("stale platform secret must be deleted after the credential is unvaulted, got err=%v", err)
	}
}
```

(`corev1` and `apierrors` are already in the import block above.)

- [ ] Run to verify it fails:

```bash
go test ./agent/credentials/
```

Expected failure: compile error `undefined: credentials.PlatformSecretSyncer` (all four tests, including `TestSyncerDeletesSecretWhenCredentialUnvaulted`, fail to compile until the syncer exists). Once the syncer is written, `TestSyncerDeletesSecretWhenCredentialUnvaulted` is what drives the `!found` branch to DELETE rather than no-op — a no-op `!found` return leaves the seeded secret in place and fails the `IsNotFound` assertion.

- [ ] Write `agent/credentials/platform_syncer.go` (`PlatformUserSub`/`PlatformUserName` already live in `types.go`, Task 4):

```go
package credentials

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PlatformSecretSyncer keeps the long-lived agent-platform-credential
// secret (§8.2) in sync with the platform user's vault row. It runs as a
// polling RunnableComponent (never notify-only, per the fork's
// notifications lesson), which also covers encryption-key rotation: the
// vault row decrypts with the current strategy on every pass.
type PlatformSecretSyncer struct {
	logger    lager.Logger
	backend   Backend
	client    kubernetes.Interface
	namespace string
}

func NewPlatformSecretSyncer(
	logger lager.Logger,
	backend Backend,
	client kubernetes.Interface,
	namespace string,
) *PlatformSecretSyncer {
	return &PlatformSecretSyncer{
		logger:    logger,
		backend:   backend,
		client:    client,
		namespace: namespace,
	}
}

// Run implements component.Runnable.
func (s *PlatformSecretSyncer) Run(ctx context.Context) error {
	userID, _, found, err := s.backend.UserBySub(PlatformUserSub)
	if err != nil {
		return fmt.Errorf("resolving platform user: %w", err)
	}
	if !found {
		s.logger.Info("platform-user-missing", lager.Data{"sub": PlatformUserSub})
		return nil
	}

	cred, found, err := s.backend.Resolve(userID, KindAnthropicOAuth)
	if err != nil {
		return fmt.Errorf("resolving platform credential: %w", err)
	}
	if !found {
		cred, found, err = s.backend.Resolve(userID, KindAnthropicAPIKey)
		if err != nil {
			return fmt.Errorf("resolving platform credential: %w", err)
		}
	}
	if !found {
		// Not an error: the platform credential is provisioned by an admin
		// running `fly agent auth --platform` (PutRequest.User = "platform").
		// Bidirectional sync (§8.2): if the credential was unvaulted (admin ran
		// `fly agent auth --platform --delete`), the stale K8s secret MUST be
		// removed so no pod can mount a revoked token. NotFound is tolerated —
		// same idiom as the run-secret Cleanup path.
		err := s.client.CoreV1().Secrets(s.namespace).Delete(ctx, PlatformSecretName, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			s.logger.Info("platform-credential-not-vaulted")
			return nil
		}
		if err != nil {
			s.logger.Error("failed-to-delete-platform-secret", err)
			return err
		}
		s.logger.Info("platform-secret-deleted")
		return nil
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PlatformSecretName,
			Namespace: s.namespace,
			Labels:    map[string]string{"concourse/agent-platform-credential": "true"},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			SecretKeyAnthropicToken: cred.Token,
		},
	}

	existing, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, PlatformSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := s.client.CoreV1().Secrets(s.namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			s.logger.Error("failed-to-create-platform-secret", err)
			return err
		}
		s.logger.Info("platform-secret-created")
		return nil
	}
	if err != nil {
		s.logger.Error("failed-to-get-platform-secret", err)
		return err
	}

	if string(existing.Data[SecretKeyAnthropicToken]) == cred.Token &&
		existing.StringData[SecretKeyAnthropicToken] == "" {
		return nil // already in sync (Data is the server-side representation)
	}
	if existing.StringData[SecretKeyAnthropicToken] == cred.Token {
		return nil // fake-clientset path: StringData not converted
	}
	if _, err := s.client.CoreV1().Secrets(s.namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		s.logger.Error("failed-to-update-platform-secret", err)
		return err
	}
	s.logger.Info("platform-secret-updated")
	return nil
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/credentials/
```

Expected: `ok`.

- [ ] Add the component constant in `atc/component.go` after `ComponentK8sWorkerReaper` (line 24):

```go
	ComponentAgentPlatformCredentialSyncer = "agent_platform_credential_syncer"
```

- [ ] Wire the component in `atc/atccmd/command.go` inside the `if cmd.Kubernetes.Namespace != ""` block, immediately after the reaper `components = append(...)` (line ~1322); add the `"github.com/concourse/concourse/agent/credentials"` import:

```go
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentAgentPlatformCredentialSyncer,
			},
			Runnable: credentials.NewPlatformSecretSyncer(
				logger.Session(atc.ComponentAgentPlatformCredentialSyncer),
				db.NewAgentUserCredentialsFactory(dbConn),
				k8sClientset,
				cmd.Kubernetes.Namespace,
			),
			Interval: time.Minute,
		})
```

- [ ] Verify compile:

```bash
go build ./atc/...
```

Expected: clean build.

- [ ] Commit:

```bash
git add agent/credentials/platform_syncer.go agent/credentials/platform_syncer_test.go \
        atc/component.go atc/atccmd/command.go
git commit -m "feat(atc): platform-credential secret syncer component (bidirectional: deletes secret on unvault)"
```

---

### Task 15a: Run-secret safety-net reaper (`RunSecretReaper` RunnableComponent)

**Added 2026-07-09 (final-review F22).** §8.2's "reaper safety-net GC" was referenced by plans 00/03/11 but implemented by none — every completed run would permanently leak `agent-run-<id>` holding the user's decrypted Anthropic token, and a crash between `Attach` and pod scheduling would orphan it forever. Ownership is recorded in the Task 1 F22 addendum: this plan implements the reaper, beside the platform syncer (same clientset + namespace, same `RunnableComponent` block in `atc/atccmd/command.go`). It sweeps worker-namespace secrets labeled `concourse/agent-run`, deletes any whose run is complete or absent (narrow `RunActive(runID)` seam), and best-effort revokes the matching per-run principal `agent-run-<run-id>` in the same pass. A 5-minute creation-grace window protects dispatch's `CreateRun`→`Attach` ordering from sweep races. Dispatch's in-process `Cleanup` remains the first line; plan 03's lifecycler stays pure (no clientset).

**Files:**
- Create: `agent/credentials/secret_reaper.go`
- Test: `agent/credentials/secret_reaper_test.go`
- Create: `atc/db/agent_run_checker.go`
- Test: `atc/db/agent_run_checker_test.go`
- Modify: `atc/component.go` (new constant after `ComponentAgentPlatformCredentialSyncer`, Task 15)
- Modify: `atc/atccmd/command.go` (append component inside the `cmd.Kubernetes.Namespace != ""` block, immediately after the Task 15 syncer)
- Modify: `deploy/chart/templates/rbac.yaml` (namespaced secrets rule for the pod-manager Role — the web SA currently has only cluster-wide `secrets: get`, which cannot list-by-label or delete)

**Steps:**

- [ ] Write the failing test `agent/credentials/secret_reaper_test.go` (fake clientset per repo convention; fakes for the two narrow seams):

```go
package credentials_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type fakeRunChecker struct {
	active map[int]bool
	failOn map[int]bool
	calls  []int
}

func (f *fakeRunChecker) RunActive(runID int) (bool, error) {
	f.calls = append(f.calls, runID)
	if f.failOn[runID] {
		return false, fmt.Errorf("checker exploded for run %d", runID)
	}
	return f.active[runID], nil
}

type fakeRevoker struct {
	names []string
	err   error
}

func (f *fakeRevoker) RevokeByName(name string) error {
	f.names = append(f.names, name)
	return f.err
}

func seedRunSecret(t *testing.T, clientset *fake.Clientset, ns string, runID int, age time.Duration) {
	t.Helper()
	_, err := clientset.CoreV1().Secrets(ns).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              credentials.RunSecretName(runID),
			Namespace:         ns,
			Labels:            map[string]string{credentials.RunLabel: strconv.Itoa(runID)},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			credentials.SecretKeyAnthropicToken: "tok",
			credentials.SecretKeyPrincipalToken: "cap1.1.x",
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReaperDeletesFinishedRunSecretAndRevokesPrincipal(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 42, time.Hour) // finished
	seedRunSecret(t, clientset, "ns", 43, time.Hour) // still running
	checker := &fakeRunChecker{active: map[int]bool{43: true}}
	revoker := &fakeRevoker{}
	reaper := credentials.NewRunSecretReaper(lagertest.NewTestLogger("reaper"), clientset, "ns", checker, revoker)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-42", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("finished run's secret must be reaped, got err=%v", err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-43", metav1.GetOptions{}); err != nil {
		t.Fatalf("active run's secret must survive: %v", err)
	}
	if len(revoker.names) != 1 || revoker.names[0] != "agent-run-42" {
		t.Fatalf("revoked principals: %v", revoker.names)
	}
}

func TestReaperDeletesSecretWhoseRunRowIsAbsent(t *testing.T) {
	// The F22 crash window: Attach succeeded but the run row was never
	// created (or was deleted). Absent = inactive = reap.
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 7, time.Hour)
	reaper := credentials.NewRunSecretReaper(
		lagertest.NewTestLogger("reaper"), clientset, "ns", &fakeRunChecker{}, nil)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-7", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("orphaned secret must be reaped, got err=%v", err)
	}
}

func TestReaperGraceWindowProtectsFreshSecrets(t *testing.T) {
	// Protects the dispatch CreateRun→Attach ordering: a just-created
	// secret is never reaped even when its run is not (yet) visible.
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 9, 0)
	checker := &fakeRunChecker{}
	reaper := credentials.NewRunSecretReaper(lagertest.NewTestLogger("reaper"), clientset, "ns", checker, nil)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-9", metav1.GetOptions{}); err != nil {
		t.Fatalf("fresh secret must survive the grace window: %v", err)
	}
	if len(checker.calls) != 0 {
		t.Fatalf("fresh secret must not even be checked: %v", checker.calls)
	}
}

func TestReaperIgnoresSecretsWithoutRunLabel(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              credentials.PlatformSecretName,
			Namespace:         "ns",
			Labels:            map[string]string{"concourse/agent-platform-credential": "true"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
	})
	checker := &fakeRunChecker{}
	reaper := credentials.NewRunSecretReaper(lagertest.NewTestLogger("reaper"), clientset, "ns", checker, nil)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{}); err != nil {
		t.Fatalf("platform secret must never be touched: %v", err)
	}
	if len(checker.calls) != 0 {
		t.Fatalf("unlabeled secrets must not be checked: %v", checker.calls)
	}
}

func TestReaperRevokeIsBestEffort(t *testing.T) {
	// nil revoker (wave-1 wiring, before agent-identity binds it)
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 11, time.Hour)
	reaper := credentials.NewRunSecretReaper(
		lagertest.NewTestLogger("reaper"), clientset, "ns", &fakeRunChecker{}, nil)
	if err := reaper.Run(context.Background()); err != nil {
		t.Fatalf("nil revoker must be tolerated: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-11", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("secret must be reaped with nil revoker, got err=%v", err)
	}

	// failing revoker: revocation is attempted, its error logged, never fatal
	seedRunSecret(t, clientset, "ns", 12, time.Hour)
	revoker := &fakeRevoker{err: fmt.Errorf("store down")}
	reaper = credentials.NewRunSecretReaper(
		lagertest.NewTestLogger("reaper"), clientset, "ns", &fakeRunChecker{}, revoker)
	if err := reaper.Run(context.Background()); err != nil {
		t.Fatalf("revoker error must not fail the sweep: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-12", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("secret must be reaped despite revoke failure, got err=%v", err)
	}
	if len(revoker.names) != 1 || revoker.names[0] != "agent-run-12" {
		t.Fatalf("revocation must be attempted: %v", revoker.names)
	}
}

func TestReaperSkipsUnparseableRunLabel(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "agent-run-mystery",
			Namespace:         "ns",
			Labels:            map[string]string{credentials.RunLabel: "not-a-number"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
	})
	reaper := credentials.NewRunSecretReaper(
		lagertest.NewTestLogger("reaper"), clientset, "ns", &fakeRunChecker{}, nil)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-mystery", metav1.GetOptions{}); err != nil {
		t.Fatalf("unparseable label must be skipped (logged), not deleted: %v", err)
	}
}

func TestReaperContinuesSweepWhenCheckerErrors(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 21, time.Hour) // checker errors
	seedRunSecret(t, clientset, "ns", 22, time.Hour) // reapable
	checker := &fakeRunChecker{failOn: map[int]bool{21: true}}
	reaper := credentials.NewRunSecretReaper(lagertest.NewTestLogger("reaper"), clientset, "ns", checker, nil)

	err := reaper.Run(context.Background())
	if err == nil {
		t.Fatal("sweep must surface the checker error (component retries next interval)")
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-21", metav1.GetOptions{}); err != nil {
		t.Fatalf("run 21's secret must be kept on checker error (fail closed): %v", err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-22", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("one bad run must not block the rest of the sweep, got err=%v", err)
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/credentials/
```

Expected failure: compile error `undefined: credentials.NewRunSecretReaper`.

- [ ] Write `agent/credentials/secret_reaper.go`:

```go
package credentials

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RunChecker reports whether an agent run is still active. Narrow seam
// (F22): the production implementation is atc/db.NewAgentRunChecker over
// the pipeline_runs table (contracts §1.5, owned by pipeline-runs); an
// absent row — or an absent table, before that wave-mate merges — means
// the run cannot be active.
type RunChecker interface {
	RunActive(runID int) (bool, error)
}

// PrincipalRevoker best-effort revokes the per-run principal named
// agent-run-<run-id> (dispatch addendum 2026-07-08). Bound to an adapter
// over agent-identity's principals.Store by its cutover task; nil until
// then — safe because per-run principals carry expires_at, unlike the
// secret this reaper exists to delete.
type PrincipalRevoker interface {
	RevokeByName(name string) error
}

// RunSecretReapGrace protects dispatch's CreateRun→Attach ordering from
// sweep races: secrets younger than this are never considered.
const RunSecretReapGrace = 5 * time.Minute

// RunSecretReaper is §8.2's "reaper safety-net GC" (final-review F22):
// dispatch's in-process Cleanup on abort/error paths is the first line of
// defense, this polling component is the guarantee. It lists worker-
// namespace secrets by the concourse/agent-run label, deletes any whose
// run is complete or absent, and best-effort revokes the matching per-run
// principal in the same pass.
type RunSecretReaper struct {
	logger    lager.Logger
	client    kubernetes.Interface
	namespace string
	runs      RunChecker
	revoker   PrincipalRevoker // may be nil (see PrincipalRevoker)
}

func NewRunSecretReaper(
	logger lager.Logger,
	client kubernetes.Interface,
	namespace string,
	runs RunChecker,
	revoker PrincipalRevoker,
) *RunSecretReaper {
	return &RunSecretReaper{
		logger:    logger,
		client:    client,
		namespace: namespace,
		runs:      runs,
		revoker:   revoker,
	}
}

// Run implements component.Runnable. One failing secret does not block
// the rest of the sweep; the first error is returned so the component
// retries on its next interval.
func (r *RunSecretReaper) Run(ctx context.Context) error {
	secrets, err := r.client.CoreV1().Secrets(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RunLabel,
	})
	if err != nil {
		return fmt.Errorf("listing run secrets: %w", err)
	}

	var sweepErr error
	for i := range secrets.Items {
		secret := &secrets.Items[i]

		runID, err := strconv.Atoi(secret.Labels[RunLabel])
		if err != nil {
			r.logger.Info("skipping-unparseable-run-label", lager.Data{
				"secret": secret.Name, "label": secret.Labels[RunLabel],
			})
			continue
		}
		if time.Since(secret.CreationTimestamp.Time) < RunSecretReapGrace {
			continue // Attach may precede the run row becoming visible
		}

		active, err := r.runs.RunActive(runID)
		if err != nil {
			// Fail closed: keep the secret, surface the error, keep sweeping.
			r.logger.Error("failed-to-check-run", err, lager.Data{"run_id": runID})
			if sweepErr == nil {
				sweepErr = err
			}
			continue
		}
		if active {
			continue
		}

		err = r.client.CoreV1().Secrets(r.namespace).Delete(ctx, secret.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			r.logger.Error("failed-to-delete-run-secret", err, lager.Data{"secret": secret.Name})
			if sweepErr == nil {
				sweepErr = err
			}
			continue
		}
		r.logger.Info("reaped-run-secret", lager.Data{"secret": secret.Name, "run_id": runID})

		if r.revoker != nil {
			if err := r.revoker.RevokeByName(RunSecretName(runID)); err != nil {
				// Best-effort: the principal expires on its own (expires_at).
				r.logger.Error("failed-to-revoke-run-principal", err, lager.Data{
					"principal": RunSecretName(runID),
				})
			}
		}
	}
	return sweepErr
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/credentials/
```

Expected: `ok`.

- [ ] Write the failing Ginkgo test `atc/db/agent_run_checker_test.go` for the production `RunActive` seam:

```go
package db_test

import (
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunChecker", func() {
	var checker *db.AgentRunChecker

	// pipeline_runs is pipeline-runs' migration 1773106031 (contracts §1.5).
	// The Task 1 merge-order addendum lands credentials BEFORE pipeline-runs,
	// so create the table with the exact §1.5 DDL when absent; once
	// 1773106031 merges this becomes a no-op.
	createPipelineRuns := func() {
		_, err := dbConn.Exec(`
			CREATE TABLE IF NOT EXISTS pipeline_runs (
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
			)`)
		Expect(err).ToNot(HaveOccurred())
	}

	BeforeEach(func() {
		checker = db.NewAgentRunChecker(dbConn)
	})

	It("reports running rows active and finished/absent rows inactive", func() {
		createPipelineRuns()

		var runningID, doneID int
		err := dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, number, status)
			VALUES ($1, 990001, 'running') RETURNING id`, defaultPipeline.ID()).Scan(&runningID)
		Expect(err).ToNot(HaveOccurred())
		err = dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, number, status, completed_at)
			VALUES ($1, 990002, 'succeeded', now()) RETURNING id`, defaultPipeline.ID()).Scan(&doneID)
		Expect(err).ToNot(HaveOccurred())

		Expect(checker.RunActive(runningID)).To(BeTrue())
		Expect(checker.RunActive(doneID)).To(BeFalse())
		Expect(checker.RunActive(999999999)).To(BeFalse()) // absent row = inactive
	})

	It("treats an absent pipeline_runs table as no-active-runs (undefined_table)", func() {
		// Each spec gets a fresh DB from the template (suite-level
		// BeforeEach: CreateTestDBFromTemplate), so dropping here cannot
		// leak into other specs.
		_, err := dbConn.Exec(`DROP TABLE IF EXISTS pipeline_runs`)
		Expect(err).ToNot(HaveOccurred())

		active, err := checker.RunActive(1)
		Expect(err).ToNot(HaveOccurred())
		Expect(active).To(BeFalse())
	})
})
```

- [ ] Run to verify it fails (PostgreSQL must be running: `pg_isready`):

```bash
ginkgo --focus="AgentRunChecker" ./atc/db/
```

Expected failure: compile error `undefined: db.AgentRunChecker`.

- [ ] Write `atc/db/agent_run_checker.go`:

```go
package db

import (
	"errors"

	"github.com/concourse/concourse/agent/credentials"
	"github.com/lib/pq"
)

// AgentRunChecker implements credentials.RunChecker (the RunSecretReaper's
// narrow RunActive seam, final-review F22) over the pipeline_runs table
// (contracts §1.5, owned by the pipeline-runs workstream).
type AgentRunChecker struct {
	conn DbConn
}

func NewAgentRunChecker(conn DbConn) *AgentRunChecker {
	return &AgentRunChecker{conn: conn}
}

// RunActive reports whether the run row exists and is still running.
// Absent rows are inactive — the run finished, was deleted, or was never
// created; either way its secret must not outlive it. An absent
// pipeline_runs TABLE (credentials merges before pipeline-runs per the
// Task 1 merge-order addendum) also means no run can be active: dispatch
// does not exist yet, so any labeled secret is a stray.
func (c *AgentRunChecker) RunActive(runID int) (bool, error) {
	var active bool
	err := c.conn.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM pipeline_runs WHERE id = $1 AND status = 'running')`,
		runID,
	).Scan(&active)

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "42P01" { // undefined_table
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active, nil
}

var _ credentials.RunChecker = (*AgentRunChecker)(nil)
```

- [ ] Run to verify pass:

```bash
ginkgo --focus="AgentRunChecker" ./atc/db/
```

Expected: green.

- [ ] Add the component constant in `atc/component.go`, immediately after `ComponentAgentPlatformCredentialSyncer` (Task 15):

```go
	ComponentAgentRunSecretReaper = "agent_run_secret_reaper"
```

- [ ] Wire the component in `atc/atccmd/command.go` inside the `if cmd.Kubernetes.Namespace != ""` block, immediately after the Task 15 syncer `components = append(...)` (the `credentials` import is already present from Task 15):

```go
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentAgentRunSecretReaper,
			},
			Runnable: credentials.NewRunSecretReaper(
				logger.Session(atc.ComponentAgentRunSecretReaper),
				k8sClientset,
				cmd.Kubernetes.Namespace,
				db.NewAgentRunChecker(dbConn),
				nil, // PrincipalRevoker: bound by agent-identity's cutover task (Task 1 F22 addendum)
			),
			Interval: time.Minute,
		})
```

- [ ] Grant the web SA the secret verbs the attacher/syncer/reaper need. In `deploy/chart/templates/rbac.yaml`, add a namespaced rule to the `pod-manager` Role (after the `pods/log` rule) — the existing ClusterRole `secrets: get` deliberately stays get-only:

```yaml
  # Agent credential secrets (§8.2): the run-secret attacher and platform
  # syncer create/update agent-run-*/agent-platform-credential secrets, and
  # the RunSecretReaper safety net (F22) lists by the concourse/agent-run
  # label and deletes strays. Namespaced — worker namespace only; the
  # cluster-wide secrets rule above remains get-only.
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "create", "update", "delete"]
```

- [ ] Verify the rendered chart contains the rule:

```bash
helm template test deploy/chart | grep -B2 -A2 '"secrets"'
```

Expected: two matches — the ClusterRole rule with `verbs: ["get"]` and the pod-manager Role rule with `verbs: ["get", "list", "create", "update", "delete"]`.

- [ ] Verify compile:

```bash
go build ./atc/...
```

Expected: clean build.

- [ ] Commit:

```bash
git add agent/credentials/secret_reaper.go agent/credentials/secret_reaper_test.go \
        atc/db/agent_run_checker.go atc/db/agent_run_checker_test.go \
        atc/component.go atc/atccmd/command.go deploy/chart/templates/rbac.yaml
git commit -m "feat(atc): run-secret safety-net reaper component (F22: per-run secret cleanup owned here)"
```

---

### Task 16: go-concourse client methods

Four methods on the `Client` interface (`go-concourse/concourse/client.go:15-40`), recipe from `user.go`/`wall.go`.

**Files:**
- Create: `go-concourse/concourse/agent_credentials.go`
- Create: `go-concourse/concourse/agent_costs.go`
- Modify: `go-concourse/concourse/client.go:38` (interface, after `ClearWall() error`)
- Test: `go-concourse/concourse/agent_credentials_test.go`
- Test: `go-concourse/concourse/agent_costs_test.go`

**Steps:**

- [ ] Write the failing Ginkgo test `go-concourse/concourse/agent_credentials_test.go`:

```go
package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/agent/credentials"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Agent user credentials", func() {
	Describe("SetAgentUserCredential", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/user-credentials"),
					ghttp.VerifyJSON(`{"kind":"anthropic_oauth","token":"sk-tok","expires_at":1783891200}`),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{"status": "saved"}),
				),
			)
		})

		It("PUTs the credential body", func() {
			err := client.SetAgentUserCredential(credentials.PutRequest{
				Kind: "anthropic_oauth", Token: "sk-tok", ExpiresAt: 1783891200,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("AgentUserCredentialStatus", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/user-credentials"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []credentials.Credential{
						{UserID: 7, UserName: "alice", Kind: "anthropic_oauth", ExpiresAt: 1783891200},
					}),
				),
			)
		})

		It("returns the caller's credentials", func() {
			creds, err := client.AgentUserCredentialStatus()
			Expect(err).NotTo(HaveOccurred())
			Expect(creds).To(HaveLen(1))
			Expect(creds[0].Kind).To(Equal("anthropic_oauth"))
			Expect(creds[0].ExpiresAt).To(Equal(int64(1783891200)))
		})
	})

	Describe("DeleteAgentUserCredential", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth"),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
		})

		It("deletes by kind", func() {
			Expect(client.DeleteAgentUserCredential("anthropic_oauth", false)).To(Succeed())
		})
	})

	Describe("DeleteAgentUserCredential for the platform user", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth", "user=platform"),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
		})

		It("adds the user=platform query param", func() {
			Expect(client.DeleteAgentUserCredential("anthropic_oauth", true)).To(Succeed())
		})
	})
})
```

- [ ] Write the failing Ginkgo test `go-concourse/concourse/agent_costs_test.go`:

```go
package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/agent/budget"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("AgentCostRollup", func() {
	BeforeEach(func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/api/v1/agent/costs", "group_by=user&since=2026-07-01"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, costs.RollupResponse{
					GroupBy: "user",
					Summary: costs.DailySummary{CapUSD: 50, SpentUSD: 5, RemainingUSD: 45},
					Rows:    []budget.RollupRow{{Key: "alice", Entries: 2, CostUSD: 5}},
				}),
			),
		)
	})

	It("fetches the rollup with query params", func() {
		resp, err := client.AgentCostRollup("user", "2026-07-01", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.GroupBy).To(Equal("user"))
		Expect(resp.Rows).To(HaveLen(1))
		Expect(resp.Summary.RemainingUSD).To(Equal(45.0))
	})
})
```

- [ ] Run to verify they fail:

```bash
ginkgo ./go-concourse/concourse/
```

Expected failure: compile error `client.SetAgentUserCredential undefined`.

- [ ] Add to the `Client` interface in `go-concourse/concourse/client.go` (after `ClearWall() error`, line 38) plus imports for `credentials` and `costs`:

```go
	SetAgentUserCredential(req credentials.PutRequest) error
	AgentUserCredentialStatus() ([]credentials.Credential, error)
	// platform=true targets the §1.13 service user's credential (admin only).
	DeleteAgentUserCredential(kind string, platform bool) error
	AgentCostRollup(groupBy, since, until string) (costs.RollupResponse, error)
```

- [ ] Write `go-concourse/concourse/agent_credentials.go`:

```go
package concourse

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

func (client *client) SetAgentUserCredential(req credentials.PutRequest) error {
	buffer := &bytes.Buffer{}
	if err := json.NewEncoder(buffer).Encode(req); err != nil {
		return err
	}
	return client.connection.Send(internal.Request{
		RequestName: atc.SetAgentUserCredential,
		Body:        buffer,
		Header: http.Header{
			"Content-Type": {"application/json"},
		},
	}, &internal.Response{})
}

func (client *client) AgentUserCredentialStatus() ([]credentials.Credential, error) {
	var creds []credentials.Credential
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentUserCredentialStatus,
	}, &internal.Response{Result: &creds})
	return creds, err
}

func (client *client) DeleteAgentUserCredential(kind string, platform bool) error {
	req := internal.Request{
		RequestName: atc.DeleteAgentUserCredential,
		Params:      rata.Params{"kind": kind},
	}
	if platform {
		req.Query = url.Values{"user": {credentials.PlatformUserName}}
	}
	return client.connection.Send(req, &internal.Response{})
}
```

(`net/url` joins the import block alongside the others.)

- [ ] Write `go-concourse/concourse/agent_costs.go`:

```go
package concourse

import (
	"net/url"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
)

func (client *client) AgentCostRollup(groupBy, since, until string) (costs.RollupResponse, error) {
	query := url.Values{}
	if groupBy != "" {
		query.Set("group_by", groupBy)
	}
	if since != "" {
		query.Set("since", since)
	}
	if until != "" {
		query.Set("until", until)
	}
	var resp costs.RollupResponse
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentCostRollup,
		Query:       query,
	}, &internal.Response{Result: &resp})
	return resp, err
}
```

- [ ] Regenerate the client fake (fly tests depend on `concoursefakes.FakeClient` satisfying the interface):

```bash
go generate ./go-concourse/concourse/
```

- [ ] Run to verify pass:

```bash
ginkgo ./go-concourse/concourse/
```

Expected: suite green including the two new files.

- [ ] Commit:

```bash
git add go-concourse/concourse/
git commit -m "feat(go-concourse): agent credential and cost rollup client methods"
```

---

### Task 17: `fly agent auth`, `fly agent costs`, and the `fly status` expiry nag

Creates the shared `AgentCommand` family (contract addendum: wave-mates add fields to this struct). `fly agent auth` walks `claude setup-token` (paste flow) or takes `--token`; `fly status` nags when a credential expires within 30 days.

**Files:**
- Create: `fly/commands/agent.go`
- Create: `fly/commands/agent_auth.go`
- Create: `fly/commands/agent_costs.go`
- Modify: `fly/commands/fly.go:90` (register `Agent` after `Curl`)
- Modify: `fly/commands/status.go:29` (nag before the success print)
- Modify: `fly/integration/status_test.go:57-64` (existing valid-token spec must handle the new credentials request — ghttp fails unhandled requests)
- Test: `fly/integration/agent_test.go`

**Steps:**

- [ ] Write the failing integration spec `fly/integration/agent_test.go` (recipe: `fly/integration/userinfo_test.go`; the suite builds the fly binary against a ghttp mock ATC):

```go
package integration_test

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent", func() {
	Describe("agent auth --token", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/user-credentials"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{"status": "saved"}),
				),
			)
		})

		It("stores the token and prints the expiry", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "auth", "--token", "sk-tok")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("stored your anthropic_oauth credential; expires"))
		})
	})

	Describe("agent auth --platform --token", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/user-credentials"),
					func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						Expect(string(body)).To(ContainSubstring(`"user":"platform"`))
					},
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{"status": "saved"}),
				),
			)
		})

		It("targets the platform service user", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "auth", "--platform", "--token", "sk-plat")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("stored the platform anthropic_oauth credential; expires"))
		})
	})

	Describe("agent auth --delete", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth"),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
		})

		It("deletes the stored credential", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "auth", "--delete")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("deleted your anthropic_oauth credential"))
		})
	})

	Describe("agent costs", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/costs", "group_by=day"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, costs.RollupResponse{
						GroupBy: "day",
						Summary: costs.DailySummary{CapUSD: 50, SpentUSD: 2.5, RemainingUSD: 47.5},
						Rows: []budget.RollupRow{
							{Key: "2026-07-08", Entries: 3, Turns: 12, CostUSD: 2.5},
						},
					}),
				),
			)
		})

		It("renders the rollup table and daily summary", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "costs")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("2026-07-08"))
			Expect(sess.Out).To(gbytes.Say(`daily cap \$50\.00`))
		})
	})

	Describe("fly status expiry nag", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/user"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{"user_name": "test"}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/user-credentials"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []credentials.Credential{
						{UserID: 7, Kind: "anthropic_oauth", ExpiresAt: time.Now().Add(10 * 24 * time.Hour).Unix()},
					}),
				),
			)
		})

		It("warns when a credential expires within 30 days", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "status")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say(fmt.Sprintf("WARNING: your agent anthropic_oauth credential expires in 9 days")))
			Expect(sess.Out).To(gbytes.Say("logged in successfully"))
		})
	})
})
```

- [ ] Run to verify it fails:

```bash
ginkgo ./fly/integration/ --focus="fly agent"
```

Expected failure: `Unknown command 'agent'` in fly output (specs exit non-zero).

- [ ] Write `fly/commands/agent.go`:

```go
package commands

// AgentCommand is the shared `fly agent` family. Per the wave-1 contract
// addendum, other workstreams append their own subcommand fields here
// (Workflows, Tickets, ...) — additive merges only.
type AgentCommand struct {
	Auth  AgentAuthCommand  `command:"auth" description:"Vault your Anthropic token for agent workloads"`
	Costs AgentCostsCommand `command:"costs" description:"Show agent cost rollups"`
}
```

- [ ] Write `fly/commands/agent_auth.go`:

```go
package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/fly/rc"
)

type AgentAuthCommand struct {
	Token     string        `long:"token" description:"Token value. If omitted, fly walks you through claude setup-token and reads the pasted token from stdin."`
	Kind      string        `long:"kind" default:"anthropic_oauth" choice:"anthropic_oauth" choice:"anthropic_api_key" description:"Credential kind"`
	ExpiresIn time.Duration `long:"expires-in" default:"8760h" description:"How long until the token expires (claude setup-token issues ~1-year tokens)"`
	Delete    bool          `long:"delete" description:"Delete the stored credential of --kind instead of storing one"`
	Platform  bool          `long:"platform" description:"Manage the shared platform credential (funds harvest judge / retrospective work) instead of your own. Admin only."`
}

func (command *AgentAuthCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	whose := "your"
	if command.Platform {
		whose = "the platform"
	}

	if command.Delete {
		if err := target.Client().DeleteAgentUserCredential(command.Kind, command.Platform); err != nil {
			return err
		}
		fmt.Printf("deleted %s %s credential\n", whose, command.Kind)
		return nil
	}

	token := command.Token
	if token == "" {
		fmt.Println("Run `claude setup-token` in a terminal where you can complete the browser login,")
		fmt.Println("then paste the resulting token below. It is stored encrypted on your Concourse")
		fmt.Println("and attached (as CLAUDE_CODE_OAUTH_TOKEN) only to agent runs you trigger.")
		fmt.Print("token: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading token from stdin: %w", err)
		}
		token = strings.TrimSpace(line)
	}
	if token == "" {
		return fmt.Errorf("no token provided")
	}

	req := credentials.PutRequest{
		Kind:      command.Kind,
		Token:     token,
		ExpiresAt: time.Now().Add(command.ExpiresIn).Unix(),
	}
	if command.Platform {
		req.User = credentials.PlatformUserName
	}
	if err := target.Client().SetAgentUserCredential(req); err != nil {
		return err
	}

	fmt.Printf("stored %s %s credential; expires %s\n", whose, command.Kind, time.Unix(req.ExpiresAt, 0).Format("2006-01-02"))
	return nil
}
```

- [ ] Write `fly/commands/agent_costs.go`:

```go
package commands

import (
	"fmt"
	"os"
	"strconv"

	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type AgentCostsCommand struct {
	GroupBy string `long:"group-by" default:"day" choice:"day" choice:"user" choice:"ticket" choice:"workflow" description:"Rollup dimension"`
	Since   string `long:"since" description:"Start (YYYY-MM-DD or RFC3339); default 30 days ago"`
	Until   string `long:"until" description:"End, exclusive (YYYY-MM-DD or RFC3339)"`
	Json    bool   `long:"json" description:"Print command result as JSON"`
}

func (command *AgentCostsCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	resp, err := target.Client().AgentCostRollup(command.GroupBy, command.Since, command.Until)
	if err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(resp)
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: command.GroupBy, Color: color.New(color.Bold)},
		{Contents: "entries", Color: color.New(color.Bold)},
		{Contents: "turns", Color: color.New(color.Bold)},
		{Contents: "cost (usd)", Color: color.New(color.Bold)},
	}}
	for _, row := range resp.Rows {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: row.Key},
			{Contents: strconv.Itoa(row.Entries)},
			{Contents: strconv.FormatInt(row.Turns, 10)},
			{Contents: strconv.FormatFloat(row.CostUSD, 'f', 4, 64)},
		})
	}
	if err := table.Render(os.Stdout, Fly.PrintTableHeaders); err != nil {
		return err
	}
	if resp.Summary.CapUSD > 0 {
		fmt.Printf("daily cap $%.2f, spent today $%.2f, remaining $%.2f\n",
			resp.Summary.CapUSD, resp.Summary.SpentUSD, resp.Summary.RemainingUSD)
	}
	return nil
}
```

- [ ] Register the family in `fly/commands/fly.go` after the `Curl` field (line 90):

```go
	Agent AgentCommand `command:"agent" description:"Agent platform: credentials and costs"`
```

- [ ] Add the nag to `fly/commands/status.go` before `fmt.Println("logged in successfully")` (line 31); add `time` and `github.com/concourse/concourse/agent/credentials` (blank-free) imports as needed:

```go
	if creds, credErr := target.Client().AgentUserCredentialStatus(); credErr == nil {
		for _, cred := range creds {
			if cred.ExpiresAt == 0 {
				continue
			}
			until := time.Until(time.Unix(cred.ExpiresAt, 0))
			if until < 30*24*time.Hour {
				fmt.Printf("WARNING: your agent %s credential expires in %d days — run `fly -t %s agent auth` to refresh\n",
					cred.Kind, int(until.Hours()/24), Fly.Target)
			}
		}
	}
```

(Errors are swallowed deliberately: older ATCs without the route must not break `fly status`. The `creds` slice's element type makes the `credentials` import unnecessary here — only `time` is added.)

- [ ] Update the existing valid-token spec in `fly/integration/status_test.go` (the `Context("when target is saved with valid token", ...)` BeforeEach at lines 57-64): `fly status` now also GETs the credentials route on the success path, and ghttp fails specs on unhandled requests. Append a second handler after the `/api/v1/user` one:

```go
		Context("when target is saved with valid token", func() {
			BeforeEach(func() {
				atcServer.Reset()
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/user"),
						ghttp.RespondWithJSONEncoded(200, map[string]any{"team": "test"}),
					),
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/user-credentials"),
						ghttp.RespondWithJSONEncoded(200, []map[string]any{}),
					),
				)
			})
```

(The rejected-token and logged-out specs return before the nag call fires — no change there.)

- [ ] Run to verify pass:

```bash
ginkgo ./fly/integration/ --focus="fly agent"
ginkgo ./fly/integration/ --focus="status"
```

Expected: 5 "fly agent" specs and all existing status specs passing.

- [ ] Run the full fly integration suite to catch regressions (mock version handshake etc.):

```bash
make test-fly-integration
```

Expected: green (576+ specs).

- [ ] Commit:

```bash
git add fly/commands/agent.go fly/commands/agent_auth.go fly/commands/agent_costs.go \
        fly/commands/fly.go fly/commands/status.go \
        fly/integration/agent_test.go fly/integration/status_test.go
git commit -m "feat(fly): fly agent auth/costs commands and status expiry nag"
```

---

### Task 18: Feed the ledger from the live review job (ci-agent)

ci-agent already parses cost from the claude CLI envelope (`ci-agent/llm/result.go` `CallResult`) but `phaserunner.Run` discards it (`runner.go:104-120`). Accumulate per-step usage into `costs.json`, then have `ci-agent publish` POST records to `/api/v1/agent/costs` — fire-and-forget: cost-publish failures warn but never fail the build. ci-agent is a standalone module (`github.com/concourse/ci-agent`); it talks to the new route by wire format only.

**Files:**
- Modify: `ci-agent/phaserunner/runner.go:36-42` (StepCost type near StepResult), `:76-79` (accumulator), `:118-120` (capture), `:197-199` (write costs.json)
- Modify: `ci-agent/publish/publish.go` (add `PublishCosts`)
- Modify: `ci-agent/cmd/ci-agent/publish.go:12-31` (`--costs` flag + env)
- Modify: `ci/tasks/ci-agent-review.yml:64` (publish invocation)
- Test: `ci-agent/phaserunner/runner_test.go` (new test appended)
- Test: `ci-agent/publish/publish_test.go` (new tests appended)

**Steps:**

- [ ] Append the failing spec to `ci-agent/phaserunner/runner_test.go`. The file is a Ginkgo suite (`package phaserunner_test`, existing `var _ = Describe("Run", ...)` at line 40 with a `fakeLLMClient` that returns no usage); add a usage-bearing fake plus a new `Describe` after the existing one. All needed imports (`context`, `encoding/json`, `os`, `path/filepath`, `llm`, `phaseconfig`, `phaserunner`) are already in the file's import block:

```go
// usageClient returns canned usage/cost data with every call.
type usageClient struct{}

func (usageClient) Call(_ context.Context, _ string, _ llm.CallOpts) (llm.CallResult, error) {
	return llm.CallResult{
		Result:  json.RawMessage(`{"ok":true}`),
		Model:   "claude-sonnet-5",
		CostUSD: 0.25,
		Usage: llm.Usage{
			InputTokens:              100,
			OutputTokens:             50,
			CacheReadInputTokens:     10,
			CacheCreationInputTokens: 5,
		},
		NumTurns:   4,
		DurationMS: 1200,
	}, nil
}

var _ = Describe("Run cost accounting", func() {
	It("writes per-step usage to costs.json", func() {
		tmpDir, err := os.MkdirTemp("", "phaserunner-costs-test")
		Expect(err).NotTo(HaveOccurred())
		defer os.RemoveAll(tmpDir)
		baseDir := filepath.Join(tmpDir, "base")
		outputDir := filepath.Join(tmpDir, "output")
		Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(baseDir, "prompt.md"), []byte("hello"), 0644)).To(Succeed())

		cfg := &phaseconfig.Config{
			Name: "review",
			Steps: []phaseconfig.Step{
				{Name: "analyze", Template: "prompt.md"},
			},
		}

		_, err = phaserunner.Run(context.Background(), phaserunner.Options{
			Config:    cfg,
			OutputDir: outputDir,
			Client:    usageClient{},
			BaseDir:   baseDir,
		})
		Expect(err).NotTo(HaveOccurred())

		data, err := os.ReadFile(filepath.Join(outputDir, "costs.json"))
		Expect(err).NotTo(HaveOccurred())
		var recs []phaserunner.StepCost
		Expect(json.Unmarshal(data, &recs)).To(Succeed())
		Expect(recs).To(HaveLen(1))
		Expect(recs[0].Step).To(Equal("analyze"))
		Expect(recs[0].Model).To(Equal("claude-sonnet-5"))
		Expect(recs[0].CostUSD).To(Equal(0.25))
		Expect(recs[0].InputTokens).To(Equal(int64(100)))
		Expect(recs[0].OutputTokens).To(Equal(int64(50)))
		Expect(recs[0].CacheReadTokens).To(Equal(int64(10)))
		Expect(recs[0].CacheCreationTokens).To(Equal(int64(5)))
		Expect(recs[0].Turns).To(Equal(4))
		Expect(recs[0].DurationMS).To(Equal(1200))
	})
})
```

- [ ] Run to verify it fails:

```bash
cd ci-agent && go test ./phaserunner/
```

Expected failure: compile error `undefined: phaserunner.StepCost`.

- [ ] In `ci-agent/phaserunner/runner.go`, add the type after `StepResult` (line 42):

```go
// StepCost captures per-step LLM usage for the cost ledger feed
// (published by `ci-agent publish --costs` to /api/v1/agent/costs).
type StepCost struct {
	Step                string  `json:"step"`
	Model               string  `json:"model,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	Turns               int     `json:"turns"`
	CostUSD             float64 `json:"cost_usd"`
	DurationMS          int     `json:"duration_ms"`
}
```

Declare the accumulator next to `stepResults` (line 78):

```go
	var stepCosts []StepCost
```

Capture usage right after the successful `opts.Client.Call` (after line 118 `output := cr.Result`):

```go
		stepCosts = append(stepCosts, StepCost{
			Step:                step.Name,
			Model:               cr.Model,
			InputTokens:         int64(cr.Usage.InputTokens),
			OutputTokens:        int64(cr.Usage.OutputTokens),
			CacheReadTokens:     int64(cr.Usage.CacheReadInputTokens),
			CacheCreationTokens: int64(cr.Usage.CacheCreationInputTokens),
			Turns:               cr.NumTurns,
			CostUSD:             cr.CostUSD,
			DurationMS:          cr.DurationMS,
		})
```

Write the artifact next to `step-results.json` (after line 199):

```go
	// Write costs.json for the ledger feed (fire-and-forget downstream).
	if len(stepCosts) > 0 {
		costData, _ := json.MarshalIndent(stepCosts, "", "  ")
		os.WriteFile(filepath.Join(opts.OutputDir, "costs.json"), costData, 0644)
	}
```

- [ ] Run to verify pass:

```bash
cd ci-agent && go test ./phaserunner/
```

Expected: `ok`.

- [ ] Append the failing tests to `ci-agent/publish/publish_test.go`:

```go
func TestPublishCostsPostsEachRecord(t *testing.T) {
	var bodies [][]byte
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/costs" || r.Method != "POST" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		auths = append(auths, r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	dir := t.TempDir()
	costsPath := filepath.Join(dir, "costs.json")
	if err := os.WriteFile(costsPath, []byte(`[
		{"step":"analyze","model":"claude-sonnet-5","input_tokens":100,"output_tokens":50,"cache_read_tokens":10,"cache_creation_tokens":5,"turns":4,"cost_usd":0.25,"duration_ms":1200}
	]`), 0644); err != nil {
		t.Fatal(err)
	}

	err := publish.PublishCosts(context.Background(), publish.CostsOptions{
		ATCURL:    srv.URL,
		BuildID:   "1234",
		Token:     "publish-secret",
		CostsPath: costsPath,
		Phase:     "review",
		UserName:  "tdmtrader",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatalf("posted %d records", len(bodies))
	}
	if auths[0] != "Bearer publish-secret" {
		t.Fatalf("auth: %q", auths[0])
	}

	var rec map[string]any
	if err := json.Unmarshal(bodies[0], &rec); err != nil {
		t.Fatal(err)
	}
	if rec["source"] != "ci_agent" || rec["build_id"] != float64(1234) ||
		rec["step_name"] != "review/analyze" || rec["cost_usd"] != 0.25 ||
		rec["user_name"] != "tdmtrader" || rec["provider"] != "anthropic" ||
		rec["turns"] != float64(4) || rec["input_tokens"] != float64(100) {
		t.Fatalf("record: %v", rec)
	}
}

func TestPublishCostsSkipsMissingFile(t *testing.T) {
	err := publish.PublishCosts(context.Background(), publish.CostsOptions{
		ATCURL:    "http://unused.invalid",
		BuildID:   "1",
		Token:     "t",
		CostsPath: filepath.Join(t.TempDir(), "absent.json"),
	})
	if err != nil {
		t.Fatalf("missing costs.json must be a silent skip: %v", err)
	}
}

func TestPublishCostsReturnsErrorOnServerFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer srv.Close()

	dir := t.TempDir()
	costsPath := filepath.Join(dir, "costs.json")
	os.WriteFile(costsPath, []byte(`[{"step":"s","cost_usd":1}]`), 0644)

	err := publish.PublishCosts(context.Background(), publish.CostsOptions{
		ATCURL: srv.URL, BuildID: "1", Token: "t", CostsPath: costsPath,
	})
	if err == nil {
		t.Fatal("server 4xx must surface as an error (caller downgrades to a warning)")
	}
}
```

(The file is `package publish_test` — its import block already has `context`, `net/http`, `net/http/httptest`, `os`, `path/filepath`, and the `publish` package; add `encoding/json` and `io`.)

- [ ] Run to verify they fail:

```bash
cd ci-agent && go test ./publish/
```

Expected failure: compile error `undefined: PublishCosts`.

- [ ] Append to `ci-agent/publish/publish.go`:

```go
// CostsOptions configures PublishCosts.
type CostsOptions struct {
	ATCURL     string
	BuildID    string
	Token      string
	CostsPath  string
	Phase      string // step_name prefix, e.g. "review"
	UserName   string // optional attribution (AGENT_COST_USER)
	HTTPClient *http.Client
}

// stepCostRecord mirrors phaserunner.StepCost (wire-format coupling only;
// ci-agent is a standalone module).
type stepCostRecord struct {
	Step                string  `json:"step"`
	Model               string  `json:"model,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	Turns               int     `json:"turns"`
	CostUSD             float64 `json:"cost_usd"`
	DurationMS          int     `json:"duration_ms"`
}

// PublishCosts POSTs each costs.json record to /api/v1/agent/costs
// (source ci_agent). A missing costs.json is a silent skip; any HTTP
// failure returns an error that the CLI downgrades to a warning — cost
// reporting must never fail a build.
func PublishCosts(ctx context.Context, opts CostsOptions) error {
	if opts.ATCURL == "" {
		return fmt.Errorf("ATC_EXTERNAL_URL is not set")
	}
	if opts.Token == "" {
		return fmt.Errorf("AGENT_REVIEW_PUBLISH_TOKEN is not set")
	}
	buildID, err := strconv.Atoi(opts.BuildID)
	if err != nil {
		return fmt.Errorf("invalid BUILD_ID %q: %w", opts.BuildID, err)
	}

	data, err := os.ReadFile(opts.CostsPath)
	if os.IsNotExist(err) {
		return nil // phase produced no costs — nothing to do
	}
	if err != nil {
		return fmt.Errorf("reading costs: %w", err)
	}
	var records []stepCostRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("parsing %s: %w", opts.CostsPath, err)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	url := strings.TrimSuffix(opts.ATCURL, "/") + "/api/v1/agent/costs"

	for _, rec := range records {
		stepName := rec.Step
		if opts.Phase != "" {
			stepName = opts.Phase + "/" + rec.Step
		}
		body, err := json.Marshal(map[string]any{
			"source":                "ci_agent",
			"provider":              "anthropic",
			"build_id":              buildID,
			"step_name":             stepName,
			"user_name":             opts.UserName,
			"model":                 rec.Model,
			"input_tokens":          rec.InputTokens,
			"output_tokens":         rec.OutputTokens,
			"cache_read_tokens":     rec.CacheReadTokens,
			"cache_creation_tokens": rec.CacheCreationTokens,
			"turns":                 rec.Turns,
			"cost_usd":              rec.CostUSD,
		})
		if err != nil {
			return fmt.Errorf("encoding cost record: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+opts.Token)

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("posting cost record: %w", err)
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("cost publish failed: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
		}
	}
	return nil
}
```

- [ ] Update `ci-agent/cmd/ci-agent/publish.go` to publish costs after the review (warning-only):

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/concourse/ci-agent/publish"
)

func runPublish(args []string) int {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	reviewPath := fs.String("review", "review/review.json", "path to review.json")
	costsPath := fs.String("costs", "", "path to costs.json (optional; missing file is skipped)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL:     os.Getenv("ATC_EXTERNAL_URL"),
		BuildID:    os.Getenv("BUILD_ID"),
		Token:      os.Getenv("AGENT_REVIEW_PUBLISH_TOKEN"),
		ReviewPath: *reviewPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "publish error: %v\n", err)
		return 1
	}
	fmt.Println("review published")

	if *costsPath != "" {
		err := publish.PublishCosts(context.Background(), publish.CostsOptions{
			ATCURL:    os.Getenv("ATC_EXTERNAL_URL"),
			BuildID:   os.Getenv("BUILD_ID"),
			Token:     os.Getenv("AGENT_REVIEW_PUBLISH_TOKEN"),
			CostsPath: *costsPath,
			Phase:     "review",
			UserName:  os.Getenv("AGENT_COST_USER"),
		})
		if err != nil {
			// Fire-and-forget: cost reporting never fails the build.
			fmt.Fprintf(os.Stderr, "warning: cost publish: %v\n", err)
		} else {
			fmt.Println("costs published")
		}
	}
	return 0
}
```

- [ ] Update `ci/tasks/ci-agent-review.yml` line 64 to pass the costs artifact (and the optional attribution param in the `params:` block):

```yaml
params:
  # ... existing params ...
  AGENT_COST_USER: ""
```

```sh
      if ci-agent publish --review "$OUTPUT_DIR/review.json" --costs "$OUTPUT_DIR/costs.json"; then
```

- [ ] Run to verify pass:

```bash
cd ci-agent && go test ./phaserunner/ ./publish/ && go build ./... && cd ..
```

Expected: `ok` for both packages.

- [ ] Commit:

```bash
git add ci-agent/phaserunner/runner.go ci-agent/phaserunner/runner_test.go \
        ci-agent/publish/publish.go ci-agent/publish/publish_test.go \
        ci-agent/cmd/ci-agent/publish.go ci/tasks/ci-agent-review.yml
git commit -m "feat(ci-agent): per-step costs.json + fire-and-forget ledger publish"
```

---

### Task 19: Live theborg validation — secret lifecycle + cost feed smoke

Fake clientsets can't validate RBAC (can the web SA create/delete secrets in the worker namespace?) or the deployed route wiring. Same live-test conventions as Task 3.

**Files:**
- Create: `agent/credentials/live_secret_attacher_test.go`

**Steps:**

- [ ] Write `agent/credentials/live_secret_attacher_test.go`:

```go
//go:build live

package credentials_test

import (
	"context"
	"os"
	"testing"

	"github.com/concourse/concourse/agent/credentials"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveSecretAttachCleanup exercises the real create/update/delete
// lifecycle against a throwaway namespace — validating API-server
// behavior (StringData conversion, AlreadyExists on re-attach) that the
// fake clientset approximates.
func TestLiveSecretAttachCleanup(t *testing.T) {
	ns := os.Getenv("K8S_TEST_NAMESPACE")
	if ns == "" {
		t.Skip("K8S_TEST_NAMESPACE not set")
	}
	for _, forbidden := range []string{"cicd", "concourse", "default", "kube-system"} {
		if ns == forbidden {
			t.Fatalf("refusing to run in live namespace %q", ns)
		}
	}
	clientset := probeClient(t) // helper from live_rate_limit_probe_test.go
	ctx := context.Background()

	attacher := credentials.NewK8sSecretAttacher(clientset, ns)
	cred := &credentials.Credential{UserID: 1, UserName: "live-test", Kind: "anthropic_oauth", Token: "live-dummy-token"}

	name, err := attacher.Attach(ctx, 990001, cred, "cap1.0.livetest")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = attacher.Cleanup(context.Background(), 990001) })

	secret, err := clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Real API server converts StringData -> Data.
	if string(secret.Data["anthropic-token"]) != "live-dummy-token" {
		t.Fatalf("anthropic-token: %q", secret.Data["anthropic-token"])
	}
	if string(secret.Data["principal-token"]) != "cap1.0.livetest" {
		t.Fatalf("principal-token: %q", secret.Data["principal-token"])
	}
	if secret.Labels["concourse/agent-run"] != "990001" {
		t.Fatalf("labels: %v", secret.Labels)
	}

	t.Log("re-attach updates in place")
	cred.Token = "live-dummy-token-2"
	if _, err := attacher.Attach(ctx, 990001, cred, "cap1.0.livetest"); err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	secret, _ = clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if string(secret.Data["anthropic-token"]) != "live-dummy-token-2" {
		t.Fatalf("re-attach did not refresh: %q", secret.Data["anthropic-token"])
	}

	t.Log("cleanup removes the secret; second cleanup is a no-op")
	if err := attacher.Cleanup(ctx, 990001); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		t.Fatal("secret survived cleanup")
	}
	if err := attacher.Cleanup(ctx, 990001); err != nil {
		t.Fatalf("idempotent cleanup: %v", err)
	}
}
```

- [ ] Verify it compiles:

```bash
go vet -tags live ./agent/credentials/
```

Expected: exits 0.

- [ ] Run it against theborg (throwaway namespace, per CLAUDE.md live-test conventions):

```bash
kubectl --context theborg create namespace agent-cred-live-$(date +%m%d)
KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=agent-cred-live-$(date +%m%d) \
  go test -tags live -run '^TestLiveSecretAttachCleanup$' -v -count=1 -timeout 5m ./agent/credentials/
kubectl --context theborg delete namespace agent-cred-live-$(date +%m%d)
```

Expected: PASS.

- [ ] Smoke-test the deployed cost feed once this branch reaches concourse.home (theborg/cicd): after a `concourse-self` agent-review build completes, verify a ledger row landed —

```bash
fly -t home login  # per memory: reference_theborg_cicd_live_concourse.md
fly -t home agent costs --group-by day
```

Expected: a `ci_agent`-sourced row for today with nonzero `cost (usd)`. (Requires the cicd pipeline's review job params to gain `AGENT_COST_USER` and the updated task yml — the pipeline already passes `AGENT_REVIEW_PUBLISH_TOKEN`.)

- [ ] Reaper steady-state check (Task 15a, F22) once the branch is deployed: the worker namespace must hold no stale run secrets —

```bash
kubectl --context theborg -n cicd get secrets -l concourse/agent-run
```

Expected: `No resources found` (per-run secrets exist only while a run is active or within the 5-minute reap grace; anything older indicates the reaper component is not running — check `agent_run_secret_reaper` in the components table).

- [ ] Commit:

```bash
git add agent/credentials/live_secret_attacher_test.go
git commit -m "test(agent): live theborg secret-attacher lifecycle validation"
```

---

## Execution notes

**Full workstream test suite:**

```bash
pg_isready                                   # PostgreSQL required for atc/db + migration suites
go test ./agent/credentials/ ./agent/budget/ ./agent/api/costs/
ginkgo ./atc/db/                             # largest suite (~1007 specs, ~90s); do NOT use --race
ginkgo ./atc/db/migration/                   # includes the three new migration specs + legacy upgrade
ginkgo ./atc/wrappa/ ./atc/api/ ./go-concourse/concourse/
make test-fly-integration                    # fly binary vs mock ATC
cd ci-agent && go test ./... && cd ..        # standalone module (make test-ci-agent)
```

If `ginkgo ./atc/db/` reports `database "testdb_template" already exists`, another test process is running — wait or kill it (CLAUDE.md).

**Live-test requirements (theborg):** Tasks 3 and 19 need `KUBECONFIG=~/.kube/config`, kube-context `theborg` (https://theborg.home:6443), and a THROWAWAY namespace — never `cicd`/`concourse`. Task 3 additionally needs a real `claude setup-token` value and an operator watching the interactive session's `/status`. Colima/Docker is usually down on this machine, so testcontainers is not an option — theborg is the live target (CLAUDE.md).

**Ordering constraints:**
- Task 1 (addendum) first — it is the wave-start agreement other planners read.
- Tasks 2–3 (probe) are the charter's FIRST DELIVERABLE; run Task 3 as soon as the harness lands, in parallel with Tasks 4+.
- Task 13 depends on 4, 7, 11, 12; Task 15 on 6 and 14; Task 15a on 14 and 15 (constants/wiring order in `atc/component.go` / `atccmd/command.go`); Task 16 on 11, 13; Task 17 on 16; Task 19 on 13–18.
- MERGE ORDER HAZARD: the migrator is version-pointer based. If this branch (1773106020–22) merges and deploys before agent-identity's 1773106010, theborg will never apply 1773106010. Coordinate merge order per the Task 1 addendum, or hold deploys until both are merged.

**Rollback notes for the risky diffs:**
- Migrations: every `.up.sql` has an exact-inverse `.down.sql`; `concourse migrate --migrate-db-to-version 1773105504` returns to the pre-workstream schema. The 1773106022 down deletes the `agent-platform` users row (cascades its vault rows — intended).
- `atc/db/migration/encryption.go` rotation-list entry: safe to revert only together with dropping the vault table; with the table present, reverting reintroduces the silent-skip-on-rotation bug.
- `atc/wrappa` pass-through for `SubmitAgentCostRecord`: the handler enforces the static token; if the route must be disabled in an emergency, deploy with `--agent-review-publish-token=""` (both reviews and costs publishing turn off, returning 403).
- Platform syncer: removing the `RunnableComponent` block in `atccmd/command.go` disables it cleanly; the `agent-platform-credential` secret is inert data (delete with `kubectl delete secret agent-platform-credential -n <ns>`). Note: while the syncer is running it OWNS this secret bidirectionally — unvaulting the credential deletes the secret on the next pass (see the 2026-07-09 amendment below), so a manual `kubectl create` will be reaped unless a matching vault row exists.
- Run-secret reaper (Task 15a): removing its `RunnableComponent` block disables it cleanly, but doing so re-opens F22 — completed runs then leak live-token secrets until dispatch's in-process `Cleanup` (the first line of defense only) or a manual `kubectl delete secret -l concourse/agent-run` sweep. The 5-minute grace window means an emergency `kubectl create secret` with the `concourse/agent-run` label survives at most one sweep interval unless a matching running `pipeline_runs` row exists.

**Plan amendments:**
- **2026-07-09 (final-review F22 — run-secret safety-net reaper was owned by nobody):** §8.2's "reaper safety-net GC" was referenced by plans 00/03/11 and implemented by none: every completed run permanently leaked `agent-run-<id>` (a live decrypted user token, no expiry), and a crash between `Attach` and pod scheduling orphaned it forever. Added **Task 15a**: `credentials.RunSecretReaper`, a polling `RunnableComponent` (`agent_run_secret_reaper`, 1-minute interval) wired beside the Task 15 platform syncer in the `atccmd/command.go` K8s block. It lists worker-namespace secrets by the `concourse/agent-run` label, deletes any whose run is complete or absent via the narrow `RunActive(runID)` seam (`credentials.RunChecker`; production impl `atc/db.NewAgentRunChecker` over `pipeline_runs` — absent row OR absent table = inactive, tolerating the credentials-before-pipeline-runs merge order), best-effort revokes the per-run principal `agent-run-<run-id>` via a nil-tolerant `PrincipalRevoker` seam (bound by agent-identity's cutover; safe interim — principals carry `expires_at`), and protects dispatch's `CreateRun`→`Attach` ordering with a 5-minute creation-grace window (`RunSecretReapGrace`). Also adds the namespaced secrets RBAC rule (`get/list/create/update/delete`) to the chart's pod-manager Role, which Tasks 14/15 silently needed too (the web SA previously had only cluster-wide `secrets: get`). Tests added: seven plain-Go reaper tests (`TestReaperDeletesFinishedRunSecretAndRevokesPrincipal`, `TestReaperDeletesSecretWhoseRunRowIsAbsent`, `TestReaperGraceWindowProtectsFreshSecrets`, `TestReaperIgnoresSecretsWithoutRunLabel`, `TestReaperRevokeIsBestEffort`, `TestReaperSkipsUnparseableRunLabel`, `TestReaperContinuesSweepWhenCheckerErrors`) and a two-spec Ginkgo `AgentRunChecker` suite (running/finished/absent rows; undefined_table 42P01 ⇒ inactive). Ownership + attribution rewording recorded in contracts §11 via the Task 1 F22 addendum (plan 03's lifecycler stays pure; plan 11's `Cleanup` is first-line only); Task 19 gains a deployed steady-state check (`kubectl get secrets -l concourse/agent-run` empty).
- **2026-07-09 (final-review F35 — probe burst below /status granularity made NO unsound):** the probe's 5–20 × "Reply with exactly: ok" burst sat below `/status` display granularity, so a null delta could not distinguish "not shared" from "shared but invisible" — yet the memo forced YES/NO/PARTIAL and seeded every later wave's budget defaults. Task 2 now (a) defaults `PROBE_PROMPT` to a ~800-word generation (env-overridable, plumbed into the probe pod), (b) sums the captured CLI envelopes into a `PROBE_TOTAL` line via new pure helper `credentials.SumProbeUsage` (`ProbeTotals`; accepts both `cost_usd` and `total_cost_usd`; fails the run loudly when zero envelopes parse), with unit tests `TestSumProbeUsageSumsEnvelopes` / `TestSumProbeUsageSkipsUnparseableLines`; and (c) the memo template replaces YES/NO/PARTIAL with SHARED / NOT SHARED / PARTIAL / INCONCLUSIVE under a calibrated, asymmetric decisiveness rule: the operator first records threshold **T** (the interactive volume that first visibly moves `/status`); any visible delta ⇒ SHARED; NOT SHARED only when `PROBE_TOTAL` ≥ 2×T with no delta; anything else ⇒ INCONCLUSIVE, which downstream consumers MUST treat as SHARED. Task 3's operator steps gain the calibration burst and the escalate-until-decisive rule.
- **2026-07-09 (design-review F11 — syncer deletes stale secret on unvault):** Task 15 `PlatformSecretSyncer.Run` no longer no-ops when the platform credential is absent. The `!found` branch now DELETEs any existing `agent-platform-credential` K8s secret (NotFound-tolerant, reusing the run-secret `Cleanup` idiom) so a revoked/unvaulted token can never remain mountable. Sync is now bidirectional: vaulted → create/update; absent → delete-if-present. Added failing test `TestSyncerDeletesSecretWhenCredentialUnvaulted` (seeds the secret, runs with no vaulted credential, asserts `IsNotFound`); the test file gains `corev1`/`apierrors` imports. The §8.2 bidirectional-sync contract text is amended in `00-shared-contracts.md` by the shared-contracts editor (same date); this plan references it and keeps `agent-platform-credential` / `anthropic-token` names identical.
- ci-agent feed: reverting the `--costs` flag from `ci/tasks/ci-agent-review.yml` stops ledger writes without code changes (publish skips when the flag is absent).
- `fly status` nag degrades silently against older ATCs (any error from the credentials route is swallowed), so fly/web version skew is safe in both directions.
