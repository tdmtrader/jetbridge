package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type sealerValidatorFunc func(context.Context, *os.Root, ValidationContext) (ValidationResult, error)

func (f sealerValidatorFunc) Validate(ctx context.Context, root *os.Root, inputs ValidationContext) (ValidationResult, error) {
	return f(ctx, root, inputs)
}

type sealerRegistry map[TypeRef]Validator

func (r sealerRegistry) Lookup(ref TypeRef) (Validator, error) {
	validator, found := r[ref]
	if !found {
		return nil, fmt.Errorf("unknown type %s", ref)
	}
	return validator, nil
}

type sealerMetadataStore struct {
	MetadataStore
	events       []string
	stages       []StageUploadRequest
	stageErr     error
	stageErrAt   int
	stageHook    func()
	states       map[Digest]DigestState
	stateErr     error
	commit       *SealCommit
	commitResult map[string]SealedOutput
	commitErr    error
	authorized   map[SnapshotID]Snapshot
}

func (s *sealerMetadataStore) StageUpload(_ context.Context, _ DigestLease, request StageUploadRequest) (StagedUpload, error) {
	s.events = append(s.events, "stage:"+request.Digest.String())
	s.stages = append(s.stages, request)
	if s.stageHook != nil {
		s.stageHook()
	}
	if s.stageErr != nil && (s.stageErrAt == 0 || s.stageErrAt == len(s.stages)) {
		return StagedUpload{}, s.stageErr
	}
	return StagedUpload{
		ID: int64(len(s.stages)), Digest: request.Digest, TeamID: request.TeamID,
		Attempt: request.Attempt, LeaseExpiresAt: request.LeaseExpiresAt, CreatedAt: request.LeaseExpiresAt.Add(-time.Hour),
	}, nil
}

func (s *sealerMetadataStore) DigestState(_ context.Context, _ DigestLease, digest Digest, _ time.Time) (DigestState, error) {
	s.events = append(s.events, "state:"+digest.String())
	if s.stateErr != nil {
		return DigestState{}, s.stateErr
	}
	if state, found := s.states[digest]; found {
		return state.Clone(), nil
	}
	return DigestState{Digest: digest}, nil
}

func (s *sealerMetadataStore) CommitSealBatch(_ context.Context, _ DigestLease, commit SealCommit) (map[string]SealedOutput, error) {
	s.events = append(s.events, "commit")
	cloned := commit.Clone()
	s.commit = &cloned
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	result := s.commitResult
	if result == nil {
		result = make(map[string]SealedOutput, len(commit.Outputs))
		for i, output := range commit.Outputs {
			result[output.ClientKey] = SealedOutput{
				Port:     output.Port,
				Snapshot: SnapshotRef{ID: SnapshotID(i + 1), Type: output.Port.Type, Digest: output.Digest},
			}
		}
	}
	return result, nil
}

func (s *sealerMetadataStore) GetAuthorized(_ context.Context, _ int, id SnapshotID) (Snapshot, bool, error) {
	snapshot, found := s.authorized[id]
	return snapshot.Clone(), found, nil
}

type sealerContentStore struct {
	ContentStore
	events      *[]string
	putCalls    int
	putBodies   [][]byte
	putErr      error
	exists      map[Location]bool
	existsErr   error
	openContent map[SnapshotID][]byte
}

func (s *sealerContentStore) Put(_ context.Context, digest Digest, reader io.Reader) ([]Location, error) {
	*s.events = append(*s.events, "put:"+digest.String())
	s.putCalls++
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	s.putBodies = append(s.putBodies, body)
	if s.putErr != nil {
		return nil, s.putErr
	}
	return []Location{{Digest: digest, Driver: "test", Key: digest.String()}}, nil
}

func (s *sealerContentStore) Exists(_ context.Context, location Location) (bool, error) {
	*s.events = append(*s.events, "exists:"+location.Key)
	if s.existsErr != nil {
		return false, s.existsErr
	}
	return s.exists[location], nil
}

