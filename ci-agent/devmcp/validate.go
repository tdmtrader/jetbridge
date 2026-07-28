package devmcp

import (
	"context"
	"fmt"
	"time"
)

const (
	ValidationStatusPassed = "passed"
	ValidationStatusFailed = "failed"
	ValidationStatusError  = "error"
)

// ValidationRequest combines parsed immutable policy with candidate-local path
// facts. It never grants the candidate control of the profile or config bytes.
type ValidationRequest struct {
	Profile      ValidationProfile
	Identity     ProfileIdentity
	ChangedPaths []string
}

// CheckAttempt retains the complete Core result and log location for one
// concrete invocation, including retries.
type CheckAttempt struct {
	CheckID     string     `json:"check_id"`
	Number      int        `json:"number"`
	Result      ToolResult `json:"result"`
	FullLogPath string     `json:"full_log_path"`
}

type ValidationResult struct {
	Status   string         `json:"status"`
	Attempts []CheckAttempt `json:"attempts"`
}

// ValidateProfile executes the supplied promoted profile through one Core.
// Declared failures are retried; infrastructure failures and cancellations are
// terminal errors. The returned attempt order is deterministic.
func ValidateProfile(ctx context.Context, core Core, request ValidationRequest, progress ProgressFunc) (ValidationResult, error) {
	result := ValidationResult{Status: ValidationStatusError, Attempts: make([]CheckAttempt, 0)}
	if ctx == nil {
		return result, fmt.Errorf("validation context is required")
	}
	if core == nil {
		return result, fmt.Errorf("validation core is required")
	}
	profile := cloneValidationProfile(request.Profile)
	if err := verifyParsedProfile(profile, request.Identity); err != nil {
		return result, err
	}
	if err := validateProfileShape(profile); err != nil {
		return result, fmt.Errorf("invalid validation profile: %w", err)
	}
	changedPaths := append([]string(nil), request.ChangedPaths...)
	if profileUsesAffectedScope(profile) && len(changedPaths) == 0 {
		return result, fmt.Errorf("affected validation requires changed paths")
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("validation context: %w", err)
	}

	result.Status = ValidationStatusPassed
	for _, check := range profile.Checks {
		timeout, err := profileCheckTimeout(check)
		if err != nil {
			result.Status = ValidationStatusError
			return result, fmt.Errorf("invalid validation profile: %w", err)
		}
		requests, err := resolveValidationRequests(ctx, core, check, changedPaths, timeout)
		if err != nil {
			result.Status = ValidationStatusError
			return result, fmt.Errorf("check %q: %w", check.ID, err)
		}

		attemptNumber := 0
		for _, coreRequest := range requests {
			for retry := 0; retry <= check.Retries; retry++ {
				attemptNumber++
				if err := ctx.Err(); err != nil {
					result.Status = ValidationStatusError
					return result, fmt.Errorf("check %q before attempt %d: %w", check.ID, attemptNumber, err)
				}
				toolResult := executeValidationAttempt(ctx, core, coreRequest, check, attemptNumber, timeout, progress)
				result.Attempts = append(result.Attempts, CheckAttempt{CheckID: check.ID, Number: attemptNumber, Result: toolResult, FullLogPath: toolResult.LogPath})
				switch toolResult.Status {
				case StatusOK:
					retry = check.Retries
				case StatusFailed:
					if retry == check.Retries {
						result.Status = ValidationStatusFailed
						return result, nil
					}
				case StatusError:
					result.Status = ValidationStatusError
					return result, nil
				default:
					result.Status = ValidationStatusError
					return result, nil
				}
			}
		}
	}
	return result, nil
}

func profileUsesAffectedScope(profile ValidationProfile) bool {
	for _, check := range profile.Checks {
		if check.Scope == ValidationScopeAffected {
			return true
		}
	}
	return false
}

func resolveValidationRequests(ctx context.Context, core Core, check ProfileCheck, changedPaths []string, timeout time.Duration) ([]Request, error) {
	switch check.Scope {
	case ValidationScopeFull:
		return []Request{{Operation: check.Operation}}, nil
	case ValidationScopeRequired:
		return componentValidationRequests(check, check.Components), nil
	case ValidationScopeAffected:
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("before affected mapping: %w", err)
		}
		affectedContext, cancel := context.WithTimeout(ctx, timeout)
		affected, err := core.AffectedComponents(affectedContext, append([]string(nil), changedPaths...))
		contextErr := affectedContext.Err()
		cancel()
		if err == nil && contextErr != nil {
			err = contextErr
		}
		if err != nil {
			return nil, fmt.Errorf("resolve affected components: %w", err)
		}
		if len(affected.UnmappedPaths) != 0 {
			return componentValidationRequests(check, check.Components), nil
		}
		affectedSet := make(map[string]struct{}, len(affected.Components))
		for _, component := range affected.Components {
			affectedSet[component] = struct{}{}
		}
		selected := make([]string, 0, len(check.Components))
		for _, component := range check.Components {
			if _, found := affectedSet[component]; found {
				selected = append(selected, component)
			}
		}
		return componentValidationRequests(check, selected), nil
	default:
		return nil, fmt.Errorf("invalid validation scope %q", check.Scope)
	}
}

func componentValidationRequests(check ProfileCheck, components []string) []Request {
	requests := make([]Request, len(components))
	for index, component := range components {
		requests[index] = Request{Operation: check.Operation, Component: component}
	}
	return requests
}

func executeValidationAttempt(ctx context.Context, core Core, request Request, check ProfileCheck, attemptNumber int, timeout time.Duration, progress ProgressFunc) ToolResult {
	attemptContext, cancel := context.WithTimeout(ctx, timeout)
	result, err := core.Execute(attemptContext, request, progress)
	contextErr := attemptContext.Err()
	cancel()
	switch {
	case err != nil:
		return ToolResult{Status: StatusError, Summary: fmt.Sprintf("%s attempt %d: error (%s)", check.ID, attemptNumber, err)}
	case contextErr != nil:
		return ToolResult{Status: StatusError, Summary: fmt.Sprintf("%s attempt %d: error (%s)", check.ID, attemptNumber, contextErr), LogPath: result.LogPath, OutputTail: result.OutputTail, DurationSeconds: result.DurationSeconds}
	case result.Status != StatusOK && result.Status != StatusFailed && result.Status != StatusError:
		return ToolResult{Status: StatusError, Summary: fmt.Sprintf("%s attempt %d: unknown core status %q", check.ID, attemptNumber, result.Status), LogPath: result.LogPath, OutputTail: result.OutputTail, DurationSeconds: result.DurationSeconds}
	default:
		return result
	}
}
