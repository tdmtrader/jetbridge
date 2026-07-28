// Package repositorymerge implements the deterministic delivery merge used by
// version-3 workflows: it rebases one immutable repository-change/v1 candidate
// onto the current tip of a repository/v1 target and seals the result as a new
// repository-change/v1 value.
//
// The function is OFFLINE and HERMETIC by construction. It resolves no remote,
// reads no credential, and never pushes. Landing the merged change remains the
// job of publish_snapshot -> agent/publisher, which performs the only outbound
// effect through the external gateway. Everything here is a local three-way
// merge over already-materialized snapshot content.
//
// Errors are reserved for malformed invocation and cancellation. A content
// conflict, an invalid candidate, and an unavailable immutable input are all
// reported as a conclusion inside validation/v1.
package repositorymerge

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	repositoryChangeType snapshot.TypeRef = "repository-change/v1"
	repositoryType       snapshot.TypeRef = "repository/v1"
	validationType       snapshot.TypeRef = "validation/v1"

	// recordFileName is the sealed record envelope of a repository-change/v1 or
	// validation/v1 value. The envelope carries record_version, the contract
	// type, the frozen schema digest, and the subjects; the change or the check
	// result is its body. Both live at the root of their own output mount, so
	// one name serves both.
	recordFileName = "record.json"

	// contentDirectory is where a record's payloads live. ContentRef.Validate
	// rejects any content path outside it, so the merged payload is written
	// beneath it and payload.path names it.
	contentDirectory = "content"
	payloadFileName  = contentDirectory + "/payload.tar"

	// payloadMediaType is what the rest of the platform already declares for a
	// canonical repository tar payload (see the repository-change fixtures in
	// agent/snapshot/contracts and agent/projection).
	payloadMediaType = "application/octet-stream"

	// baseSubjectID names the single base subject a repository-change record
	// carries. For a merged change the base is the delivery target.
	baseSubjectID = "base"

	maximumDetailBytes = 4096

	// The preflight runs exactly ONE check — can this candidate be rebased onto
	// the delivery target — so its validation/v1 record carries one
	// ValidationCheck with one attempt. Conflicting paths are that attempt's
	// evidence rather than checks of their own: they are not independent
	// verdicts, they are where the single verdict came from, and a check id must
	// be an identifier, which a repository path is not.
	mergeCheckID   = "repository-merge"
	mergeCheckKind = "policy"
	mergeCheckName = "delivery merge"

	// The report's candidate is primary; its sealed base and delivery target are
	// rev3 base subjects in canonical lexical input order.
	baseReportSubjectID = "base"
	candidateSubjectID  = "candidate"
	targetSubjectID     = "target"
	mergeLogPath        = "content/logs/repository-merge-attempt-1.log"
	mergeLogMediaType   = "text/plain; charset=utf-8"

	// candidateRef holds the candidate's result commit after its objects have
	// been copied into the target repository. It lives outside refs/heads so it
	// can never be mistaken for a branch a human authored.
	candidateRef = "refs/concourse/candidate"
)

// Method is how the delivered change is integrated into the target.
type Method string

const (
	// MethodMerge creates a true merge commit (two parents).
	MethodMerge Method = "merge"
	// MethodSquash collapses the change into a single commit on the target.
	MethodSquash Method = "squash"
)

// scratchBranch is where the prospective merge is computed. Using a scratch
// branch keeps the caller's checked-out refs untouched.
const scratchBranch = "concourse-merge-scratch"

// Bot identity for platform-generated commits, so a platform merge commit is
// never counted as human touch. This is the single surviving v3 agent identity
// (see agent/repodiff).
const (
	BotName  = "concourse-agent[bot]"
	BotEmail = "agent@concourse.local"
)

// TrailerKey ties a merge commit back to the exact candidate result commit it
// integrated. It is derivable offline from the candidate's record.json, so it
// needs no renderer token and no execution identity.
const TrailerKey = "Agent-Change"

// Plan describes one prospective integration.
type Plan struct {
	Branch  string // delivered commit-ish, e.g. refs/concourse/candidate
	Target  string // target commit-ish, e.g. the resolved target HEAD
	Method  Method
	Message string // commit message for the merge/squash commit
}

// Result is what a prospective merge produced.
type Result struct {
	Ok            bool
	Conflict      bool
	ConflictPaths []string
	ResultSha     string // what the target would point at if landed
}