func (s *sealerContentStore) Open(_ context.Context, manifest Snapshot) (io.ReadCloser, error) {
	body, found := s.openContent[manifest.ID]
	if !found {
		return nil, fmt.Errorf("missing content")
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

type sealerLease struct {
	digests  map[Digest]bool
	closed   int
	closeErr error
}

func (l *sealerLease) Covers(digest Digest) bool { return l.digests[digest] }
func (l *sealerLease) Close() error {
	l.closed++
	return l.closeErr
}

type sealerLocks struct {
	lease      *sealerLease
	acquired   [][]Digest
	acquireErr error
}

type sealerReadCloser struct {
	io.Reader
	closeErr error
	closed   int
}

func (r *sealerReadCloser) Close() error {
	r.closed++
	return r.closeErr
}

func (l *sealerLocks) AcquireMany(_ context.Context, digests []Digest) (DigestLease, error) {
	l.acquired = append(l.acquired, append([]Digest(nil), digests...))
	if l.lease != nil {
		l.lease.digests = make(map[Digest]bool, len(digests))
		for _, digest := range digests {
			l.lease.digests[digest] = true
		}
	}
	return l.lease, l.acquireErr
}

func TestNewBatchSealerRejectsMissingDependenciesAndInvalidOptions(t *testing.T) {
	validator := sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	})
	registry := sealerRegistry{TypeRef("opaque/v1"): validator}
	metadata := &sealerMetadataStore{}
	events := []string{}
	content := &sealerContentStore{events: &events}
	locks := &sealerLocks{}
	canonicalizer := Canonicalizer{TempDir: t.TempDir()}

	tests := map[string]func() error{
		"nil registry": func() error { _, err := NewBatchSealer(canonicalizer, nil, metadata, content, locks); return err },
		"nil metadata": func() error { _, err := NewBatchSealer(canonicalizer, registry, nil, content, locks); return err },
		"nil content":  func() error { _, err := NewBatchSealer(canonicalizer, registry, metadata, nil, locks); return err },
		"nil locks":    func() error { _, err := NewBatchSealer(canonicalizer, registry, metadata, content, nil); return err },
		"zero TTL": func() error {
			_, err := NewBatchSealer(canonicalizer, registry, metadata, content, locks, WithBatchSealerStageTTL(0))
			return err
		},
		"excessive TTL": func() error {
			_, err := NewBatchSealer(canonicalizer, registry, metadata, content, locks, WithBatchSealerStageTTL(24*time.Hour+time.Nanosecond))
			return err
		},
		"nil clock": func() error {
			_, err := NewBatchSealer(canonicalizer, registry, metadata, content, locks, WithBatchSealerClock(nil))
			return err
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("NewBatchSealer accepted invalid construction")
			}
		})
	}
}

func TestBatchSealerValidatesCompleteBatchBeforeStorage(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	metadata := &sealerMetadataStore{}
	events := []string{}
	content := &sealerContentStore{events: &events}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	validatorCalls := 0
	registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(_ context.Context, _ *os.Root, _ ValidationContext) (ValidationResult, error) {
		validatorCalls++
		if validatorCalls == 2 {
			return ValidationResult{}, errors.New("invalid second output")
		}
		return ValidationResult{IntrinsicMetadata: json.RawMessage(`{"kind":"opaque"}`)}, nil
	})}
	tempDir := t.TempDir()
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: tempDir}, registry, metadata, content, locks, WithBatchSealerClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}

	request := sealerRequest([]OutputSource{
		sealerSource("first", "first", "opaque/v1", tarBytes(t, "first.txt", "first")),
		sealerSource("second", "second", "opaque/v1", tarBytes(t, "second.txt", "second")),
	})
	request.OutputDeclarations = []Port{
		{Name: "first", Type: TypeRef("opaque/v1")},
		{Name: "second", Type: TypeRef("opaque/v1")},
	}
	if _, err := sealer.Seal(context.Background(), request); err == nil || !strings.Contains(err.Error(), "invalid second output") {
		t.Fatalf("Seal() error = %v", err)
	}
	if len(locks.acquired) != 0 || len(metadata.stages) != 0 || content.putCalls != 0 || metadata.commit != nil {
		t.Fatalf("invalid batch mutated storage: locks=%v stages=%v puts=%d commit=%v", locks.acquired, metadata.stages, content.putCalls, metadata.commit)
	}
	assertDirectoryEmpty(t, tempDir)
}

func TestBatchSealerUnknownTypeCleansCaptureBeforeStorage(t *testing.T) {
	tempDir := t.TempDir()
	metadata := &sealerMetadataStore{}
	events := []string{}
	content := &sealerContentStore{events: &events}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: tempDir}, sealerRegistry{}, metadata, content, locks)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := sealer.Seal(context.Background(), sealerRequest([]OutputSource{
		sealerSource("result", "result", "unknown/v1", tarBytes(t, "value", "output")),
	})); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("Seal() error = %v, want unknown-type rejection", err)
	}
	if len(locks.acquired) != 0 || len(metadata.stages) != 0 || content.putCalls != 0 || metadata.commit != nil {
		t.Fatalf("unknown type mutated storage: locks=%v stages=%v puts=%d commit=%v", locks.acquired, metadata.stages, content.putCalls, metadata.commit)
	}
	assertDirectoryEmpty(t, tempDir)
}

