package implement

import (
	"context"

	"github.com/concourse/ci-agent/implement/adapter"
	"github.com/concourse/ci-agent/implement/tdd"
)

// SequencerOpts configures the multi-task sequencer.
type SequencerOpts struct {
	RepoDir                string
	Phases                 []Phase
	SpecContext            string
	Adapter                adapter.Adapter
	Tracker                *TaskTracker
	TestCmd                string
	MaxRetries             int
	MaxConsecutiveFailures int
	OutputDir              string
}

// SequencerResult summarizes the outcome of running all tasks.
type SequencerResult struct {
	Total     int `json:"total"`
	Committed int `json:"committed"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	Pending   int `json:"pending"`
}

// RunAll executes all plan tasks in order using the TDD loop.
func RunAll(ctx context.Context, opts SequencerOpts) (*SequencerResult, error) {
	for {
		if !opts.Tracker.CanContinue(opts.MaxConsecutiveFailures) {
			break
		}

		next := opts.Tracker.NextPending()
		if next == nil {
			break
		}

		planTask := findTask(opts.Phases, next.TaskID)

		loopOpts := tdd.TaskLoopOpts{
			RepoDir: opts.RepoDir,
			Task: tdd.TaskInfo{
				ID:          planTask.ID,
				Description: planTask.Description,
				Phase:       planTask.Phase,
				Files:       planTask.Files,
			},
			SpecContext: opts.SpecContext,
			Adapter:     opts.Adapter,
			TestCmd:     opts.TestCmd,
			MaxRetries:  opts.MaxRetries,
		}

		taskResult, err := tdd.ExecuteTask(ctx, loopOpts)
		if err != nil {
			opts.Tracker.Fail(next.TaskID, "executor_error: "+err.Error())
			continue
		}

		switch taskResult.Status {
		case tdd.TaskCommitted:
			opts.Tracker.Advance(next.TaskID)
			opts.Tracker.Advance(next.TaskID)
			opts.Tracker.Advance(next.TaskID)
			opts.Tracker.SetCommitInfo(next.TaskID, taskResult.CommitSHA, taskResult.TestFile)
		case tdd.TaskSkipped:
			opts.Tracker.Skip(next.TaskID, taskResult.Reason)
		case tdd.TaskFailed:
			opts.Tracker.Fail(next.TaskID, taskResult.Reason)
		}

		if opts.OutputDir != "" {
			opts.Tracker.Save(opts.OutputDir)
		}
	}

	summary := opts.Tracker.Summary()
	return &SequencerResult{
		Total:     summary.Total,
		Committed: summary.Committed,
		Skipped:   summary.Skipped,
		Failed:    summary.Failed,
		Pending:   summary.Pending,
	}, nil
}

func findTask(phases []Phase, taskID string) PlanTask {
	for _, p := range phases {
		for _, t := range p.Tasks {
			if t.ID == taskID {
				return t
			}
		}
	}
	return PlanTask{ID: taskID}
}
