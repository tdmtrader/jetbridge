package harvest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	schema "github.com/concourse/concourse/agent/schema"
)

const judgeDiffMaxBytes = 256 << 10

// Run executes the harvest flow against workspaceDir (§2.8.1): refuse
// invalid config (exit 2), verify the committed clean git tree (F33:
// dirty ⇒ fail, exit 1, nothing pushed, nothing auto-discarded),
// resolve head/base + patch manifest, run the §6.3 gate policy, run the
// judge (ADVISORY, §6.4: a judge error is recorded as judge_error and
// never blocks the push), then — only when every gate passed —
// push-by-sha --force-with-lease to the stable ticket branch.
//
// flightDir empty (the pre-flight-recorder exec) skips every flight
// output; stdout always carries the schema.Results document. credsDir
// semantics are unchanged from v0.5.
func Run(cfg Config, workspaceDir, credsDir, flightDir string, out io.Writer) int {
	started := time.Now()

	rec, recErr := newFlightRecorder(flightDir)
	if recErr != nil {
		// a broken flight dir is a platform fault: evidence could not be
		// recorded, so nothing may proceed to a push
		res := buildResults(schema.StatusError, "flight dir: "+recErr.Error(), nil)
		json.NewEncoder(out).Encode(res)
		return 2
	}
	defer rec.close()

	rec.emit(schema.EventStepStart, schema.StepStartData{
		StepName: cfg.StepName,
		BuildID:  envInt("BUILD_ID"),
		PlanID:   os.Getenv("AGENT_PLAN_ID"),
		TicketID: optionalInt(cfg.TicketID),
	})

	facts := &runFacts{}

	finish := func(status schema.Status, detail string) int {
		meta := facts.metadata(detail)
		res := buildResults(status, detail, meta)
		rec.writeJSON("review.json", assembleEvidence(cfg, status, detail, facts, int(time.Since(started).Seconds())))
		rec.writeJSON("results.json", res)
		rec.emit(schema.EventStepEnd, schema.StepEndData{
			StepName: cfg.StepName, Status: stepEndStatus(status),
			Summary: detail, WallTimeSeconds: int(time.Since(started).Seconds()),
			CostUSD: facts.judgeCost(),
		})
		json.NewEncoder(out).Encode(res)
		switch status {
		case schema.StatusPass:
			return 0
		case schema.StatusFail:
			return 1
		default:
			return 2
		}
	}

	// -- admission (the runner-side boundary; exec/render mirror it) --
	if cfg.Judge != nil {
		if err := cfg.Judge.Validate(); err != nil {
			return finish(schema.StatusError, "judge config invalid: "+err.Error())
		}
	}
	if cfg.Push && cfg.Branch == "" {
		return finish(schema.StatusError, "push requires a branch")
	}

	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspaceDir
		var buf strings.Builder
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		return strings.TrimSpace(buf.String()), err
	}

	// -- committed clean tree (F33, unchanged semantics) --
	if _, err := git("rev-parse", "--git-dir"); err != nil {
		return finish(schema.StatusFail, "workspace is not a git repository — the agent must commit its work into the workspace checkout")
	}
	status, err := git("status", "--porcelain")
	if err != nil {
		return finish(schema.StatusError, "git status: "+status)
	}
	if status != "" {
		facts.Dirty = status
		return finish(schema.StatusFail, "workspace-dirty: uncommitted changes present (F33) — commit or clean up; nothing was pushed:\n"+status)
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return finish(schema.StatusFail, "workspace has no commits: "+head)
	}
	facts.HeadSHA = head

	// -- base + manifest (best-effort context: absence degrades the
	//    judge diff and skips manifest.json, never fails the harvest) --
	if base, err := BaseSHA(workspaceDir, cfg.TargetBranch); err == nil {
		facts.BaseSHA = base
		if m, err := BuildManifest(workspaceDir, base, head, cfg.Repo, cfg.Branch); err == nil {
			rec.writeJSON("manifest.json", m)
			facts.ManifestWritten = true
		}
	}

	// -- no-op guard: a clean workspace whose HEAD is still the base has zero
	//    agent commits. Running gates against an unchanged tree is meaningless
	//    and pushing it publishes an EMPTY branch that reads as a successful
	//    review — the money-burning silent no-op the audit found (agent-ticket-43
	//    runs 42/43). Fail loudly here, before gates and push. Only fires when
	//    the base actually resolved (absence stays the existing degraded path). --
	if facts.BaseSHA != "" && facts.HeadSHA == facts.BaseSHA {
		return finish(schema.StatusFail, "no-op: the workspace HEAD equals the base commit "+facts.BaseSHA+" — the agent committed no work into the workspace checkout, so there is nothing to push. (A frequent cause: the agent edited the input repo/ tree instead of the workspace output.) Nothing was pushed.")
	}

	// -- gates (between cleanliness and push, §6.3; unchanged engine) --
	if len(cfg.GatePolicy.Gates) > 0 {
		outcomes, gatesErr := RunGates(cfg.GatePolicy, workspaceDir, facts.BaseSHA, rec.eventWriter())
		facts.Gates = outcomes
		if gatesErr != nil {
			return finish(schema.StatusError, "gate engine failure: "+gatesErr.Error())
		}
		for _, o := range outcomes {
			switch o.Status {
			case "ok":
				continue
			case "failed":
				return finish(schema.StatusFail, fmt.Sprintf("gate %q failed — nothing pushed:\n%s", o.Gate, o.Detail))
			default: // "error"
				return finish(schema.StatusError, fmt.Sprintf("gate %q errored: %s", o.Gate, o.Detail))
			}
		}
	}

	// -- judge (ADVISORY; after head-sha capture so judge-process
	//    workspace mutation can never alter what is pushed — §2.8.1) --
	if cfg.Judge != nil {
		diff := ""
		if facts.BaseSHA != "" {
			if d, err := Diff(workspaceDir, facts.BaseSHA, judgeDiffMaxBytes); err == nil {
				diff = d
			}
		}
		jr, jerr := RunJudge(context.Background(), *cfg.Judge, JudgeOpts{
			ClaudePath: os.Getenv("HARVEST_JUDGE_CLI"), // test seam; "" = "claude"
			WorkDir:    workspaceDir,
			Diff:       diff,
		})
		if jerr != nil {
			facts.JudgeErr = jerr.Error()
		} else {
			facts.Judge = jr
			rec.emit(schema.EventJudgeScore, schema.JudgeScoreData{
				RubricHash: jr.RubricHash, Dimensions: jr.Dimensions,
				Total: jr.Total, MaxTotal: jr.MaxTotal,
				Model: jr.Model, CostUSD: jr.CostUSD,
			})
			rec.emit(schema.EventCostRecord, schema.CostRecordData{
				Source: "harvest_judge", Provider: "anthropic", Model: jr.Model,
				InputTokens: jr.InputTokens, OutputTokens: jr.OutputTokens,
				Turns: jr.Turns, CostUSD: jr.CostUSD,
			})
		}
	}

	if !cfg.Push {
		return finish(schema.StatusPass, passSummary(facts))
	}

	// -- push-by-sha (unchanged v0.5 mechanics: lease, askpass) --
	if _, err := git("remote", "get-url", "origin"); err != nil {
		return finish(schema.StatusFail, "workspace has no origin remote to push to")
	}
	pushArgs := []string{
		"push",
		"--force-with-lease=refs/heads/" + cfg.Branch,
		"origin",
		head + ":refs/heads/" + cfg.Branch,
	}
	cmd := exec.Command("git", pushArgs...)
	cmd.Dir = workspaceDir
	cmd.Env = os.Environ()
	if credsDir != "" {
		askpass, cleanup, err := writeAskpass(credsDir)
		if err != nil {
			return finish(schema.StatusError, "git credentials: "+err.Error())
		}
		defer cleanup()
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+askpass, "GIT_TERMINAL_PROMPT=0")
	}
	var pushOut strings.Builder
	cmd.Stdout = &pushOut
	cmd.Stderr = &pushOut
	if err := cmd.Run(); err != nil {
		// Auth/network/lease failures are platform faults (the lease only
		// trips on a concurrent harvest, which correctly errors).
		return finish(schema.StatusError, "git push failed: "+pushOut.String())
	}
	facts.PushedBranch = cfg.Branch
	rec.emit(schema.EventPushDone, schema.PushDoneData{
		Branch: cfg.Branch, Sha: head, ManifestArtifact: manifestArtifactName(facts),
	})

	return finish(schema.StatusPass, passSummary(facts))
}