func TestBatchSealerChecksOutputCompletenessBeforeCaptureOrStorage(t *testing.T) {
	tests := map[string]func(*SealRequest){
		"missing required": func(request *SealRequest) {
			request.Outputs = nil
		},
		"undeclared": func(request *SealRequest) {
			request.Outputs[0].Port.Name = "other"
		},
		"duplicate port": func(request *SealRequest) {
			duplicate := request.Outputs[0]
			duplicate.ClientKey = "other"
			request.Outputs = append(request.Outputs, duplicate)
		},
		"duplicate client key": func(request *SealRequest) {
			request.OutputDeclarations = append(request.OutputDeclarations, Port{Name: "other", Type: TypeRef("opaque/v1")})
			duplicate := request.Outputs[0]
			duplicate.Port.Name = "other"
			request.Outputs = append(request.Outputs, duplicate)
		},
		"fixture retention": func(request *SealRequest) {
			request.Outputs[0].Retention = RetentionClassFixture
		},
		"pin retention": func(request *SealRequest) {
			request.Outputs[0].Retention = RetentionClassPin
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			metadata := &sealerMetadataStore{}
			events := []string{}
			content := &sealerContentStore{events: &events}
			lease := &sealerLease{digests: map[Digest]bool{}}
			locks := &sealerLocks{lease: lease}
			validatorCalls := 0
			registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
				validatorCalls++
				return ValidationResult{}, nil
			})}
			sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks)
			if err != nil {
				t.Fatal(err)
			}
			opens := 0
			source := sealerSource("result", "result", "opaque/v1", tarBytes(t, "value", "output"))
			originalOpen := source.OpenTar
			source.OpenTar = func(ctx context.Context) (io.ReadCloser, error) {
				opens++
				return originalOpen(ctx)
			}
			request := sealerRequest([]OutputSource{source})
			mutate(&request)

			if _, err := sealer.Seal(context.Background(), request); err == nil {
				t.Fatal("Seal() accepted an invalid output set")
			}
			if opens != 0 || validatorCalls != 0 || len(locks.acquired) != 0 || len(metadata.stages) != 0 || content.putCalls != 0 || metadata.commit != nil {
				t.Fatalf("invalid output set performed work: opens=%d validators=%d locks=%v stages=%v puts=%d commit=%v", opens, validatorCalls, locks.acquired, metadata.stages, content.putCalls, metadata.commit)
			}
		})
	}
}

func TestBatchSealerHandlesAbsentAndPresentOptionalOutputs(t *testing.T) {
	newSealer := func(t *testing.T) (*BatchSealer, *sealerMetadataStore, *sealerContentStore, *sealerLocks) {
		t.Helper()
		metadata := &sealerMetadataStore{}
		events := &metadata.events
		content := &sealerContentStore{events: events, exists: map[Location]bool{}}
		locks := &sealerLocks{lease: &sealerLease{digests: map[Digest]bool{}}}
		registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
			return ValidationResult{}, nil
		})}
		sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks)
		if err != nil {
			t.Fatal(err)
		}
		return sealer, metadata, content, locks
	}

	t.Run("absent", func(t *testing.T) {
		sealer, metadata, content, locks := newSealer(t)
		request := sealerRequest(nil)
		request.OutputDeclarations = []Port{{Name: "result", Type: TypeRef("opaque/v1"), Optional: true}}
		result, err := sealer.Seal(context.Background(), request)
		if err != nil {
			t.Fatalf("Seal() error = %v", err)
		}
		if len(result) != 0 || len(locks.acquired) != 0 || len(metadata.stages) != 0 || content.putCalls != 0 || metadata.commit != nil {
			t.Fatalf("absent optional output performed storage work: result=%v locks=%v stages=%v puts=%d commit=%v", result, locks.acquired, metadata.stages, content.putCalls, metadata.commit)
		}
	})

	t.Run("present", func(t *testing.T) {
		sealer, metadata, _, _ := newSealer(t)
		source := sealerSource("result", "result", "opaque/v1", tarBytes(t, "value", "output"))
		source.Port.Optional = true
		result, err := sealer.Seal(context.Background(), sealerRequest([]OutputSource{source}))
		if err != nil {
			t.Fatalf("Seal() error = %v", err)
		}
		if len(result) != 1 || metadata.commit == nil || len(metadata.commit.Outputs) != 1 || !metadata.commit.Outputs[0].Port.Optional {
			t.Fatalf("present optional output was not committed: result=%v commit=%#v", result, metadata.commit)
		}
	})
}

func TestBatchSealerCleansCapturedTreeWhenSourceCloseFails(t *testing.T) {
	tempDir := t.TempDir()
	metadata := &sealerMetadataStore{}
	events := []string{}
	content := &sealerContentStore{events: &events}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	})}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: tempDir}, registry, metadata, content, locks)
	if err != nil {
		t.Fatal(err)
	}
	reader := &sealerReadCloser{Reader: bytes.NewReader(tarBytes(t, "value", "output")), closeErr: errors.New("close source")}
	source := sealerSource("result", "result", "opaque/v1", nil)
	source.OpenTar = func(context.Context) (io.ReadCloser, error) { return reader, nil }

	if _, err := sealer.Seal(context.Background(), sealerRequest([]OutputSource{source})); err == nil || !strings.Contains(err.Error(), "close source") {
		t.Fatalf("Seal() error = %v, want source-close error", err)
	}
	if reader.closed != 1 {
		t.Fatalf("source Close calls = %d, want 1", reader.closed)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("captured tree leaked after source close failure: %v", entries)
	}
	if len(locks.acquired) != 0 || len(metadata.stages) != 0 || content.putCalls != 0 || metadata.commit != nil {
		t.Fatalf("source-close failure mutated storage: locks=%v stages=%v puts=%d commit=%v", locks.acquired, metadata.stages, content.putCalls, metadata.commit)
	}
}

