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

	// DefaultBindingRetention is the post-production grace used by every
	// snapshot-producing boundary unless the operator configures another
	// duration.
	DefaultBindingRetention = 168 * time.Hour
)

type BatchSealer struct {
	canonicalizer    Canonicalizer
	validators       ValidatorRegistry
	metadata         MetadataStore
	content          ContentStore
	locks            DigestLockManager
	now              func() time.Time
	stageTTL         time.Duration
	bindingRetention time.Duration
}

type BatchSealerOption func(*BatchSealer)

func WithBatchSealerClock(now func() time.Time) BatchSealerOption {
	return func(sealer *BatchSealer) { sealer.now = now }
}

func WithBatchSealerStageTTL(ttl time.Duration) BatchSealerOption {
	return func(sealer *BatchSealer) { sealer.stageTTL = ttl }
}

func WithBatchSealerBindingRetention(retention time.Duration) BatchSealerOption {
	return func(sealer *BatchSealer) { sealer.bindingRetention = retention }
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
		canonicalizer:    canonicalizer,
		validators:       validators,
		metadata:         metadata,
		content:          content,
		locks:            locks,
		now:              time.Now,
		stageTTL:         defaultStageTTL,
		bindingRetention: DefaultBindingRetention,
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
	if sealer.bindingRetention <= 0 {
		return nil, fmt.Errorf("snapshot: binding retention must be greater than zero")
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
	retention []RetentionSpec
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
		captured[len(captured)-1].retention = retentionForOutput(request, source)
	}
	if err := validateCapturedBatch(captured); err != nil {
		return nil, err
	}
	commitContext, err := request.CommitContext()
	if err != nil {
		return nil, err
	}
	return sealer.commitCaptured(ctx, commitContext, captured)
}

