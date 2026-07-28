package atc_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestTaskPlanKeepsValidationAuthorityForResumeButNeverPublicizesIt(t *testing.T) {
	profile := []byte("schema_version: 1\nname: trusted\nchecks: []\n")
	config := []byte("commands: []\n")
	digest := func(raw []byte) snapshot.Digest {
		sum := sha256.Sum256(raw)
		return snapshot.Digest(fmt.Sprintf("sha256:%x", sum[:]))
	}
	plan := atc.TaskPlan{
		Name:           "validate",
		FunctionID:     "dev-validation-trusted",
		Hermetic:       true,
		ReadOnlyInputs: map[string]struct{}{"candidate": {}},
		DevValidationAuthority: &atc.DevValidationAuthority{
			ProfileName: "trusted", Profile: profile, ProfileDigest: digest(profile),
			ProtectedConfig: config, ProtectedConfigDigest: digest(config),
			CapabilityImage: "example.test/dev@sha256:" + strings.Repeat("a", 64), CapabilityImageDigest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
			WorkflowDefinitionID: 12, WorkflowVersion: 3, CandidateInput: "candidate",
		},
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded atc.TaskPlan
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.DevValidationAuthority == nil || string(reloaded.DevValidationAuthority.Profile) != string(profile) || string(reloaded.DevValidationAuthority.ProtectedConfig) != string(config) {
		t.Fatal("private validation authority was not retained for a resumed task")
	}
	if _, found := reloaded.ReadOnlyInputs["candidate"]; !found {
		t.Fatal("read-only boundary was not retained for a resumed task")
	}
	public := string(*plan.Public())
	for _, secret := range []string{"dev_validation_authority", "protected_config", "schema_version", "read_only_inputs"} {
		if strings.Contains(public, secret) {
			t.Fatalf("public task plan leaked private validation material %q: %s", secret, public)
		}
	}
}
