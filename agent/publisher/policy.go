package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

const maxPolicyBytes int64 = 1 << 20

var ErrDestinationNotAllowed = errors.New("publisher: destination is not allowed")

// AdapterKind identifies the concrete in-process adapter selected by a policy
// rule. It is deployment configuration, never an authored request value.
type AdapterKind string

const (
	AdapterGateway AdapterKind = "gateway"
)

// Policy is the complete startup-loaded publisher authorization policy. Each
// rule represents one exact request matcher and one server-owned destination.
type Policy struct {
	SchemaVersion int          `json:"schema_version"`
	Rules         []PolicyRule `json:"rules"`
}

// PolicyRule binds one exact publication request to its in-process adapter
// and server-owned destination authority. Every rule names its destination
// with CredentialReference and RemoteURL.
type PolicyRule struct {
	Team                  string           `json:"team"`
	Publisher             snapshot.TypeRef `json:"publisher"`
	Mode                  Mode             `json:"mode"`
	ApprovalPolicyVersion string           `json:"approval_policy_version"`
	TargetBranch          string           `json:"target_branch,omitempty"`
	Destination           string           `json:"destination"`
	Adapter               AdapterKind      `json:"adapter"`
	CredentialReference   string           `json:"credential_reference,omitempty"`
	RemoteURL             string           `json:"remote_url,omitempty"`
}

// LoadPolicy reads and strictly decodes a bounded policy file.
func LoadPolicy(filePath string) (Policy, error) {
	if filePath == "" || !filepath.IsAbs(filePath) || filepath.Clean(filePath) != filePath {
		return Policy{}, fmt.Errorf("publisher policy: file must be an absolute clean path")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filePath))
	if err != nil {
		return Policy{}, fmt.Errorf("publisher policy: inspect file parent: %w", err)
	}
	root, err := newMountedFileRoot(parent, "publisher policy")
	if err != nil {
		return Policy{}, err
	}
	return loadPolicyFromMountedRoot(root, filepath.Join(parent, filepath.Base(filePath)))
}

func loadPolicyFromMountedRoot(root *mountedFileRoot, filePath string) (Policy, error) {
	binding, err := root.bind(filePath, "policy file", maxPolicyBytes, true, false)
	if err != nil {
		return Policy{}, err
	}
	body, err := root.read(binding)
	if err != nil {
		return Policy{}, err
	}
	return decodePolicy(body)
}

