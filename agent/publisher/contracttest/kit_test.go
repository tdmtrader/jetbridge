package contracttest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher/contracttest"
)

func TestReferenceGatewaySatisfiesTheConformanceKit(t *testing.T) {
	base := strings.Repeat("1", 40)
	result := strings.Repeat("2", 40)
	tree := strings.Repeat("3", 40)
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{CurrentBase: base})
	change := contracttest.NewSyntheticChange(t, base, result, tree)

	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenPath, []byte("mounted-token"), 0600); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(caPath, contracttest.ReferenceCAPEM(reference), 0600); err != nil {
		t.Fatal(err)
	}

	contracttest.Run(t, contracttest.Config{
		Endpoint: reference.Server.URL, TokenFile: tokenPath, CACertificateFile: caPath,
		TeamName: "engineering", ApprovalPolicyVersion: "engineering/v1",
		GitDestination: "git.example/acme/widget", GitTargetBranch: "main",
		Change: &change,
	})
	if reference.ExternalEffects() != 1 {
		t.Fatalf("the kit's write checks produced %d external effects, want exactly 1", reference.ExternalEffects())
	}
}