// runFacts accumulates everything the finish path folds into results
// metadata, evidence, and step.end.
type runFacts struct {
	HeadSHA, BaseSHA string
	Dirty            string
	Gates            []GateOutcome
	Judge            *JudgeResult
	JudgeErr         string
	PushedBranch     string
	ManifestWritten  bool
}

func (f *runFacts) metadata(detail string) map[string]interface{} {
	m := map[string]interface{}{}
	if detail != "" {
		m["detail"] = detail
	}
	if f.HeadSHA != "" {
		m["head_sha"] = f.HeadSHA
	}
	if f.BaseSHA != "" {
		m["base_sha"] = f.BaseSHA
	}
	if f.PushedBranch != "" {
		m["pushed_branch"] = f.PushedBranch
	}
	if len(f.Gates) > 0 {
		m["gates"] = f.Gates
	}
	if f.Judge != nil {
		m["judge"] = map[string]interface{}{
			"rubric_hash": f.Judge.RubricHash, "total": f.Judge.Total,
			"max_total": f.Judge.MaxTotal, "pass": f.Judge.Pass,
		}
	}
	if f.JudgeErr != "" {
		m["judge_error"] = f.JudgeErr
	}
	return m
}

func (f *runFacts) judgeCost() float64 {
	if f.Judge == nil {
		return 0
	}
	return f.Judge.CostUSD
}

