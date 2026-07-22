package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	defaultStageTTL = time.Hour
	maxStageTTL     = 24 * time.Hour
)

type BatchSealer struct {
	canonicalizer Canonicalizer
	validators    ValidatorRegistry
	metadata      MetadataStore
	content       ContentStore
	locks         DigestLockManager
	now           func() time.Time
	stageTTL      time.Duration
}

type BatchSealerOption func(*BatchSealer)

func WithBatchSealerClock(now func() time.Time) BatchSealerOption {
	return func(sealer *BatchSealer) { sealer.now = now }
}

func WithBatchSealerStageTTL(ttl time.Duration) BatchSealerOption {
	return func(sealer *BatchSealer) { sealer.stageTTL = ttl }
}

func NewBatchSealer(
	canonicalizer Canonicalizer,
	validators ValidatorRegistry,
	metadata MetadataStore,
	content ContentStore,
	locks DigestLockManager,
	opts ...BatchSealerOption,
) (*BatchSealer, error) {
	if interfaceIsNil(validators) {
		return nil, fmt.Errorf("snapshot: validator registry is required")
	}
	if interfaceIsNil(metadata) {
		return nil, fmt.Errorf("snapshot: metadata store is required")
	}
	if interfaceIsNil(content) {
		return nil, fmt.Errorf("snapshot: content store is required")
	}
	if interfaceIsNil(locks) {
		return nil, fmt.Errorf("snapshot: digest lock manager is required")
	}
	sealer := &BatchSealer{
		canonicalizer: canonicalizer,
		validators:    validators,
		metadata:      metadata,
		content:       content,
		locks:         locks,
		now:           time.Now,
		stageTTL:      defaultStageTTL,
	}
	for _, option := range opts {
		if option == nil {
			return nil, fmt.Errorf("snapshot: batch sealer option is required")
		}
		option(sealer)
	}
	if sealer.now == nil {
		return nil, fmt.Errorf("snapshot: batch sealer clock is required")
	}
	if sealer.stageTTL <= 0 || sealer.stageTTL > maxStageTTL {
		return nil, fmt.Errorf("snapshot: stage TTL must be greater than zero and at most %s", maxStageTTL)
	}
	return sealer, nil
}

func interfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type capturedSealOutput struct {
	source    OutputSource
	candidate CandidateOutput
	tree      *CapturedTree
}

