package publisher_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestPolicyLoadsAndResolvesExactServerOwnedDestination(t *testing.T) {
	policy := loadPolicyForTest(t, `{
		"schema_version": 1,
		"rules": [{
			"team": "engineering",
			"publisher": "git-publisher/v1",
			"mode": "branch",
			"approval_policy_version": "engineering/v1",
			"target_branch": "main",
			"destination": "git.example/acme/widget",
			"adapter": "direct-git",
			"credential_reference": "widget-git",
			"remote_url": "https://git.example/acme/widget.git"
		}]
	}`)

	rule, err := policy.Resolve(context.Background(), policyGitRequest())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rule != (publisher.PolicyRule{
		Team: "engineering", Publisher: publisher.GitPublisher,
		Mode: publisher.ModeBranch, ApprovalPolicyVersion: "engineering/v1",
		TargetBranch: "main", Destination: "git.example/acme/widget",
		Adapter: publisher.AdapterDirectGit, CredentialReference: "widget-git",
		RemoteURL: "https://git.example/acme/widget.git",
	}) {
		t.Fatalf("resolved rule = %+v", rule)
	}
}

func TestPolicyRequiresEveryExactAuthorizationDimension(t *testing.T) {
	tests := map[string]string{
		"team":                    `"team":""`,
		"publisher":               `"publisher":""`,
		"mode":                    `"mode":""`,
		"approval policy version": `"approval_policy_version":""`,
		"target branch":           `"target_branch":""`,
		"destination":             `"destination":""`,
		"adapter":                 `"adapter":""`,
		"credential reference":    `"credential_reference":""`,
		"remote URL":              `"remote_url":""`,
	}
	for name, replacement := range tests {
		t.Run(name, func(t *testing.T) {
			path := writePolicyForTest(t, `{
				"schema_version":1,
				"rules":[{
					"team":"engineering",
					"publisher":"git-publisher/v1",
					"mode":"branch",
					"approval_policy_version":"engineering/v1",
					"target_branch":"main",
					"destination":"git.example/acme/widget",
					"adapter":"direct-git",
					"credential_reference":"widget-git",
					"remote_url":"https://git.example/acme/widget.git"
				}]
			}`)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "team":
				body = []byte(replacePolicyField(string(body), `"team":"engineering"`, replacement))
			case "publisher":
				body = []byte(replacePolicyField(string(body), `"publisher":"git-publisher/v1"`, replacement))
			case "mode":
				body = []byte(replacePolicyField(string(body), `"mode":"branch"`, replacement))
			case "approval policy version":
				body = []byte(replacePolicyField(string(body), `"approval_policy_version":"engineering/v1"`, replacement))
			case "target branch":
				body = []byte(replacePolicyField(string(body), `"target_branch":"main"`, replacement))
			case "destination":
				body = []byte(replacePolicyField(string(body), `"destination":"git.example/acme/widget"`, replacement))
			case "adapter":
				body = []byte(replacePolicyField(string(body), `"adapter":"direct-git"`, replacement))
			case "credential reference":
				body = []byte(replacePolicyField(string(body), `"credential_reference":"widget-git"`, replacement))
			case "remote URL":
				body = []byte(replacePolicyField(string(body), `"remote_url":"https://git.example/acme/widget.git"`, replacement))
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := publisher.LoadPolicy(path); err == nil {
				t.Fatalf("policy without %s was accepted", name)
			}
		})
	}
}

func TestPolicyRejectsAmbiguousExactMatchers(t *testing.T) {
	path := writePolicyForTest(t, `{
		"schema_version":1,
		"rules":[
			{
				"team":"engineering",
				"publisher":"git-publisher/v1",
				"mode":"branch",
				"approval_policy_version":"engineering/v1",
				"target_branch":"main",
				"destination":"git.example/acme/widget",
				"adapter":"direct-git",
				"credential_reference":"widget-git",
				"remote_url":"https://git.example/acme/widget.git"
			},
			{
				"team":"engineering",
				"publisher":"git-publisher/v1",
				"mode":"branch",
				"approval_policy_version":"engineering/v1",
				"target_branch":"main",
				"destination":"git.example/acme/widget",
				"adapter":"other-git",
				"credential_reference":"other",
				"remote_url":"ssh://git.example/acme/widget.git"
			}
		]
	}`)
	if _, err := publisher.LoadPolicy(path); err == nil {
		t.Fatal("ambiguous rules with one exact matcher were accepted")
	}
}