func TestBatchSealerRejectsDuplicateWorkflowPortsBeforeStorage(t *testing.T) {
	definitionID := 3
	runID := WorkflowRunID(4)
	metadata := &sealerMetadataStore{}
	events := []string{}
	content := &sealerContentStore{events: &events}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	registry := sealerRegistry{TypeRef("review/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	})}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks)
	if err != nil {
		t.Fatal(err)
	}
	first := sealerSource("first", "first", "review/v1", tarBytes(t, "first", "one"))
	first.Retention, first.WorkflowPort = RetentionClassWorkflow, "review"
	second := sealerSource("second", "second", "review/v1", tarBytes(t, "second", "two"))
	second.Retention, second.WorkflowPort = RetentionClassWorkflow, "review"
	request := sealerRequest([]OutputSource{first, second})
	request.WorkflowDefinitionID, request.WorkflowRunID = &definitionID, &runID

	if _, err := sealer.Seal(context.Background(), request); err == nil || !strings.Contains(err.Error(), "duplicate workflow port") {
		t.Fatalf("Seal() error = %v, want duplicate workflow-port rejection", err)
	}
	if len(locks.acquired) != 0 || len(metadata.stages) != 0 || content.putCalls != 0 || metadata.commit != nil {
		t.Fatalf("duplicate workflow port mutated storage: locks=%v stages=%v puts=%d commit=%v", locks.acquired, metadata.stages, content.putCalls, metadata.commit)
	}
}

func TestBatchSealerDeduplicatesContentAndCommitsDeclaredOrder(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	metadata := &sealerMetadataStore{}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	registry := sealerRegistry{
		TypeRef("opaque/v1"): sealerValidatorFunc(func(_ context.Context, root *os.Root, _ ValidationContext) (ValidationResult, error) {
			body, err := root.ReadFile("value.txt")
			if err != nil || string(body) != "same" {
				return ValidationResult{}, fmt.Errorf("unexpected tree: %q: %v", body, err)
			}
			return ValidationResult{IntrinsicMetadata: json.RawMessage(`{"kind":"opaque"}`)}, nil
		}),
	}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks,
		WithBatchSealerClock(func() time.Time { return now }), WithBatchSealerStageTTL(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	body := tarBytes(t, "value.txt", "same")
	request := sealerRequest([]OutputSource{
		sealerSource("mapped-second", "second", "opaque/v1", body),
		sealerSource("mapped-first", "first", "opaque/v1", body),
	})
	request.OutputDeclarations = []Port{
		{Name: "first", Type: TypeRef("opaque/v1")},
		{Name: "second", Type: TypeRef("opaque/v1")},
	}

	result, err := sealer.Seal(context.Background(), request)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if got := len(result); got != 2 {
		t.Fatalf("Seal() returned %d outputs, want 2", got)
	}
	if len(locks.acquired) != 1 || len(locks.acquired[0]) != 1 {
		t.Fatalf("AcquireMany calls = %#v, want one unique digest", locks.acquired)
	}
	if len(metadata.stages) != 1 || content.putCalls != 1 || lease.closed != 1 {
		t.Fatalf("dedupe calls: stages=%d puts=%d closes=%d", len(metadata.stages), content.putCalls, lease.closed)
	}
	if !metadata.stages[0].LeaseExpiresAt.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("stage expiry = %s", metadata.stages[0].LeaseExpiresAt)
	}
	if metadata.commit == nil {
		t.Fatal("CommitSealBatch was not called")
	}
	gotPorts := []string{metadata.commit.Outputs[0].Port.Name, metadata.commit.Outputs[1].Port.Name}
	if want := []string{"first", "second"}; !reflect.DeepEqual(gotPorts, want) {
		t.Fatalf("commit port order = %v, want %v", gotPorts, want)
	}
	if metadata.commit.Outputs[0].ClientKey != "mapped-first" || metadata.commit.Outputs[1].ClientKey != "mapped-second" {
		t.Fatalf("commit client keys did not follow declaration order: %#v", metadata.commit.Outputs)
	}
	for _, output := range metadata.commit.Outputs {
		wantActor := fmt.Sprintf("build:11:plan:plan-1:attempt:2:output:%s", output.Port.Name)
		if got := output.Retention; len(got) != 1 || got[0].Class != RetentionClassBinding || got[0].Actor != wantActor || got[0].Reason != "build output" {
			t.Fatalf("binding retention = %#v, want actor %q", got, wantActor)
		}
	}
	stageIndex, putIndex, commitIndex := eventIndex(*events, "stage:"), eventIndex(*events, "put:"), eventIndex(*events, "commit")
	if !(stageIndex >= 0 && putIndex > stageIndex && commitIndex > putIndex) {
		t.Fatalf("storage event order = %v", *events)
	}
}

func TestBatchSealerLocksDigestsLexicallyAndStagesAllBeforeUploading(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	metadata := &sealerMetadataStore{}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	})}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks, WithBatchSealerClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	firstBody := tarBytes(t, "value", "first")
	secondBody := tarBytes(t, "value", "second")
	firstDigest := canonicalDigest(t, firstBody)
	secondDigest := canonicalDigest(t, secondBody)
	request := sealerRequest([]OutputSource{
		sealerSource("first", "first", "opaque/v1", firstBody),
		sealerSource("second", "second", "opaque/v1", secondBody),
	})

	if _, err := sealer.Seal(context.Background(), request); err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	wantDigests := []Digest{firstDigest, secondDigest}
	sort.Slice(wantDigests, func(i, j int) bool { return wantDigests[i] < wantDigests[j] })
	if len(locks.acquired) != 1 || !reflect.DeepEqual(locks.acquired[0], wantDigests) {
		t.Fatalf("AcquireMany calls = %#v, want lexical %v", locks.acquired, wantDigests)
	}
	firstPut := eventIndex(*events, "put:")
	lastStage := lastEventIndex(*events, "stage:")
	lastPut := lastEventIndex(*events, "put:")
	commitIndex := eventIndex(*events, "commit")
	if !(lastStage >= 0 && firstPut > lastStage && commitIndex > lastPut) {
		t.Fatalf("storage event order = %v, want every stage before any upload and every upload before commit", *events)
	}
}

