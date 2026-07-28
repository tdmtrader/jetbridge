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
	"github.com/concourse/concourse/agent/workflow"
)

type devValidateOptions struct{ root, candidate, workspace, output, candidateType, candidateID, candidateDigest, profileName, profilePath, configPath, image, definitionID, version string }

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
	for name, value := range map[string]string{"candidate": o.candidate, "workspace": o.workspace, "output": o.output, "candidate-type": o.candidateType, "candidate-id": o.candidateID, "candidate-digest": o.candidateDigest, "profile-name": o.profileName, "profile": o.profilePath, "config": o.configPath, "capability-image": o.image, "workflow-definition-id": o.definitionID, "workflow-version": o.version} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("-%s is required", name)
		}
	}
	_, source, err := parseMount(o.root, o.candidate)
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
	canonicalizer := snapshot.Canonicalizer{}
	candidateTree, err := repositorymerge.CaptureDirectory(ctx, canonicalizer, source)
	if err != nil {
		return fmt.Errorf("capture exact candidate: %w", err)
	}
	defer candidateTree.Close()
	if candidateTree.Digest != digest {
		return fmt.Errorf("mounted candidate does not match server digest")
	}
	if err := copyCandidate(ctx, source, workspace); err != nil {
		return err
	}
	workspaceTree, err := repositorymerge.CaptureDirectory(ctx, canonicalizer, workspace)
	if err != nil {
		return fmt.Errorf("capture copied candidate: %w", err)
	}
	defer workspaceTree.Close()
	if workspaceTree.Digest != digest {
		return fmt.Errorf("fresh scratch is not the exact candidate")
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
	profile := workflow.CompiledDevValidationProfile{Name: o.profileName, Candidate: workflow.DevValidationContract{Name: "candidate", Type: typeRef}, CapabilityImage: o.image, CapabilityImageDigest: imageDigest, Command: []string{workflow.DevValidationCLIPath, workflow.DevValidationCLIValidateCommand}, Profile: profileBytes, ProfileDigest: contentDigest(profileBytes), ProtectedConfig: configBytes, ProtectedConfigDigest: contentDigest(configBytes)}
	_, err = devvalidate.NewRunner(nil).Run(ctx, devvalidate.Request{Candidate: snapshot.SnapshotRef{ID: id, Type: typeRef, Digest: digest}, CandidateInput: "candidate", WorkspaceRoot: workspace, OutputRoot: output, Profile: profile, WorkflowDefinitionID: definition, WorkflowVersion: version})
	return err
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
