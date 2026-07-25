package merge

import (
	"fmt"
	"io"
	"strings"
)

// Exit codes match the harvest runner's convention so the two steps read
// alike: 0 ok, 1 fail (the merge legitimately did not land), 2 error
// (tooling fault — nothing was attempted).
const (
	exitOK    = 0
	exitFail  = 1
	exitError = 2
)

// GateRunner gates the PROSPECTIVE merge result. It is handed the working
// clone with the merge already applied, so it tests what would actually
// land rather than what the delivered branch looked like in isolation — the
// merge-queue property that a branch green on its own can still break the
// target. A non-nil error blocks the push.
//
// It is a function rather than a harvest.GatePolicy so this package stays a
// leaf: cmd/merge-runner composes harvest.RunGates in.
type GateRunner func(workspaceDir string) error

// Config is the pod-side merge runner's input. TargetBranch comes from the
// TICKET, never a hardcoded branch — platform work targets `jetbridge`,
// where CI promotes to main, so the platform never needs push-to-main.
type Config struct {
	Repo         string
	TicketID     int
	Branch       string // delivered branch, e.g. agent/ticket-12
	TargetBranch string // e.g. jetbridge
	Method       Method
	Message      string
	Push         bool // false = speculative: answer, do not land
}

// Outcome is the recorded result. Because the PLATFORM performed the merge,
// these are facts rather than inferences — the entire point of the design.
type Outcome struct {
	Status        string // ok | fail | error
	Detail        string
	ResultSha     string
	Behind        int
	Conflict      bool
	ConflictPaths []string
	Pushed        bool
}

// Run performs the pod-side merge: fetch, prepare speculatively, gate the
// merged result, then push. It never force-pushes: if the target moved
// since the fetch, the push is rejected and reported as a fail, because the
// speculative result is then stale and must be recomputed. That rejection
// is the merge-queue race, handled by declining rather than overwriting.
func Run(cfg Config, workspaceDir string, gates GateRunner, out io.Writer) (Outcome, int) {
	fail := func(detail string, o Outcome) (Outcome, int) {
		o.Status, o.Detail = "fail", detail
		fmt.Fprintln(out, "merge failed: "+detail)
		return o, exitFail
	}
	fault := func(detail string) (Outcome, int) {
		fmt.Fprintln(out, "merge error: "+detail)
		return Outcome{Status: "error", Detail: detail}, exitError
	}

	if cfg.Branch == "" || cfg.TargetBranch == "" {
		return fault("merge requires both a delivered branch and a target branch")
	}
	if cfg.Method != MethodMerge && cfg.Method != MethodSquash {
		return fault(fmt.Sprintf("unknown merge method %q", cfg.Method))
	}

	if _, err := run(workspaceDir, "fetch", "--prune", "origin",
		"+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return fault("git fetch: " + err.Error())
	}

	branchRef := "origin/" + cfg.Branch
	targetRef := "origin/" + cfg.TargetBranch

	res, err := Prepare(workspaceDir, Plan{
		Branch: branchRef, Target: targetRef,
		Method: cfg.Method, Message: cfg.Message,
	})
	if err != nil {
		return fault("prepare: " + err.Error())
	}
	if res.Conflict {
		return fail(
			"merge conflict in: "+strings.Join(res.ConflictPaths, ", ")+
				" — the branch needs a rebase or a fix-loop re-dispatch, not a forced merge",
			Outcome{Conflict: true, ConflictPaths: res.ConflictPaths, Behind: res.Behind},
		)
	}

	outcome := Outcome{ResultSha: res.ResultSha, Behind: res.Behind}

	// Gates run HERE — against the merged tree, before anything is published.
	if gates != nil {
		if err := gates(workspaceDir); err != nil {
			return fail("gates failed on the merged result: "+err.Error(), outcome)
		}
	}

	if !cfg.Push {
		outcome.Status, outcome.Detail = "ok", "speculative: would land cleanly and pass"
		fmt.Fprintln(out, outcome.Detail+" at "+outcome.ResultSha)
		return outcome, exitOK
	}

	if _, err := run(workspaceDir, "push", "origin",
		"HEAD:refs/heads/"+cfg.TargetBranch); err != nil {
		// Non-fast-forward means the target moved under us; the speculative
		// result is stale. Declining is correct — never force a target branch.
		return fail("push rejected (target moved since fetch; recompute the merge): "+err.Error(), outcome)
	}

	outcome.Status = "ok"
	outcome.Pushed = true
	outcome.Detail = fmt.Sprintf("merged %s into %s at %s", cfg.Branch, cfg.TargetBranch, outcome.ResultSha)
	fmt.Fprintln(out, outcome.Detail)
	return outcome, exitOK
}
