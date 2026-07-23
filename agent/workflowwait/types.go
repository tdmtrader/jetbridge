package workflowwait

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

type ID int64

func ParseID(raw string) (ID, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || raw[0] == '+' || len(raw) > 1 && raw[0] == '0' {
		return 0, fmt.Errorf("workflow wait: ID must be a canonical positive decimal")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != raw {
		return 0, fmt.Errorf("workflow wait: ID must be a canonical positive decimal")
	}
	return ID(value), nil
}

func (id ID) Validate() error {
	if id <= 0 {
		return fmt.Errorf("workflow wait: ID must be positive")
	}
	return nil
}

func (id ID) String() string { return fmt.Sprintf("%d", id) }

func (id ID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return []byte(`"` + strconv.FormatInt(int64(id), 10) + `"`), nil
}

func (id *ID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("workflow wait: ID must be a quoted canonical decimal")
	}
	parsed, err := ParseID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusResolved  Status = "resolved"
	StatusTimedOut  Status = "timed_out"
	StatusCancelled Status = "cancelled"
)

func (status Status) Validate() error {
	switch status {
	case StatusWaiting, StatusResolved, StatusTimedOut, StatusCancelled:
		return nil
	default:
		return fmt.Errorf("workflow wait: invalid status %q", status)
	}
}

type TimeoutPolicy string

const (
	TimeoutFail    TimeoutPolicy = "fail"
	TimeoutDefault TimeoutPolicy = "default"
)

func (policy TimeoutPolicy) Validate() error {
	if policy != TimeoutFail && policy != TimeoutDefault {
		return fmt.Errorf("workflow wait: timeout policy must be fail or default")
	}
	return nil
}

// ExecutionKey is the exact server-owned identity of one planned attempt.
// OutputName prevents two waits in the same plan node from ever aliasing if
// the step contract later grows multiple outputs.
type ExecutionKey struct {
	TeamID        int
	WorkflowRunID snapshot.WorkflowRunID
	BuildID       int64
	PlanID        string
	Attempt       string
	OutputName    string
}

func (key ExecutionKey) Validate() error {
	if key.TeamID <= 0 || key.BuildID <= 0 {
		return fmt.Errorf("workflow wait: team and build IDs must be positive")
	}
	if err := key.WorkflowRunID.Validate(); err != nil {
		return err
	}
	for label, value := range map[string]string{"plan ID": key.PlanID, "attempt": key.Attempt, "output name": key.OutputName} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("workflow wait: %s is required", label)
		}
	}
	return nil
}

type CreateRequest struct {
	Key                  ExecutionKey
	QuestionName         string
	Question             snapshot.SnapshotRef
	ExpectedType         snapshot.TypeRef
	Deadline             time.Time
	TimeoutPolicy        TimeoutPolicy
	Default              *snapshot.SnapshotRef
	WorkflowPort         string
	WorkflowDefinitionID int
}

func (request CreateRequest) Validate() error {
	if err := request.Key.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(request.QuestionName) == "" {
		return fmt.Errorf("workflow wait: question name is required")
	}
	if err := request.Question.Validate(); err != nil {
		return fmt.Errorf("workflow wait: question: %w", err)
	}
	if request.Question.Type != snapshot.TypeRef("question/v1") {
		return fmt.Errorf("workflow wait: question must have exact type question/v1")
	}
	if request.ExpectedType != snapshot.TypeRef("human-answer/v1") {
		return fmt.Errorf("workflow wait: expected type must be human-answer/v1")
	}
	if request.Deadline.IsZero() {
		return fmt.Errorf("workflow wait: deadline is required")
	}
	if err := request.TimeoutPolicy.Validate(); err != nil {
		return err
	}
	if request.TimeoutPolicy == TimeoutDefault {
		if request.Default == nil {
			return fmt.Errorf("workflow wait: default snapshot is required")
		}
		if err := request.Default.Validate(); err != nil || request.Default.Type != request.ExpectedType {
			return fmt.Errorf("workflow wait: default snapshot must match the expected type")
		}
	} else if request.Default != nil {
		return fmt.Errorf("workflow wait: default snapshot requires default timeout policy")
	}
	hasPublicOutput := request.WorkflowPort != "" || request.WorkflowDefinitionID != 0
	if hasPublicOutput && (strings.TrimSpace(request.WorkflowPort) == "" || request.WorkflowDefinitionID <= 0) {
		return fmt.Errorf("workflow wait: public output linkage is incomplete")
	}
	return nil
}