func TestBatchSealerReusesOnlyVerifiedAvailableLocations(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	metadata := &sealerMetadataStore{}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	})}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks, WithBatchSealerClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	request := sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", tarBytes(t, "value", "same"))})

	// Capture once to obtain the deterministic digest used by the request.
	tree, err := (Canonicalizer{TempDir: t.TempDir()}).Capture(context.Background(), bytes.NewReader(tarBytes(t, "value", "same")))
	if err != nil {
		t.Fatal(err)
	}
	digest := tree.Digest
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	verified := Location{Digest: digest, Driver: "test", Key: "a"}
	missing := Location{Digest: digest, Driver: "test", Key: "b"}
	content.exists[verified] = true
	content.exists[missing] = false
	metadata.states = map[Digest]DigestState{digest: {
		Digest: digest,
		Snapshots: []Snapshot{{
			ID: 1, Type: TypeRef("opaque/v1"), Digest: digest, ByteSize: int64(len(canonicalBody(t, tarBytes(t, "value", "same")))),
			FileCount: 1, Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: now,
		}},
		Locations: []Location{missing, verified},
	}}

	if _, err := sealer.Seal(context.Background(), request); err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if content.putCalls != 0 {
		t.Fatalf("Put called %d times for reusable digest", content.putCalls)
	}
	if got := metadata.commit.Outputs[0].Locations; !reflect.DeepEqual(got, []Location{verified}) {
		t.Fatalf("committed locations = %#v, want only verified %#v", got, verified)
	}
}

func TestBatchSealerReuploadsUnavailableOrUnverifiedDigests(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	body := tarBytes(t, "value", "same")
	digest := canonicalDigest(t, body)
	canonical := canonicalBody(t, body)
	location := Location{Digest: digest, Driver: "test", Key: "recorded"}

	tests := map[string]DigestState{
		"available location is missing": {
			Digest: digest,
			Snapshots: []Snapshot{{
				ID: 1, Type: TypeRef("opaque/v1"), Digest: digest, ByteSize: int64(len(canonical)), FileCount: 1,
				Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: now,
			}},
			Locations: []Location{location},
		},
		"expired manifest has a recorded location": {
			Digest: digest,
			Snapshots: []Snapshot{{
				ID: 1, Type: TypeRef("opaque/v1"), Digest: digest, ByteSize: int64(len(canonical)), FileCount: 1,
				Representation: "application/x-tar", ContentState: ContentStateExpired, CreatedAt: now,
			}},
			Locations: []Location{location},
		},
	}
	for name, state := range tests {
		t.Run(name, func(t *testing.T) {
			metadata := &sealerMetadataStore{states: map[Digest]DigestState{digest: state}}
			events := &metadata.events
			content := &sealerContentStore{events: events, exists: map[Location]bool{location: false}}
			lease := &sealerLease{digests: map[Digest]bool{}}
			locks := &sealerLocks{lease: lease}
			registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
				return ValidationResult{}, nil
			})}
			sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks, WithBatchSealerClock(func() time.Time { return now }))
			if err != nil {
				t.Fatal(err)
			}

			if _, err := sealer.Seal(context.Background(), sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", body)})); err != nil {
				t.Fatalf("Seal() error = %v", err)
			}
			if content.putCalls != 1 {
				t.Fatalf("Put calls = %d, want reupload", content.putCalls)
			}
			if len(metadata.commit.Outputs) != 1 || len(metadata.commit.Outputs[0].Locations) != 1 || metadata.commit.Outputs[0].Locations[0].Key == location.Key {
				t.Fatalf("commit locations = %#v, want newly uploaded location", metadata.commit.Outputs)
			}
		})
	}
}