func decodePolicy(body []byte) (Policy, error) {
	if err := validatePolicyJSONMembers(body); err != nil {
		return Policy{}, fmt.Errorf("publisher policy: decode: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("publisher policy: decode: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Policy{}, fmt.Errorf("publisher policy: decode: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// Validate checks that the policy is complete and that no request can resolve
// to more than one rule.
func (policy Policy) Validate() error {
	if policy.SchemaVersion != 1 || len(policy.Rules) == 0 {
		return fmt.Errorf("publisher policy: requires schema_version 1 and at least one rule")
	}
	type matcher struct {
		team                  string
		publisher             snapshot.TypeRef
		mode                  Mode
		approvalPolicyVersion string
		targetBranch          string
		destination           string
	}
	seen := make(map[matcher]int, len(policy.Rules))
	for index, rule := range policy.Rules {
		if err := rule.validate(); err != nil {
			return fmt.Errorf("publisher policy: rule %d: %w", index, err)
		}
		key := matcher{
			team: rule.Team, publisher: rule.Publisher, mode: rule.Mode,
			approvalPolicyVersion: rule.ApprovalPolicyVersion,
			targetBranch:          rule.TargetBranch,
			destination:           rule.Destination,
		}
		if previous, duplicate := seen[key]; duplicate {
			return fmt.Errorf("publisher policy: rules %d and %d ambiguously match the same request", previous, index)
		}
		seen[key] = index
	}
	return nil
}

func (rule PolicyRule) validate() error {
	if !boundedText(rule.Team, 256, false) {
		return fmt.Errorf("team is invalid")
	}
	if rule.Publisher != GitPublisher && rule.Publisher != WorkItemPublisher {
		return fmt.Errorf("publisher is invalid")
	}
	if !policyModeMatchesPublisher(rule.Publisher, rule.Mode) {
		return fmt.Errorf("mode is invalid for publisher")
	}
	if !boundedText(rule.ApprovalPolicyVersion, 128, false) {
		return fmt.Errorf("approval_policy_version is invalid")
	}
	switch {
	case rule.Publisher == GitPublisher:
		if !boundedText(rule.TargetBranch, 1024, false) {
			return fmt.Errorf("target_branch is required for Git")
		}
	case rule.Publisher == WorkItemPublisher:
		if rule.TargetBranch != "" {
			return fmt.Errorf("target_branch is not valid for work items")
		}
	}
	if !boundedText(rule.Destination, 2048, false) {
		return fmt.Errorf("destination is invalid")
	}
	if !boundedText(string(rule.Adapter), 128, false) {
		return fmt.Errorf("adapter is invalid")
	}
	if !boundedText(rule.CredentialReference, 256, false) {
		return fmt.Errorf("credential_reference is invalid")
	}
	if err := validatePolicyRemoteURL(rule.RemoteURL); err != nil {
		return fmt.Errorf("remote_url is invalid: %w", err)
	}
	return nil
}

// Resolve returns the unique server-owned rule for a persisted publication
// request. It never reads remote or credential material from request fields.
func (policy Policy) Resolve(ctx context.Context, request Request) (PolicyRule, error) {
	if ctx == nil {
		return PolicyRule{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return PolicyRule{}, err
	}
	if err := request.ValidatePersisted(); err != nil {
		return PolicyRule{}, err
	}
	if err := policy.Validate(); err != nil {
		return PolicyRule{}, err
	}
	targetBranch := request.Parameters["target_branch"]
	for _, rule := range policy.Rules {
		if rule.Team == request.Authority.TeamName &&
			rule.Publisher == request.Publisher &&
			rule.Mode == request.Mode &&
			rule.ApprovalPolicyVersion == request.ApprovalPolicyVersion &&
			rule.TargetBranch == targetBranch &&
			rule.Destination == request.Destination {
			return rule, nil
		}
	}
	return PolicyRule{}, ErrDestinationNotAllowed
}

func policyModeMatchesPublisher(publisher snapshot.TypeRef, mode Mode) bool {
	switch publisher {
	case GitPublisher:
		return mode == ModeBranch || mode == ModeMerge
	case WorkItemPublisher:
		return mode == ModeComment || mode == ModeState
	default:
		return false
	}
}

func validatePolicyRemoteURL(raw string) error {
	if !boundedText(raw, 4096, false) {
		return fmt.Errorf("must be non-empty bounded text")
	}
	remote, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("could not be parsed")
	}
	if !remote.IsAbs() || remote.Opaque != "" {
		return fmt.Errorf("must be an absolute hierarchical URL")
	}
	if remote.User != nil {
		return fmt.Errorf("must not contain user information")
	}
	if remote.RawQuery != "" || remote.ForceQuery {
		return fmt.Errorf("must not contain a query")
	}
	if remote.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("must not contain a fragment")
	}
	switch strings.ToLower(remote.Scheme) {
	case "https", "ssh":
		if remote.Hostname() == "" || remote.Path == "" || remote.Path == "/" {
			return fmt.Errorf("%s URL requires a host and repository path", remote.Scheme)
		}
		if strings.HasSuffix(remote.Host, ":") {
			return fmt.Errorf("%s URL contains an invalid port", remote.Scheme)
		}
		if port := remote.Port(); port != "" {
			portNumber, err := strconv.Atoi(port)
			if err != nil || portNumber < 1 || portNumber > 65535 {
				return fmt.Errorf("%s URL contains an invalid port", remote.Scheme)
			}
		}
	case "file":
		if remote.Host != "" || !filepath.IsAbs(filepath.FromSlash(remote.Path)) || remote.Path == "/" {
			return fmt.Errorf("file URL requires an absolute repository path and no host")
		}
	default:
		return fmt.Errorf("unsupported scheme %q", remote.Scheme)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func validatePolicyJSONMembers(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}

	var policyObject map[string]json.RawMessage
	if err := json.Unmarshal(body, &policyObject); err != nil {
		return err
	}
	for member := range policyObject {
		if member != "schema_version" && member != "rules" {
			return fmt.Errorf("policy contains an unknown or case-aliased JSON member %q", member)
		}
	}
	rulesBody, found := policyObject["rules"]
	if !found {
		return nil
	}
	var ruleObjects []map[string]json.RawMessage
	if err := json.Unmarshal(rulesBody, &ruleObjects); err != nil {
		return err
	}
	for index, ruleObject := range ruleObjects {
		for member := range ruleObject {
			if !isExactPolicyRuleJSONMember(member) {
				return fmt.Errorf("policy rule %d contains an unknown or case-aliased JSON member %q", index, member)
			}
		}
	}
	return nil
}

func isExactPolicyRuleJSONMember(member string) bool {
	switch member {
	case "team",
		"publisher",
		"mode",
		"approval_policy_version",
		"target_branch",
		"destination",
		"adapter",
		"credential_reference",
		"remote_url":
		return true
	default:
		return false
	}
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			member, ok := token.(string)
			if !ok {
				return fmt.Errorf("object member name is not a string")
			}
			if _, duplicate := members[member]; duplicate {
				return fmt.Errorf("duplicate JSON member %q", member)
			}
			members[member] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return nil
}
