//go:build live
// +build live

package contracttest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/publisher/contracttest"
)

// TestLiveGatewayContract runs the publisher gateway conformance kit against a
// real deployment. It is read-only unless a change fixture is supplied.
//
//	PUBLISHER_GATEWAY_URL=https://gateway.example \
//	PUBLISHER_GATEWAY_TOKEN=<bearer token> \
//	PUBLISHER_GATEWAY_DESTINATION=git.example/acme/scratch \
//	PUBLISHER_GATEWAY_TARGET_BRANCH=main \
//	PUBLISHER_GATEWAY_TEAM=engineering \
//	PUBLISHER_GATEWAY_POLICY_VERSION=engineering/v1 \
//	go test -tags live -run '^TestLiveGatewayContract$' -count=1 -v ./agent/publisher/contracttest/
//
// Optional:
//
//	PUBLISHER_GATEWAY_CA_FILE   absolute path to a private CA bundle
//	PUBLISHER_GATEWAY_CHANGE_TAR, _BASE_SHA, _RESULT_SHA, _RESULT_TREE
//	    enable the write checks against a SCRATCH destination. They create a
//	    real external effect. Never point them at a production repository.
func TestLiveGatewayContract(t *testing.T) {
	endpoint := os.Getenv("PUBLISHER_GATEWAY_URL")
	token := os.Getenv("PUBLISHER_GATEWAY_TOKEN")
	if endpoint == "" || token == "" {
		t.Skip("PUBLISHER_GATEWAY_URL and PUBLISHER_GATEWAY_TOKEN are not set")
	}
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	config := contracttest.Config{
		Endpoint: endpoint, TokenFile: tokenPath,
		CACertificateFile:     os.Getenv("PUBLISHER_GATEWAY_CA_FILE"),
		TeamName:              envOrDefault("PUBLISHER_GATEWAY_TEAM", "engineering"),
		ApprovalPolicyVersion: envOrDefault("PUBLISHER_GATEWAY_POLICY_VERSION", "engineering/v1"),
		GitDestination:        os.Getenv("PUBLISHER_GATEWAY_DESTINATION"),
		GitTargetBranch:       envOrDefault("PUBLISHER_GATEWAY_TARGET_BRANCH", "main"),
	}
	// Supplying a change fixture opts the write checks in, and that includes
	// publish_is_idempotent_under_one_key. A pass on that check is NOT a
	// certification that this gateway is safe against eventual-consistency
	// double-publish: the real client looks up before it writes, so a gateway
	// with a working lookup but a non-deduplicating publish side recovers the
	// second attempt at lookup, passes this check, and still double-publishes
	// the moment its read side lags its write side. Do not read a green run as
	// that assurance. Pinning publish-side dedupe needs either the deferred
	// concurrent-double-attempt strengthening of the kit or out-of-band
	// verification of the gateway's durable (publisher, operation_key) map;
	// in-repo it is pinned by the blind-lookup test
	// TestGatewayRetriesCarryBytewiseIdenticalIdempotencyKeyWhenLookupLagsThePublish
	// in agent/publisher/gateway_idempotency_test.go.
	if tarPath := os.Getenv("PUBLISHER_GATEWAY_CHANGE_TAR"); tarPath != "" {
		change := contracttest.LoadChangeFixture(t, tarPath,
			os.Getenv("PUBLISHER_GATEWAY_BASE_SHA"),
			os.Getenv("PUBLISHER_GATEWAY_RESULT_SHA"),
			os.Getenv("PUBLISHER_GATEWAY_RESULT_TREE"),
		)
		config.Change = &change
	}
	contracttest.Run(t, config)
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
