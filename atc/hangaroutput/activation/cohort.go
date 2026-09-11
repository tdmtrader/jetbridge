package activation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Member is one daemon in the cohort, named the way the Kubernetes API named it.
type Member struct {
	Node    string
	Address string
}

// CohortSource enumerates the daemons this cluster actually runs.
//
// From the KUBERNETES API and never from node labels. A label is a hint the
// daemon itself writes; attesting a cohort from the hints its members published
// would be asking the cohort whether it is homogeneous. The API's view of the
// DaemonSet's pods is the one thing here that no member controls.
type CohortSource interface {
	Members(ctx context.Context) ([]Member, error)
}

// Handshaker asks one daemon what it speaks.
type Handshaker interface {
	Base(ctx context.Context, member Member) (executioncontrol.Handshake, error)
	Extension(ctx context.Context, member Member) (output.ExtensionHandshake, error)
}

// AttestBase gathers the base cohort's evidence.
//
// Homogeneity is the claim, and a cohort of one node is still a cohort -- but a
// cohort of ZERO is not, and it is the case that would otherwise pass: an
// attestation over an empty set is vacuously homogeneous, and enabling a facet
// on it would put a plane into service with nothing serving it.
func AttestBase(ctx context.Context, source CohortSource, handshakes Handshaker,
	epoch executioncontrol.ActivationEpoch) (Evidence, error) {
	members, err := source.Members(ctx)
	if err != nil {
		return Evidence{}, err
	}
	if len(members) == 0 {
		return Evidence{}, fmt.Errorf("%w: the Kubernetes API reports no output daemon pods. "+
			"An attestation over an empty cohort is vacuously homogeneous, which would enable "+
			"a facet with nothing serving it", output.ErrIncomplete)
	}

	type record struct {
		Node            string `json:"node"`
		ProtocolVersion string `json:"protocol_version"`
		LedgerVersion   string `json:"ledger_version"`
		ControlKeyID    string `json:"control_key_id"`
		ActivationEpoch uint64 `json:"activation_epoch"`
	}

	records := make([]record, 0, len(members))
	var mixed []string
	for _, member := range members {
		handshake, err := handshakes.Base(ctx, member)
		if err != nil {
			return Evidence{}, fmt.Errorf("%w: %s did not answer the base handshake: %v",
				output.ErrIncomplete, member.Node, err)
		}
		if err := handshake.Validate(); err != nil {
			return Evidence{}, fmt.Errorf("%w: %s answered an invalid base handshake: %v",
				output.ErrIncomplete, member.Node, err)
		}
		if handshake.ActivationEpoch != epoch {
			mixed = append(mixed, fmt.Sprintf("%s speaks for epoch %d",
				member.Node, handshake.ActivationEpoch))
		}
		records = append(records, record{
			Node:            member.Node,
			ProtocolVersion: handshake.ProtocolVersion,
			LedgerVersion:   handshake.LedgerVersion,
			ControlKeyID:    handshake.ControlKeyID,
			ActivationEpoch: uint64(handshake.ActivationEpoch),
		})
	}
	if len(mixed) != 0 {
		return Evidence{}, mixedCohort(epoch, mixed)
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Node < records[j].Node })

	protocols, ledgers := map[string]bool{}, map[string]bool{}
	for _, one := range records {
		protocols[one.ProtocolVersion] = true
		ledgers[one.LedgerVersion] = true
	}
	if len(protocols) != 1 || len(ledgers) != 1 {
		return Evidence{}, mixedCohort(epoch, []string{
			fmt.Sprintf("protocol versions %v and ledger versions %v",
				sortedKeysOf(protocols), sortedKeysOf(ledgers)),
		})
	}

	bundle, err := json.Marshal(map[string]any{"members": records})
	if err != nil {
		return Evidence{}, fmt.Errorf("%w: encoding the base attestation: %v",
			output.ErrIncomplete, err)
	}

	return Evidence{
		Attestation:     bundle,
		ProtocolVersion: records[0].ProtocolVersion,
		LedgerVersion:   records[0].LedgerVersion,
		CohortDigest:    digestOf(bundle),
	}, nil
}

