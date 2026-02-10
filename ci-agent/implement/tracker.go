package implement

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// TaskStatus represents the state of a task during execution.
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusRed       TaskStatus = "red"
	StatusGreen     TaskStatus = "green"
	StatusCommitted TaskStatus = "committed"
	StatusSkipped   TaskStatus = "skipped"
	StatusFailed    TaskStatus = "failed"
)

// TaskProgress records the state of a single task.
type TaskProgress struct {
	TaskID      string     `json:"task_id"`
	Description string     `json:"description"`
	Phase       string     `json:"phase"`
	Status      TaskStatus `json:"status"`
	Reason      string     `json:"reason,omitempty"`
	CommitSHA   string     `json:"commit_sha,omitempty"`
	TestFile    string     `json:"test_file,omitempty"`
}

// TrackerSummary holds aggregate counts.
type TrackerSummary struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Red       int `json:"red"`
	Green     int `json:"green"`
	Committed int `json:"committed"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}

// TaskTracker maintains per-task progress state.
type TaskTracker struct {
	Tasks              []TaskProgress `json:"tasks"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
}

// NewTaskTracker creates a tracker from parsed plan phases.
func NewTaskTracker(phases []Phase) *TaskTracker {
	var tasks []TaskProgress
	for _, p := range phases {
		for _, t := range p.Tasks {
			status := StatusPending
			if t.Completed {
				status = StatusCommitted
			}
			tasks = append(tasks, TaskProgress{
				TaskID:      t.ID,
				Description: t.Description,
				Phase:       t.Phase,
				Status:      status,
			})
		}
	}
	return &TaskTracker{Tasks: tasks}
}

func (t *TaskTracker) find(taskID string) *TaskProgress {
	for i := range t.Tasks {
		if t.Tasks[i].TaskID == taskID {
			return &t.Tasks[i]
		}
	}
	return nil
}

// StatusOf returns the current status of a task.
func (t *TaskTracker) StatusOf(taskID string) TaskStatus {
	tp := t.find(taskID)
	if tp == nil {
		return ""
	}
	return tp.Status
}

// Advance moves a task to the next state: pending→red→green→committed.
func (t *TaskTracker) Advance(taskID string) error {
	tp := t.find(taskID)
	if tp == nil {
		return fmt.Errorf("unknown task: %s", taskID)
	}

	switch tp.Status {
	case StatusPending:
		tp.Status = StatusRed
	case StatusRed:
		tp.Status = StatusGreen
	case StatusGreen:
		tp.Status = StatusCommitted
		t.ConsecutiveFailures = 0
	default:
		return fmt.Errorf("cannot advance task %s in state %s", taskID, tp.Status)
	}
	return nil
}

// Skip marks a task as skipped with a reason.
func (t *TaskTracker) Skip(taskID, reason string) {
	tp := t.find(taskID)
	if tp == nil {
		return
	}
	tp.Status = StatusSkipped
	tp.Reason = reason
	t.ConsecutiveFailures = 0
}

// Fail marks a task as failed with a reason.
func (t *TaskTracker) Fail(taskID, reason string) {
	tp := t.find(taskID)
	if tp == nil {
		return
	}
	tp.Status = StatusFailed
	tp.Reason = reason
	t.ConsecutiveFailures++
}

// NextPending returns the next pending task, or nil if none remain.
func (t *TaskTracker) NextPending() *TaskProgress {
	for i := range t.Tasks {
		if t.Tasks[i].Status == StatusPending {
			return &t.Tasks[i]
		}
	}
	return nil
}

// IsComplete returns true when no pending or in-flight tasks remain.
func (t *TaskTracker) IsComplete() bool {
	for _, tp := range t.Tasks {
		if tp.Status == StatusPending || tp.Status == StatusRed || tp.Status == StatusGreen {
			return false
		}
	}
	return true
}

// CanContinue returns false when consecutive failures exceed the threshold.
func (t *TaskTracker) CanContinue(maxConsecutiveFailures int) bool {
	return t.ConsecutiveFailures < maxConsecutiveFailures
}

// Summary returns aggregate counts.
func (t *TaskTracker) Summary() TrackerSummary {
	s := TrackerSummary{Total: len(t.Tasks)}
	for _, tp := range t.Tasks {
		switch tp.Status {
		case StatusPending:
			s.Pending++
		case StatusRed:
			s.Red++
		case StatusGreen:
			s.Green++
		case StatusCommitted:
			s.Committed++
		case StatusSkipped:
			s.Skipped++
		case StatusFailed:
			s.Failed++
		}
	}
	return s
}

// Save persists the tracker to progress.json in the given directory.
func (t *TaskTracker) Save(outputDir string) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "progress.json"), data, 0644)
}

// LoadTracker reads a tracker from a progress.json file.
func LoadTracker(path string) (*TaskTracker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tracker TaskTracker
	if err := json.Unmarshal(data, &tracker); err != nil {
		return nil, err
	}
	return &tracker, nil
}
