package gittransport_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/directgit"
	"github.com/concourse/concourse/agent/publisher/gittransport"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
)

const verifiedTreeSHA = "dddddddddddddddddddddddddddddddddddddddd"

func TestVerifiedBranchWriterPublishesOnlyTheVerifiedNestedRepository(t *testing.T) {
	for _, test := range []struct {
		name           string
		provider       publisher.PRProvider
		remote         string
		authentication gittransport.AuthenticationMode
	}{
		{
			name: "GitHub", provider: publisher.PRProviderGitHub,
			remote:         "https://github.example/acme/widget.git",
			authentication: gittransport.AuthenticationAskpass,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerifiedWriterFixture(t)
			fixture.request.Locator.Provider = test.provider
			runner := newVerifiedWriterRunner(t, fixture.request)
			writer := fixture.newWriter(
				t, runner, test.remote, test.authentication,
			)

			result, err := writer.CompareAndSwapBranch(
				context.Background(),
				mutationFor(t, fixture.request),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Applied || result.HeadSHA != fixture.request.NewSourceSHA {
				t.Fatalf("CompareAndSwapBranch result = %+v", result)
			}
			if !runner.pushed || !runner.sawNestedRepository ||
				runner.sawOuterSnapshot {
				t.Fatalf(
					"writer execution pushed=%t nested=%t outer=%t",
					runner.pushed,
					runner.sawNestedRepository,
					runner.sawOuterSnapshot,
				)
			}
			for _, mode := range runner.remoteAuthentication {
				if mode != directgit.AuthenticationMode(test.authentication) {
					t.Fatalf("remote authentication mode = %q, want %q", mode, test.authentication)
				}
			}
			fixture.requireScratchEmpty(t)
		})
	}
}

func TestVerifiedBranchWriterRejectsUnverifiedCandidateAuthorityAndContent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*verifiedWriterFixture, *pullrequest.BranchMutation)
	}{
		{
			name: "tampered outer record",
			mutate: func(fixture *verifiedWriterFixture, _ *pullrequest.BranchMutation) {
				tampered := bytes.Replace(
					fixture.record,
					[]byte(fixture.document.ResultCommit),
					[]byte(objectID('e')),
					1,
				)
				fixture.archive = verifiedTarBytes(fixture.t, map[string][]byte{
					"content/result.tar": fixture.payload,
					"record.json":        tampered,
				})
			},
		},
		{
			name: "tampered nested payload digest",
			mutate: func(fixture *verifiedWriterFixture, _ *pullrequest.BranchMutation) {
				document := fixture.document
				document.Payload.Digest = fixedDigest('f')
				fixture.resealOuter(document)
			},
		},
		{
			name: "non git-tree representation",
			mutate: func(fixture *verifiedWriterFixture, _ *pullrequest.BranchMutation) {
				document := fixture.document
				document.Representation = "git-bundle"
				document.Payload.MediaType = "application/x-git-bundle"
				fixture.resealOuter(document)
			},
		},
		{
			name: "wrong base",
			mutate: func(fixture *verifiedWriterFixture, _ *pullrequest.BranchMutation) {
				document := fixture.document
				document.BaseSHA = objectID('e')
				fixture.resealOuter(document)
			},
		},
		{
			name: "wrong result commit",
			mutate: func(fixture *verifiedWriterFixture, _ *pullrequest.BranchMutation) {
				document := fixture.document
				document.ResultCommit = objectID('e')
				fixture.resealOuter(document)
			},
		},
		{
			name: "wrong team",
			mutate: func(fixture *verifiedWriterFixture, mutation *pullrequest.BranchMutation) {
				fixture.request.Authority.TeamID++
				*mutation = mutationFor(fixture.t, fixture.request)
			},
		},
		{
			name: "wrong snapshot",
			mutate: func(fixture *verifiedWriterFixture, mutation *pullrequest.BranchMutation) {
				fixture.request.Candidate.ID++
				*mutation = mutationFor(fixture.t, fixture.request)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerifiedWriterFixture(t)
			mutation := mutationFor(t, fixture.request)
			test.mutate(fixture, &mutation)
			runner := newVerifiedWriterRunner(t, fixture.request)
			writer := fixture.newWriter(
				t,
				runner,
				"https://github.example/acme/widget.git",
				gittransport.AuthenticationAskpass,
			)

			if _, err := writer.CompareAndSwapBranch(
				context.Background(),
				mutation,
			); err == nil {
				t.Fatal("unverified candidate was published")
			}
			if runner.pushed {
				t.Fatal("unverified candidate reached Git push")
			}
			fixture.requireScratchEmpty(t)
		})
	}
}