func buildResults(status schema.Status, detail string, meta map[string]interface{}) schema.Results {
	summary := detail
	if summary == "" {
		summary = string(status)
	}
	return schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    1,
		Summary:       summary,
		Artifacts:     []schema.Artifact{},
		Metadata:      meta,
	}
}

func stepEndStatus(s schema.Status) string {
	switch s {
	case schema.StatusPass:
		return "ok"
	case schema.StatusFail:
		return "failed"
	default:
		return "error"
	}
}

func manifestArtifactName(f *runFacts) string {
	if f.ManifestWritten {
		return "manifest.json"
	}
	return ""
}

func passSummary(f *runFacts) string {
	s := "verified"
	if len(f.Gates) > 0 {
		s = fmt.Sprintf("%d gate(s) ok", len(f.Gates))
	}
	if f.Judge != nil {
		verdict := "fail"
		if f.Judge.Pass {
			verdict = "pass"
		}
		s += fmt.Sprintf("; judge %.1f/10 (%s)", f.Judge.Total, verdict)
	}
	if f.JudgeErr != "" {
		s += "; judge errored (advisory)"
	}
	if f.PushedBranch != "" {
		s += "; pushed " + f.PushedBranch
	}
	return s
}

func envInt(key string) int {
	n, _ := strconv.Atoi(os.Getenv(key))
	return n
}

