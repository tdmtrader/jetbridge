package activation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/hangaroutput/activation"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// The homogeneous-cohort half of Req 57, which until now was exercised by
// nothing at all.
//
// Measured coverage of cohort.go was 0.0%: the empty-cohort refusal, the
// mixed-epoch refusal and the six-field homogeneity comparison could all have
// been deleted and every suite in the tree would have stayed green. The two
// tests that mention attestation build their evidence by hand and never reach
// this file.
//
// The cohort source and the handshaker are stood up here rather than reached
// for. They are the Kubernetes API's view of the DaemonSet's pods and an HTTP
// call to each daemon: there is no real one to run against outside a cluster,
// which is precisely why these two things are interfaces. What is under test is
// the decision made over their answers, and the answers are the real value
// types, validated by the real Validate.

const (
	epochUnderTest = executioncontrol.ActivationEpoch(7)
	otherEpoch     = executioncontrol.ActivationEpoch(8)
)

type cohortSource struct {
	members []activation.Member
	failure error
}

func (source cohortSource) Members(context.Context) ([]activation.Member, error) {
	return source.members, source.failure
}

// handshaker answers per node, so a table entry changes ONE member's answer and
// leaves the rest of the cohort alone -- which is what "mixed" means.
type handshaker struct {
	base      map[string]executioncontrol.Handshake
	extension map[string]output.ExtensionHandshake
	failures  map[string]error
}

func (shakes handshaker) Base(_ context.Context, member activation.Member) (executioncontrol.Handshake, error) {
	if err := shakes.failures[member.Node]; err != nil {
		return executioncontrol.Handshake{}, err
	}

	return shakes.base[member.Node], nil
}

func (shakes handshaker) Extension(_ context.Context, member activation.Member) (output.ExtensionHandshake, error) {
	if err := shakes.failures[member.Node]; err != nil {
		return output.ExtensionHandshake{}, err
	}

	return shakes.extension[member.Node], nil
}

func healthyBase() executioncontrol.Handshake {
	return executioncontrol.Handshake{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		LedgerVersion:   executioncontrol.LedgerVersion,
		ControlKeyID:    "control-key-1",
		ActivationEpoch: epochUnderTest,
	}
}

func healthyExtension() output.ExtensionHandshake {
	return output.ExtensionHandshake{
		Base:                    healthyBase(),
		CaptureExtensionVersion: output.ProtocolVersion,
		SourceLedgerVersion:     output.SourceLedgerVersion,
		ReceiptPublicKeyID:      "receipt-key-1",
		MaterializationKeyID:    "materialize-key-1",
		BucketFingerprint:       "gs://one-bucket",
		DerivedNamespace:        "deployments/blue/one",
	}
}

// twoNodes is the cohort every case below starts from: two members, agreeing on
// everything. Each case then changes exactly one thing, so a red row names the
// check rather than "attestation failed".
func twoNodes() ([]activation.Member, handshaker) {
	members := []activation.Member{
		{Node: "node-b", Address: "10.0.0.2:7788"},
		{Node: "node-a", Address: "10.0.0.1:7788"},
	}
	shakes := handshaker{
		base:      map[string]executioncontrol.Handshake{},
		extension: map[string]output.ExtensionHandshake{},
		failures:  map[string]error{},
	}
	for _, member := range members {
		shakes.base[member.Node] = healthyBase()
		shakes.extension[member.Node] = healthyExtension()
	}

	return members, shakes
}

