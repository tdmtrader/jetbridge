package publisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func changeManifest(baseSHA string) snapshot.Snapshot {
	manifest := snapshot.Snapshot{
		ID: 41, Type: snapshot.TypeRef("repository-change/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)), ByteSize: 1024, FileCount: 3,
		Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
		CreatedAt: time.Now().UTC(),
	}
	encoded, err := json.Marshal(contracts.RepositoryChangeMetadata{
		RepositoryID: "sha256:" + strings.Repeat("9", 64), BaseSHA: baseSHA,
		ResultCommit: strings.Repeat("e", 40), ResultTree: strings.Repeat("f", 40),
		Representation: "git-tree",
	})
	if err != nil {
		panic(err)
	}
	manifest.IntrinsicMetadata = encoded
	return manifest
}

// The merge base assertion is read out of the snapshot the workflow produced,
// which is what lets await_snapshot and publish_snapshot describe one merge
// without an author keeping two YAML literals in sync.
func TestMergeBaseIsDerivedFromTheSealedRepositoryChange(t *testing.T) {
	base := strings.Repeat("b", 40)
	derived, err := publisher.MergeBaseFromChange(changeManifest(base))
	if err != nil || derived != base {
		t.Fatalf("derived merge base = (%q, %v), want %q", derived, err, base)
	}

	sha256Base := strings.Repeat("c", 64)
	derived, err = publisher.MergeBaseFromChange(changeManifest(sha256Base))
	if err != nil || derived != sha256Base {
		t.Fatalf("sha256 merge base = (%q, %v), want %q", derived, err, sha256Base)
	}
}

func TestMergeBaseDerivationFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(*snapshot.Snapshot){
		"wrong type": func(manifest *snapshot.Snapshot) {
			manifest.Type = snapshot.TypeRef("repository/v1")
		},
		"no intrinsic metadata": func(manifest *snapshot.Snapshot) { manifest.IntrinsicMetadata = nil },
		"unreadable metadata": func(manifest *snapshot.Snapshot) {
			manifest.IntrinsicMetadata = json.RawMessage(`["not an object"]`)
		},
		"abbreviated base": func(manifest *snapshot.Snapshot) {
			manifest.IntrinsicMetadata = json.RawMessage(`{"base_sha":"abc1234"}`)
		},
		"non-hexadecimal base": func(manifest *snapshot.Snapshot) {
			manifest.IntrinsicMetadata = json.RawMessage(`{"base_sha":"` + strings.Repeat("z", 40) + `"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			manifest := changeManifest(strings.Repeat("b", 40))
			mutate(&manifest)
			derived, err := publisher.MergeBaseFromChange(manifest)
			if !errors.Is(err, publisher.ErrInvalidRequest) || derived != "" {
				t.Fatalf("derived = (%q, %v), want an invalid-request refusal", derived, err)
			}
		})
	}
}

// Authoring the assertion is refused rather than overwritten. A silent
// overwrite would let a plan display one base to a reviewer while the
// publication asserted another.
func TestStampMergeBaseRefusesAnAuthoredAssertion(t *testing.T) {
	base := strings.Repeat("b", 40)
	stamped, err := publisher.StampMergeBase(map[string]string{"target_branch": "main"}, base)
	if err != nil || stamped["expected_base_sha"] != base || stamped["target_branch"] != "main" {
		t.Fatalf("stamped = (%v, %v)", stamped, err)
	}

	authored := map[string]string{"target_branch": "main", "expected_base_sha": base}
	if _, err := publisher.StampMergeBase(authored, base); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("authored assertion error = %v, want an invalid-request refusal", err)
	}
	if err := publisher.RejectAuthoredMergeBase(authored); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("reject authored error = %v", err)
	}
	if err := publisher.RejectAuthoredMergeBase(map[string]string{"target_branch": "main"}); err != nil {
		t.Fatalf("unauthored parameters were refused: %v", err)
	}
	// An empty authored value is still authored: it would fail the durable
	// request's own validation later, so refuse it at the boundary.
	if err := publisher.RejectAuthoredMergeBase(map[string]string{"expected_base_sha": ""}); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("empty authored assertion error = %v", err)
	}

	source := map[string]string{"target_branch": "main"}
	if _, err := publisher.StampMergeBase(source, "not-a-sha"); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("invalid derived base error = %v", err)
	}
	if _, mutated := source["expected_base_sha"]; mutated {
		t.Fatal("StampMergeBase mutated the authored parameter map")
	}
}

func TestValidateAuthoredMergeIntentRefusesAnAuthoredMergeBase(t *testing.T) {
	valid := publisher.AuthoredMergeIntent{
		Publisher: publisher.GitPublisher, Destination: "git.example/acme/widget",
		Parameters: map[string]string{"target_branch": "main"}, ApprovalPolicyVersion: "engineering/v1",
	}
	if err := publisher.ValidateAuthoredMergeIntent(valid); err != nil {
		t.Fatalf("valid authored intent: %v", err)
	}

	authored := valid
	authored.Parameters = map[string]string{
		"target_branch": "main", "expected_base_sha": strings.Repeat("b", 40),
	}
	if err := publisher.ValidateAuthoredMergeIntent(authored); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("authored merge base error = %v", err)
	}

	missingBranch := valid
	missingBranch.Parameters = map[string]string{}
	if err := publisher.ValidateAuthoredMergeIntent(missingBranch); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("missing target_branch error = %v", err)
	}
}

// A durable merge request always carries the stamped assertion. The verifier
// must refuse one that does not, so a server that failed to derive it cannot
// fall through to an unasserted merge.
func TestMergeApprovalContextRequiresACompleteDerivedBase(t *testing.T) {
	request := publisher.MergeApprovalRequest{
		TeamID: 9, WorkflowRunID: 17, BuildID: 12,
		Input: snapshot.SnapshotRef{
			ID: 41, Type: snapshot.TypeRef("repository-change/v1"),
			Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
		},
		Publisher: publisher.GitPublisher, Mode: publisher.ModeMerge,
		Destination: "git.example/acme/widget",
		Parameters: map[string]string{
			"target_branch": "main", "expected_base_sha": strings.Repeat("b", 40),
		},
		ExpectedBaseSHA: strings.Repeat("b", 40), ApprovalPolicyVersion: "engineering/v1",
	}
	if _, err := publisher.BuildMergeApprovalContext(request); err != nil {
		t.Fatalf("complete merge approval request: %v", err)
	}

	for name, mutate := range map[string]func(*publisher.MergeApprovalRequest){
		"empty": func(r *publisher.MergeApprovalRequest) {
			r.ExpectedBaseSHA = ""
			delete(r.Parameters, "expected_base_sha")
		},
		"abbreviated": func(r *publisher.MergeApprovalRequest) {
			r.ExpectedBaseSHA = "abc1234"
			r.Parameters["expected_base_sha"] = "abc1234"
		},
		"parameter disagrees": func(r *publisher.MergeApprovalRequest) {
			r.Parameters["expected_base_sha"] = strings.Repeat("c", 40)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			candidate.Parameters = map[string]string{
				"target_branch": "main", "expected_base_sha": strings.Repeat("b", 40),
			}
			mutate(&candidate)
			if _, err := publisher.BuildMergeApprovalContext(candidate); !errors.Is(err, publisher.ErrInvalidRequest) {
				t.Fatalf("error = %v, want an invalid-request refusal", err)
			}
		})
	}
}

// SAFETY PROPERTY, do not weaken: making the assertion server-derived does not
// make it the stale-base gate. Even when the assertion agrees exactly with the
// change being published, a destination that moved after approval must still be
// caught by comparing the backend's CURRENT base, with no side effect.
func TestServerDerivedBaseDoesNotBypassTheCurrentBaseGate(t *testing.T) {
	derivedBase := strings.Repeat("b", 40)
	store := publisher.NewMemoryStore(time.Now)
	backend := &gitBackendStub{base: strings.Repeat("c", 40)}
	service, err := publisher.NewGitService(store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
		changeInspectorStub{change: publisher.RepositoryChange{
			BaseSHA: derivedBase, ResultSHA: "head", MaterializedRoot: "/change",
		}}, backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	merge := branchRequest()
	merge.Mode = publisher.ModeMerge
	merge.ApprovedBy = "alice"
	merge.Approval = approvalEvidence("alice", 11)
	// Exactly what the exec step stamps: the assertion equals the change's own
	// base, so it can never disagree and can never be the thing that fails.
	merge.Parameters, err = publisher.StampMergeBase(map[string]string{"target_branch": "main"}, derivedBase)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := service.Execute(context.Background(), merge)
	if err != nil || publication.Status != publisher.StatusStaleBase {
		t.Fatalf("merge with a matching assertion onto a moved target = (%+v, %v), want stale_base", publication, err)
	}
	if len(backend.operations) != 0 {
		t.Fatalf("stale base reached the provider: %+v", backend.operations)
	}
	if publication.Result.BaseSHA != backend.base {
		t.Fatalf("result base = %q, want the destination's current tip %q", publication.Result.BaseSHA, backend.base)
	}

	// The identical request lands once the destination's tip is what the change
	// was actually based on, proving the CurrentBase comparison is the gate and
	// the assertion is not. A fresh store is used because the stale outcome
	// above is terminal for this operation key.
	backend.base = derivedBase
	backend.result = publisher.GitResult{HeadSHA: "head"}
	landing, err := publisher.NewGitService(publisher.NewMemoryStore(time.Now),
		&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
		changeInspectorStub{change: publisher.RepositoryChange{
			BaseSHA: derivedBase, ResultSHA: "head", MaterializedRoot: "/change",
		}}, backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	landed, err := landing.Execute(context.Background(), merge)
	if err != nil || landed.Status != publisher.StatusSucceeded || len(backend.operations) != 1 {
		t.Fatalf("merge onto the exact base = (%+v, %v), operations=%d", landed, err, len(backend.operations))
	}
	if backend.operations[0].BaseSHA != derivedBase {
		t.Fatalf("published operation base = %q, want %q", backend.operations[0].BaseSHA, derivedBase)
	}
}