func TestVerifiedBranchWriterBindsTheExactPersistedAction(t *testing.T) {
	fixture := newVerifiedWriterFixture(t)
	runner := newVerifiedWriterRunner(t, fixture.request)
	writer := fixture.newWriter(
		t,
		runner,
		"https://github.example/acme/widget.git",
		gittransport.AuthenticationAskpass,
	)
	mutation := mutationFor(t, fixture.request)
	mutation.Ref = "refs/heads/agent/other"

	if _, err := writer.CompareAndSwapBranch(
		context.Background(),
		mutation,
	); err == nil {
		t.Fatal("writer accepted a mutation from another persisted action")
	}
	if runner.pushed {
		t.Fatal("cross-action mutation reached Git push")
	}
	fixture.requireScratchEmpty(t)
}

func TestVerifiedBranchWriterRequiresProviderSelectedAuthentication(t *testing.T) {
	for _, test := range []struct {
		name           string
		provider       publisher.PRProvider
		authentication gittransport.AuthenticationMode
	}{
		{
			name:     "GitHub does not infer default authentication",
			provider: publisher.PRProviderGitHub,
		},
		{
			name:     "GitHub does not accept an unknown mode",
			provider: publisher.PRProviderGitHub, authentication: gittransport.AuthenticationMode("bearer"),
		},
		{
			name:           "unknown provider is refused outright",
			provider:       publisher.PRProvider("unsupported-provider"),
			authentication: gittransport.AuthenticationAskpass,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerifiedWriterFixture(t)
			fixture.request.Locator.Provider = test.provider
			runner := newVerifiedWriterRunner(t, fixture.request)
			config := fixture.writerConfig(
				runner,
				"https://provider.example/acme/widget.git",
				test.authentication,
			)

			if _, err := gittransport.NewVerifiedBranchWriter(config); err == nil {
				t.Fatal("writer accepted the wrong provider authentication mode")
			}
			fixture.requireScratchEmpty(t)
		})
	}
}

func TestVerifiedBranchWriterPreservesStaleSentinelsAndCleansEveryMaterialization(t *testing.T) {
	for _, test := range []struct {
		name  string
		stale error
	}{
		{name: "source", stale: gittransport.ErrStaleSource},
		{name: "target", stale: gittransport.ErrStaleTarget},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerifiedWriterFixture(t)
			runner := newVerifiedWriterRunner(t, fixture.request)
			runner.stale = test.stale
			writer := fixture.newWriter(
				t,
				runner,
				"https://github.example/acme/widget.git",
				gittransport.AuthenticationAskpass,
			)

			_, err := writer.CompareAndSwapBranch(
				context.Background(),
				mutationFor(t, fixture.request),
			)
			if !errors.Is(err, test.stale) {
				t.Fatalf("CompareAndSwapBranch error = %v, want %v", err, test.stale)
			}
			if runner.pushed {
				t.Fatal("stale mutation reached Git push")
			}
			fixture.requireScratchEmpty(t)
		})
	}
}

type verifiedWriterFixture struct {
	t        *testing.T
	tempRoot string
	metadata *snapshotfakes.FakeMetadataStore
	content  *snapshotfakes.FakeContentStore
	manifest snapshot.Snapshot
	request  publisher.BranchPublicationRequest
	document contracts.RepositoryChangeBody
	payload  []byte
	record   []byte
	archive  []byte
}

