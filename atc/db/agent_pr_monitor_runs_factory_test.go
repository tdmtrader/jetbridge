package db

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestDecodeAgentPRMonitorSelectedVersionRequiresExactReservationAuthority(
	t *testing.T,
) {
	evidence := pullrequest.MonitorRunEvidence{
		TeamID: 17, BindingID: 9, BindingRevision: 8,
		WorkflowRunID: 91,
		ActionDigest:  "sha256:" + strings.Repeat("d", 64),
		Observation: snapshot.SnapshotRef{
			ID: 501, Type: "pull-request/v1",
			Digest: snapshot.Digest(
				"sha256:" + strings.Repeat("1", 64),
			),
		},
		Cursor: "cursor-2", SourceSHA: strings.Repeat("a", 40),
		TargetSHA: strings.Repeat("b", 40),
		Locator: pullrequest.Locator{
			Provider:   pullrequest.ProviderGitHub,
			Repository: "acme/widget", ExternalID: "42",
		},
	}
	version := map[string]string{
		"provider":         string(evidence.Locator.Provider),
		"external_id":      evidence.Locator.ExternalID,
		"source_sha":       evidence.SourceSHA,
		"target_sha":       evidence.TargetSHA,
		"action_kind":      string(pullrequest.ActionReviewBatch),
		"action_digest":    evidence.ActionDigest,
		"cursor":           string(evidence.Cursor),
		"binding_revision": strconv.FormatInt(evidence.BindingRevision-1, 10),
	}
	encode := func(value map[string]string) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	kind, err := decodeAgentPRMonitorSelectedVersion(
		encode(version), evidence, evidence.BindingRevision-1,
	)
	if err != nil || kind != pullrequest.ActionReviewBatch {
		t.Fatalf("exact selected version = (%q, %v)", kind, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]string)
	}{
		{
			name: "extra field",
			mutate: func(value map[string]string) {
				value["caller_authority"] = "forged"
			},
		},
		{
			name: "provider",
			mutate: func(value map[string]string) {
				value["provider"] = "other-forge"
			},
		},
		{
			name: "external id",
			mutate: func(value map[string]string) {
				value["external_id"] = "43"
			},
		},
		{
			name: "source",
			mutate: func(value map[string]string) {
				value["source_sha"] = strings.Repeat("c", 40)
			},
		},
		{
			name: "target",
			mutate: func(value map[string]string) {
				value["target_sha"] = strings.Repeat("c", 40)
			},
		},
		{
			name: "digest",
			mutate: func(value map[string]string) {
				value["action_digest"] = "sha256:" + strings.Repeat("e", 64)
			},
		},
		{
			name: "cursor",
			mutate: func(value map[string]string) {
				value["cursor"] = "cursor-3"
			},
		},
		{
			name: "revision",
			mutate: func(value map[string]string) {
				value["binding_revision"] = "8"
			},
		},
		{
			name: "action kind",
			mutate: func(value map[string]string) {
				value["action_kind"] = "invented"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := make(map[string]string, len(version))
			for key, value := range version {
				mutated[key] = value
			}
			test.mutate(mutated)
			if _, err := decodeAgentPRMonitorSelectedVersion(
				encode(mutated), evidence, evidence.BindingRevision-1,
			); err == nil {
				t.Fatal("inexact selected version was accepted")
			}
		})
	}
}