// AttestOutput gathers the output cohort's evidence.
//
// Every member must agree about the bucket and the derived namespace as well as
// the versions. Two cohorts on one bucket is two planes disagreeing about whose
// object is whose, and the one that loses that argument deletes the other's.
func AttestOutput(ctx context.Context, source CohortSource, handshakes Handshaker,
	epoch executioncontrol.ActivationEpoch) (Evidence, error) {
	members, err := source.Members(ctx)
	if err != nil {
		return Evidence{}, err
	}
	if len(members) == 0 {
		return Evidence{}, fmt.Errorf("%w: the Kubernetes API reports no output daemon pods",
			output.ErrIncomplete)
	}

	type record struct {
		Node                 string `json:"node"`
		CaptureVersion       string `json:"capture_extension_version"`
		SourceLedgerVersion  string `json:"source_ledger_version"`
		ReceiptPublicKeyID   string `json:"receipt_public_key_id"`
		MaterializationKeyID string `json:"materialization_key_id"`
		BucketFingerprint    string `json:"bucket_fingerprint"`
		DerivedNamespace     string `json:"derived_namespace"`
	}

	records := make([]record, 0, len(members))
	var mixed []string
	for _, member := range members {
		handshake, err := handshakes.Extension(ctx, member)
		if err != nil {
			return Evidence{}, fmt.Errorf("%w: %s did not answer the capture extension "+
				"handshake: %v", output.ErrIncomplete, member.Node, err)
		}
		if err := handshake.Validate(); err != nil {
			return Evidence{}, fmt.Errorf("%w: %s answered an invalid extension handshake: %v",
				output.ErrIncomplete, member.Node, err)
		}
		if handshake.Base.ActivationEpoch != epoch {
			mixed = append(mixed, fmt.Sprintf("%s speaks for epoch %d",
				member.Node, handshake.Base.ActivationEpoch))
		}
		records = append(records, record{
			Node:                 member.Node,
			CaptureVersion:       handshake.CaptureExtensionVersion,
			SourceLedgerVersion:  handshake.SourceLedgerVersion,
			ReceiptPublicKeyID:   handshake.ReceiptPublicKeyID,
			MaterializationKeyID: handshake.MaterializationKeyID,
			BucketFingerprint:    handshake.BucketFingerprint,
			DerivedNamespace:     handshake.DerivedNamespace,
		})
	}
	if len(mixed) != 0 {
		return Evidence{}, mixedCohort(epoch, mixed)
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Node < records[j].Node })

	for _, field := range []struct {
		name  string
		value func(record) string
	}{
		{"capture extension version", func(r record) string { return r.CaptureVersion }},
		{"source ledger version", func(r record) string { return r.SourceLedgerVersion }},
		{"receipt public key id", func(r record) string { return r.ReceiptPublicKeyID }},
		{"materialization key id", func(r record) string { return r.MaterializationKeyID }},
		{"bucket", func(r record) string { return r.BucketFingerprint }},
		{"derived namespace", func(r record) string { return r.DerivedNamespace }},
	} {
		distinct := map[string]bool{}
		for _, one := range records {
			distinct[field.value(one)] = true
		}
		if len(distinct) != 1 {
			return Evidence{}, mixedCohort(epoch, []string{
				fmt.Sprintf("the cohort reports %d different values for the %s: %v",
					len(distinct), field.name, sortedKeysOf(distinct)),
			})
		}
	}

	bundle, err := json.Marshal(map[string]any{"members": records})
	if err != nil {
		return Evidence{}, fmt.Errorf("%w: encoding the output attestation: %v",
			output.ErrIncomplete, err)
	}

	return Evidence{
		Attestation:          bundle,
		CohortDigest:         digestOf(bundle),
		ReceiptPublicKeyID:   records[0].ReceiptPublicKeyID,
		MaterializationKeyID: records[0].MaterializationKeyID,
		BucketFingerprint:    records[0].BucketFingerprint,
		DerivedNamespace:     records[0].DerivedNamespace,
	}, nil
}

func mixedCohort(epoch executioncontrol.ActivationEpoch, detail []string) error {
	return fmt.Errorf("%w: the cohort is mixed and epoch %d cannot be attested over it: %s. "+
		"Two cohorts on one bucket is two planes disagreeing about whose object is whose, and "+
		"the one that loses that argument deletes the other's. Roll the DaemonSet to one "+
		"version, or begin a new epoch for the new one",
		output.ErrUnsupportedProtocol, epoch, strings.Join(detail, "; "))
}

func digestOf(bundle []byte) string {
	sum := sha256.Sum256(bundle)

	return hex.EncodeToString(sum[:])
}

func sortedKeysOf(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return keys
}