func (sealer *BatchSealer) Seal(ctx context.Context, request SealRequest) (result map[string]SealedOutput, err error) {
	if sealer == nil {
		return nil, fmt.Errorf("snapshot: batch sealer is required")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if len(request.Outputs) == 0 {
		return map[string]SealedOutput{}, nil
	}

	validationContext, err := NewValidationContext(request.Inputs, sealer.inputOpener(request.TeamID))
	if err != nil {
		return nil, err
	}
	sourcesByPort := make(map[string]OutputSource, len(request.Outputs))
	for _, source := range request.Outputs {
		sourcesByPort[source.Port.Name] = source.Clone()
	}

	captured := make([]capturedSealOutput, 0, len(request.Outputs))
	defer func() {
		for i := len(captured) - 1; i >= 0; i-- {
			err = errors.Join(err, captured[i].tree.Close())
		}
	}()

	for _, declaration := range request.OutputDeclarations {
		source, present := sourcesByPort[declaration.Name]
		if !present {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		stream, err := source.OpenTar(ctx)
		if err != nil {
			return nil, fmt.Errorf("snapshot: open output %q tar stream: %w", source.ClientKey, err)
		}
		if stream == nil {
			return nil, fmt.Errorf("snapshot: open output %q tar stream returned no reader", source.ClientKey)
		}
		tree, captureErr := sealer.canonicalizer.Capture(ctx, stream)
		if tree != nil {
			captured = append(captured, capturedSealOutput{source: source, tree: tree})
		}
		closeErr := stream.Close()
		if captureErr != nil || closeErr != nil {
			return nil, errors.Join(
				wrapIfNonNil(fmt.Sprintf("snapshot: capture output %q", source.ClientKey), captureErr),
				wrapIfNonNil(fmt.Sprintf("snapshot: close output %q tar stream", source.ClientKey), closeErr),
			)
		}
		if tree == nil {
			return nil, fmt.Errorf("snapshot: capture output %q returned no tree", source.ClientKey)
		}

		root, err := os.OpenRoot(tree.Root)
		if err != nil {
			return nil, fmt.Errorf("snapshot: open captured output %q root: %w", source.ClientKey, err)
		}
		validator, lookupErr := sealer.validators.Lookup(source.Port.Type)
		if lookupErr != nil {
			err = root.Close()
			return nil, errors.Join(fmt.Errorf("snapshot: resolve output %q validator: %w", source.ClientKey, lookupErr), err)
		}
		if interfaceIsNil(validator) {
			err = root.Close()
			return nil, errors.Join(fmt.Errorf("snapshot: resolve output %q validator returned nil", source.ClientKey), err)
		}
		validation, validationErr := validator.Validate(ctx, root, validationContext)
		closeErr = root.Close()
		if validationErr != nil || closeErr != nil {
			return nil, errors.Join(
				wrapIfNonNil(fmt.Sprintf("snapshot: validate output %q", source.ClientKey), validationErr),
				wrapIfNonNil(fmt.Sprintf("snapshot: close output %q root", source.ClientKey), closeErr),
			)
		}
		if err := validation.Validate(); err != nil {
			return nil, fmt.Errorf("snapshot: validate output %q result: %w", source.ClientKey, err)
		}
		candidate := CandidateOutput{
			Port: source.Port, ArchivePath: tree.ArchivePath, Digest: tree.Digest,
			ByteSize: tree.ByteSize, FileCount: tree.FileCount, Representation: "application/x-tar",
			IntrinsicMetadata: validation.IntrinsicMetadata,
		}.Clone()
		if err := candidate.Validate(); err != nil {
			return nil, fmt.Errorf("snapshot: captured output %q: %w", source.ClientKey, err)
		}
		captured[len(captured)-1].candidate = candidate
	}
	if err := validateCapturedBatch(captured); err != nil {
		return nil, err
	}

	digests := uniqueCapturedDigests(captured)
	now := sealer.now()
	var sealed map[string]SealedOutput
	err = WithDigestLease(ctx, sealer.locks, digests, func(lease DigestLease) error {
		stages := make(map[Digest]StagedUpload, len(digests))
		for _, digest := range digests {
			if err := ctx.Err(); err != nil {
				return err
			}
			request := StageUploadRequest{
				Digest: digest, TeamID: request.TeamID, Attempt: request.Attempt,
				LeaseExpiresAt: now.Add(sealer.stageTTL),
			}
			stage, err := sealer.metadata.StageUpload(ctx, lease, request)
			if err != nil {
				return fmt.Errorf("snapshot: stage digest %s: %w", digest, err)
			}
			if err := validateReturnedStage(stage, request); err != nil {
				return err
			}
			stages[digest] = stage
		}

		locations := make(map[Digest][]Location, len(digests))
		for _, digest := range digests {
			if err := ctx.Err(); err != nil {
				return err
			}
			state, err := sealer.metadata.DigestState(ctx, lease, digest, now)
			if err != nil {
				return fmt.Errorf("snapshot: inspect digest %s: %w", digest, err)
			}
			if err := state.Validate(); err != nil {
				return fmt.Errorf("snapshot: conflicting digest state %s: %w", digest, err)
			}
			for _, output := range captured {
				if output.candidate.Digest == digest {
					if err := validateCandidateAgainstState(output.candidate, state); err != nil {
						return err
					}
				}
			}

			verified, err := sealer.verifiedLocations(ctx, state)
			if err != nil {
				return err
			}
			if state.Available() && len(verified) > 0 {
				locations[digest] = verified
				continue
			}
			representative := capturedForDigest(captured, digest)
			archive, err := os.Open(representative.candidate.ArchivePath)
			if err != nil {
				return fmt.Errorf("snapshot: open canonical archive for %s: %w", digest, err)
			}
			uploaded, putErr := sealer.content.Put(ctx, digest, archive)
			closeErr := archive.Close()
			if putErr != nil || closeErr != nil {
				return errors.Join(
					wrapIfNonNil(fmt.Sprintf("snapshot: upload digest %s", digest), putErr),
					wrapIfNonNil(fmt.Sprintf("snapshot: close canonical archive for %s", digest), closeErr),
				)
			}
			uploaded, err = normalizeLocations(digest, uploaded)
			if err != nil {
				return err
			}
			locations[digest] = uploaded
		}

		context, err := request.CommitContext()
		if err != nil {
			return err
		}
		commit := SealCommit{Context: context, Outputs: make([]SealCommitOutput, 0, len(captured))}
		for _, output := range captured {
			retention := retentionForOutput(request, output.source)
			committed, err := output.candidate.CommitOutput(
				output.source.ClientKey, output.source.WorkflowPort, stages[output.candidate.Digest],
				locations[output.candidate.Digest], retention, output.source.SourceMetadata,
			)
			if err != nil {
				return fmt.Errorf("snapshot: build commit output %q: %w", output.source.ClientKey, err)
			}
			commit.Outputs = append(commit.Outputs, committed)
		}
		sealed, err = sealer.metadata.CommitSealBatch(ctx, lease, commit)
		if err != nil {
			return fmt.Errorf("snapshot: commit seal batch: %w", err)
		}
		return validateSealedResult(sealed, captured)
	})
	if err != nil {
		return nil, err
	}
	return cloneSealedOutputs(sealed), nil
}

func (sealer *BatchSealer) inputOpener(teamID int) InputOpener {
	return func(ctx context.Context, _ string, ref SnapshotRef) (io.ReadCloser, error) {
		persisted, found, err := sealer.metadata.GetAuthorized(ctx, teamID, ref.ID)
		if err != nil {
			return nil, err
		}
		if !found || persisted.ContentState != ContentStateAvailable {
			return nil, fmt.Errorf("snapshot %s is absent, unavailable, or unauthorized", ref.ID)
		}
		if persisted.ID != ref.ID || persisted.Type != ref.Type || persisted.Digest != ref.Digest {
			return nil, fmt.Errorf("snapshot %s does not match the exact declared reference", ref.ID)
		}
		return sealer.content.Open(ctx, persisted)
	}
}

func (sealer *BatchSealer) verifiedLocations(ctx context.Context, state DigestState) ([]Location, error) {
	if !state.Available() {
		return nil, nil
	}
	locations := append([]Location(nil), state.Locations...)
	sortLocations(locations)
	verified := make([]Location, 0, len(locations))
	for _, location := range locations {
		exists, err := sealer.content.Exists(ctx, location)
		if err != nil {
			return nil, fmt.Errorf("snapshot: verify location %s/%s: %w", location.Driver, location.Key, err)
		}
		if exists {
			verified = append(verified, location)
		}
	}
	return verified, nil
}

func validateCapturedBatch(outputs []capturedSealOutput) error {
	type physical struct {
		byteSize       int64
		fileCount      int64
		representation string
	}
	physicalByDigest := make(map[Digest]physical, len(outputs))
	type semantic struct {
		digest  Digest
		typeRef TypeRef
	}
	intrinsic := make(map[semantic]json.RawMessage, len(outputs))
	for _, output := range outputs {
		candidate := output.candidate
		value := physical{candidate.ByteSize, candidate.FileCount, candidate.Representation}
		if prior, found := physicalByDigest[candidate.Digest]; found && prior != value {
			return fmt.Errorf("snapshot: captured outputs sharing digest %s have contradictory physical metadata", candidate.Digest)
		}
		physicalByDigest[candidate.Digest] = value
		identity := semantic{candidate.Digest, candidate.Port.Type}
		if prior, found := intrinsic[identity]; found && !rawJSONEqual(prior, candidate.IntrinsicMetadata) {
			return fmt.Errorf("snapshot: captured outputs sharing type %s and digest %s have contradictory intrinsic metadata", candidate.Port.Type, candidate.Digest)
		}
		intrinsic[identity] = candidate.IntrinsicMetadata
	}
	return nil
}

func uniqueCapturedDigests(outputs []capturedSealOutput) []Digest {
	set := make(map[Digest]struct{}, len(outputs))
	for _, output := range outputs {
		set[output.candidate.Digest] = struct{}{}
	}
	digests := make([]Digest, 0, len(set))
	for digest := range set {
		digests = append(digests, digest)
	}
	sort.Slice(digests, func(i, j int) bool { return digests[i] < digests[j] })
	return digests
}

func validateReturnedStage(stage StagedUpload, request StageUploadRequest) error {
	if err := stage.Validate(); err != nil {
		return fmt.Errorf("snapshot: metadata store returned invalid stage: %w", err)
	}
	if stage.Digest != request.Digest || stage.TeamID != request.TeamID || stage.Attempt != request.Attempt || !stage.LeaseExpiresAt.Equal(request.LeaseExpiresAt) {
		return fmt.Errorf("snapshot: metadata store returned a stage that does not match its request")
	}
	return nil
}

func validateCandidateAgainstState(candidate CandidateOutput, state DigestState) error {
	for _, manifest := range state.Snapshots {
		if manifest.ByteSize != candidate.ByteSize || manifest.FileCount != candidate.FileCount || manifest.Representation != candidate.Representation {
			return fmt.Errorf("snapshot: output %q conflicts with persisted physical digest metadata", candidate.Port.Name)
		}
		if manifest.Type == candidate.Port.Type && !rawJSONEqual(manifest.IntrinsicMetadata, candidate.IntrinsicMetadata) {
			return fmt.Errorf("snapshot: output %q conflicts with persisted intrinsic metadata", candidate.Port.Name)
		}
	}
	return nil
}

func capturedForDigest(outputs []capturedSealOutput, digest Digest) capturedSealOutput {
	for _, output := range outputs {
		if output.candidate.Digest == digest {
			return output
		}
	}
	panic("captured digest disappeared")
}

func normalizeLocations(digest Digest, locations []Location) ([]Location, error) {
	if len(locations) == 0 {
		return nil, fmt.Errorf("snapshot: content store returned no locations for %s", digest)
	}
	unique := make(map[string]Location, len(locations))
	for _, location := range locations {
		if err := location.Validate(); err != nil {
			return nil, fmt.Errorf("snapshot: content store returned invalid location: %w", err)
		}
		if location.Digest != digest {
			return nil, fmt.Errorf("snapshot: content store returned a location for another digest")
		}
		key := location.Driver + "\x00" + location.Key + "\x00" + location.Node
		unique[key] = location
	}
	normalized := make([]Location, 0, len(unique))
	for _, location := range unique {
		normalized = append(normalized, location)
	}
	sortLocations(normalized)
	return normalized, nil
}

func sortLocations(locations []Location) {
	sort.Slice(locations, func(i, j int) bool {
		left := locations[i].Driver + "\x00" + locations[i].Key + "\x00" + locations[i].Node
		right := locations[j].Driver + "\x00" + locations[j].Key + "\x00" + locations[j].Node
		return left < right
	})
}

func retentionForOutput(request SealRequest, source OutputSource) []RetentionSpec {
	if source.Retention == RetentionClassWorkflow {
		return []RetentionSpec{{
			Class:  RetentionClassWorkflow,
			Actor:  fmt.Sprintf("workflow-run:%s:output:%s", request.WorkflowRunID.String(), source.WorkflowPort),
			Reason: "durable workflow-run output",
		}}
	}
	return []RetentionSpec{{
		Class:  RetentionClassBinding,
		Actor:  fmt.Sprintf("build:%d:plan:%s:attempt:%s:output:%s", request.BuildID, request.PlanID, request.Attempt, source.Port.Name),
		Reason: "build output",
	}}
}

func validateSealedResult(result map[string]SealedOutput, captured []capturedSealOutput) error {
	if len(result) != len(captured) {
		return fmt.Errorf("snapshot: metadata store returned %d outputs, want %d", len(result), len(captured))
	}
	wanted := make(map[string]capturedSealOutput, len(captured))
	for _, output := range captured {
		wanted[output.source.ClientKey] = output
	}
	for key, output := range result {
		candidate, found := wanted[key]
		if !found {
			return fmt.Errorf("snapshot: metadata store returned unknown client key %q", key)
		}
		if err := output.Validate(); err != nil {
			return fmt.Errorf("snapshot: metadata store returned invalid output %q: %w", key, err)
		}
		if output.Port != candidate.source.Port || output.Snapshot.Type != candidate.source.Port.Type || output.Snapshot.Digest != candidate.candidate.Digest {
			return fmt.Errorf("snapshot: metadata store returned output %q that does not match its declaration", key)
		}
	}
	return nil
}

func cloneSealedOutputs(outputs map[string]SealedOutput) map[string]SealedOutput {
	cloned := make(map[string]SealedOutput, len(outputs))
	for key, output := range outputs {
		cloned[key] = output
	}
	return cloned
}

func wrapIfNonNil(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", strings.TrimSpace(prefix), err)
}

var _ OutputSealer = (*BatchSealer)(nil)
