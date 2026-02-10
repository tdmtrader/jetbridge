package implement

import (
	"fmt"
	"strings"
	"time"
)

// RenderSummary generates a Markdown summary of the implementation run.
func RenderSummary(tracker *TaskTracker, conf *ConfidenceResult, duration time.Duration) string {
	var b strings.Builder
	summary := tracker.Summary()

	b.WriteString("# Implementation Summary\n\n")

	// Stats table.
	b.WriteString("| Metric | Value |\n")
	b.WriteString("|--------|-------|\n")
	fmt.Fprintf(&b, "| Total Tasks | %d |\n", summary.Total)
	fmt.Fprintf(&b, "| Committed | %d |\n", summary.Committed)
	fmt.Fprintf(&b, "| Skipped | %d |\n", summary.Skipped)
	fmt.Fprintf(&b, "| Failed | %d |\n", summary.Failed)
	fmt.Fprintf(&b, "| Confidence | %.2f |\n", conf.Score)
	fmt.Fprintf(&b, "| Status | %s |\n", conf.Status)
	fmt.Fprintf(&b, "| Duration | %s |\n", formatDuration(duration))
	b.WriteString("\n")

	// Per-phase breakdown.
	currentPhase := ""
	for _, tp := range tracker.Tasks {
		if tp.Phase != currentPhase {
			currentPhase = tp.Phase
			fmt.Fprintf(&b, "## Phase: %s\n\n", currentPhase)
		}

		icon := statusIcon(tp.Status)
		fmt.Fprintf(&b, "- %s **%s** — %s\n", icon, tp.TaskID, tp.Description)

		if tp.CommitSHA != "" {
			sha := tp.CommitSHA
			if len(sha) > 8 {
				sha = sha[:8]
			}
			fmt.Fprintf(&b, "  - Commit: `%s`\n", sha)
		}
		if tp.TestFile != "" {
			fmt.Fprintf(&b, "  - Test: `%s`\n", tp.TestFile)
		}
		if tp.Reason != "" {
			fmt.Fprintf(&b, "  - Reason: %s\n", tp.Reason)
		}
	}

	return b.String()
}

func statusIcon(s TaskStatus) string {
	switch s {
	case StatusCommitted:
		return "[x]"
	case StatusSkipped:
		return "[-]"
	case StatusFailed:
		return "[!]"
	case StatusRed, StatusGreen:
		return "[~]"
	default:
		return "[ ]"
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
