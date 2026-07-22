package atccmd

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestWorkflowRunCreatorIdentityUsesConfiguredDisplayUserID(t *testing.T) {
	identity, err := workflowRunCreatorIdentity(atc.UserInfo{
		Name:          "raw-idp-name",
		UserName:      "preferred-idp-name",
		DisplayUserId: "credential-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity != "credential-owner" {
		t.Fatalf("identity = %q, want configured display user ID", identity)
	}
}

func TestWorkflowRunCreatorIdentityRejectsBlankDisplayUserID(t *testing.T) {
	if _, err := workflowRunCreatorIdentity(atc.UserInfo{
		Name:     "raw-idp-name",
		UserName: "preferred-idp-name",
	}); err == nil {
		t.Fatal("expected missing configured display user ID to be rejected")
	}
}