// Prepare computes the merge WITHOUT pushing. A conflict is a reported Result,
// not an error — errors are reserved for tooling faults. On conflict the merge
// is aborted so the working tree stays clean, which repository/v1 requires.
func Prepare(dir string, plan Plan) (Result, error) {
	if plan.Method != MethodMerge && plan.Method != MethodSquash {
		return Result{}, fmt.Errorf("unknown merge method %q", plan.Method)
	}

	// Compute on a scratch branch rooted at the target so the caller's
	// checked-out refs are never disturbed.
	if _, err := run(dir, "checkout", "-B", scratchBranch, plan.Target); err != nil {
		return Result{}, err
	}

	var mergeErr error
	switch plan.Method {
	case MethodMerge:
		_, mergeErr = run(dir, "merge", "--no-ff", "--no-commit", plan.Branch)
	case MethodSquash:
		_, mergeErr = run(dir, "merge", "--squash", plan.Branch)
	}

	if mergeErr != nil {
		paths := conflictPaths(dir)
		abort(dir, plan.Method)
		if len(paths) == 0 {
			// Not a content conflict — a real tooling fault.
			return Result{}, mergeErr
		}
		return Result{Conflict: true, ConflictPaths: paths}, nil
	}

	// A squash leaves changes staged; a --no-commit merge leaves them staged
	// with MERGE_HEAD set. Both need an explicit commit.
	if _, err := run(dir, "commit", "-m", plan.Message); err != nil {
		abort(dir, plan.Method)
		return Result{}, err
	}

	sha, err := run(dir, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	return Result{Ok: true, ResultSha: strings.TrimSpace(sha)}, nil
}

// conflictPaths lists unmerged files. Empty means the failure was not a
// content conflict.
func conflictPaths(dir string) []string {
	out, err := run(dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// abort restores a clean working tree. A squash merge sets no MERGE_HEAD, so
// `merge --abort` does not apply to it; a hard reset plus an untracked sweep
// covers both shapes. Cleanliness is load-bearing in version 3: repository/v1
// rejects a repository whose work tree or index is dirty, so an aborted merge
// that left residue would poison the merged snapshot.
func abort(dir string, method Method) {
	if method == MethodMerge {
		if _, err := run(dir, "merge", "--abort"); err == nil {
			return
		}
	}
	_, _ = run(dir, "reset", "--hard", "HEAD")
	_, _ = run(dir, "clean", "-fd")
}

// run executes git with a deterministic bot identity and without ambient
// configuration, hooks, or credential helpers, so results never depend on the
// environment the pod happens to have.
func run(dir string, args ...string) (string, error) {
	configured := append([]string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.autocrlf=false",
		"-c", "core.safecrlf=false",
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.excludesFile=" + os.DevNull,
		"-c", "credential.helper=",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "submodule.recurse=false",
		"-c", "filter.lfs.process=",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.clean=",
	}, args...)
	cmd := exec.Command("git", configured...)
	cmd.Dir = dir
	cmd.Env = append(hermeticGitEnvironment(),
		"GIT_AUTHOR_NAME="+BotName,
		"GIT_AUTHOR_EMAIL="+BotEmail,
		"GIT_COMMITTER_NAME="+BotName,
		"GIT_COMMITTER_EMAIL="+BotEmail,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

func hermeticGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+5)
	for _, variable := range os.Environ() {
		name := variable
		if separator := strings.IndexByte(variable, '='); separator >= 0 {
			name = variable[:separator]
		}
		if strings.HasPrefix(name, "GIT_") || name == "LC_ALL" {
			continue
		}
		environment = append(environment, variable)
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
	)
}

// trailerLine matches a conventional git trailer ("Key: value"), used to decide
// whether a new trailer joins an existing block or starts one.
var trailerLine = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*:\s`)

// appendTrailer joins an existing trailer block when the message already ends
// in one, and otherwise starts a new block after a blank line. Getting this
// wrong splits the block and breaks trailer parsers.
//
// The trailer is applied to the message BEFORE the commit exists. Stamping it
// afterwards with `commit --amend` would mint a new result_sha and invalidate a
// payload digest that has already been computed over the merged repository.
func appendTrailer(message, trailer string) string {
	message = strings.TrimRight(message, "\n")
	if message == "" {
		return trailer
	}
	lines := strings.Split(message, "\n")
	if trailerLine.MatchString(lines[len(lines)-1]) {
		return message + "\n" + trailer
	}
	return message + "\n\n" + trailer
}

// RecordAuthority is the contract identity the PLATFORM declares for one record
// output port, handed to the pod as AGENT_OUTPUT_<PORT>_RECORD_TYPE and
// AGENT_OUTPUT_<PORT>_RECORD_SCHEMA.
//
// It is copied into the record envelope verbatim. A producer does not get to
// choose which contract identity its own output advertises, so it must not
// derive one: the agent-runner image is released independently of the web node,
// and a pod that stamped its own compiled digest would advertise an identity the
// side that seals the output never declared.
type RecordAuthority struct {
	Type                  snapshot.TypeRef
	Schema                snapshot.Digest
	ProfileDigest         snapshot.Digest
	ProtectedConfigDigest snapshot.Digest
	CapabilityImage       string
	CapabilityImageDigest snapshot.Digest
	WorkflowDefinitionID  int
	WorkflowVersion       int
	Toolchain             string
}

// Request identifies the exact immutable candidate, the exact repository tip it
// must be rebased onto, and the input bindings needed to revalidate both.
// OpenInput is the only way the candidate's base lineage may be read.
type Request struct {
	// Candidate is the repository-change/v1 value being delivered.
	Candidate     snapshot.SnapshotRef
	CandidateRoot string
	// CandidateInput is the declared port name the candidate is bound at. It
	// becomes the input of the report's PRIMARY subject: the report is a
	// judgement about that exact snapshot, bound at that exact port.
	CandidateInput string
	Base           snapshot.SnapshotRef
	BaseInput      string
	// Target is the repository/v1 value the candidate is rebased onto. It is
	// the current tip of the delivery target.
	Target snapshot.SnapshotRef
	// TargetInput is the declared port name the target is bound at. It becomes
	// the input of the merged value's base SUBJECT, so the platform can resolve
	// the merged change's base lineage when it seals the output, and the input
	// of the report's context subject.
	TargetInput string
	TargetRoot  string
	// Inputs are the declared bindings visible to snapshot validation. They
	// must include the candidate's own base repository under the port name its
	// record.json names as its base subject's input, the candidate under
	// CandidateInput, and the target under TargetInput.
	Inputs    map[string]snapshot.SnapshotRef
	OpenInput snapshot.InputOpener
	Method    Method
	Message   string
	// ReportAuthority is the declared contract identity of the validation/v1
	// port the merge report is sealed at. The caller reads it out of the task
	// environment; this package never derives it.
	ReportAuthority RecordAuthority
}

type Runner struct {
	registry      snapshot.ValidatorRegistry
	canonicalizer snapshot.Canonicalizer
}

func NewRunner(registry snapshot.ValidatorRegistry) (*Runner, error) {
	if isNilInterface(registry) {
		return nil, fmt.Errorf("repository merge: validator registry is required")
	}
	return &Runner{registry: registry}, nil
}

// WithCanonicalizer selects the bounded, deployment-owned scratch policy used
// to materialize the candidate payload and to emit the merged payload.
func (runner *Runner) WithCanonicalizer(canonicalizer snapshot.Canonicalizer) *Runner {
	clone := *runner
	clone.canonicalizer = canonicalizer
	return &clone
}

// Merged is one completed merge attempt. Report is always populated and always
// valid. Change and PayloadPath are meaningful only when the report's derived
// conclusion is "passed". Close releases the materialized payload; call it once
// the value has been written out.
type Merged struct {
	Report      contracts.Record[contracts.ValidationBody]
	Change      contracts.Record[contracts.RepositoryChangeBody]
	PayloadPath string

	payload *snapshot.CapturedTree
}

// Conclusion is the derived verdict of the merge attempt: "passed", "failed", or
// "error". It is the single place callers should read the outcome from — the
// contract recomputes it from the checks and rejects a record that disagrees.
func (merged *Merged) Conclusion() string {
	if merged == nil {
		return ""
	}
	return merged.Report.Body.Conclusion
}

func (merged *Merged) Close() error {
	if merged == nil {
		return nil
	}
	if merged.payload == nil {
		return nil
	}
	return merged.payload.Close()
}

// Run is the preflight mode: it computes the prospective merge purely to report
// whether it is clean, and discards the merged value. It always returns a report
// for a well-formed invocation, so a conflicting merge can still be shown to a
// human before an approval is requested.
//
// Discarding the value is not the same as leaving the target untouched — the
// merge is computed in the target mount, which stays mutated. Preflight and
// prepare are separate steps with separate mounts, so that never leaks.
func (runner *Runner) Run(ctx context.Context, request Request) (contracts.Record[contracts.ValidationBody], error) {
	merged, err := runner.Merge(ctx, request)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	closeErr := merged.Close()
	if closeErr != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository merge: release merged payload: %w", closeErr)
	}
	return merged.Report, nil
}

// Merge revalidates the candidate against its own sealed base, copies its
// objects into the target repository, and computes the three-way merge onto the
// target tip. On success it materializes the merged repository as a canonical
// git-tree payload and the matching sealed repository-change/v1 record.
//
// The target working tree is MUTATED: the merge is computed in place, exactly
// as a pod-side step runner does with its own input mount.
func (runner *Runner) Merge(ctx context.Context, request Request) (*Merged, error) {
	if ctx == nil {
		return nil, fmt.Errorf("repository merge: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	// Every exit below reports the same single attempt, so the clock starts once,
	// here, and the duration the record carries is the whole evaluation rather
	// than whichever stage happened to reject it.
	attempt := newMergeAttempt(request)

	validationContext, err := snapshot.NewValidationContext(request.Inputs, request.OpenInput)
	if err != nil {
		return nil, fmt.Errorf("repository merge: inputs: %w", err)
	}
	changeValidator, err := runner.lookup(repositoryChangeType)
	if err != nil {
		return nil, err
	}
	repositoryValidator, err := runner.lookup(repositoryType)
	if err != nil {
		return nil, err
	}

	// 1. The incoming candidate must still satisfy its own contract against the
	//    exact base lineage it names. A candidate that no longer validates is a
	//    semantic rejection, never a merge attempt.
	if err := validateTree(ctx, changeValidator, request.CandidateRoot, validationContext); err != nil {
		return attempt.failed(fmt.Errorf("candidate is not a valid repository-change/v1: %w", err)), nil
	}
	// validateTree above ran the full contract validator, which is what checks
	// the record's subjects against the declared inputs. Reading the record now
	// only re-derives the body the merge needs.
	record, err := readChangeRecord(ctx, request.CandidateRoot)
	if err != nil {
		return attempt.failed(err), nil
	}
	document := record.Body

	// 2. The target must still be an exact repository/v1. Its intrinsic
	//    metadata is the authority for repository identity and base_sha; we
	//    deliberately do not recompute either.
	targetMetadata, err := repositoryMetadata(ctx, repositoryValidator, request.TargetRoot, validationContext)
	if err != nil {
		return attempt.errored(fmt.Errorf("target is not a usable repository/v1: %w", err)), nil
	}
	if document.RepositoryID != targetMetadata.RepositoryID {
		return attempt.failed(fmt.Errorf("candidate targets repository %s, which is not the delivery target", document.RepositoryID)), nil
	}
	if document.Representation != "git-tree" {
		// A patch carries no commit to merge and a bundle can only be unbundled
		// when its prerequisites are already present in the target. Both are
		// deliberately out of scope for the offline merge; the caller can
		// re-express such a candidate as git-tree.
		return attempt.errored(fmt.Errorf("representation %q cannot be merged offline; only git-tree candidates carry the commits a three-way merge needs", document.Representation)), nil
	}

	// 3. Copy the candidate's objects into the target repository. This is a
	//    local-path object transfer: no remote, no refspec a human authored,
	//    and no credential.
	candidate, err := runner.materializeCandidate(ctx, request.CandidateRoot, document)
	if err != nil {
		return attempt.errored(err), nil
	}
	transferErr := transferCandidate(request.TargetRoot, candidate.Root, document.ResultCommit)
	closeErr := candidate.Close()
	if err := errors.Join(transferErr, closeErr); err != nil {
		return attempt.errored(fmt.Errorf("copy candidate history into the target repository: %w", err)), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 4. The merge itself.
	message := appendTrailer(request.Message, TrailerKey+": "+document.ResultCommit)
	result, err := Prepare(request.TargetRoot, Plan{
		Branch: candidateRef, Target: targetMetadata.HeadSHA, Method: request.Method, Message: message,
	})
	if err != nil {
		return attempt.errored(fmt.Errorf("compute merge: %w", err)), nil
	}
	if result.Conflict {
		return attempt.conflicted(result.ConflictPaths), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 5. Seal the merged repository as a fresh repository-change/v1 value whose
	//    base is the target tip we merged onto.
	merged, err := runner.materializeMerged(ctx, attempt, targetMetadata, result.ResultSha)
	if err != nil {
		return attempt.errored(err), nil
	}
	return merged, nil
}

func (request Request) validate() error {
	if err := request.Candidate.Validate(); err != nil {
		return fmt.Errorf("repository merge: candidate: %w", err)
	}
	if request.Candidate.Type != repositoryChangeType {
		return fmt.Errorf(
			"repository merge: candidate must have exact type %s, got %s",
			repositoryChangeType, request.Candidate.Type,
		)
	}
	if err := request.Target.Validate(); err != nil {
		return fmt.Errorf("repository merge: target: %w", err)
	}
	if request.Target.Type != repositoryType {
		return fmt.Errorf(
			"repository merge: target must have exact type %s, got %s",
			repositoryType, request.Target.Type,
		)
	}
	if err := request.Base.Validate(); err != nil {
		return fmt.Errorf("repository merge: base: %w", err)
	}
	if request.Base.Type != repositoryType {
		return fmt.Errorf("repository merge: base must have exact type %s, got %s", repositoryType, request.Base.Type)
	}
	if strings.TrimSpace(request.CandidateRoot) == "" {
		return fmt.Errorf("repository merge: candidate root is required")
	}
	if strings.TrimSpace(request.TargetRoot) == "" {
		return fmt.Errorf("repository merge: target root is required")
	}
	if strings.TrimSpace(request.CandidateInput) == "" {
		return fmt.Errorf("repository merge: candidate input port is required")
	}
	if bound, found := request.Inputs[request.CandidateInput]; !found || bound != request.Candidate {
		return fmt.Errorf("repository merge: candidate input %q is not bound to the exact candidate snapshot", request.CandidateInput)
	}
	if strings.TrimSpace(request.BaseInput) == "" {
		return fmt.Errorf("repository merge: base input port is required")
	}
	if bound, found := request.Inputs[request.BaseInput]; !found || bound != request.Base {
		return fmt.Errorf("repository merge: base input %q is not bound to the exact base snapshot", request.BaseInput)
	}
	if strings.TrimSpace(request.TargetInput) == "" {
		return fmt.Errorf("repository merge: target input port is required")
	}
	if bound, found := request.Inputs[request.TargetInput]; !found || bound != request.Target {
		return fmt.Errorf("repository merge: target input %q is not bound to the exact target snapshot", request.TargetInput)
	}
	// The report identity is declared by the platform, never derived here, so a
	// caller that did not supply one has no way to produce a sealable report and
	// must be told now rather than after the merge has been computed.
	if request.ReportAuthority.Type != validationType {
		return fmt.Errorf(
			"repository merge: merge report type must be exactly %s, got %q",
			validationType, request.ReportAuthority.Type,
		)
	}
	if err := request.ReportAuthority.Schema.Validate(); err != nil {
		return fmt.Errorf("repository merge: merge report schema: %w", err)
	}
	for _, field := range []struct {
		name  string
		value snapshot.Digest
	}{
		{"merge report profile", request.ReportAuthority.ProfileDigest},
		{"merge report protected config", request.ReportAuthority.ProtectedConfigDigest},
		{"merge report image", request.ReportAuthority.CapabilityImageDigest},
	} {
		if err := field.value.Validate(); err != nil {
			return fmt.Errorf("repository merge: %s: %w", field.name, err)
		}
	}
	if strings.TrimSpace(request.ReportAuthority.CapabilityImage) == "" || request.ReportAuthority.WorkflowDefinitionID <= 0 || request.ReportAuthority.WorkflowVersion <= 0 || strings.TrimSpace(request.ReportAuthority.Toolchain) == "" {
		return fmt.Errorf("repository merge: merge report attestation authority is incomplete")
	}
	if request.Method != MethodMerge && request.Method != MethodSquash {
		return fmt.Errorf("repository merge: unknown merge method %q", request.Method)
	}
	if strings.TrimSpace(request.Message) == "" {
		return fmt.Errorf("repository merge: commit message is required")
	}
	return nil
}

func (runner *Runner) lookup(ref snapshot.TypeRef) (snapshot.Validator, error) {
	validator, err := runner.registry.Lookup(ref)
	if err != nil {
		return nil, fmt.Errorf("repository merge: resolve %s validator: %w", ref, err)
	}
	if isNilInterface(validator) {
		return nil, fmt.Errorf("repository merge: registry returned no %s validator", ref)
	}
	return validator, nil
}

func validateTree(ctx context.Context, validator snapshot.Validator, path string, validationContext snapshot.ValidationContext) error {
	_, err := validateTreeMetadata(ctx, validator, path, validationContext)
	return err
}

func validateTreeMetadata(
	ctx context.Context,
	validator snapshot.Validator,
	path string,
	validationContext snapshot.ValidationContext,
) (json.RawMessage, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Everything this function validates was sealed by an earlier step: the
	// candidate change and the delivery target are both stored snapshots. They go
	// through read-time revalidation, so a descriptor bump that happened after
	// they were sealed is a versioning event and not a merge failure.
	result, validationErr := validator.RevalidateSealed(ctx, root, validationContext)
	closeErr := root.Close()
	if err := errors.Join(validationErr, closeErr); err != nil {
		return nil, err
	}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("validator returned invalid metadata: %w", err)
	}
	return result.IntrinsicMetadata, nil
}

func repositoryMetadata(
	ctx context.Context,
	validator snapshot.Validator,
	path string,
	validationContext snapshot.ValidationContext,
) (contracts.RepositoryMetadata, error) {
	raw, err := validateTreeMetadata(ctx, validator, path, validationContext)
	if err != nil {
		return contracts.RepositoryMetadata{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var metadata contracts.RepositoryMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return contracts.RepositoryMetadata{}, fmt.Errorf("decode repository metadata: %w", err)
	}
	if metadata.RepositoryID == "" || metadata.HeadSHA == "" || metadata.ObjectFormat == "" {
		return contracts.RepositoryMetadata{}, fmt.Errorf("repository metadata is incomplete")
	}
	return metadata, nil
}

// readChangeRecord reads the candidate's sealed record.json at the READ-TIME
// gate: the candidate was sealed by an earlier step, so it may legitimately carry
// a superseded schema digest. The contract layer owns the bounded strict decode,
// the envelope shape, and the body invariants, so this package keeps no second
// copy of those rules. The subject binding against this step's own inputs is done
// by the validator's RevalidateSealed, which Merge runs first.
func readChangeRecord(ctx context.Context, changeRoot string) (contracts.Record[contracts.RepositoryChangeBody], error) {
	root, err := os.OpenRoot(changeRoot)
	if err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("open candidate root: %w", err)
	}
	record, readErr := contracts.ReadSealedRepositoryChangeRecord(ctx, root)
	closeErr := root.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("%s: %w", recordFileName, err)
	}
	return record, nil
}

// materializeCandidate extracts the candidate's git-tree payload into a private
// scratch repository. Its digest is re-derived from the exact emitted bytes, so
// a payload that does not match the sealed record cannot be merged.
func (runner *Runner) materializeCandidate(
	ctx context.Context,
	changeRoot string,
	document contracts.RepositoryChangeBody,
) (*snapshot.CapturedTree, error) {
	root, err := os.OpenRoot(changeRoot)
	if err != nil {
		return nil, fmt.Errorf("open candidate root: %w", err)
	}
	payload, err := root.Open(document.Payload.Path)
	closeRootErr := root.Close()
	if err := errors.Join(err, closeRootErr); err != nil {
		return nil, fmt.Errorf("open candidate payload: %w", err)
	}
	tree, captureErr := runner.canonicalizer.Capture(ctx, contextReader{ctx: ctx, reader: payload})
	closeErr := payload.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return nil, fmt.Errorf("candidate payload is not a canonical repository tar: %w", err)
	}
	if tree.Digest != document.Payload.Digest {
		_ = tree.Close()
		return nil, fmt.Errorf("candidate payload.digest does not match the exact payload bytes")
	}
	return tree, nil
}

// transferCandidate copies the candidate's history into the target repository
// over git's local transport. The source is a directory on this machine, so no
// network, protocol helper, or credential is involved.
func transferCandidate(targetRoot, candidateRoot, resultCommit string) error {
	if _, err := run(targetRoot, "fetch", "--no-tags", "--force", candidateRoot, "HEAD:"+candidateRef); err != nil {
		return err
	}
	fetched, err := run(targetRoot, "rev-parse", "--verify", candidateRef+"^{commit}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(fetched) != resultCommit {
		return fmt.Errorf("candidate payload HEAD %s does not equal result_commit %s", strings.TrimSpace(fetched), resultCommit)
	}
	return nil
}

func (runner *Runner) materializeMerged(
	ctx context.Context,
	attempt mergeAttempt,
	target contracts.RepositoryMetadata,
	resultCommit string,
) (*Merged, error) {
	request := attempt.request
	resultTree, err := run(request.TargetRoot, "rev-parse", "--verify", resultCommit+"^{tree}")
	if err != nil {
		return nil, fmt.Errorf("resolve merged tree: %w", err)
	}
	// A merge that grafted an unrelated history would change the repository's
	// identity, and the platform would reject the sealed value later. Catch it
	// here, where the diagnosis is still specific.
	roots, err := run(request.TargetRoot, "rev-list", "--max-parents=0", resultCommit)
	if err != nil {
		return nil, fmt.Errorf("resolve merged root commits: %w", err)
	}
	if !sameStringSet(strings.Fields(roots), target.RootCommits) {
		return nil, fmt.Errorf("merged history introduces root commits the delivery target does not have")
	}

	payload, err := runner.captureRepository(ctx, request.TargetRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize merged repository: %w", err)
	}
	body := contracts.RepositoryChangeBody{
		RepositoryID:   target.RepositoryID,
		BaseSHA:        target.HeadSHA,
		Representation: "git-tree",
		Payload: contracts.ContentRef{
			Path:      payloadFileName,
			Digest:    payload.Digest,
			MediaType: payloadMediaType,
		},
		ResultTree:   strings.TrimSpace(resultTree),
		ResultCommit: resultCommit,
	}
	// The merged change's base is the delivery target, bound at the declared
	// port the caller supplied. NewRecord seals the envelope: record_version,
	// the contract type, and the frozen schema digest for it.
	record, err := contracts.NewRecord(
		repositoryChangeType,
		[]contracts.Subject{contracts.SubjectFromInput(
			baseSubjectID, contracts.SubjectRoleBase, request.TargetInput, request.Target,
		)},
		body,
	)
	if err != nil {
		_ = payload.Close()
		return nil, fmt.Errorf("produced invalid repository-change/v1: %w", err)
	}
	if err := body.Validate(record.Subjects); err != nil {
		_ = payload.Close()
		return nil, fmt.Errorf("produced invalid repository-change/v1: %w", err)
	}
	return &Merged{
		Report:      attempt.report("passed", "merge is clean", "the candidate rebases onto the delivery target with no conflict", nil),
		Change:      record,
		PayloadPath: payload.ArchivePath,
		payload:     payload,
	}, nil
}

// captureRepository turns a repository directory into the canonical archive the
// snapshot contract expects. The emitted bytes are the payload; their digest is
// payload_digest.
func (runner *Runner) captureRepository(ctx context.Context, directory string) (*snapshot.CapturedTree, error) {
	return CaptureDirectory(ctx, runner.canonicalizer, directory)
}

// CaptureDirectory canonicalizes a materialized directory. The returned tree
// owns both the extracted copy and the canonical archive whose bytes are what a
// snapshot digest is taken over; close it when neither is needed.
//
// This is the pod-side counterpart of reading sealed snapshot content: a task
// only ever sees materialized directories, so a function that must reason about
// snapshot identity has to re-derive it from those bytes.
func CaptureDirectory(ctx context.Context, canonicalizer snapshot.Canonicalizer, directory string) (*snapshot.CapturedTree, error) {
	if ctx == nil {
		return nil, fmt.Errorf("repository merge: context is required")
	}
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("repository merge: directory is required")
	}
	reader, writer := io.Pipe()
	go func() {
		writer.CloseWithError(writeDirectoryTar(ctx, directory, writer))
	}()
	tree, err := canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if err := errors.Join(err, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return nil, err
	}
	return tree, nil
}

func writeDirectoryTar(ctx context.Context, directory string, sink io.Writer) error {
	paths := make([]string, 0, 256)
	if err := filepath.WalkDir(directory, func(name string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name != directory {
			paths = append(paths, name)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(paths)

	writer := tar.NewWriter(sink)
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		link := ""
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if link, err = os.Readlink(name); err != nil {
				return err
			}
		case info.IsDir(), info.Mode().IsRegular():
		default:
			return fmt.Errorf("repository contains unsupported entry %q", name)
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, name)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Uname, header.Gname = "", ""
		header.Uid, header.Gid = 0, 0
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, contextReader{ctx: ctx, reader: file})
		if err := errors.Join(copyErr, file.Close()); err != nil {
			return err
		}
	}
	return writer.Close()
}

// mergeAttempt is one merge evaluation in progress: the request being evaluated
// and when the evaluation started. Every exit path derives its validation/v1
// record from it, so the single check the preflight runs always carries the
// elapsed time of the whole evaluation rather than of whichever stage rejected
// it.
type mergeAttempt struct {
	request Request
	started time.Time
}

func newMergeAttempt(request Request) mergeAttempt {
	return mergeAttempt{request: request, started: time.Now()}
}

// failed records a semantic rejection: the candidate itself is not mergeable.
// An unavailable or expired immutable input is not the candidate's fault, so it
// is escalated to a tooling error instead.
func (attempt mergeAttempt) failed(reason error) *Merged {
	if isInfrastructureFailure(reason) {
		return attempt.errored(reason)
	}
	return &Merged{Report: attempt.report("failed", "candidate cannot be merged", boundedDetail(reason.Error()), nil)}
}

// errored records a tooling fault: the merge could not be decided either way.
func (attempt mergeAttempt) errored(reason error) *Merged {
	return &Merged{Report: attempt.report("error", "merge could not be evaluated", boundedDetail(reason.Error()), nil)}
}

// conflicted records the one outcome a human most needs to see. Each unmerged
// path becomes an EVIDENCE anchor on the attempt, anchored to the candidate:
// the paths are where the single verdict came from, not verdicts of their own.
func (attempt mergeAttempt) conflicted(paths []string) *Merged {
	evidence := make([]contracts.Anchor, 0, len(paths))
	for _, path := range paths {
		evidence = append(evidence, contracts.Anchor{
			Subject: candidateSubjectID,
			// An opaque locator is the honest kind here: the merge knows the path
			// is unmerged and knows nothing about which of its lines conflict, so
			// a line- or byte-range would be a fabricated claim.
			Locator: contracts.Locator{Kind: "opaque", Value: path},
		})
	}
	return &Merged{Report: attempt.report(
		"failed", "merge conflicts with the delivery target",
		boundedDetail("unmerged paths: "+strings.Join(paths, ", ")), evidence,
	)}
}

// report derives the guaranteed validation/v1 record for this attempt. The
// envelope's contract identity is copied from the platform-declared authority
// verbatim and the conclusion is derived from the checks, so neither is a claim
// this function gets to make up.
func (attempt mergeAttempt) report(status, summary, detail string, evidence []contracts.Anchor) contracts.Record[contracts.ValidationBody] {
	log := mergeAttemptLog(status, summary, detail)
	checks := []contracts.ValidationCheck{{
		ID:     mergeCheckID,
		Kind:   mergeCheckKind,
		Name:   mergeCheckName,
		Status: status,
		Detail: detail,
		Attempts: []contracts.ValidationAttempt{{
			Number:   1,
			Status:   status,
			Duration: time.Since(attempt.started).String(),
			Log:      validationLog(log),
			Evidence: evidence,
			Detail:   detail,
		}},
	}}
	return contracts.Record[contracts.ValidationBody]{
		RecordVersion: contracts.RecordVersion,
		Type:          attempt.request.ReportAuthority.Type,
		Schema:        attempt.request.ReportAuthority.Schema,
		Subjects:      attempt.request.reportSubjects(),
		Body: contracts.ValidationBody{
			Conclusion:  contracts.DeriveValidationConclusion(checks),
			Summary:     summary,
			Attestation: attempt.request.reportAttestation(),
			Checks:      checks,
		},
	}
}

// reportSubjects binds the report to the exact snapshots it judged, at the exact
// ports they were declared on. The platform re-checks both against its own view
// of the step when it seals the output.
func (request Request) reportSubjects() []contracts.Subject {
	return []contracts.Subject{
		contracts.SubjectFromInput(baseReportSubjectID, contracts.SubjectRoleBase, request.BaseInput, request.Base),
		contracts.SubjectFromInput(
			candidateSubjectID, contracts.SubjectRolePrimary, request.CandidateInput, request.Candidate,
		),
		contracts.SubjectFromInput(targetSubjectID, contracts.SubjectRoleBase, request.TargetInput, request.Target),
	}
}

func (request Request) reportAttestation() contracts.ValidationAttestation {
	return contracts.ValidationAttestation{
		CandidateDigest: request.Candidate.Digest,
		BaseInputs: []contracts.ValidationBaseInput{
			{Input: request.BaseInput, Type: request.Base.Type, Digest: request.Base.Digest},
			{Input: request.TargetInput, Type: request.Target.Type, Digest: request.Target.Digest},
		},
		ProfileDigest:         request.ReportAuthority.ProfileDigest,
		ProtectedConfigDigest: request.ReportAuthority.ProtectedConfigDigest,
		CapabilityImage:       request.ReportAuthority.CapabilityImage,
		CapabilityImageDigest: request.ReportAuthority.CapabilityImageDigest,
		WorkflowDefinitionID:  request.ReportAuthority.WorkflowDefinitionID,
		WorkflowVersion:       request.ReportAuthority.WorkflowVersion,
		Toolchain:             request.ReportAuthority.Toolchain,
	}
}

func mergeAttemptLog(status, summary, detail string) []byte {
	return []byte(fmt.Sprintf("repository merge: %s\nsummary: %s\ndetail: %s\n", status, summary, detail))
}

func validationLog(content []byte) contracts.ValidationLog {
	sum := sha256.Sum256(content)
	return contracts.ValidationLog{Path: mergeLogPath, Digest: snapshot.Digest("sha256:" + hex.EncodeToString(sum[:])), Size: int64(len(content)), MediaType: mergeLogMediaType}
}

func isInfrastructureFailure(err error) bool {
	return errors.Is(err, snapshot.ErrContentUnavailable) ||
		errors.Is(err, snapshot.ErrExpired) ||
		errors.Is(err, snapshot.ErrNotFound)
}

// WriteReport materializes the guaranteed merge report as a sealed validation/v1
// record beneath the caller-provided output mount.
func WriteReport(ctx context.Context, outputRoot string, report contracts.Record[contracts.ValidationBody]) error {
	if ctx == nil {
		return fmt.Errorf("repository merge: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputRoot) == "" {
		return fmt.Errorf("repository merge: output root is required")
	}
	if err := validateReportEnvelope(report); err != nil {
		return fmt.Errorf("repository merge: invalid %s: %w", validationType, err)
	}
	if err := report.Body.Validate(report.Subjects); err != nil {
		return fmt.Errorf("repository merge: invalid %s: %w", validationType, err)
	}
	if len(report.Body.Checks) != 1 || len(report.Body.Checks[0].Attempts) != 1 {
		return fmt.Errorf("repository merge: invalid %s: expected one merge attempt", validationType)
	}
	attempt := report.Body.Checks[0].Attempts[0]
	log := mergeAttemptLog(attempt.Status, report.Body.Summary, attempt.Detail)
	if validationLog(log) != attempt.Log {
		return fmt.Errorf("repository merge: invalid %s: attempt log does not match retained bytes", validationType)
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("repository merge: marshal %s: %w", validationType, err)
	}
	payload = append(payload, '\n')
	root, err := os.OpenRoot(outputRoot)
	if err != nil {
		return fmt.Errorf("repository merge: open output root: %w", err)
	}
	if err := root.MkdirAll(path.Dir(attempt.Log.Path), 0700); err != nil {
		_ = root.Close()
		return fmt.Errorf("repository merge: create report log directory: %w", err)
	}
	if err := root.WriteFile(attempt.Log.Path, log, 0600); err != nil {
		_ = root.Close()
		return fmt.Errorf("repository merge: write report log: %w", err)
	}
	written, err := root.ReadFile(attempt.Log.Path)
	if err != nil || validationLog(written) != attempt.Log {
		_ = root.Close()
		return fmt.Errorf("repository merge: report log changed while writing")
	}
	writeErr := root.WriteFile(recordFileName, payload, 0600)
	closeErr := root.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("repository merge: write %s: %w", recordFileName, err)
	}
	return nil
}

// validateReportEnvelope checks everything this function is the authority on,
// and deliberately stops short of the schema-DIGEST gate.
//
// The digest arrived from the web node as AGENT_OUTPUT_<PORT>_RECORD_SCHEMA and
// is copied through verbatim. The agent-runner image is released independently
// of the web, so a pod that re-judged the digest against its own compiled table
// would reject a perfectly good identity the moment the two builds differ — and
// it would do so by refusing to write the one durable artifact a conflicted
// delivery produces. contracts.Record.AdmitForSeal on the web is where the
// digest is judged, by the side that issued it.
func validateReportEnvelope(report contracts.Record[contracts.ValidationBody]) error {
	if report.RecordVersion != contracts.RecordVersion {
		return fmt.Errorf("record_version must be exactly %s", contracts.RecordVersion)
	}
	if report.Type != validationType {
		return fmt.Errorf("record type must be exactly %s, got %q", validationType, report.Type)
	}
	if err := report.Schema.Validate(); err != nil {
		return fmt.Errorf("record schema: %w", err)
	}
	ids := make([]string, len(report.Subjects))
	inputs := make(map[string]struct{}, len(report.Subjects))
	for index, subject := range report.Subjects {
		if err := subject.Validate(); err != nil {
			return fmt.Errorf("subjects[%d]: %w", index, err)
		}
		if _, found := inputs[subject.Input]; found {
			return fmt.Errorf("subjects[%d].input %q is duplicate", index, subject.Input)
		}
		inputs[subject.Input] = struct{}{}
		ids[index] = subject.ID
	}
	return contracts.ValidateEntityIDs("subjects", ids)
}

// WriteMergedChange materializes the merged repository-change/v1 value beneath
// the caller-provided output mount: the canonical payload first, beneath
// content/, then the sealed record that names it.
func WriteMergedChange(ctx context.Context, outputRoot string, merged *Merged) error {
	if ctx == nil {
		return fmt.Errorf("repository merge: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputRoot) == "" {
		return fmt.Errorf("repository merge: output root is required")
	}
	if merged == nil || merged.PayloadPath == "" {
		return fmt.Errorf("repository merge: no merged change was produced")
	}
	encoded, err := json.MarshalIndent(merged.Change, "", "  ")
	if err != nil {
		return fmt.Errorf("repository merge: marshal repository-change/v1: %w", err)
	}
	encoded = append(encoded, '\n')
	// A reader trusts the envelope for contract identity, so validate the exact
	// bytes about to be written rather than the in-memory value. These bytes are a
	// candidate this function is authoring, so they go through the SEAL-TIME gate:
	// DecodeRecordForSeal rejects anything whose record_version, type, subject set
	// or schema digest is not the sealed shape, and in particular requires the
	// CURRENT schema digest rather than any superseded revision. The body carries
	// the change invariants. Everything below is driven by what was decoded.
	var sealed contracts.Record[contracts.RepositoryChangeBody]
	if err := contracts.DecodeRecordForSeal(encoded, repositoryChangeType, &sealed); err != nil {
		return fmt.Errorf("repository merge: invalid repository-change/v1: %w", err)
	}
	if err := sealed.Body.Validate(sealed.Subjects); err != nil {
		return fmt.Errorf("repository merge: invalid repository-change/v1: %w", err)
	}

	source, err := os.Open(merged.PayloadPath)
	if err != nil {
		return fmt.Errorf("repository merge: open merged payload: %w", err)
	}
	defer source.Close()

	root, err := os.OpenRoot(outputRoot)
	if err != nil {
		return fmt.Errorf("repository merge: open output root: %w", err)
	}
	defer root.Close()
	payloadPath := sealed.Body.Payload.Path
	if directory := path.Dir(payloadPath); directory != "." {
		if err := root.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("repository merge: create %s: %w", directory, err)
		}
	}
	destination, err := root.Create(payloadPath)
	if err != nil {
		return fmt.Errorf("repository merge: create %s: %w", payloadPath, err)
	}
	_, copyErr := io.Copy(destination, contextReader{ctx: ctx, reader: source})
	if err := errors.Join(copyErr, destination.Close()); err != nil {
		return fmt.Errorf("repository merge: write %s: %w", payloadPath, err)
	}
	if err := root.WriteFile(recordFileName, encoded, 0600); err != nil {
		return fmt.Errorf("repository merge: write %s: %w", recordFileName, err)
	}
	return nil
}

func sameStringSet(left, right []string) bool {
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func boundedDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) <= maximumDetailBytes {
		return detail
	}
	return strings.TrimSpace(detail[:maximumDetailBytes-3]) + "..."
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func isNilInterface(value any) bool {
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
