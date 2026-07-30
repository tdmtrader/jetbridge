package pullrequest

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// ImpactMode selects who may require another human approval. Deterministic
// platform rules are authoritative in every mode in which they are admitted;
// an agent assessment can only add an escalation.
type ImpactMode string

const (
	ImpactModeAlways       ImpactMode = "always"
	ImpactModeRules        ImpactMode = "rules"
	ImpactModeAgentDecides ImpactMode = "agent-decides"

	// Short aliases keep policy construction readable at call sites.
	ImpactAlways       = ImpactModeAlways
	ImpactRules        = ImpactModeRules
	ImpactAgentDecides = ImpactModeAgentDecides
)

type ImpactPolicy struct {
	Mode                             ImpactMode
	SensitivePaths                   []string
	MaxChangedFiles                  int
	MaxChangedLines                  int
	ConflictRequiresApproval         bool
	ValidationChangeRequiresApproval bool
}

// DeterministicImpact is the server-computed delta between the last
// human-approved candidate and the final candidate proposed for publication.
// AssessmentError and AssessmentAmbiguous carry fail-closed execution facts;
// their underlying details are deliberately not copied into sealed evidence.
type DeterministicImpact struct {
	BaselineDigest      string
	CandidateDigest     string
	ChangedFiles        []contracts.PublishChangedFile
	ChangedLines        int
	ConflictResolution  bool
	ValidationChanges   []string
	Ambiguous           bool
	AssessmentError     error
	AssessmentAmbiguous bool
}

// DecideImpact derives the complete publish-impact body. It never accepts a
// caller-authored final boolean: failed deterministic rules, ambiguity, and
// assessment failure are re-derived here and cannot be waived by the agent.
func DecideImpact(
	policy ImpactPolicy,
	impact DeterministicImpact,
	assessment *contracts.AgentImpactAssessment,
) (contracts.PublishImpactBody, error) {
	if err := validateImpactPolicy(policy); err != nil {
		return contracts.PublishImpactBody{}, err
	}

	body := contracts.PublishImpactBody{
		BaselineDigest:     impact.BaselineDigest,
		CandidateDigest:    impact.CandidateDigest,
		ChangedFiles:       append([]contracts.PublishChangedFile(nil), impact.ChangedFiles...),
		ChangedLines:       impact.ChangedLines,
		ConflictResolution: impact.ConflictResolution,
		ValidationChanges:  append([]string(nil), impact.ValidationChanges...),
	}
	sort.Slice(body.ChangedFiles, func(left, right int) bool {
		return body.ChangedFiles[left].Path < body.ChangedFiles[right].Path
	})
	sort.Strings(body.ValidationChanges)

	rules, reasons, err := decideDeterministicRules(policy, body)
	if err != nil {
		return contracts.PublishImpactBody{}, err
	}
	body.RuleResults = rules

	switch {
	case impact.Ambiguous:
		reasons = append(reasons, "Impact evidence is ambiguous.")
	case impact.AssessmentError != nil:
		reasons = append(reasons, "Agent impact assessment failed.")
	case impact.AssessmentAmbiguous:
		reasons = append(reasons, "Agent impact assessment is ambiguous.")
	default:
		if assessment != nil {
			cloned := *assessment
			if err := cloned.Validate(); err != nil {
				reasons = append(reasons, "Agent impact assessment is invalid.")
			} else {
				body.AgentAssessment = &cloned
				if cloned.ReapprovalRequired {
					reasons = append(reasons, "Agent assessment requires reapproval: "+cloned.Rationale)
				}
			}
		} else {
			reasons = append(reasons, "Agent impact assessment is unavailable.")
		}
	}

	body.ReapprovalRequired = len(reasons) > 0
	body.Reasons = sortedUniqueImpactStrings(reasons)
	if err := body.Validate(nil); err != nil {
		return contracts.PublishImpactBody{}, fmt.Errorf("pullrequest: deterministic impact is invalid: %w", err)
	}
	return body, nil
}

func validateImpactPolicy(policy ImpactPolicy) error {
	switch policy.Mode {
	case ImpactModeAlways, ImpactModeRules, ImpactModeAgentDecides:
	default:
		return fmt.Errorf("pullrequest: impact policy mode is invalid")
	}
	if policy.MaxChangedFiles < 0 || policy.MaxChangedLines < 0 {
		return fmt.Errorf("pullrequest: impact policy thresholds must not be negative")
	}
	if policy.Mode == ImpactModeAgentDecides &&
		(len(policy.SensitivePaths) != 0 ||
			policy.MaxChangedFiles != 0 ||
			policy.MaxChangedLines != 0 ||
			policy.ConflictRequiresApproval ||
			policy.ValidationChangeRequiresApproval) {
		return fmt.Errorf("pullrequest: agent-decides mode cannot configure optional deterministic rules")
	}
	patterns := append([]string(nil), policy.SensitivePaths...)
	sort.Strings(patterns)
	for index, pattern := range patterns {
		if err := validateSensitivePattern(pattern); err != nil {
			return err
		}
		if index > 0 && patterns[index-1] == pattern {
			return fmt.Errorf("pullrequest: sensitive path patterns must be unique")
		}
	}
	return nil
}