// Upload captures and validates one raw tar before sharing the same durable
// stage, digest lease, content reuse/upload, and atomic metadata commit used by
// build outputs.
func (sealer *BatchSealer) Upload(ctx context.Context, request UploadRequest) (result Snapshot, err error) {
	if sealer == nil {
		return Snapshot{}, fmt.Errorf("snapshot: batch sealer is required")
	}
	if err := request.Validate(); err != nil {
		return Snapshot{}, err
	}
	validator, lookupErr := sealer.validators.Lookup(request.Type)
	if lookupErr != nil || interfaceIsNil(validator) {
		if lookupErr == nil {
			lookupErr = fmt.Errorf("validator returned nil")
		}
		return Snapshot{}, fmt.Errorf("%w: %v", ErrUnsupportedType, lookupErr)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	stream, err := request.OpenTar(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Snapshot{}, err
		}
		return Snapshot{}, errors.Join(ErrInvalidArchive, fmt.Errorf("snapshot: open upload tar stream: %w", err))
	}
	if stream == nil {
		return Snapshot{}, fmt.Errorf("%w: upload tar opener returned no reader", ErrInvalidArchive)
	}
	tree, captureErr := sealer.canonicalizer.Capture(ctx, stream)
	closeErr := stream.Close()
	if captureErr != nil || closeErr != nil {
		if tree != nil {
			err = errors.Join(err, tree.Close())
		}
		return Snapshot{}, errors.Join(classifyArchiveFailure(captureErr), wrapIfNonNil("snapshot: close upload tar stream", closeErr), err)
	}
	if tree == nil {
		return Snapshot{}, fmt.Errorf("%w: canonicalizer returned no captured tree", ErrInvalidArchive)
	}
	defer func() { err = errors.Join(err, tree.Close()) }()

	root, err := os.OpenRoot(tree.Root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: open captured upload root: %w", err)
	}
	validationContext, contextErr := NewValidationContext(nil, nil)
	if contextErr != nil {
		closeErr = root.Close()
		return Snapshot{}, errors.Join(contextErr, closeErr)
	}
	validation, validationErr := validator.Validate(ctx, root, validationContext)
	closeErr = root.Close()
	if validationErr != nil || closeErr != nil {
		return Snapshot{}, errors.Join(
			wrapCategory(ErrValidation, "snapshot: validate upload", validationErr),
			wrapIfNonNil("snapshot: close upload root", closeErr),
		)
	}
	if err := validation.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("%w: invalid validator result: %v", ErrValidation, err)
	}
	port := Port{Name: "snapshot", Type: request.Type}
	candidate := CandidateOutput{
		Port: port, ArchivePath: tree.ArchivePath, Digest: tree.Digest,
		ByteSize: tree.ByteSize, FileCount: tree.FileCount, Representation: "application/x-tar",
		IntrinsicMetadata: validation.IntrinsicMetadata,
	}.Clone()
	if err := candidate.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: captured upload: %w", err)
	}
	context := SealCommitContext{
		TeamID: request.TeamID, TeamName: request.TeamName, CreatedBy: request.UploadedBy,
		Upload: &UploadOccurrence{IdempotencyKey: request.IdempotencyKey},
		Inputs: map[string]SnapshotRef{}, InputOrder: []string{}, ExpectedOutputs: []Port{port},
	}
	captured := []capturedSealOutput{{
		source:    OutputSource{ClientKey: "upload", Port: port, SourceMetadata: cloneRaw(request.SourceMetadata)},
		candidate: candidate, tree: tree,
		retention: []RetentionSpec{{Class: RetentionClassPin, Actor: request.Actor, Reason: "manual upload"}},
	}}
	sealed, err := sealer.commitCaptured(ctx, context, captured)
	if err != nil {
		return Snapshot{}, err
	}
	output, found := sealed["upload"]
	if !found {
		return Snapshot{}, fmt.Errorf("snapshot: committed upload result is missing")
	}
	manifest, found, err := sealer.metadata.GetAuthorized(ctx, request.TeamID, output.Snapshot.ID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: read committed upload: %w", err)
	}
	if !found || manifest.ID != output.Snapshot.ID || manifest.Type != output.Snapshot.Type || manifest.Digest != output.Snapshot.Digest {
		return Snapshot{}, fmt.Errorf("snapshot: committed upload manifest is absent or inconsistent")
	}
	return manifest.Clone(), nil
}