type Wait struct {
	ID                    ID
	Key                   ExecutionKey
	QuestionName          string
	Question              snapshot.SnapshotRef
	ExpectedType          snapshot.TypeRef
	Deadline              time.Time
	TimeoutPolicy         TimeoutPolicy
	Default               *snapshot.SnapshotRef
	WorkflowPort          string
	WorkflowDefinitionID  int
	Status                Status
	Answer                *snapshot.SnapshotRef
	ResolvedBy            string
	ResolvedByDisplayName string
	ResolutionSource      string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ResolvedAt            *time.Time
}

func (wait Wait) Validate() error {
	if err := wait.ID.Validate(); err != nil {
		return err
	}
	if err := (CreateRequest{
		Key: wait.Key, QuestionName: wait.QuestionName, Question: wait.Question,
		ExpectedType: wait.ExpectedType, Deadline: wait.Deadline, TimeoutPolicy: wait.TimeoutPolicy,
		Default: wait.Default, WorkflowPort: wait.WorkflowPort, WorkflowDefinitionID: wait.WorkflowDefinitionID,
	}).Validate(); err != nil {
		return err
	}
	if err := wait.Status.Validate(); err != nil {
		return err
	}
	if wait.CreatedAt.IsZero() || wait.UpdatedAt.IsZero() {
		return fmt.Errorf("workflow wait: timestamps are required")
	}
	if wait.Answer != nil {
		if err := wait.Answer.Validate(); err != nil || wait.Answer.Type != wait.ExpectedType {
			return fmt.Errorf("workflow wait: answer does not match expected type")
		}
	}
	switch wait.Status {
	case StatusWaiting:
		if wait.Answer != nil || wait.ResolvedAt != nil || wait.ResolvedBy != "" ||
			wait.ResolvedByDisplayName != "" || wait.ResolutionSource != "" {
			return fmt.Errorf("workflow wait: waiting state carries terminal resolution")
		}
	case StatusResolved:
		if wait.Answer == nil || wait.ResolvedAt == nil || strings.TrimSpace(wait.ResolvedBy) == "" ||
			strings.TrimSpace(wait.ResolvedByDisplayName) == "" || wait.ResolutionSource != "human" {
			return fmt.Errorf("workflow wait: resolved state is incomplete")
		}
	case StatusTimedOut:
		if wait.ResolvedAt == nil || wait.ResolutionSource != "timeout" {
			return fmt.Errorf("workflow wait: timed-out state is incomplete")
		}
		if wait.TimeoutPolicy == TimeoutDefault && wait.Answer == nil {
			return fmt.Errorf("workflow wait: default timeout has no answer")
		}
		if wait.TimeoutPolicy == TimeoutFail && wait.Answer != nil {
			return fmt.Errorf("workflow wait: failing timeout carries an answer")
		}
	case StatusCancelled:
		if wait.Answer != nil || wait.ResolvedAt == nil || strings.TrimSpace(wait.ResolvedBy) == "" || wait.ResolutionSource != "cancel" {
			return fmt.Errorf("workflow wait: cancelled state is incomplete")
		}
	}
	return nil
}

func cloneRef(ref *snapshot.SnapshotRef) *snapshot.SnapshotRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneWait(wait Wait) Wait {
	wait.Default = cloneRef(wait.Default)
	wait.Answer = cloneRef(wait.Answer)
	wait.ResolvedAt = cloneTime(wait.ResolvedAt)
	return wait
}