func newVerifiedWriterFixture(t *testing.T) *verifiedWriterFixture {
	t.Helper()
	fixture := &verifiedWriterFixture{t: t, tempRoot: t.TempDir()}
	rawPayload := verifiedTarBytes(t, map[string][]byte{
		".git/config": []byte(
			"[core]\n\trepositoryformatversion = 0\n\tbare = false\n",
		),
		"README.md": []byte("verified nested repository\n"),
	})
	tree, err := (snapshot.Canonicalizer{
		TempDir: fixture.tempRoot,
	}).Capture(context.Background(), bytes.NewReader(rawPayload))
	if err != nil {
		t.Fatal(err)
	}
	fixture.payload, err = os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := tree.Digest
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.document = contracts.RepositoryChangeBody{
		RepositoryID:   fixedDigest('9').String(),
		BaseSHA:        objectID('b'),
		ResultCommit:   objectID('c'),
		ResultTree:     verifiedTreeSHA,
		Representation: "git-tree",
		Payload: contracts.ContentRef{
			Path: "content/result.tar", Digest: payloadDigest,
			MediaType: "application/x-tar",
		},
	}
	fixture.metadata = &snapshotfakes.FakeMetadataStore{}
	fixture.content = &snapshotfakes.FakeContentStore{}
	fixture.resealOuter(fixture.document)
	fixture.metadata.GetAuthorizedStub = func(
		_ context.Context,
		teamID int,
		snapshotID snapshot.SnapshotID,
	) (snapshot.Snapshot, bool, error) {
		if teamID != 17 || snapshotID != fixture.manifest.ID {
			return snapshot.Snapshot{}, false, nil
		}
		return fixture.manifest.Clone(), true, nil
	}
	fixture.content.OpenStub = func(
		context.Context,
		snapshot.Snapshot,
	) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(fixture.archive)), nil
	}
	fixture.request = verifiedBranchRequest(manifestRef(fixture.manifest))
	return fixture
}