func TestBatchSealerAuthorizesExactInputWhenValidatorOpensIt(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	inputDigest, err := ParseDigest("sha256:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	inputRef := SnapshotRef{ID: 41, Type: TypeRef("repository/v1"), Digest: inputDigest}
	metadata := &sealerMetadataStore{authorized: map[SnapshotID]Snapshot{41: {
		ID: 41, Type: inputRef.Type, Digest: inputRef.Digest, ByteSize: 5, FileCount: 1,
		Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: now,
	}}}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}, openContent: map[SnapshotID][]byte{41: []byte("input")}}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(ctx context.Context, _ *os.Root, inputs ValidationContext) (ValidationResult, error) {
		reader, err := inputs.OpenInput(ctx, "source")
		if err != nil {
			return ValidationResult{}, err
		}
		defer reader.Close()
		body, err := io.ReadAll(reader)
		if err != nil || string(body) != "input" {
			return ValidationResult{}, fmt.Errorf("read input: %q: %v", body, err)
		}
		return ValidationResult{}, nil
	})}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks, WithBatchSealerClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	request := sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", tarBytes(t, "value", "output"))})
	request.InputOrder = []string{"source"}
	request.Inputs = map[string]SnapshotRef{"source": inputRef}

	if _, err := sealer.Seal(context.Background(), request); err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if got := metadata.commit.Context.InputOrder; !reflect.DeepEqual(got, []string{"source"}) {
		t.Fatalf("commit input order = %v", got)
	}

	stale := request.Clone()
	stale.Inputs["source"] = SnapshotRef{ID: 41, Type: inputRef.Type, Digest: mustOtherDigest(t)}
	if _, err := sealer.Seal(context.Background(), stale); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Seal(stale input) error = %v", err)
	}

	missing := request.Clone()
	missing.Inputs["source"] = SnapshotRef{ID: 42, Type: inputRef.Type, Digest: inputRef.Digest}
	acquiresBefore := len(locks.acquired)
	if _, err := sealer.Seal(context.Background(), missing); err == nil || !strings.Contains(err.Error(), "absent, unavailable, or unauthorized") {
		t.Fatalf("Seal(missing input) error = %v", err)
	}
	if len(locks.acquired) != acquiresBefore {
		t.Fatalf("missing input acquired a digest lease: before=%d after=%d", acquiresBefore, len(locks.acquired))
	}
}

func TestBatchSealerWorkflowRetentionAndReturnedMapValidation(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	definitionID := 3
	runID := WorkflowRunID(4)
	metadata := &sealerMetadataStore{}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	})}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks, WithBatchSealerClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	source := sealerSource("mapped", "result", "opaque/v1", tarBytes(t, "value", "output"))
	source.Retention = RetentionClassWorkflow
	source.WorkflowPort = "public-result"
	request := sealerRequest([]OutputSource{source})
	request.WorkflowDefinitionID = &definitionID
	request.WorkflowRunID = &runID

	if _, err := sealer.Seal(context.Background(), request); err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	output := metadata.commit.Outputs[0]
	if output.WorkflowPort != "public-result" || len(output.Retention) != 1 ||
		output.Retention[0].Actor != "workflow-run:4:output:public-result" ||
		output.Retention[0].Reason != "durable workflow-run output" {
		t.Fatalf("workflow output policy = %#v", output)
	}

	metadata.commitResult = map[string]SealedOutput{"wrong": {
		Port: output.Port, Snapshot: SnapshotRef{ID: 99, Type: output.Port.Type, Digest: output.Digest},
	}}
	if _, err := sealer.Seal(context.Background(), request); err == nil || !strings.Contains(err.Error(), "returned") {
		t.Fatalf("Seal(mismatched result) error = %v", err)
	}
	if lease.closed != 2 {
		t.Fatalf("lease close count = %d, want 2", lease.closed)
	}
}

