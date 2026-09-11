package main

// The workload whose word the plane trusts about whether the bucket is safe,
// at the level a main is responsible for: flags, the four configured cloud
// identities, and the refresh bound.
//
// The thing this binary is FOR is that it holds no object capability at all,
// and that is measured rather than asserted here:
// TestEachOutputControllerLinksOnlyItsOwnRole reads the real build graph, and
// this root links hangar/output/policy and no other role. What the derivation
// concludes from a reading is covered in hangar/output/policy; what the pass
// does with it is covered in atc/hangaroutput/attestpass's own composition.

import (
	"context"
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/output"
)

func completeAttestor() attestorConfig {
	return attestorConfig{
		DSN:               "postgres://unreachable.invalid/none",
		Bucket:            "output-bucket",
		ActivationEpoch:   7,
		Interval:          output.MaxPolicyEvidenceAge / 3,
		PublisherIdentity: "publisher@project.iam.gserviceaccount.com",
		InventoryIdentity: "inventory@project.iam.gserviceaccount.com",
		ReclaimerIdentity: "reclaimer@project.iam.gserviceaccount.com",
		AttestorIdentity:  "attestor@project.iam.gserviceaccount.com",
	}
}

func TestTheAttestorRefreshDefaultsWellInsideTheDetectionBound(t *testing.T) {
	config := attestorConfig{}
	flags := flag.NewFlagSet("attestor", flag.ContinueOnError)
	config.bind(flags)

	if err := flags.Parse(nil); err != nil {
		t.Fatalf("parsing no arguments: %v", err)
	}

	// Inside the bound and not at it. An attestor configured to refresh exactly
	// at fifteen minutes is one whose evidence is stale for an instant before
	// every refresh, which blocks admission for that instant on every cycle.
	if config.Interval >= output.MaxPolicyEvidenceAge {
		t.Errorf("the default refresh is %s against a %s detection bound",
			config.Interval, output.MaxPolicyEvidenceAge)
	}
	if config.Interval <= 0 {
		t.Errorf("the default refresh is %s; a zero interval is a monitor that never runs",
			config.Interval)
	}
}

func TestTheAttestorFlagsAreRead(t *testing.T) {
	config := attestorConfig{}
	flags := flag.NewFlagSet("attestor", flag.ContinueOnError)
	config.bind(flags)

	if err := flags.Parse([]string{
		"-database", "postgres://example",
		"-output-bucket", "output-bucket",
		"-activation-epoch", "7",
		"-interval", "10m",
		"-publisher-identity", "publisher@project.iam.gserviceaccount.com",
		"-inventory-identity", "inventory@project.iam.gserviceaccount.com",
		"-reclaimer-identity", "reclaimer@project.iam.gserviceaccount.com",
		"-attestor-identity", "attestor@project.iam.gserviceaccount.com",
	}); err != nil {
		t.Fatalf("parsing: %v", err)
	}

	if config.Interval != 10*time.Minute {
		t.Errorf("-interval parsed as %s, expected 10m", config.Interval)
	}

	expectation := config.expectation()
	if err := expectation.Validate(); err != nil {
		t.Fatalf("a fully configured expectation was refused: %v", err)
	}
	if expectation.BucketFingerprint != "gs://output-bucket" {
		t.Errorf("the bucket fingerprint is %q; an attestation and a namespace must name the "+
			"same bucket the same way", expectation.BucketFingerprint)
	}
	for _, role := range output.PrincipalRoles() {
		if expectation.Principals[role] == "" {
			t.Errorf("the %s role has no configured identity", role)
		}
	}
}

// The identities are CONFIGURATION and never discovery. Activation verifies the
// KSA-to-cloud identities and the exact grants rather than assuming the chart
// created them, and a deployment that learned its own principals from the
// bucket would be verifying the bucket against itself.
func TestTheAttestorRefusesAMissingOrSharedIdentity(t *testing.T) {
	if err := completeAttestor().expectation().Validate(); err != nil {
		t.Fatalf("a fully configured expectation was refused: %v", err)
	}

	for _, broken := range []struct {
		name   string
		mutate func(*attestorConfig)
	}{
		{"no publisher identity", func(c *attestorConfig) { c.PublisherIdentity = "" }},
		{"no inventory identity", func(c *attestorConfig) { c.InventoryIdentity = "" }},
		{"no reclaimer identity", func(c *attestorConfig) { c.ReclaimerIdentity = "" }},
		{"no attestor identity", func(c *attestorConfig) { c.AttestorIdentity = "" }},
		{"no bucket", func(c *attestorConfig) { c.Bucket = "" }},
		{"no activation epoch", func(c *attestorConfig) { c.ActivationEpoch = 0 }},
		// A Kubernetes service account is Pod-wide, so a shared identity is
		// shared permission and the isolation is not real.
		{"the publisher and the reclaimer sharing one identity", func(c *attestorConfig) {
			c.ReclaimerIdentity = c.PublisherIdentity
		}},
	} {
		config := completeAttestor()
		broken.mutate(&config)
		if err := config.expectation().Validate(); err == nil {
			t.Errorf("an expectation with %s was accepted", broken.name)
		}
	}
}

// The refresh bound is checked before anything is opened, and it is the
// attestor's own bound rather than the one the other workers have.
func TestTheAttestorRefusesARefreshAtOrPastItsDetectionBound(t *testing.T) {
	for _, interval := range []time.Duration{
		0, -time.Second, output.MaxPolicyEvidenceAge, output.MaxPolicyEvidenceAge + time.Minute,
	} {
		config := completeAttestor()
		config.Interval = interval

		err := run(context.Background(), config)
		if err == nil {
			t.Errorf("a refresh interval of %s was accepted", interval)

			continue
		}
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("a refresh interval of %s was refused as %v, which is not a configuration "+
				"refusal; the run may have got as far as opening a connection", interval, err)
		}
	}
}