func (fixture *verifiedWriterFixture) resealOuter(
	document contracts.RepositoryChangeBody,
) {
	fixture.t.Helper()
	record, err := contracts.NewRecord(
		snapshot.TypeRef("repository-change/v1"),
		[]contracts.Subject{{
			ID: "base", Role: contracts.SubjectRoleBase, Input: "repository",
			Type: "repository/v1", Digest: fixedDigest('8'),
		}},
		document,
	)
	if err != nil {
		fixture.t.Fatal(err)
	}
	fixture.record, err = json.Marshal(record)
	if err != nil {
		fixture.t.Fatal(err)
	}
	rawOuter := verifiedTarBytes(fixture.t, map[string][]byte{
		"content/result.tar": fixture.payload,
		"record.json":        fixture.record,
	})
	tree, err := (snapshot.Canonicalizer{
		TempDir: fixture.tempRoot,
	}).Capture(context.Background(), bytes.NewReader(rawOuter))
	if err != nil {
		fixture.t.Fatal(err)
	}
	fixture.archive, err = os.ReadFile(tree.ArchivePath)
	if err != nil {
		fixture.t.Fatal(err)
	}
	metadata, err := json.Marshal(contracts.RepositoryChangeMetadata{
		RepositoryID: document.RepositoryID,
		BaseSHA:      document.BaseSHA, ResultCommit: document.ResultCommit,
		ResultTree: document.ResultTree, Representation: document.Representation,
		ChangedFiles: []string{},
	})
	if err != nil {
		fixture.t.Fatal(err)
	}
	fixture.manifest = snapshot.Snapshot{
		ID: 22, Type: "repository-change/v1", Digest: tree.Digest,
		ByteSize: tree.ByteSize, FileCount: tree.FileCount,
		Representation:    "application/x-tar",
		IntrinsicMetadata: metadata,
		ContentState:      snapshot.ContentStateAvailable,
		CreatedAt:         time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
	}
	if fixture.request.Candidate.ID != 0 {
		fixture.request.Candidate = manifestRef(fixture.manifest)
	}
	fixture.document = document
	if err := tree.Close(); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *verifiedWriterFixture) newWriter(
	t *testing.T,
	runner directgit.Runner,
	remote string,
	authentication gittransport.AuthenticationMode,
) *gittransport.VerifiedBranchWriter {
	t.Helper()
	writer, err := gittransport.NewVerifiedBranchWriter(
		fixture.writerConfig(runner, remote, authentication),
	)
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

func (fixture *verifiedWriterFixture) writerConfig(
	runner directgit.Runner,
	remote string,
	authentication gittransport.AuthenticationMode,
) gittransport.VerifiedBranchWriterConfig {
	return gittransport.VerifiedBranchWriterConfig{
		Request:  fixture.request,
		Metadata: fixture.metadata,
		Content:  fixture.content,
		Canonicalizer: snapshot.Canonicalizer{
			TempDir: fixture.tempRoot,
		},
		Runner: runner, RemoteURL: remote,
		Token:          staticToken("provider-write-token"),
		Authentication: authentication,
		Timeout:        time.Second,
	}
}

func (fixture *verifiedWriterFixture) requireScratchEmpty(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(fixture.tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("verified writer scratch was not cleaned: %#v", entries)
	}
}

type verifiedWriterRunner struct {
	t                    *testing.T
	request              publisher.BranchPublicationRequest
	mu                   sync.Mutex
	pushed               bool
	stale                error
	sawNestedRepository  bool
	sawOuterSnapshot     bool
	remoteAuthentication []directgit.AuthenticationMode
}

func newVerifiedWriterRunner(
	t *testing.T,
	request publisher.BranchPublicationRequest,
) *verifiedWriterRunner {
	t.Helper()
	return &verifiedWriterRunner{t: t, request: request}
}

func (runner *verifiedWriterRunner) Run(
	_ context.Context,
	command directgit.Command,
) (directgit.CommandResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if command.Dir != "" {
		if _, err := os.Stat(filepath.Join(command.Dir, ".git", "config")); err == nil {
			runner.sawNestedRepository = true
		}
		if _, err := os.Stat(filepath.Join(command.Dir, "record.json")); err == nil {
			runner.sawOuterSnapshot = true
		}
	}
	if len(command.Credential) > 0 {
		runner.remoteAuthentication = append(
			runner.remoteAuthentication,
			command.Authentication,
		)
	}
	switch command.Args[0] {
	case "rev-parse":
		switch command.Args[len(command.Args)-1] {
		case "--is-inside-work-tree":
			return directgit.CommandResult{Stdout: "true\n"}, nil
		case "--show-object-format=storage":
			return directgit.CommandResult{Stdout: "sha1\n"}, nil
		case "HEAD^{commit}", runner.request.NewSourceSHA + "^{commit}":
			return directgit.CommandResult{
				Stdout: runner.request.NewSourceSHA + "\n",
			}, nil
		case runner.request.ExpectedTargetSHA + "^{commit}":
			return directgit.CommandResult{
				Stdout: runner.request.ExpectedTargetSHA + "\n",
			}, nil
		case runner.request.NewSourceSHA + "^{tree}":
			return directgit.CommandResult{Stdout: verifiedTreeSHA + "\n"}, nil
		default:
			runner.t.Fatalf("unexpected rev-parse command: %#v", command.Args)
		}
	case "merge-base", "fsck", "cat-file":
		return directgit.CommandResult{}, nil
	case "ls-remote":
		source := runner.request.ExpectedSource.SHA
		target := runner.request.ExpectedTargetSHA
		if errors.Is(runner.stale, gittransport.ErrStaleSource) {
			source = objectID('e')
		}
		if errors.Is(runner.stale, gittransport.ErrStaleTarget) {
			target = objectID('e')
		}
		if runner.pushed {
			source = runner.request.NewSourceSHA
		}
		return directgit.CommandResult{
			Stdout: source + "\t" + runner.request.SourceRef + "\n" +
				target + "\t" + runner.request.TargetRef + "\n",
		}, nil
	case "push":
		runner.pushed = true
		return directgit.CommandResult{}, nil
	default:
		runner.t.Fatalf("unexpected Git command: %#v", command)
	}
	return directgit.CommandResult{}, nil
}

func verifiedBranchRequest(
	candidate snapshot.SnapshotRef,
) publisher.BranchPublicationRequest {
	return publisher.BranchPublicationRequest{
		Authority: publisher.Authority{
			TeamID: 17, TeamName: "engineering", BuildID: 42,
			WorkflowRunID: 91, Actor: "alice",
		},
		Observation: exactSnapshotRef(21, "pull-request/v1", '1'),
		Candidate:   candidate,
		Validation:  exactSnapshotRef(23, "validation/v1", '3'),
		Impact:      exactSnapshotRef(24, "publish-impact/v1", '4'),
		Evidence: publisher.PublicationEvidence{
			Kind: publisher.EvidenceAcceptedReview,
			AcceptedReview: &publisher.AcceptedReviewEvidence{
				Review:              exactSnapshotRef(11, "review/v1", '5'),
				Candidate:           exactSnapshotRef(12, "repository/v1", '6'),
				Validation:          exactSnapshotRef(13, "validation/v1", '7'),
				ReviewWorkflowRunID: 81,
				OutcomeRevision:     3,
				AcceptedBy:          "alice",
				AcceptedAt: time.Date(
					2026, 7, 29, 12, 0, 0, 0, time.UTC,
				),
			},
		},
		Destination:           "github.example/acme/widget",
		ApprovalPolicyVersion: "engineering/v3",
		Locator: publisher.PRLocator{
			Provider:   publisher.PRProviderGitHub,
			Repository: "acme/widget",
		},
		SourceRef: "refs/heads/agent/upgrade",
		TargetRef: "refs/heads/main",
		ExpectedSource: contracts.PullRequestHeadExpectation{
			Exists: true, SHA: objectID('a'),
		},
		ExpectedTargetSHA: objectID('b'),
		NewSourceSHA:      objectID('c'),
	}
}

func mutationFor(
	t *testing.T,
	request publisher.BranchPublicationRequest,
) pullrequest.BranchMutation {
	t.Helper()
	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	return pullrequest.BranchMutation{
		Locator: pullrequest.Locator{
			Provider:   pullrequest.Provider(request.Locator.Provider),
			Repository: request.Locator.Repository,
			ExternalID: request.Locator.ExternalID,
		},
		Ref: request.SourceRef, TargetRef: request.TargetRef,
		ExpectedSource:    request.ExpectedSource,
		ExpectedTargetSHA: request.ExpectedTargetSHA,
		NewSourceSHA:      request.NewSourceSHA,
		OperationKey:      key,
	}
}

func exactSnapshotRef(
	id snapshot.SnapshotID,
	typeRef snapshot.TypeRef,
	fill byte,
) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: id, Type: typeRef, Digest: fixedDigest(fill),
	}
}

func manifestRef(manifest snapshot.Snapshot) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest,
	}
}

func fixedDigest(fill byte) snapshot.Digest {
	return snapshot.Digest(
		"sha256:" + strings.Repeat(string(fill), 64),
	)
}

func verifiedTarBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	directories := map[string]struct{}{}
	for name := range files {
		names = append(names, name)
		for directory := filepath.ToSlash(filepath.Dir(name)); directory != "."; directory = filepath.ToSlash(filepath.Dir(directory)) {
			directories[directory] = struct{}{}
		}
	}
	slices.Sort(names)
	directoryNames := make([]string, 0, len(directories))
	for name := range directories {
		directoryNames = append(directoryNames, name)
	}
	slices.Sort(directoryNames)

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range directoryNames {
		if err := writer.WriteHeader(&tar.Header{
			Name: name, Mode: 0700, Typeflag: tar.TypeDir,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names {
		body := files[name]
		if err := writer.WriteHeader(&tar.Header{
			Name: name, Mode: 0600, Typeflag: tar.TypeReg,
			Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