func optionalInt(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

// assembleEvidence maps runFacts onto the §6.4.1 evidence payload:
// failed gates → proven_issues gate-<gate> (category gate), a dirty
// tree → proven issue workspace-dirty, judge citations → observations
// judge-<dim>-<n> (category judge). score.value = judge total when the
// judge ran, else 10/0 for pass/fail; score.pass = status pass AND (no
// judge OR judge pass OR judge errored).
func assembleEvidence(cfg Config, status schema.Status, detail string, f *runFacts, durationSec int) *Evidence {
	ev := &Evidence{
		SchemaVersion: "harvest/1",
		Metadata: EvidenceMetadata{
			Repo: cfg.Repo, Commit: f.HeadSHA, Branch: cfg.Branch,
			DurationSec: durationSec,
		},
		ProvenIssues: []EvidenceIssue{},
		Observations: []EvidenceIssue{},
		Summary:      detail,
		Gates:        f.Gates,
		JudgeError:   f.JudgeErr,
	}
	if ev.Gates == nil {
		ev.Gates = []GateOutcome{}
	}
	if ev.Summary == "" {
		ev.Summary = string(status)
	}

	if f.Dirty != "" {
		ev.ProvenIssues = append(ev.ProvenIssues, EvidenceIssue{
			ID: "workspace-dirty", Severity: "high",
			Title:       "uncommitted changes in the workspace (F33)",
			Description: f.Dirty, Category: "correctness",
		})
	}
	for _, g := range f.Gates {
		if g.Status == "failed" {
			ev.ProvenIssues = append(ev.ProvenIssues, EvidenceIssue{
				ID: "gate-" + g.Gate, Severity: "high",
				Title:       fmt.Sprintf("gate %q failed", g.Gate),
				Description: truncate(g.Detail, 8192), Category: "gate",
			})
		}
	}
	if f.Judge != nil {
		ev.Metadata.AgentModel = f.Judge.Model
		perDim := map[string]int{}
		for _, iss := range f.Judge.Issues {
			perDim[iss.Dimension]++
			ev.Observations = append(ev.Observations, EvidenceIssue{
				ID:    fmt.Sprintf("judge-%s-%d", iss.Dimension, perDim[iss.Dimension]),
				Title: iss.Title, Description: iss.Description,
				File: iss.File, Line: iss.Line, Category: "judge",
			})
		}
		dims := make([]EvidenceDimension, len(f.Judge.Dimensions))
		for i, d := range f.Judge.Dimensions {
			dims[i] = EvidenceDimension{Name: d.Name, Score: d.Score, Max: d.Max, Rationale: d.Rationale}
		}
		ev.Judge = &EvidenceJudge{
			RubricHash: f.Judge.RubricHash, Dimensions: dims,
			Total: f.Judge.Total, MaxTotal: f.Judge.MaxTotal, Pass: f.Judge.Pass,
			Model: f.Judge.Model, CostUSD: f.Judge.CostUSD,
			BudgetExceeded: cfg.Judge != nil && cfg.Judge.BudgetUSD > 0 && f.Judge.CostUSD > cfg.Judge.BudgetUSD,
		}
	}

	gatesOK := status == schema.StatusPass
	switch {
	case f.Judge != nil:
		ev.Score = EvidenceScore{Value: f.Judge.Total, Max: 10, Pass: gatesOK && f.Judge.Pass}
	case gatesOK:
		ev.Score = EvidenceScore{Value: 10, Max: 10, Pass: true}
	default:
		ev.Score = EvidenceScore{Value: 0, Max: 10, Pass: false}
	}
	if f.JudgeErr != "" {
		// §6.4: an errored judge never blocks — score falls back to gates
		ev.Score = EvidenceScore{Value: 10, Max: 10, Pass: gatesOK}
		if !gatesOK {
			ev.Score.Value = 0
		}
	}
	return ev
}

// writeAskpass materializes a GIT_ASKPASS helper answering username /
// password prompts from the mounted secret files. The token never
// touches argv or logs; it flows only through the helper's stdout into
// git.
func writeAskpass(credsDir string) (path string, cleanup func(), err error) {
	token, err := os.ReadFile(filepath.Join(credsDir, "token"))
	if err != nil {
		return "", nil, fmt.Errorf("read token: %w", err)
	}
	username := "x-access-token"
	if u, err := os.ReadFile(filepath.Join(credsDir, "username")); err == nil && len(strings.TrimSpace(string(u))) > 0 {
		username = strings.TrimSpace(string(u))
	}

	dir, err := os.MkdirTemp("", "harvest-askpass")
	if err != nil {
		return "", nil, err
	}
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  Username*) printf '%%s' '%s';;\n  *) printf '%%s' '%s';;\nesac\n",
		strings.ReplaceAll(username, "'", ""), strings.ReplaceAll(strings.TrimSpace(string(token)), "'", ""))
	path = filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { os.RemoveAll(dir) }, nil
}
