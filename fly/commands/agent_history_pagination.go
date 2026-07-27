package commands

import (
	"fmt"
	"io"
	"net/url"
	"strconv"

	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/workflow"
)

func addAgentHistoryCursor(query url.Values, raw string) error {
	if raw == "" {
		return nil
	}
	if _, err := pagination.Decode(raw); err != nil {
		return fmt.Errorf("invalid agent history cursor")
	}
	query.Set("cursor", raw)
	return nil
}

func reportNextAgentHistoryCursor(writer io.Writer, kind, raw string) error {
	if raw == "" {
		return nil
	}
	if _, err := pagination.Decode(raw); err != nil {
		return fmt.Errorf("server returned invalid %s cursor %q", kind, raw)
	}
	_, err := fmt.Fprintf(writer, "# next cursor: %s\n", raw)
	return err
}

// opaqueAgentHistoryCursor validates the shared durable-history token every
// agent listing surface serves.
func opaqueAgentHistoryCursor(raw string) error {
	_, err := pagination.Decode(raw)
	return err
}

// workflowVersionCursor validates the workflow-version history token, which
// is still a plain workflow version number rather than the opaque durable
// cursor (see the versions handler and store).
func workflowVersionCursor(raw string) error {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > workflow.MaxWorkflowVersion {
		return fmt.Errorf("not a workflow version between 1 and %d", workflow.MaxWorkflowVersion)
	}
	return nil
}

// followAgentHistoryPages walks every page of a cursor-paginated agent
// listing. fetchPage receives the cursor to request ("" for the first page)
// and returns the server's X-Next-Cursor; returning done stops the walk
// immediately, and an empty next cursor ends it normally.
//
// The loop owns the two invariants no caller may skip: a next cursor whose
// SHAPE the client recognises (validateCursor — a server that hands back
// something else is not a cursor source we can follow), and no REPEAT, since
// a cursor the server already returned would otherwise page forever.
func followAgentHistoryPages(
	kind string,
	validateCursor func(string) error,
	fetchPage func(cursor string) (nextCursor string, done bool, err error),
) error {
	cursor := ""
	seen := map[string]struct{}{}
	for {
		next, done, err := fetchPage(cursor)
		if err != nil {
			return err
		}
		if done || next == "" {
			return nil
		}
		if err := validateCursor(next); err != nil {
			return fmt.Errorf("server returned invalid %s cursor %q: %w", kind, next, err)
		}
		if _, duplicate := seen[next]; duplicate {
			return fmt.Errorf("server returned repeated %s cursor %q", kind, next)
		}
		seen[next] = struct{}{}
		cursor = next
	}
}