func (sealer *BatchSealer) commitCaptured(
	ctx context.Context,
	commitContext SealCommitContext,
	captured []capturedSealOutput,
) (map[string]SealedOutput, error) {
	if err := commitContext.Validate(); err != nil {
		return nil, err
	}
	if err := validateCapturedBatch(captured); err != nil {
		return nil, err
	}
	digests := uniqueCapturedDigests(captured)
	// PostgreSQL timestamptz and pgx preserve microseconds. Canonicalize the
	// single seal timestamp before deriving any persisted expiry so the value
	// returned by StageUpload is exactly comparable to the request and every
	// expiry in the batch shares the same durable clock instant.
	now := sealer.now().Truncate(time.Microsecond)
	var sealed map[string]SealedOutput
	err := WithDigestLease(ctx, sealer.locks, digests, func(lease DigestLease) error {
		stages := make(map[Digest]StagedUpload, len(digests))
		for _, digest := range digests {
			if err := ctx.Err(); err != nil {
				return err
			}
			request := StageUploadRequest{
				Digest: digest, TeamID: commitContext.TeamID, Attempt: commitContext.StageAttempt(),
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

			verified, verificationErr := sealer.verifiedLocations(ctx, state)
			if state.Available() && len(verified) > 0 {
				locations[digest] = verified
				continue
			}
			representative := capturedForDigest(captured, digest)
			archive, err := os.Open(representative.candidate.ArchivePath)
			if err != nil {
				return errors.Join(
					verificationErr,
					fmt.Errorf("snapshot: open canonical archive for %s: %w", digest, err),
				)
			}
			uploaded, putErr := sealer.content.Put(ctx, digest, archive)
			closeErr := archive.Close()
			if putErr != nil || closeErr != nil {
				return errors.Join(
					verificationErr,
					wrapCategory(ErrContentUnavailable, fmt.Sprintf("snapshot: upload digest %s", digest), putErr),
					wrapIfNonNil(fmt.Sprintf("snapshot: close canonical archive for %s", digest), closeErr),
				)
			}
			uploaded, err = normalizeLocations(digest, uploaded)
			if err != nil {
				return err
			}
			locations[digest] = uploaded
		}

		commit := SealCommit{Context: commitContext, Outputs: make([]SealCommitOutput, 0, len(captured))}
		for _, output := range captured {
			retention := stampBindingRetention(output.retention, now, sealer.bindingRetention)
			committed, err := output.candidate.CommitOutput(
				output.source.ClientKey, output.source.WorkflowPort, stages[output.candidate.Digest],
				locations[output.candidate.Digest], retention, output.source.SourceMetadata,
			)
			if err != nil {
				return fmt.Errorf("snapshot: build commit output %q: %w", output.source.ClientKey, err)
			}
			commit.Outputs = append(commit.Outputs, committed)
		}
		var commitErr error
		sealed, commitErr = sealer.metadata.CommitSealBatch(ctx, lease, commit)
		if commitErr != nil {
			return fmt.Errorf("snapshot: commit seal batch: %w", commitErr)
		}
		return validateSealedResult(sealed, captured)
	})
	if err != nil {
		return nil, err
	}
	return cloneSealedOutputs(sealed), nil
}

func stampBindingRetention(specs []RetentionSpec, sealNow time.Time, duration time.Duration) []RetentionSpec {
	stamped := make([]RetentionSpec, len(specs))
	for i, spec := range specs {
		stamped[i] = spec.Clone()
		if spec.Class == RetentionClassBinding {
			expiresAt := sealNow.Add(duration)
			stamped[i].ExpiresAt = &expiresAt
		}
	}
	return stamped
}

func classifyArchiveFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrLimitExceeded) {
		return errors.Join(ErrInvalidArchive, err)
	}
	return errors.Join(ErrInvalidArchive, err)
}

func wrapCategory(category error, message string, err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(category, fmt.Errorf("%s: %w", message, err))
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
	var failures []error
	for _, location := range locations {
		exists, err := sealer.content.Exists(ctx, location)
		if err != nil {
			failures = append(failures, errors.Join(
				ErrContentUnavailable,
				fmt.Errorf("snapshot: verify location %s/%s: %w", location.Driver, location.Key, err),
			))
			continue
		}
		if exists {
			verified = append(verified, location)
		}
	}
	if len(verified) > 0 {
		// Reuse is existential. An unreachable replica does not invalidate a
		// cryptographically verified sibling; lifecycle repair handles the
		// degraded location set independently.
		return verified, nil
	}
	return nil, errors.Join(failures...)
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
		return nil, fmt.Errorf("%w: content store returned no locations for %s", ErrContentUnavailable, digest)
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
	retention := []RetentionSpec{{
		Class:  RetentionClassBinding,
		Actor:  fmt.Sprintf("build:%d:plan:%s:attempt:%s:output:%s", request.BuildID, request.PlanID, request.Attempt, source.Port.Name),
		Reason: "build output",
	}}
	if request.WorkflowRunID != nil {
		retention = append(retention, RetentionSpec{
			Class:         RetentionClassRun,
			WorkflowRunID: cloneWorkflowRunID(request.WorkflowRunID),
			Actor: fmt.Sprintf(
				"workflow-run:%s:build:%d:plan:%s:attempt:%s:output:%s",
				request.WorkflowRunID.String(), request.BuildID, request.PlanID, request.Attempt, source.Port.Name,
			),
			Reason: "active workflow-run internal output",
		})
	}
	return retention
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