func TestPolicyRejectsUnsafeOrNonAbsoluteRemoteURLs(t *testing.T) {
	for name, remoteURL := range map[string]string{
		"userinfo":           "https://token@git.example/acme/widget.git",
		"query":              "https://git.example/acme/widget.git?ref=main",
		"empty query":        "https://git.example/acme/widget.git?",
		"fragment":           "https://git.example/acme/widget.git#main",
		"empty fragment":     "https://git.example/acme/widget.git#",
		"relative":           "git.example/acme/widget.git",
		"scp syntax":         "git@git.example:acme/widget.git",
		"insecure HTTP":      "http://git.example/acme/widget.git",
		"custom Git helper":  "ext::sh -c publish",
		"unsupported scheme": "ftp://git.example/acme/widget.git",
		"missing hostname":   "https://:443/acme/widget.git",
		"empty port":         "https://git.example:/acme/widget.git",
		"zero port":          "https://git.example:0/acme/widget.git",
		"oversized port":     "ssh://git.example:65536/acme/widget.git",
		"invalid port":       "https://git.example:notaport/acme/widget.git",
	} {
		t.Run(name, func(t *testing.T) {
			policy := exactPolicy()
			policy.Rules[0].RemoteURL = remoteURL
			if err := policy.Validate(); err == nil {
				t.Fatalf("unsafe remote URL %q was accepted", remoteURL)
			}
		})
	}
}

func TestPolicyRemoteURLParseErrorsAreRedacted(t *testing.T) {
	policy := exactPolicy()
	policy.Rules[0].RemoteURL = "https://mounted-secret@[::1/acme/widget.git"

	err := policy.Validate()
	if err == nil {
		t.Fatal("malformed remote URL was accepted")
	}
	if strings.Contains(err.Error(), "mounted-secret") ||
		strings.Contains(err.Error(), policy.Rules[0].RemoteURL) {
		t.Fatalf("remote URL parse error exposed configured contents: %v", err)
	}
}

func TestPolicyAcceptsExplicitHTTPSSSHAndFileRemoteURLs(t *testing.T) {
	for _, remoteURL := range []string{
		"https://git.example/acme/widget.git",
		"https://git.example:443/acme/widget.git",
		"ssh://git.example/acme/widget.git",
		"ssh://[::1]:22/acme/widget.git",
		"file:///srv/git/widget.git",
	} {
		t.Run(remoteURL, func(t *testing.T) {
			policy := exactPolicy()
			policy.Rules[0].RemoteURL = remoteURL
			if err := policy.Validate(); err != nil {
				t.Fatalf("Validate(%q): %v", remoteURL, err)
			}
		})
	}
}

