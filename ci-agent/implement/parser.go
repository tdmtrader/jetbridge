package implement

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Phase represents a phase in a plan with ordered tasks.
type Phase struct {
	Name  string     `json:"name"`
	Tasks []PlanTask `json:"tasks"`
}

// PlanTask represents a single actionable task extracted from a plan.
type PlanTask struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Phase       string   `json:"phase"`
	Files       []string `json:"files,omitempty"`
	Completed   bool     `json:"completed,omitempty"`
	InProgress  bool     `json:"in_progress,omitempty"`
}

var (
	phaseRe    = regexp.MustCompile(`^##\s+Phase\s+\d+:\s+(.+)$`)
	taskRe     = regexp.MustCompile(`^- \[([ x~])\]\s+(.+)$`)
	backtickRe = regexp.MustCompile("`([^`]+)`")
	fileExtRe  = regexp.MustCompile(`\.\w+$`)
)

// ParsePlan parses a Markdown plan into phases and tasks.
func ParsePlan(r io.Reader) ([]Phase, error) {
	scanner := bufio.NewScanner(r)
	var phases []Phase
	var currentPhase *Phase
	taskCount := 0

	for scanner.Scan() {
		line := scanner.Text()

		// Check for phase heading.
		if m := phaseRe.FindStringSubmatch(line); m != nil {
			if currentPhase != nil {
				phases = append(phases, *currentPhase)
			}
			currentPhase = &Phase{Name: strings.TrimSpace(m[1])}
			continue
		}

		// Check for task line (top-level only — sub-bullets are indented).
		if m := taskRe.FindStringSubmatch(line); m != nil && currentPhase != nil {
			phaseIdx := len(phases) + 1
			taskIdx := len(currentPhase.Tasks) + 1

			task := PlanTask{
				ID:          fmt.Sprintf("%d.%d", phaseIdx, taskIdx),
				Description: strings.TrimSpace(m[2]),
				Phase:       currentPhase.Name,
			}

			switch m[1] {
			case "x":
				task.Completed = true
			case "~":
				task.InProgress = true
			}

			// Extract file references from backtick code spans.
			for _, bt := range backtickRe.FindAllStringSubmatch(task.Description, -1) {
				candidate := bt[1]
				if fileExtRe.MatchString(candidate) && strings.Contains(candidate, "/") {
					task.Files = append(task.Files, candidate)
				}
			}

			currentPhase.Tasks = append(currentPhase.Tasks, task)
			taskCount++
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Flush last phase.
	if currentPhase != nil {
		phases = append(phases, *currentPhase)
	}

	if len(phases) == 0 {
		return nil, fmt.Errorf("no phases found in plan")
	}

	if taskCount == 0 {
		return nil, fmt.Errorf("no tasks found in plan")
	}

	return phases, nil
}

// ParsePlanFile reads a plan from a file path.
func ParsePlanFile(path string) ([]Phase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParsePlan(f)
}
