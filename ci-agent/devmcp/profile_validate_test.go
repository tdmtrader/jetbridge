package devmcp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/ci-agent/devmcp"
)

const protectedConfig = `schema_version: 1
repo:
  build: {cmd: ["sh", "-c", "printf 'full build\\n'"]}
components:
  - id: app
    description: application
    paths: ["app/"]
    kind: service
    test: {cmd: ["sh", "-c", "printf 'attempt log\\n'; if [ -f retry-marker ]; then exit 0; fi; touch retry-marker; exit 1"]}
  - id: docs
    description: documentation
    paths: ["docs/"]
    kind: docs
    test: {cmd: ["sh", "-c", "printf 'docs log\\n'"]}
`

const profileWithFullAndAffected = `schema_version: 1
name: authoritative-review
checks:
  - id: 01-build
    operation: build
    scope: full
    timeout: 1m
    retries: 0
  - id: 02-test
    operation: test
    scope: affected
    components: [app, docs]
    timeout: 1m
    retries: 1
`

// TestValidationProfileUsesOnlySuppliedPolicyBytes catches an implementation
// that loads a candidate profile/config or accepts malformed policy.
func TestValidationProfileUsesOnlySuppliedPolicyBytes(t *testing.T) {
	profile, identity, err := devmcp.ParseValidationProfile(
		[]byte(profileWithFullAndAffected), []byte(protectedConfig))
	if err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	if profile.Name != "authoritative-review" || identity.ProfileDigest == "" || identity.ProtectedConfigDigest == "" {
		t.Fatalf("profile identity = %#v, profile=%#v", identity, profile)
	}
	if _, _, err := devmcp.ParseValidationProfile(
		[]byte(profileWithFullAndAffected+"---\nname: second\n"), []byte(protectedConfig)); err == nil || !strings.Contains(err.Error(), "trailing document") {
		t.Fatalf("trailing profile error = %v, want strict trailing-document rejection", err)
	}
	if _, _, err := devmcp.ParseValidationProfile(
		[]byte("schema_version: 1\nname: bad\nunknown: true\nchecks: []\n"), []byte(protectedConfig)); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown profile field error = %v", err)
	}
}

// TestValidateProfileRunsFullAndAffectedChecksWithRetries catches changes to
// scope selection, retry policy, complete log retention, and status mapping.
func TestValidateProfileRunsFullAndAffectedChecksWithRetries(t *testing.T) {
	profile, identity, err := devmcp.ParseValidationProfile(
		[]byte(profileWithFullAndAffected), []byte(protectedConfig))
	if err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	config, err := devmcp.Parse([]byte(protectedConfig))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	workdir := t.TempDir()
	core, err := devmcp.NewCore(config, workdir)
	if err != nil {
		t.Fatalf("new core: %v", err)
	}

	result, err := devmcp.ValidateProfile(context.Background(), core, devmcp.ValidationRequest{
		Profile: profile, Identity: identity, ChangedPaths: []string{"app/main.go"},
	}, nil)
	if err != nil {
		t.Fatalf("validate profile: %v", err)
	}
	if result.Status != devmcp.ValidationStatusPassed {
		t.Fatalf("validation status = %q, attempts=%#v", result.Status, result.Attempts)
	}
	if len(result.Attempts) != 3 {
		t.Fatalf("attempts = %#v, want full build and two app test attempts", result.Attempts)
	}
	if result.Attempts[0].CheckID != "01-build" || result.Attempts[1].CheckID != "02-test" || result.Attempts[2].Number != 2 {
		t.Fatalf("attempt order = %#v", result.Attempts)
	}
	if result.Attempts[1].Result.Status != devmcp.StatusFailed || result.Attempts[2].Result.Status != devmcp.StatusOK {
		t.Fatalf("retry statuses = %#v", result.Attempts)
	}
	for _, attempt := range result.Attempts {
		if attempt.FullLogPath == "" || attempt.FullLogPath != attempt.Result.LogPath {
			t.Fatalf("complete log identity = %#v", attempt)
		}
		log, err := os.ReadFile(filepath.Join(workdir, attempt.FullLogPath))
		if err != nil || len(log) == 0 {
			t.Fatalf("complete log %q: %v; contents=%q", attempt.FullLogPath, err, log)
		}
	}
}

func TestValidateProfileConservativelyRunsConfiguredComponentsForUnmappedPaths(t *testing.T) {
	profile, identity, err := devmcp.ParseValidationProfile(
		[]byte(profileWithFullAndAffected), []byte(protectedConfig))
	if err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	config, err := devmcp.Parse([]byte(protectedConfig))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	core, err := devmcp.NewCore(config, t.TempDir())
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	result, err := devmcp.ValidateProfile(context.Background(), core, devmcp.ValidationRequest{
		Profile: profile, Identity: identity, ChangedPaths: []string{"LICENSE"},
	}, nil)
	if err != nil {
		t.Fatalf("validate profile: %v", err)
	}
	// Build plus app retry twice plus docs: an unmapped change must not skip
	// the promoted affected check's conservative component set.
	if len(result.Attempts) != 4 || result.Attempts[3].CheckID != "02-test" {
		t.Fatalf("unmapped attempts = %#v", result.Attempts)
	}
}