func TestTheBaseCohortIsAttestedOnlyWhenItIsHomogeneous(t *testing.T) {
	for name, probe := range map[string]struct {
		spoil     func(members *[]activation.Member, shakes *handshaker)
		sentinel  error
		substring string
	}{
		// A cohort of ZERO is the case that would otherwise pass: an
		// attestation over an empty set is vacuously homogeneous, and enabling
		// a facet on it puts a plane into service with nothing serving it.
		"no members at all": {
			spoil:     func(members *[]activation.Member, _ *handshaker) { *members = nil },
			sentinel:  output.ErrIncomplete,
			substring: "empty cohort",
		},
		"one member speaking for another epoch": {
			spoil: func(_ *[]activation.Member, shakes *handshaker) {
				answer := healthyBase()
				answer.ActivationEpoch = otherEpoch
				shakes.base["node-a"] = answer
			},
			sentinel:  output.ErrUnsupportedProtocol,
			substring: "node-a speaks for epoch 8",
		},
		"two members on different protocol versions": {
			spoil: func(_ *[]activation.Member, shakes *handshaker) {
				answer := healthyBase()
				answer.ProtocolVersion = "hangar-execution-control-v0"
				shakes.base["node-a"] = answer
			},
			// Caught by Validate before the homogeneity comparison, because a
			// protocol version this cohort does not speak is not a version
			// disagreement -- it is a member that cannot be talked to.
			sentinel:  output.ErrIncomplete,
			substring: "invalid base handshake",
		},
		"one member answering nothing at all": {
			spoil: func(_ *[]activation.Member, shakes *handshaker) {
				shakes.failures["node-b"] = errors.New("connection refused")
			},
			sentinel:  output.ErrIncomplete,
			substring: "node-b did not answer",
		},
		"one member answering an incomplete handshake": {
			spoil: func(_ *[]activation.Member, shakes *handshaker) {
				answer := healthyBase()
				answer.ControlKeyID = ""
				shakes.base["node-b"] = answer
			},
			sentinel:  output.ErrIncomplete,
			substring: "node-b answered an invalid base handshake",
		},
		"two members on different ledger versions": {
			spoil: func(_ *[]activation.Member, shakes *handshaker) {
				answer := healthyBase()
				answer.LedgerVersion = "some-other-ledger"
				shakes.base["node-a"] = answer
			},
			sentinel:  output.ErrUnsupportedProtocol,
			substring: "ledger versions",
		},
	} {
		t.Run(name, func(t *testing.T) {
			members, shakes := twoNodes()
			probe.spoil(&members, &shakes)

			evidence, err := activation.AttestBase(context.Background(),
				cohortSource{members: members}, shakes, epochUnderTest)
			if !errors.Is(err, probe.sentinel) {
				t.Fatalf("the cohort was attested, or refused for the wrong reason: %v "+
					"(evidence: %s)", err, evidence.Attestation)
			}
			if !strings.Contains(err.Error(), probe.substring) {
				t.Errorf("the refusal does not say which member or which field: %v", err)
			}
		})
	}
}

