package output_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/output"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestDecodeValidatesFixedToolContracts(t *testing.T) {
	subjects := []contracts.Subject{subject("primary", contracts.SubjectRolePrimary)}
	review, err := output.Decode(string(broker.ToolRequestReview), []byte(
		`{"conclusion":"accept","summary":"No defects found.","findings":[]}`,
	), subjects, 4096)
	if err != nil {
		t.Fatalf("review Decode(): %v", err)
	}
	if _, ok := review.(contracts.ReviewBody); !ok {
		t.Fatalf("review type = %T", review)
	}

	consultation, err := output.Decode(string(broker.ToolConsultAgent), []byte(
		`{"answer":"Keep the endpoint.","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`,
	), subjects, 4096)
	if err != nil {
		t.Fatalf("consultation Decode(): %v", err)
	}
	if _, ok := consultation.(contracts.ConsultationBody); !ok {
		t.Fatalf("consultation type = %T", consultation)
	}
}

func TestDecodeFailsClosedOnNonContractOutput(t *testing.T) {
	subjects := []contracts.Subject{subject("primary", contracts.SubjectRolePrimary)}
	tests := []struct {
		name string
		raw  string
		want string
		max  int
	}{
		{"prose", "Looks good.", "JSON", 4096},
		{"trailing", `{"answer":"a","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]} {}`, "one", 4096},
		{"duplicate", `{"answer":"a","answer":"b","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`, "duplicate", 4096},
		{"unknown", `{"answer":"a","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[],"model":"x"}`, "unknown", 4096},
		{"semantic", `{"answer":" ","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`, "answer", 4096},
		{"oversized", strings.Repeat("x", 100), "limit", 64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := output.Decode(string(broker.ToolConsultAgent), []byte(tc.raw), subjects, tc.max)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Decode() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func subject(id string, role contracts.SubjectRole) contracts.Subject {
	return contracts.Subject{
		ID: id, Role: role, Input: id, Type: "repository/v1",
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}
}
