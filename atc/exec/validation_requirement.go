package exec

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec/build"
)

const maxValidationRequirementRecordBytes int64 = 1 << 20

type validationRequirement interface {
	requirementNames() (string, string)
	authorityFor(snapshot.SnapshotRef, []snapshot.ValidationAuthorityInput) (snapshot.ValidationAttestationAuthority, error)
}
type reviewRequirement atc.ReviewValidationRequirement

func (r reviewRequirement) requirementNames() (string, string) { return r.Candidate, r.Validation }
func (r reviewRequirement) authorityFor(c snapshot.SnapshotRef, b []snapshot.ValidationAuthorityInput) (snapshot.ValidationAttestationAuthority, error) {
	return atc.ReviewValidationRequirement(r).AuthorityFor(c, b)
}

type mergeRequirement atc.MergeApprovalValidationRequirement

func (r mergeRequirement) requirementNames() (string, string) { return r.Candidate, r.Validation }
func (r mergeRequirement) authorityFor(c snapshot.SnapshotRef, b []snapshot.ValidationAuthorityInput) (snapshot.ValidationAttestationAuthority, error) {
	return atc.MergeApprovalValidationRequirement(r).AuthorityFor(c, b)
}

type publishRequirement atc.PublishValidationRequirement

func (r publishRequirement) requirementNames() (string, string) { return r.Candidate, r.Validation }
func (r publishRequirement) authorityFor(c snapshot.SnapshotRef, b []snapshot.ValidationAuthorityInput) (snapshot.ValidationAttestationAuthority, error) {
	return atc.PublishValidationRequirement(r).AuthorityFor(c, b)
}

func requireValidationRequirement(ctx context.Context, component string, repository *build.Repository, metadata StepMetadata, metadataStore snapshot.MetadataStore, contentStore snapshot.ContentStore, requirement validationRequirement, expectedCandidate string) error {
	if repository == nil || metadataStore == nil || contentStore == nil || requirement == nil || metadata.TeamID <= 0 {
		return fmt.Errorf("%s: authoritative validation plan is unavailable", component)
	}
	candidateName, validationName := requirement.requirementNames()
	if candidateName != expectedCandidate || validateRequirementPlan(requirement) != nil {
		return fmt.Errorf("%s: authoritative validation plan is unavailable", component)
	}
	candidate, _, err := authorizedRequirementArtifact(ctx, repository, metadata.TeamID, candidateName, snapshot.TypeRef("repository-change/v1"), metadataStore)
	if err != nil {
		return fmt.Errorf("%s: authoritative validation was rejected", component)
	}
	_, validation, err := authorizedRequirementArtifact(ctx, repository, metadata.TeamID, validationName, snapshot.TypeRef("validation/v1"), metadataStore)
	if err != nil {
		return fmt.Errorf("%s: authoritative validation was rejected", component)
	}
	bases, err := validationRequirementBases(ctx, repository, metadata.TeamID, requirement, metadataStore)
	if err != nil {
		return fmt.Errorf("%s: authoritative validation was rejected", component)
	}
	authority, err := requirement.authorityFor(candidate, bases)
	if err != nil || metadata.WorkflowDefinitionID == nil || metadata.WorkflowVersion == nil || authority.WorkflowDefinitionID != *metadata.WorkflowDefinitionID || authority.WorkflowVersion != *metadata.WorkflowVersion {
		return fmt.Errorf("%s: authoritative validation plan is unavailable", component)
	}
	reader, err := contentStore.Open(ctx, validation)
	if err != nil || reader == nil {
		if reader != nil {
			_ = reader.Close()
		}
		return fmt.Errorf("%s: authoritative validation was rejected", component)
	}
	document, readErr := readValidationRequirementRecord(ctx, reader, validation)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("%s: authoritative validation was rejected", component)
	}
	var record contracts.Record[contracts.ValidationBody]
	if err := contracts.DecodeSealedRecord(document, snapshot.TypeRef("validation/v1"), &record); err != nil {
		return fmt.Errorf("%s: authoritative validation was rejected", component)
	}
	if err := workflow.RequirePassingValidationAuthority(ctx, candidate, record, authority); err != nil {
		return fmt.Errorf("%s: authoritative validation was rejected", component)
	}
	return nil
}

func validateRequirementPlan(requirement validationRequirement) error {
	switch r := requirement.(type) {
	case reviewRequirement:
		return atc.ReviewValidationRequirement(r).Validate()
	case mergeRequirement:
		return atc.MergeApprovalValidationRequirement(r).Validate()
	case publishRequirement:
		return atc.PublishValidationRequirement(r).Validate()
	default:
		return errors.New("unknown requirement")
	}
}

func validationRequirementBases(ctx context.Context, repository *build.Repository, teamID int, requirement validationRequirement, metadata snapshot.MetadataStore) ([]snapshot.ValidationAuthorityInput, error) {
	var bases []atc.DevValidationBaseInput
	switch r := requirement.(type) {
	case reviewRequirement:
		bases = r.Authority.BaseInputs
	case mergeRequirement:
		bases = r.Authority.BaseInputs
	case publishRequirement:
		bases = r.Authority.BaseInputs
	default:
		return nil, errors.New("unknown requirement")
	}
	values := make([]snapshot.ValidationAuthorityInput, 0, len(bases))
	for _, base := range bases {
		ref, _, err := authorizedRequirementArtifact(ctx, repository, teamID, base.Name, base.Type, metadata)
		if err != nil {
			return nil, err
		}
		values = append(values, snapshot.ValidationAuthorityInput{Input: base.Name, Ref: ref})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Input < values[j].Input })
	return values, nil
}

func authorizedRequirementArtifact(ctx context.Context, repository *build.Repository, teamID int, name string, typ snapshot.TypeRef, metadata snapshot.MetadataStore) (snapshot.SnapshotRef, snapshot.Snapshot, error) {
	entry, found := repository.ArtifactEntryFor(build.ArtifactName(name))
	if !found || entry.Snapshot == nil || entry.Snapshot.Type != typ {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, errors.New("unavailable")
	}
	ref := *entry.Snapshot
	if ref.Validate() != nil {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, errors.New("invalid")
	}
	manifest, found, err := metadata.GetAuthorized(ctx, teamID, ref.ID)
	if err != nil || !found || manifest.Validate() != nil || manifest.ID != ref.ID || manifest.Type != typ || manifest.Digest != ref.Digest || manifest.ContentState != snapshot.ContentStateAvailable {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, errors.New("unavailable")
	}
	return ref, manifest, nil
}

func readValidationRequirementRecord(ctx context.Context, reader io.Reader, manifest snapshot.Snapshot) ([]byte, error) {
	if manifest.ByteSize < 0 {
		return nil, errors.New("negative archive size")
	}
	hash := sha256.New()
	source := &countingValidationReader{reader: io.TeeReader(io.LimitReader(reader, manifest.ByteSize+1), hash)}
	archive := tar.NewReader(source)
	var document []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Name != "record.json" {
			continue
		}
		if document != nil || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size < 0 || header.Size > maxValidationRequirementRecordBytes {
			return nil, errors.New("invalid record.json")
		}
		document = make([]byte, header.Size)
		if _, err := io.ReadFull(archive, document); err != nil {
			return nil, err
		}
	}
	if _, err := io.Copy(io.Discard, source); err != nil {
		return nil, err
	}
	if source.count != manifest.ByteSize || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != manifest.Digest.String() || document == nil {
		return nil, errors.New("archive mismatch")
	}
	return document, nil
}

type countingValidationReader struct {
	reader io.Reader
	count  int64
}

func (r *countingValidationReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.count += int64(n)
	return n, err
}
