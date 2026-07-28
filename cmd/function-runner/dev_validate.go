package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/functions/devvalidate"
	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
)

type devValidateOptions struct {
	root, candidate, workspace, output, candidateType, candidateID, candidateDigest, profileName, profilePath, configPath, image, definitionID, version string
	bases, baseRefs                                                                                                                                     stringList
}
type stringList []string

func (v *stringList) String() string       { return strings.Join(*v, ",") }
func (v *stringList) Set(raw string) error { *v = append(*v, raw); return nil }

// newDevValidationRunner is fixed to the image-baked absolute CLI path in
// production. Tests may replace only the process-launch adapter in order to
// execute a freshly built real dev-capability binary on hosts that do not have
// the validator image mounted at /usr/local/bin; the compiled profile and its
// fixed absolute command remain unchanged.
var newDevValidationRunner = func() devValidationRunner { return devvalidate.NewRunner(nil) }

type devValidationRunner interface {
	Run(context.Context, devvalidate.Request) (contracts.Record[contracts.ValidationBody], error)
}

func runDevValidate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var o devValidateOptions
	f := flag.NewFlagSet("function-runner dev-validate", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&o.root, "root", ".", "mount root")
	f.StringVar(&o.candidate, "candidate", "", "candidate mount")
	f.StringVar(&o.workspace, "workspace", "", "fresh scratch mount")
	f.StringVar(&o.output, "output", "", "validation output mount")
	f.StringVar(&o.candidateType, "candidate-type", "", "server candidate type")
	f.StringVar(&o.candidateID, "candidate-id", "", "server candidate ID")
	f.StringVar(&o.candidateDigest, "candidate-digest", "", "server candidate digest")
	f.StringVar(&o.profileName, "profile-name", "", "server profile name")
	f.StringVar(&o.profilePath, "profile", "", "protected profile")
	f.StringVar(&o.configPath, "config", "", "protected config")
	f.StringVar(&o.image, "capability-image", "", "pinned image")
	f.StringVar(&o.definitionID, "workflow-definition-id", "", "workflow definition")
	f.StringVar(&o.version, "workflow-version", "", "workflow version")
	f.Var(&o.bases, "base", "declared base mount")
	f.Var(&o.baseRefs, "base-ref", "bound base snapshot reference")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return exitUsage
	}
	if err := executeDevValidate(ctx, o); err != nil {
		fmt.Fprintf(stderr, "function-runner: dev-validate: %v\n", err)
		return exitUsage
	}
	fmt.Fprintln(stdout, "authoritative development validation complete")
	return exitOK
}

func executeDevValidate(ctx context.Context, o devValidateOptions) error {
	return executeDevValidateWithRunner(ctx, o, newDevValidationRunner())
}