func TestBatchSealerReleasesLeaseAndCapturedTreeOnStorageErrors(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	body := tarBytes(t, "value", "output")
	digest := canonicalDigest(t, body)
	canonical := canonicalBody(t, body)
	location := Location{Digest: digest, Driver: "test", Key: "existing"}

	tests := map[string]func(*sealerMetadataStore, *sealerContentStore, error){
		"stage": func(metadata *sealerMetadataStore, _ *sealerContentStore, failure error) {
			metadata.stageErr = failure
		},
		"state": func(metadata *sealerMetadataStore, _ *sealerContentStore, failure error) {
			metadata.stateErr = failure
		},
		"exists": func(metadata *sealerMetadataStore, content *sealerContentStore, failure error) {
			metadata.states = map[Digest]DigestState{digest: availableDigestState(digest, int64(len(canonical)), now, location)}
			content.existsErr = failure
		},
		"put": func(_ *sealerMetadataStore, content *sealerContentStore, failure error) {
			content.putErr = failure
		},
		"commit": func(metadata *sealerMetadataStore, _ *sealerContentStore, failure error) {
			metadata.commitErr = failure
		},
	}
	for name, configure := range tests {
		t.Run(name, func(t *testing.T) {
			tempDir := t.TempDir()
			failure := errors.New(name + " failed")
			metadata := &sealerMetadataStore{}
			events := &metadata.events
			content := &sealerContentStore{events: events, exists: map[Location]bool{}}
			lease := &sealerLease{digests: map[Digest]bool{}}
			locks := &sealerLocks{lease: lease}
			configure(metadata, content, failure)
			sealer := mustNewSealer(t, tempDir, metadata, content, locks, sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
				return ValidationResult{}, nil
			}), WithBatchSealerClock(func() time.Time { return now }))

			if _, err := sealer.Seal(context.Background(), sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", body)})); !errors.Is(err, failure) {
				t.Fatalf("Seal() error = %v, want %v", err, failure)
			}
			if lease.closed != 1 {
				t.Fatalf("lease Close calls = %d, want 1", lease.closed)
			}
			assertDirectoryEmpty(t, tempDir)
			if name != "commit" && metadata.commit != nil {
				t.Fatalf("%s error reached commit: %#v", name, metadata.commit)
			}
		})
	}
}

func TestBatchSealerStagesEveryDigestBeforeUploadAndStopsOnLaterStageError(t *testing.T) {
	tempDir := t.TempDir()
	failure := errors.New("second stage failed")
	metadata := &sealerMetadataStore{stageErr: failure, stageErrAt: 2}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	sealer := mustNewSealer(t, tempDir, metadata, content, locks, sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	}))
	request := sealerRequest([]OutputSource{
		sealerSource("first", "first", "opaque/v1", tarBytes(t, "value", "first")),
		sealerSource("second", "second", "opaque/v1", tarBytes(t, "value", "second")),
	})

	if _, err := sealer.Seal(context.Background(), request); !errors.Is(err, failure) {
		t.Fatalf("Seal() error = %v, want %v", err, failure)
	}
	if len(metadata.stages) != 2 || content.putCalls != 0 || metadata.commit != nil || lease.closed != 1 {
		t.Fatalf("later stage failure: stages=%d puts=%d commit=%v closes=%d", len(metadata.stages), content.putCalls, metadata.commit, lease.closed)
	}
	assertDirectoryEmpty(t, tempDir)
}

func TestBatchSealerCancellationAfterStageReleasesLeaseAndCapture(t *testing.T) {
	tempDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	metadata := &sealerMetadataStore{stageHook: cancel}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	sealer := mustNewSealer(t, tempDir, metadata, content, locks, sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	}))

	if _, err := sealer.Seal(ctx, sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", tarBytes(t, "value", "output"))})); !errors.Is(err, context.Canceled) {
		t.Fatalf("Seal() error = %v, want context cancellation", err)
	}
	if len(metadata.stages) != 1 || content.putCalls != 0 || metadata.commit != nil || lease.closed != 1 {
		t.Fatalf("cancellation cleanup: stages=%d puts=%d commit=%v closes=%d", len(metadata.stages), content.putCalls, metadata.commit, lease.closed)
	}
	assertDirectoryEmpty(t, tempDir)
}

func TestBatchSealerJoinsAcquireOrWorkErrorsWithLeaseClose(t *testing.T) {
	tests := map[string]struct {
		acquireErr error
		putErr     error
	}{
		"partial acquire": {acquireErr: errors.New("partial acquire")},
		"work":            {putErr: errors.New("upload failed")},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			tempDir := t.TempDir()
			closeErr := errors.New("close lease")
			metadata := &sealerMetadataStore{}
			events := &metadata.events
			content := &sealerContentStore{events: events, exists: map[Location]bool{}, putErr: test.putErr}
			lease := &sealerLease{digests: map[Digest]bool{}, closeErr: closeErr}
			locks := &sealerLocks{lease: lease, acquireErr: test.acquireErr}
			sealer := mustNewSealer(t, tempDir, metadata, content, locks, sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
				return ValidationResult{}, nil
			}))

			_, err := sealer.Seal(context.Background(), sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", tarBytes(t, "value", "output"))}))
			workErr := test.acquireErr
			if workErr == nil {
				workErr = test.putErr
			}
			if !errors.Is(err, workErr) || !errors.Is(err, closeErr) {
				t.Fatalf("Seal() error = %v, want joined %v and %v", err, workErr, closeErr)
			}
			if lease.closed != 1 {
				t.Fatalf("lease Close calls = %d, want 1", lease.closed)
			}
			assertDirectoryEmpty(t, tempDir)
		})
	}
}

