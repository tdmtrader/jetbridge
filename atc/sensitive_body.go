package atc

import "strings"

// sensitiveBodyActions name the routes whose request body belongs only to the
// authorized handler: session credentials, input bearers, uploaded trees and
// operational text. Middleware that runs before that handler -- the audit log
// and the API policy check -- records route metadata for these and never
// parses, reads or forwards the body. Parsing it there would both copy it into
// a log or a remote policy service and consume it before the handler runs.
//
// This is the one declaration both consult. A guard test requires every name
// here to be a route in Routes.
var sensitiveBodyActions = []string{
	// The owner's session credentials, on stdin to the review worker.
	HandoffPipelineRunCredentials,
	// Named inputs carry short-lived source-grant bearers.
	CreatePipelineRunV2,
	// The body is the uploaded input tree.
	UploadPipelineRunInput,
	// The reason is operational text recorded by the authorized handler.
	CancelPipelineRun,
}

// SensitiveBodyActions returns a copy of the declared sensitive-body actions.
func SensitiveBodyActions() []string {
	return append([]string(nil), sensitiveBodyActions...)
}

// RequestBodyIsSensitive reports whether middleware must leave action's body
// unread.
func RequestBodyIsSensitive(action string) bool {
	for _, sensitive := range sensitiveBodyActions {
		if action == sensitive {
			return true
		}
	}
	return false
}

// RouteParamNames returns the `:name` path parameters the route table declares
// for action, in path order and without the colon. An action served by more
// than one route gets the union, first occurrence first.
func RouteParamNames(action string) []string {
	var names []string
	seen := map[string]bool{}
	for _, route := range Routes {
		if route.Name != action {
			continue
		}
		for _, segment := range strings.Split(route.Path, "/") {
			name, ok := strings.CutPrefix(segment, ":")
			if ok && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}