func decideDeterministicRules(
	policy ImpactPolicy,
	body contracts.PublishImpactBody,
) ([]contracts.PublishImpactRule, []string, error) {
	rules := make([]contracts.PublishImpactRule, 0, 6)
	reasons := make([]string, 0, 6)
	addRule := func(id string, passed bool, passedReason, failedReason string) {
		reason := passedReason
		if !passed {
			reason = failedReason
			reasons = append(reasons, failedReason)
		}
		rules = append(rules, contracts.PublishImpactRule{ID: id, Passed: passed, Reason: reason})
	}

	if policy.Mode == ImpactModeAlways {
		addRule(
			"always",
			false,
			"The candidate is unchanged.",
			"Impact policy requires reapproval for every changed candidate.",
		)
	}
	if policy.MaxChangedFiles > 0 {
		addRule(
			"changed-files",
			len(body.ChangedFiles) <= policy.MaxChangedFiles,
			"Changed file count is within policy.",
			fmt.Sprintf("Changed file count exceeds the policy maximum of %d.", policy.MaxChangedFiles),
		)
	}
	if policy.MaxChangedLines > 0 {
		addRule(
			"changed-lines",
			body.ChangedLines <= policy.MaxChangedLines,
			"Changed line count is within policy.",
			fmt.Sprintf("Changed line count exceeds the policy maximum of %d.", policy.MaxChangedLines),
		)
	}
	if policy.ConflictRequiresApproval {
		addRule(
			"conflict-resolution",
			!body.ConflictResolution,
			"No conflict resolution is present.",
			"Conflict resolution requires reapproval.",
		)
	}
	if len(policy.SensitivePaths) > 0 {
		matched, err := sensitiveChangedPath(policy.SensitivePaths, body.ChangedFiles)
		if err != nil {
			return nil, nil, err
		}
		addRule(
			"sensitive-paths",
			matched == "",
			"No sensitive path changed.",
			fmt.Sprintf("Sensitive path %q changed.", matched),
		)
	}
	if policy.ValidationChangeRequiresApproval {
		addRule(
			"validation-changes",
			len(body.ValidationChanges) == 0,
			"Authoritative validation did not change.",
			"Authoritative validation changed.",
		)
	}
	sort.Slice(rules, func(left, right int) bool { return rules[left].ID < rules[right].ID })
	return rules, reasons, nil
}

func validateSensitivePattern(pattern string) error {
	if strings.TrimSpace(pattern) != pattern || pattern == "" ||
		!utf8.ValidString(pattern) || len(pattern) > 1024 ||
		strings.HasPrefix(pattern, "/") || strings.Contains(pattern, `\`) {
		return fmt.Errorf("pullrequest: sensitive path pattern is invalid")
	}
	for _, character := range pattern {
		if unicode.IsControl(character) {
			return fmt.Errorf("pullrequest: sensitive path pattern is invalid")
		}
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("pullrequest: sensitive path pattern is invalid")
		}
	}
	probe := pattern
	if strings.HasSuffix(probe, "/**") {
		probe = strings.TrimSuffix(probe, "/**") + "/*"
	}
	if _, err := path.Match(probe, "probe"); err != nil {
		return fmt.Errorf("pullrequest: sensitive path pattern is invalid")
	}
	return nil
}

func sensitiveChangedPath(
	patterns []string,
	files []contracts.PublishChangedFile,
) (string, error) {
	sortedPatterns := append([]string(nil), patterns...)
	sort.Strings(sortedPatterns)
	for _, file := range files {
		for _, pattern := range sortedPatterns {
			matched := false
			if strings.HasSuffix(pattern, "/**") {
				prefix := strings.TrimSuffix(pattern, "/**")
				matched = file.Path == prefix || strings.HasPrefix(file.Path, prefix+"/")
			} else {
				var err error
				matched, err = path.Match(pattern, file.Path)
				if err != nil {
					return "", fmt.Errorf("pullrequest: sensitive path pattern is invalid")
				}
			}
			if matched {
				return file.Path, nil
			}
		}
	}
	return "", nil
}

func sortedUniqueImpactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	output := values[:0]
	for _, value := range values {
		if len(output) == 0 || output[len(output)-1] != value {
			output = append(output, value)
		}
	}
	return output
}