func TestTheOutputCohortIsAttestedOnlyWhenItIsHomogeneous(t *testing.T) {
	// The six fields AttestOutput compares, one entry each, each spoiled on ONE
	// member so the cohort really is mixed rather than uniformly wrong.
	//
	// Two of them -- the capture extension version and the source ledger
	// version -- are ALSO pinned to constants by ExtensionHandshake.Validate,
	// which runs first, so a member that disagrees about those is refused as
	// invalid before the homogeneity comparison ever sees it. Those two entries
	// say so and assert the refusal that actually arrives: the comparison over
	// them is a backstop that cannot fire while Validate pins the constants,
	// and the honest record of that is here rather than a claim in a comment.
	for name, probe := range map[string]struct {
		spoil     func(*output.ExtensionHandshake)
		sentinel  error
		substring string
	}{
		"capture extension version": {
			spoil:     func(h *output.ExtensionHandshake) { h.CaptureExtensionVersion = "v0" },
			sentinel:  output.ErrIncomplete,
			substring: "invalid extension handshake",
		},
		"source ledger version": {
			spoil:     func(h *output.ExtensionHandshake) { h.SourceLedgerVersion = "v0" },
			sentinel:  output.ErrIncomplete,
			substring: "invalid extension handshake",
		},
		"receipt public key id": {
			spoil:     func(h *output.ExtensionHandshake) { h.ReceiptPublicKeyID = "receipt-key-2" },
			sentinel:  output.ErrUnsupportedProtocol,
			substring: "receipt public key id",
		},
		"materialization key id": {
			spoil:     func(h *output.ExtensionHandshake) { h.MaterializationKeyID = "materialize-key-2" },
			sentinel:  output.ErrUnsupportedProtocol,
			substring: "materialization key id",
		},
		// The one that matters most: two cohorts on one bucket is two planes
		// disagreeing about whose object is whose, and the one that loses that
		// argument deletes the other's.
		"bucket": {
			spoil:     func(h *output.ExtensionHandshake) { h.BucketFingerprint = "gs://another-bucket" },
			sentinel:  output.ErrUnsupportedProtocol,
			substring: "bucket",
		},
		"derived namespace": {
			spoil:     func(h *output.ExtensionHandshake) { h.DerivedNamespace = "deployments/green/one" },
			sentinel:  output.ErrUnsupportedProtocol,
			substring: "derived namespace",
		},
	} {
		t.Run("the cohort disagrees about the "+name, func(t *testing.T) {
			members, shakes := twoNodes()
			answer := healthyExtension()
			probe.spoil(&answer)
			shakes.extension["node-a"] = answer

			evidence, err := activation.AttestOutput(context.Background(),
				cohortSource{members: members}, shakes, epochUnderTest)
			if !errors.Is(err, probe.sentinel) {
				t.Fatalf("the cohort was attested, or refused for the wrong reason: %v "+
					"(evidence: %s)", err, evidence.Attestation)
			}
			if !strings.Contains(err.Error(), probe.substring) {
				t.Errorf("the refusal does not name the field the cohort disagrees about: %v", err)
			}
		})
	}

	t.Run("there are no members at all", func(t *testing.T) {
		_, shakes := twoNodes()
		_, err := activation.AttestOutput(context.Background(),
			cohortSource{}, shakes, epochUnderTest)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Fatalf("an empty output cohort was attested: %v", err)
		}
		if !strings.Contains(err.Error(), "no output daemon pods") {
			t.Errorf("the refusal does not say the cohort is empty: %v", err)
		}
	})

	t.Run("one member speaks for another epoch", func(t *testing.T) {
		members, shakes := twoNodes()
		answer := healthyExtension()
		answer.Base.ActivationEpoch = otherEpoch
		shakes.extension["node-b"] = answer

		_, err := activation.AttestOutput(context.Background(),
			cohortSource{members: members}, shakes, epochUnderTest)
		if !errors.Is(err, output.ErrUnsupportedProtocol) {
			t.Fatalf("a cohort straddling two epochs was attested: %v", err)
		}
		if !strings.Contains(err.Error(), "node-b speaks for epoch 8") {
			t.Errorf("the refusal does not name the member or the epoch: %v", err)
		}
	})

	t.Run("one member answers nothing", func(t *testing.T) {
		members, shakes := twoNodes()
		shakes.failures["node-a"] = errors.New("i/o timeout")

		_, err := activation.AttestOutput(context.Background(),
			cohortSource{members: members}, shakes, epochUnderTest)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Fatalf("a cohort with an unreachable member was attested: %v", err)
		}
		if !strings.Contains(err.Error(), "node-a did not answer") {
			t.Errorf("the refusal does not name the member: %v", err)
		}
	})

	t.Run("the source itself cannot be read", func(t *testing.T) {
		_, shakes := twoNodes()
		failure := errors.New("the Kubernetes API said no")
		_, err := activation.AttestOutput(context.Background(),
			cohortSource{failure: failure}, shakes, epochUnderTest)
		if !errors.Is(err, failure) {
			t.Fatalf("a cohort nobody could enumerate was attested: %v", err)
		}
	})
}