func TestPolicyRejectsDuplicateAndCaseAliasedJSONMembers(t *testing.T) {
	for name, body := range map[string]string{
		"policy field": `{
			"schema_version":1,
			"schema_version":1,
			"rules":[{
				"team":"engineering","publisher":"git-publisher/v1","mode":"branch",
				"approval_policy_version":"engineering/v1","target_branch":"main",
				"destination":"git.example/acme/widget","adapter":"direct-git",
				"credential_reference":"widget-git",
				"remote_url":"https://git.example/acme/widget.git"
			}]
		}`,
		"rule field": `{
			"schema_version":1,
			"rules":[{
				"team":"engineering","team":"engineering",
				"publisher":"git-publisher/v1","mode":"branch",
				"approval_policy_version":"engineering/v1","target_branch":"main",
				"destination":"git.example/acme/widget","adapter":"direct-git",
				"credential_reference":"widget-git",
				"remote_url":"https://git.example/acme/widget.git"
			}]
		}`,
		"policy field case alias": `{
			"schema_version":1,
			"SCHEMA_VERSION":1,
			"rules":[{
				"team":"engineering","publisher":"git-publisher/v1","mode":"branch",
				"approval_policy_version":"engineering/v1","target_branch":"main",
				"destination":"git.example/acme/widget","adapter":"direct-git",
				"credential_reference":"widget-git",
				"remote_url":"https://git.example/acme/widget.git"
			}]
		}`,
		"rule field case alias": `{
			"schema_version":1,
			"rules":[{
				"team":"engineering","TEAM":"engineering",
				"publisher":"git-publisher/v1","mode":"branch",
				"approval_policy_version":"engineering/v1","target_branch":"main",
				"destination":"git.example/acme/widget","adapter":"direct-git",
				"credential_reference":"widget-git",
				"remote_url":"https://git.example/acme/widget.git"
			}]
		}`,
		"remote field case alias": `{
			"schema_version":1,
			"rules":[{
				"team":"engineering","publisher":"git-publisher/v1","mode":"branch",
				"approval_policy_version":"engineering/v1","target_branch":"main",
				"destination":"git.example/acme/widget","adapter":"direct-git",
				"credential_reference":"widget-git",
				"remote_url":"https://git.example/acme/widget.git",
				"REMOTE_URL":"https://git.example/acme/widget.git"
			}]
		}`,
		"case alias without canonical field": `{
			"schema_version":1,
			"rules":[{
				"TEAM":"engineering","publisher":"git-publisher/v1","mode":"branch",
				"approval_policy_version":"engineering/v1","target_branch":"main",
				"destination":"git.example/acme/widget","adapter":"direct-git",
				"credential_reference":"widget-git",
				"remote_url":"https://git.example/acme/widget.git"
			}]
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := publisher.LoadPolicy(writePolicyForTest(t, body))
			if err == nil {
				t.Fatal("policy with duplicate JSON member was accepted")
			}
		})
	}
}

func TestPolicyMatchesEveryExactFieldAndNeverUsesAuthoredRemoteMaterial(t *testing.T) {
	base := policyGitRequest()
	for name, mutate := range map[string]func(*publisher.Request){
		"team": func(request *publisher.Request) {
			request.Authority.TeamName = "other"
		},
		"publisher": func(request *publisher.Request) {
			request.Publisher = publisher.WorkItemPublisher
			request.Input.Type = "work-item/v1"
			request.Mode = publisher.ModeComment
			request.Parameters = map[string]string{"body": "message"}
		},
		"mode": func(request *publisher.Request) {
			request.Mode = publisher.ModePullRequest
		},
		"approval policy": func(request *publisher.Request) {
			request.ApprovalPolicyVersion = "engineering/v2"
		},
		"target branch": func(request *publisher.Request) {
			request.Parameters["target_branch"] = "release"
		},
		"destination": func(request *publisher.Request) {
			request.Destination = "git.example/acme/other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := base.Clone()
			mutate(&request)
			if _, err := exactPolicy().Resolve(context.Background(), request); !errors.Is(err, publisher.ErrDestinationNotAllowed) {
				t.Fatalf("Resolve mismatch error = %v, want ErrDestinationNotAllowed", err)
			}
		})
	}

	request := base.Clone()
	request.Parameters["remote_url"] = "ssh://attacker.invalid/owned.git"
	request.Parameters["repository_url"] = "https://attacker.invalid/owned.git"
	rule, err := exactPolicy().Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if rule.RemoteURL != "https://git.example/acme/widget.git" {
		t.Fatalf("remote URL = %q, want server-owned policy URL", rule.RemoteURL)
	}
}

func TestPolicyRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{
			"schema_version":1,
			"rules":[{
				"team":"engineering","publisher":"git-publisher/v1","mode":"branch",
				"approval_policy_version":"engineering/v1","target_branch":"main",
				"destination":"git.example/acme/widget","adapter":"direct-git",
				"credential_reference":"widget-git",
				"remote_url":"https://git.example/acme/widget.git","destination_template":"${destination}"
			}]
		}`,
		"trailing document": `{
			"schema_version":1,
			"rules":[{
				"team":"engineering","publisher":"git-publisher/v1","mode":"branch",
				"approval_policy_version":"engineering/v1","target_branch":"main",
				"destination":"git.example/acme/widget","adapter":"direct-git",
				"credential_reference":"widget-git",
				"remote_url":"https://git.example/acme/widget.git"
			}]
		}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := publisher.LoadPolicy(writePolicyForTest(t, body)); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

func TestPolicyLoadsAndResolvesExactProviderNativePRDestination(t *testing.T) {
	policy := loadPolicyForTest(t, `{
		"schema_version": 1,
		"rules": [{
			"team": "engineering",
			"publisher": "git-publisher/v1",
			"mode": "pull-request",
			"approval_policy_version": "engineering/v3",
			"target_branch": "refs/heads/main",
			"destination": "github.example/acme/widget",
			"adapter": "github",
			"provider": "github",
			"repository": "acme/widget",
			"api_base_url": "https://api.github.example",
			"repository_url": "https://github.example/acme/widget.git",
			"read_credential_reference": "widget-github-read",
			"write_credential_reference": "widget-github-write"
		}]
	}`)

	rule, err := policy.ResolvePR(context.Background(), policyPRAction())
	if err != nil {
		t.Fatalf("ResolvePR: %v", err)
	}
	if rule != exactPRPolicy().Rules[0] {
		t.Fatalf("resolved PR rule = %+v", rule)
	}
}

func TestPolicyResolvesEveryPRKindByExactProviderRepositoryAndTargetRef(t *testing.T) {
	for name, action := range policyPRActions() {
		t.Run(name, func(t *testing.T) {
			rule, err := exactPRPolicy().ResolvePR(context.Background(), action)
			if err != nil {
				t.Fatalf("ResolvePR: %v", err)
			}
			if rule.TargetBranch != "refs/heads/main" ||
				rule.Provider != publisher.PRProviderGitHub ||
				rule.Repository != "acme/widget" {
				t.Fatalf("resolved PR rule = %+v", rule)
			}
		})
	}
}

func TestPolicySelectsExactPRProviderAndTargetAmongMultipleRules(t *testing.T) {
	main := exactPRPolicy().Rules[0]
	release := main
	release.TargetBranch = "refs/heads/release"
	release.WriteCredentialReference = "widget-release-write"
	other := main
	other.Destination = "github.example/acme/other-widget"
	other.TargetBranch = "refs/heads/main"
	other.Repository = "acme/other-widget"
	other.RepositoryURL = "https://github.example/acme/other-widget.git"
	other.ReadCredentialReference = "other-widget-read"
	other.WriteCredentialReference = "other-widget-write"
	policy := publisher.Policy{
		SchemaVersion: 1,
		Rules:         []publisher.PolicyRule{main, release, other},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	releaseAction := policyPRAction()
	releaseAction.Branch.TargetRef = "refs/heads/release"
	rule, err := policy.ResolvePR(context.Background(), releaseAction)
	if err != nil || rule.WriteCredentialReference != "widget-release-write" {
		t.Fatalf("release ResolvePR = (%+v, %v)", rule, err)
	}

	otherAction := policyPRAction()
	otherAction.Branch.Destination = other.Destination
	otherAction.Branch.Locator.Repository = other.Repository
	rule, err = policy.ResolvePR(context.Background(), otherAction)
	if err != nil || rule.WriteCredentialReference != "other-widget-write" {
		t.Fatalf("other-repository ResolvePR = (%+v, %v)", rule, err)
	}
}

func TestPolicyPRResolutionRefusesCrossRoutingAndLegacyEntryPoints(t *testing.T) {
	action := policyPRAction()
	action.Branch.Locator.Repository = "acme/other-widget"
	if _, err := exactPRPolicy().ResolvePR(context.Background(), action); !errors.Is(err, publisher.ErrDestinationNotAllowed) {
		t.Fatalf("cross-repository ResolvePR error = %v, want ErrDestinationNotAllowed", err)
	}
	if _, err := exactPolicy().ResolvePR(context.Background(), policyPRAction()); !errors.Is(err, publisher.ErrDestinationNotAllowed) {
		t.Fatalf("direct-Git ResolvePR error = %v, want ErrDestinationNotAllowed", err)
	}

	legacyPR := policyGitRequest()
	legacyPR.Mode = publisher.ModePullRequest
	legacyPR.Parameters = map[string]string{
		"source_branch": "agent/change",
		"target_branch": "refs/heads/main",
	}
	legacyPR.Destination = "github.example/acme/widget"
	legacyPR.ApprovalPolicyVersion = "engineering/v3"
	if _, err := exactPRPolicy().Resolve(context.Background(), legacyPR); !errors.Is(err, publisher.ErrDestinationNotAllowed) {
		t.Fatalf("legacy Resolve PR error = %v, want ErrDestinationNotAllowed", err)
	}
}

func TestPolicyRequiresExactPRFieldsAndForbidsCrossLaneFields(t *testing.T) {
	tests := map[string]func(*publisher.PolicyRule){
		"fully qualified target ref": func(rule *publisher.PolicyRule) {
			rule.TargetBranch = "main"
		},
		"PR adapter": func(rule *publisher.PolicyRule) {
			rule.Adapter = publisher.AdapterDirectGit
		},
		"provider": func(rule *publisher.PolicyRule) {
			rule.Provider = ""
		},
		"repository": func(rule *publisher.PolicyRule) {
			rule.Repository = ""
		},
		"API base URL": func(rule *publisher.PolicyRule) {
			rule.APIBaseURL = ""
		},
		"repository URL": func(rule *publisher.PolicyRule) {
			rule.RepositoryURL = ""
		},
		"read credential reference": func(rule *publisher.PolicyRule) {
			rule.ReadCredentialReference = ""
		},
		"write credential reference": func(rule *publisher.PolicyRule) {
			rule.WriteCredentialReference = ""
		},
		"distinct credentials": func(rule *publisher.PolicyRule) {
			rule.WriteCredentialReference = rule.ReadCredentialReference
		},
		"legacy credential": func(rule *publisher.PolicyRule) {
			rule.CredentialReference = "legacy-write"
		},
		"legacy remote": func(rule *publisher.PolicyRule) {
			rule.RemoteURL = "https://github.example/acme/widget.git"
		},
		"adapter/provider mismatch": func(rule *publisher.PolicyRule) {
			rule.Adapter = publisher.AdapterGateway
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy := exactPRPolicy()
			mutate(&policy.Rules[0])
			if err := policy.Validate(); err == nil {
				t.Fatal("invalid PR policy was accepted")
			}
		})
	}

	for name, mutate := range map[string]func(*publisher.PolicyRule){
		"provider": func(rule *publisher.PolicyRule) {
			rule.Provider = publisher.PRProviderGitHub
		},
		"repository": func(rule *publisher.PolicyRule) {
			rule.Repository = "acme/widget"
		},
		"API base URL": func(rule *publisher.PolicyRule) {
			rule.APIBaseURL = "https://api.github.example"
		},
		"repository URL": func(rule *publisher.PolicyRule) {
			rule.RepositoryURL = "https://github.example/acme/widget.git"
		},
		"read credential": func(rule *publisher.PolicyRule) {
			rule.ReadCredentialReference = "widget-read"
		},
		"write credential": func(rule *publisher.PolicyRule) {
			rule.WriteCredentialReference = "widget-write"
		},
		"PR adapter": func(rule *publisher.PolicyRule) {
			rule.Adapter = publisher.AdapterGitHub
		},
	} {
		t.Run("direct Git with "+name, func(t *testing.T) {
			policy := exactPolicy()
			mutate(&policy.Rules[0])
			if err := policy.Validate(); err == nil {
				t.Fatal("direct-Git policy carrying PR-only fields was accepted")
			}
		})
	}
}

func TestPolicyRejectsUnsafePRURLsWithoutExposingConfiguredContents(t *testing.T) {
	for name, mutate := range map[string]func(*publisher.PolicyRule){
		"insecure API URL": func(rule *publisher.PolicyRule) {
			rule.APIBaseURL = "http://api.github.example"
		},
		"credentialed API URL": func(rule *publisher.PolicyRule) {
			rule.APIBaseURL = "https://api-secret@api.github.example"
		},
		"queried API URL": func(rule *publisher.PolicyRule) {
			rule.APIBaseURL = "https://api.github.example?token=api-secret"
		},
		"credentialed repository URL": func(rule *publisher.PolicyRule) {
			rule.RepositoryURL = "https://repository-secret@github.example/acme/widget.git"
		},
		"fragmented repository URL": func(rule *publisher.PolicyRule) {
			rule.RepositoryURL = "https://github.example/acme/widget.git#repository-secret"
		},
		"repository without path": func(rule *publisher.PolicyRule) {
			rule.RepositoryURL = "https://github.example"
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy := exactPRPolicy()
			mutate(&policy.Rules[0])
			if err := policy.Validate(); err == nil {
				t.Fatal("unsafe PR URL was accepted")
			}
		})
	}

	policy := exactPRPolicy()
	policy.Rules[0].APIBaseURL = "https://api-secret@[::1"
	err := policy.Validate()
	if err == nil {
		t.Fatal("malformed PR API URL was accepted")
	}
	if strings.Contains(err.Error(), "api-secret") ||
		strings.Contains(err.Error(), policy.Rules[0].APIBaseURL) {
		t.Fatalf("PR URL validation exposed configured contents: %v", err)
	}
}

func exactPolicy() publisher.Policy {
	return publisher.Policy{
		SchemaVersion: 1,
		Rules: []publisher.PolicyRule{{
			Team: "engineering", Publisher: publisher.GitPublisher,
			Mode: publisher.ModeBranch, ApprovalPolicyVersion: "engineering/v1",
			TargetBranch: "main", Destination: "git.example/acme/widget",
			Adapter: publisher.AdapterDirectGit, CredentialReference: "widget-git",
			RemoteURL: "https://git.example/acme/widget.git",
		}},
	}
}

func exactPRPolicy() publisher.Policy {
	return publisher.Policy{
		SchemaVersion: 1,
		Rules: []publisher.PolicyRule{{
			Team: "engineering", Publisher: publisher.GitPublisher,
			Mode: publisher.ModePullRequest, ApprovalPolicyVersion: "engineering/v3",
			TargetBranch: "refs/heads/main", Destination: "github.example/acme/widget",
			Adapter:                  publisher.AdapterGitHub,
			Provider:                 publisher.PRProviderGitHub,
			Repository:               "acme/widget",
			APIBaseURL:               "https://api.github.example",
			RepositoryURL:            "https://github.example/acme/widget.git",
			ReadCredentialReference:  "widget-github-read",
			WriteCredentialReference: "widget-github-write",
		}},
	}
}

func policyGitRequest() publisher.Request {
	return publisher.Request{
		Publisher: publisher.GitPublisher,
		Input: snapshot.SnapshotRef{
			ID: 41, Type: "repository-change/v1",
			Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Destination: "git.example/acme/widget",
		Mode:        publisher.ModeBranch,
		Parameters: map[string]string{
			"source_branch": "agent/change",
			"target_branch": "main",
		},
		ApprovalPolicyVersion: "engineering/v1",
		Authority: publisher.Authority{
			TeamID: 9, TeamName: "engineering", BuildID: 12,
			WorkflowRunID: 17, Actor: "build/12",
		},
	}
}

func policyPRAction() publisher.PRAction {
	request := validBranchPublicationRequest()
	return publisher.PRAction{
		Kind:   publisher.OperationPublishPRBranch,
		Branch: &request,
	}
}

func policyPRActions() map[string]publisher.PRAction {
	branch := validBranchPublicationRequest()
	create := validPullRequestPublicationRequest()
	status := validStatusPublicationRequest()
	response := validResponsePublicationRequest()
	return map[string]publisher.PRAction{
		"branch": {
			Kind:   publisher.OperationPublishPRBranch,
			Branch: &branch,
		},
		"create": {
			Kind:        publisher.OperationCreatePR,
			PullRequest: &create,
		},
		"status": {
			Kind:   publisher.OperationPublishPRStatus,
			Status: &status,
		},
		"response": {
			Kind:     publisher.OperationRespondToReview,
			Response: &response,
		},
	}
}

func loadPolicyForTest(t *testing.T, body string) publisher.Policy {
	t.Helper()
	policy, err := publisher.LoadPolicy(writePolicyForTest(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func writePolicyForTest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func replacePolicyField(body, old, replacement string) string {
	return stringReplaceOnce(body, old, replacement)
}

func stringReplaceOnce(body, old, replacement string) string {
	for index := 0; index+len(old) <= len(body); index++ {
		if body[index:index+len(old)] == old {
			return body[:index] + replacement + body[index+len(old):]
		}
	}
	return body
}