func executeDevValidateWithRunner(ctx context.Context, o devValidateOptions, runner devValidationRunner) error {
	if runner == nil {
		return fmt.Errorf("validation runner is required")
	}
	for name, value := range map[string]string{"candidate": o.candidate, "workspace": o.workspace, "output": o.output, "candidate-type": o.candidateType, "candidate-id": o.candidateID, "candidate-digest": o.candidateDigest, "profile-name": o.profileName, "profile": o.profilePath, "config": o.configPath, "capability-image": o.image, "workflow-definition-id": o.definitionID, "workflow-version": o.version} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("-%s is required", name)
		}
	}
	candidateName, source, err := parseMount(o.root, o.candidate)
	if err != nil {
		return err
	}
	_, workspace, err := parseMount(o.root, o.workspace)
	if err != nil {
		return err
	}
	_, output, err := parseMount(o.root, o.output)
	if err != nil {
		return err
	}
	typeRef, err := snapshot.ParseTypeRef(o.candidateType)
	if err != nil {
		return err
	}
	id, err := snapshot.ParseSnapshotID(o.candidateID)
	if err != nil {
		return err
	}
	digest, err := snapshot.ParseDigest(o.candidateDigest)
	if err != nil {
		return err
	}
	boundBases, err := boundBaseRefs(o, o.root)
	if err != nil {
		return err
	}
	canonicalizer := snapshot.Canonicalizer{}
	candidateTree, err := repositorymerge.CaptureDirectory(ctx, canonicalizer, source)
	if err != nil {
		return fmt.Errorf("capture exact candidate: %w", err)
	}
	defer candidateTree.Close()
	if candidateTree.Digest != digest {
		return fmt.Errorf("mounted candidate does not match server digest")
	}
	changedPaths := []string(nil)
	if typeRef == snapshot.TypeRef("repository-change/v1") {
		baseName, basePath, baseRef, err := exactRepositoryBase(o, o.root)
		if err != nil {
			return err
		}
		materialized, err := repositorymerge.MaterializeForValidation(ctx, snapshot.Canonicalizer{}, source, snapshot.SnapshotRef{ID: id, Type: typeRef, Digest: digest}, baseName, basePath, baseRef, workspace)
		changedPaths = materialized.ChangedPaths
		if err != nil {
			return err
		}
	} else if err := copyCandidate(ctx, source, workspace); err != nil {
		return err
	}
	if typeRef != snapshot.TypeRef("repository-change/v1") {
		workspaceTree, err := repositorymerge.CaptureDirectory(ctx, canonicalizer, workspace)
		if err != nil {
			return fmt.Errorf("capture copied candidate: %w", err)
		}
		defer workspaceTree.Close()
		if workspaceTree.Digest != digest {
			return fmt.Errorf("fresh scratch is not the exact candidate")
		}
	}
	definition, err := positive(o.definitionID)
	if err != nil {
		return err
	}
	version, err := positive(o.version)
	if err != nil {
		return err
	}
	profileBytes, err := protected(o.profilePath, source, workspace, output)
	if err != nil {
		return err
	}
	configBytes, err := protected(o.configPath, source, workspace, output)
	if err != nil {
		return err
	}
	imageDigest, err := pinnedImageDigest(o.image)
	if err != nil {
		return err
	}
	baseContracts := make([]workflow.DevValidationContract, 0, len(o.bases))
	requestBases := make([]snapshot.ValidationAuthorityInput, 0, len(o.bases))
	for _, name := range o.bases {
		ref, found := boundBases[name]
		if !found {
			return fmt.Errorf("declared base %q is not bound", name)
		}
		baseContracts = append(baseContracts, workflow.DevValidationContract{Name: name, Type: ref.Type})
		requestBases = append(requestBases, snapshot.ValidationAuthorityInput{Input: name, Ref: ref})
	}
	profile := workflow.CompiledDevValidationProfile{Name: o.profileName, Candidate: workflow.DevValidationContract{Name: candidateName, Type: typeRef}, BaseInputs: baseContracts, CapabilityImage: o.image, CapabilityImageDigest: imageDigest, Command: []string{workflow.DevValidationCLIPath, workflow.DevValidationCLIValidateCommand}, Profile: profileBytes, ProfileDigest: contentDigest(profileBytes), ProtectedConfig: configBytes, ProtectedConfigDigest: contentDigest(configBytes)}
	_, err = runner.Run(ctx, devvalidate.Request{Candidate: snapshot.SnapshotRef{ID: id, Type: typeRef, Digest: digest}, Bases: requestBases, CandidateInput: candidateName, WorkspaceRoot: workspace, OutputRoot: output, Profile: profile, WorkflowDefinitionID: definition, WorkflowVersion: version, ChangedPaths: changedPaths})
	return err
}