// THE HAPPY PATH, AND THE DIGEST'S STABILITY.
//
// The digest is what the epoch row stores and what a later checkpoint compares
// against, so it has to be a function of the cohort and not of the order the
// Kubernetes API happened to list it in. Both attestations sort their records
// by node before encoding; this is what says so.
func TestAnAgreeingCohortAttestsToAStableSortedDigest(t *testing.T) {
	ctx := context.Background()

	forward, shakes := twoNodes()
	reversed := []activation.Member{forward[1], forward[0]}
	if forward[0].Node == reversed[0].Node {
		t.Fatal("the two orders are the same order, so this test compares nothing")
	}

	for _, attest := range []struct {
		name string
		call func(source activation.CohortSource) (activation.Evidence, error)
	}{
		{"base", func(source activation.CohortSource) (activation.Evidence, error) {
			return activation.AttestBase(ctx, source, shakes, epochUnderTest)
		}},
		{"output", func(source activation.CohortSource) (activation.Evidence, error) {
			return activation.AttestOutput(ctx, source, shakes, epochUnderTest)
		}},
	} {
		t.Run(attest.name, func(t *testing.T) {
			first, err := attest.call(cohortSource{members: forward})
			if err != nil {
				t.Fatalf("an agreeing cohort was refused: %v", err)
			}
			second, err := attest.call(cohortSource{members: reversed})
			if err != nil {
				t.Fatalf("the same cohort listed the other way round was refused: %v", err)
			}

			if first.CohortDigest != second.CohortDigest {
				t.Errorf("the digest depends on the order the members were listed in: %s vs %s",
					first.CohortDigest, second.CohortDigest)
			}
			if string(first.Attestation) != string(second.Attestation) {
				t.Errorf("the attestation bundle depends on listing order:\n%s\n%s",
					first.Attestation, second.Attestation)
			}

			// The digest is of the bundle that is stored beside it, computed
			// here independently. A digest of something else would still be
			// stable and would still be wrong.
			sum := sha256.Sum256(first.Attestation)
			if want := hex.EncodeToString(sum[:]); first.CohortDigest != want {
				t.Errorf("the cohort digest %s is not the sha256 of the attestation it is "+
					"stored beside (%s)", first.CohortDigest, want)
			}

			// And node-a sorts before node-b in the bundle, which is the
			// property the digest's stability rests on.
			bundle := string(first.Attestation)
			if a, b := strings.Index(bundle, "node-a"), strings.Index(bundle, "node-b"); a < 0 || b < 0 || a > b {
				t.Errorf("the bundle is not in sorted node order: %s", bundle)
			}
		})
	}

	// One node is a cohort. The empty-cohort refusal must not be a
	// small-cohort refusal.
	t.Run("a cohort of one", func(t *testing.T) {
		evidence, err := activation.AttestBase(ctx,
			cohortSource{members: []activation.Member{{Node: "node-a"}}}, shakes, epochUnderTest)
		if err != nil {
			t.Fatalf("a single-node cohort was refused: %v", err)
		}
		if evidence.CohortDigest == "" || evidence.ProtocolVersion != executioncontrol.ProtocolVersion {
			t.Errorf("the evidence is %+v", evidence)
		}
		if !strings.Contains(string(evidence.Attestation), "node-a") {
			t.Errorf("the attestation does not name its one member: %s", evidence.Attestation)
		}
	})

	t.Run("the output evidence carries the facts the epoch row stores", func(t *testing.T) {
		evidence, err := activation.AttestOutput(ctx, cohortSource{members: forward}, shakes,
			epochUnderTest)
		if err != nil {
			t.Fatalf("an agreeing cohort was refused: %v", err)
		}
		healthy := healthyExtension()
		for _, field := range []struct{ name, got, want string }{
			{"receipt public key id", evidence.ReceiptPublicKeyID, healthy.ReceiptPublicKeyID},
			{"materialization key id", evidence.MaterializationKeyID, healthy.MaterializationKeyID},
			{"bucket fingerprint", evidence.BucketFingerprint, healthy.BucketFingerprint},
			{"derived namespace", evidence.DerivedNamespace, healthy.DerivedNamespace},
		} {
			if field.got != field.want {
				t.Errorf("the attested %s is %q and the cohort reported %q",
					field.name, field.got, field.want)
			}
		}
	})
}