func TestBatchSealerRejectsPersistedDigestConflictBeforeUploadOrCommit(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	body := tarBytes(t, "value", "output")
	digest := canonicalDigest(t, body)
	tempDir := t.TempDir()
	metadata := &sealerMetadataStore{states: map[Digest]DigestState{digest: {
		Digest: digest,
		Snapshots: []Snapshot{{
			ID: 1, Type: TypeRef("opaque/v1"), Digest: digest, ByteSize: 1, FileCount: 1,
			Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: now,
		}},
	}}}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	sealer := mustNewSealer(t, tempDir, metadata, content, locks, sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	}), WithBatchSealerClock(func() time.Time { return now }))

	if _, err := sealer.Seal(context.Background(), sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", body)})); err == nil || !strings.Contains(err.Error(), "conflicts with persisted physical") {
		t.Fatalf("Seal() error = %v, want persisted metadata conflict", err)
	}
	if content.putCalls != 0 || metadata.commit != nil || lease.closed != 1 {
		t.Fatalf("digest conflict: puts=%d commit=%v closes=%d", content.putCalls, metadata.commit, lease.closed)
	}
	assertDirectoryEmpty(t, tempDir)
}

func TestBatchSealerReleasesLeaseWhenCanonicalArchiveCannotBeReopened(t *testing.T) {
	tempDir := t.TempDir()
	metadata := &sealerMetadataStore{}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}}
	lease := &sealerLease{digests: map[Digest]bool{}}
	locks := &sealerLocks{lease: lease}
	validator := sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		captures, err := os.ReadDir(tempDir)
		if err != nil || len(captures) != 1 {
			return ValidationResult{}, fmt.Errorf("find capture: %v (%d entries)", err, len(captures))
		}
		if err := os.Remove(filepath.Join(tempDir, captures[0].Name(), "canonical.tar")); err != nil {
			return ValidationResult{}, err
		}
		return ValidationResult{}, nil
	})
	sealer := mustNewSealer(t, tempDir, metadata, content, locks, validator)

	if _, err := sealer.Seal(context.Background(), sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", tarBytes(t, "value", "output"))})); err == nil || !strings.Contains(err.Error(), "open canonical archive") {
		t.Fatalf("Seal() error = %v, want archive-open error", err)
	}
	if len(metadata.stages) != 1 || content.putCalls != 0 || metadata.commit != nil || lease.closed != 1 {
		t.Fatalf("archive-open failure: stages=%d puts=%d commit=%v closes=%d", len(metadata.stages), content.putCalls, metadata.commit, lease.closed)
	}
	assertDirectoryEmpty(t, tempDir)
}

func sealerRequest(outputs []OutputSource) SealRequest {
	declarations := make([]Port, len(outputs))
	for i, output := range outputs {
		declarations[i] = output.Port
	}
	return SealRequest{
		BuildID: 11, TeamID: 12, TeamName: "main", CreatedBy: "concourse",
		PlanID: "plan-1", Attempt: "2", StepKind: "task", StepName: "produce",
		Inputs: map[string]SnapshotRef{}, InputOrder: []string{},
		OutputDeclarations: declarations, Outputs: outputs,
	}
}

func mustNewSealer(
	t *testing.T,
	tempDir string,
	metadata *sealerMetadataStore,
	content *sealerContentStore,
	locks *sealerLocks,
	validator Validator,
	opts ...BatchSealerOption,
) *BatchSealer {
	t.Helper()
	sealer, err := NewBatchSealer(
		Canonicalizer{TempDir: tempDir},
		sealerRegistry{TypeRef("opaque/v1"): validator},
		metadata,
		content,
		locks,
		opts...,
	)
	if err != nil {
		t.Fatal(err)
	}
	return sealer
}

func availableDigestState(digest Digest, byteSize int64, createdAt time.Time, locations ...Location) DigestState {
	return DigestState{
		Digest: digest,
		Snapshots: []Snapshot{{
			ID: 1, Type: TypeRef("opaque/v1"), Digest: digest, ByteSize: byteSize, FileCount: 1,
			Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: createdAt,
		}},
		Locations: append([]Location(nil), locations...),
	}
}

func sealerSource(clientKey, port, typeRef string, body []byte) OutputSource {
	return OutputSource{
		ClientKey: clientKey, Port: Port{Name: port, Type: TypeRef(typeRef)},
		OpenTar: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		},
	}
}

func tarBytes(t *testing.T, name, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := tar.NewWriter(&buffer)
	if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func canonicalBody(t *testing.T, raw []byte) []byte {
	t.Helper()
	tree, err := (Canonicalizer{TempDir: t.TempDir()}).Capture(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	body, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func canonicalDigest(t *testing.T, raw []byte) Digest {
	t.Helper()
	tree, err := (Canonicalizer{TempDir: t.TempDir()}).Capture(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	return tree.Digest
}

func mustOtherDigest(t *testing.T) Digest {
	t.Helper()
	digest, err := ParseDigest("sha256:" + strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func eventIndex(events []string, prefix string) int {
	for i, event := range events {
		if strings.HasPrefix(event, prefix) {
			return i
		}
	}
	return -1
}

func lastEventIndex(events []string, prefix string) int {
	for i := len(events) - 1; i >= 0; i-- {
		if strings.HasPrefix(events[i], prefix) {
			return i
		}
	}
	return -1
}