func exactRepositoryBase(o devValidateOptions, root string) (string, string, snapshot.SnapshotRef, error) {
	if len(o.bases) == 0 || len(o.baseRefs) != len(o.bases) {
		return "", "", snapshot.SnapshotRef{}, fmt.Errorf("repository-change validation requires exactly bound declared bases")
	}
	var foundName, foundPath string
	var foundRef snapshot.SnapshotRef
	for _, raw := range o.baseRefs {
		parts := strings.Split(raw, "=")
		if len(parts) != 2 {
			return "", "", snapshot.SnapshotRef{}, fmt.Errorf("invalid base reference")
		}
		fields := strings.Split(parts[1], ",")
		if len(fields) != 3 {
			return "", "", snapshot.SnapshotRef{}, fmt.Errorf("invalid base reference")
		}
		id, err := snapshot.ParseSnapshotID(fields[0])
		if err != nil {
			return "", "", snapshot.SnapshotRef{}, err
		}
		typ, err := snapshot.ParseTypeRef(fields[1])
		if err != nil {
			return "", "", snapshot.SnapshotRef{}, err
		}
		digest, err := snapshot.ParseDigest(fields[2])
		if err != nil {
			return "", "", snapshot.SnapshotRef{}, err
		}
		if typ != snapshot.TypeRef("repository/v1") {
			continue
		}
		if foundName != "" {
			return "", "", snapshot.SnapshotRef{}, fmt.Errorf("repository-change validation has ambiguous repository/v1 bases")
		}
		_, path, err := parseMount(root, parts[0])
		if err != nil {
			return "", "", snapshot.SnapshotRef{}, err
		}
		foundName, foundPath, foundRef = parts[0], path, snapshot.SnapshotRef{ID: id, Type: typ, Digest: digest}
	}
	if foundName == "" {
		return "", "", snapshot.SnapshotRef{}, fmt.Errorf("repository-change validation lacks repository/v1 base")
	}
	return foundName, foundPath, foundRef, nil
}

func boundBaseRefs(o devValidateOptions, root string) (map[string]snapshot.SnapshotRef, error) {
	if len(o.bases) != len(o.baseRefs) {
		return nil, fmt.Errorf("declared bases and bound base references differ")
	}
	bound := make(map[string]snapshot.SnapshotRef, len(o.bases))
	for _, raw := range o.baseRefs {
		parts := strings.Split(raw, "=")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid base reference")
		}
		if _, _, err := parseMount(root, parts[0]); err != nil {
			return nil, err
		}
		fields := strings.Split(parts[1], ",")
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid base reference")
		}
		id, err := snapshot.ParseSnapshotID(fields[0])
		if err != nil {
			return nil, err
		}
		typ, err := snapshot.ParseTypeRef(fields[1])
		if err != nil {
			return nil, err
		}
		digest, err := snapshot.ParseDigest(fields[2])
		if err != nil {
			return nil, err
		}
		if _, duplicate := bound[parts[0]]; duplicate {
			return nil, fmt.Errorf("duplicate base reference %q", parts[0])
		}
		bound[parts[0]] = snapshot.SnapshotRef{ID: id, Type: typ, Digest: digest}
	}
	for _, name := range o.bases {
		if _, found := bound[name]; !found {
			return nil, fmt.Errorf("declared base %q is not bound", name)
		}
	}
	return bound, nil
}

func copyCandidate(ctx context.Context, source, dest string) error {
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 0 {
		return fmt.Errorf("workspace must start empty")
	}
	return filepath.WalkDir(source, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.Type()&os.ModeSymlink != 0 {
			targetValue, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), targetValue))
			inside, err := filepath.Rel(source, resolved)
			if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return fmt.Errorf("candidate symlink %q escapes candidate root", rel)
			}
			if err := os.Symlink(targetValue, target); err != nil {
				return err
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("candidate entry %q is not regular", rel)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
func protected(path string, mounts ...string) ([]byte, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	for _, mount := range mounts {
		rel, _ := filepath.Rel(mount, absolute)
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return nil, fmt.Errorf("protected file is beneath task mount")
		}
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("protected file must be regular non-symlink")
	}
	return os.ReadFile(absolute)
}
func contentDigest(raw []byte) snapshot.Digest {
	sum := sha256.Sum256(raw)
	return snapshot.Digest(fmt.Sprintf("sha256:%x", sum[:]))
}
func positive(raw string) (int, error) {
	var value int
	if _, err := fmt.Sscan(raw, &value); err != nil || value <= 0 {
		return 0, fmt.Errorf("positive integer required")
	}
	return value, nil
}
func pinnedImageDigest(image string) (snapshot.Digest, error) {
	at := strings.LastIndexByte(image, '@')
	if at < 1 {
		return "", fmt.Errorf("pinned image required")
	}
	return snapshot.ParseDigest(image[at+1:])
}
