package contracts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
)

type RepositoryMetadata struct {
	RepositoryID string   `json:"repository_id"`
	ObjectFormat string   `json:"object_format"`
	HeadSHA      string   `json:"head_sha"`
	TreeSHA      string   `json:"tree_sha"`
	RootCommits  []string `json:"root_commits"`
}

type RepositoryChangeDocument struct {
	SchemaVersion  string `json:"schema_version"`
	RepositoryID   string `json:"repository_id"`
	BaseInput      string `json:"base_input"`
	BaseSHA        string `json:"base_sha"`
	ResultSHA      string `json:"result_sha,omitempty"`
	ResultTreeSHA  string `json:"result_tree_sha"`
	Representation string `json:"representation"`
	PayloadPath    string `json:"payload_path"`
	PayloadDigest  string `json:"payload_digest"`
}

type repositoryValidator struct{}

func (repositoryValidator) Validate(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	metadata, err := validateRepository(ctx, root, "HEAD")
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: encode repository metadata: %w", err)
	}
	return snapshot.ValidationResult{IntrinsicMetadata: encoded}, nil
}

func validateRepository(ctx context.Context, root *os.Root, revision string) (RepositoryMetadata, error) {
	if root == nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: snapshot root is required")
	}
	if err := ctx.Err(); err != nil {
		return RepositoryMetadata{}, err
	}
	gitInfo, err := root.Lstat(".git")
	if err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository .git directory is required: %w", err)
	}
	if !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository .git must be a real contained directory")
	}
	if err := validateGitAdministrativeFiles(ctx, root); err != nil {
		return RepositoryMetadata{}, err
	}

	runner := controlledGit{dir: root.Name()}
	inside, err := runner.run(ctx, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository work tree is invalid: %w", err)
	}
	if inside != "true" {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository work tree is invalid")
	}
	if err := runner.verifyConfinement(ctx); err != nil {
		return RepositoryMetadata{}, err
	}
	objectFormat, err := runner.run(ctx, "rev-parse", "--show-object-format=storage")
	if err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: determine repository object format: %w", err)
	}
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: unsupported repository object format %q", objectFormat)
	}
	shallow, err := runner.run(ctx, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: determine shallow repository state: %w", err)
	}
	if shallow != "false" {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: shallow repositories are not allowed")
	}
	headSHA, err := runner.run(ctx, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository HEAD must resolve to a full commit: %w", err)
	}
	if err := validateObjectID(objectFormat, headSHA); err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository HEAD: %w", err)
	}
	treeSHA, err := runner.run(ctx, "rev-parse", "--verify", headSHA+"^{tree}")
	if err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: resolve repository tree: %w", err)
	}
	if err := validateObjectID(objectFormat, treeSHA); err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository tree: %w", err)
	}
	if err := validateCommitTree(ctx, runner, treeSHA, objectFormat); err != nil {
		return RepositoryMetadata{}, err
	}
	rootOutput, err := runner.run(ctx, "rev-list", "--max-parents=0", headSHA)
	if err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: resolve repository root commits: %w", err)
	}
	rootCommits := strings.Fields(rootOutput)
	if len(rootCommits) == 0 {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository has no reachable root commit")
	}
	for _, commit := range rootCommits {
		if err := validateObjectID(objectFormat, commit); err != nil {
			return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository root commit: %w", err)
		}
	}
	sort.Strings(rootCommits)
	if _, err := runner.run(ctx, "fsck", "--full", "--strict", "--no-reflogs"); err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository is broken: %w", err)
	}
	gitlinks, err := runner.run(ctx, "ls-files", "--stage")
	if err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: inspect repository index: %w", err)
	}
	for _, line := range strings.Split(gitlinks, "\n") {
		if strings.HasPrefix(line, "160000 ") {
			return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repositories with submodule gitlinks are not supported")
		}
	}
	dirty, err := runner.run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: inspect repository cleanliness: %w", err)
	}
	if dirty != "" {
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository work tree and index must be clean")
	}

	repositoryID := repositoryIdentity(objectFormat, rootCommits)
	return RepositoryMetadata{
		RepositoryID: repositoryID,
		ObjectFormat: objectFormat,
		HeadSHA:      headSHA,
		TreeSHA:      treeSHA,
		RootCommits:  append([]string(nil), rootCommits...),
	}, nil
}

func validateGitAdministrativeFiles(ctx context.Context, root *os.Root) error {
	config, err := readRegularFile(ctx, root, ".git/config", maxJSONDocumentBytes)
	if err != nil {
		return fmt.Errorf("snapshot contracts: repository config: %w", err)
	}
	lowerConfig := strings.ToLower(string(config))
	for _, forbidden := range []string{"[include", "[includeif", "[filter"} {
		if strings.Contains(lowerConfig, forbidden) {
			return fmt.Errorf("snapshot contracts: repository config contains forbidden external include or filter configuration")
		}
	}
	if gitConfigDefinesCoreWorktree(string(config)) {
		return fmt.Errorf("snapshot contracts: repository config must not define core.worktree")
	}
	if info, err := root.Lstat(".git/shallow"); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 0 {
			return fmt.Errorf("snapshot contracts: shallow repositories are not allowed")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("snapshot contracts: inspect shallow marker: %w", err)
	}
	if err := validateContainedGitReference(ctx, root, ".git/commondir", ".git"); err != nil {
		return fmt.Errorf("snapshot contracts: commondir: %w", err)
	}
	if err := validateAlternates(ctx, root); err != nil {
		return err
	}
	return nil
}

func gitConfigDefinesCoreWorktree(config string) bool {
	section := ""
	for _, rawLine := range strings.Split(config, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if section != "core" {
			continue
		}
		key := line
		if separator := strings.IndexAny(key, "= \t"); separator >= 0 {
			key = key[:separator]
		}
		if strings.EqualFold(strings.TrimSpace(key), "worktree") {
			return true
		}
	}
	return false
}

func validateContainedGitReference(ctx context.Context, root *os.Root, fileName, relativeBase string) error {
	info, err := root.Lstat(fileName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file", fileName)
	}
	contents, err := readRegularFile(ctx, root, fileName, 64<<10)
	if err != nil {
		return err
	}
	reference := strings.TrimSpace(string(contents))
	if reference == "" || path.IsAbs(reference) || strings.Contains(reference, `\`) {
		return fmt.Errorf("%s names an external path", fileName)
	}
	resolved := path.Clean(path.Join(relativeBase, reference))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("%s escapes the snapshot", fileName)
	}
	target, err := root.Lstat(resolved)
	if err != nil || !target.IsDir() || target.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s does not name a contained real directory", fileName)
	}
	return nil
}

func validateAlternates(ctx context.Context, root *os.Root) error {
	const alternates = ".git/objects/info/alternates"
	info, err := root.Lstat(alternates)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("snapshot contracts: alternates must be a regular file")
		}
		contents, err := readRegularFile(ctx, root, alternates, 1<<20)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(contents)) != "" {
			return fmt.Errorf("snapshot contracts: nonempty alternates are not allowed")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("snapshot contracts: inspect alternates: %w", err)
	}
	if _, err := root.Lstat(".git/objects/info/http-alternates"); err == nil {
		return fmt.Errorf("snapshot contracts: network alternates are not allowed")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("snapshot contracts: inspect network alternates: %w", err)
	}
	return nil
}

func validateObjectID(objectFormat, objectID string) error {
	wantLength := 0
	switch objectFormat {
	case "sha1":
		wantLength = 40
	case "sha256":
		wantLength = 64
	default:
		return fmt.Errorf("unsupported object format %q", objectFormat)
	}
	if len(objectID) != wantLength {
		return fmt.Errorf("object ID must contain %d lowercase hexadecimal characters", wantLength)
	}
	for _, character := range objectID {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return fmt.Errorf("object ID must contain %d lowercase hexadecimal characters", wantLength)
		}
	}
	return nil
}

func repositoryIdentity(objectFormat string, rootCommits []string) string {
	canonical := "concourse.repository/v1\n" + objectFormat + "\n" + strings.Join(rootCommits, "\n") + "\n"
	digest := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(digest[:])
}

type controlledGit struct {
	dir string
}

func (g controlledGit) run(ctx context.Context, args ...string) (string, error) {
	output, err := g.runRaw(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (g controlledGit) runRaw(ctx context.Context, args ...string) ([]byte, error) {
	configuredArgs := []string{
		"--git-dir=" + filepath.Join(g.dir, ".git"),
		"--work-tree=" + g.dir,
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.autocrlf=false",
		"-c", "core.safecrlf=false",
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.excludesFile=" + os.DevNull,
		"-c", "credential.helper=",
		"-c", "protocol.allow=never",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "submodule.recurse=false",
		"-c", "filter.lfs.process=",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.clean=",
	}
	configuredArgs = append(configuredArgs, args...)
	command := exec.CommandContext(ctx, "git", configuredArgs...)
	command.Dir = g.dir
	command.Env = controlledGitEnvironment()
	var output boundedBuffer
	output.limit = 8 << 20
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
	return output.Bytes(), nil
}

func (g controlledGit) verifyConfinement(ctx context.Context) error {
	wantWorkTree, err := filepath.EvalSymlinks(g.dir)
	if err != nil {
		return fmt.Errorf("snapshot contracts: resolve supplied repository root: %w", err)
	}
	wantGitDir, err := filepath.EvalSymlinks(filepath.Join(g.dir, ".git"))
	if err != nil {
		return fmt.Errorf("snapshot contracts: resolve supplied git directory: %w", err)
	}
	workTree, err := g.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("snapshot contracts: resolve repository work tree: %w", err)
	}
	gitDir, err := g.run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("snapshot contracts: resolve repository git directory: %w", err)
	}
	resolvedWorkTree, err := filepath.EvalSymlinks(workTree)
	if err != nil {
		return fmt.Errorf("snapshot contracts: resolve Git work tree result: %w", err)
	}
	resolvedGitDir, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		return fmt.Errorf("snapshot contracts: resolve Git directory result: %w", err)
	}
	if filepath.Clean(resolvedWorkTree) != filepath.Clean(wantWorkTree) || filepath.Clean(resolvedGitDir) != filepath.Clean(wantGitDir) {
		return fmt.Errorf("snapshot contracts: Git work tree or git directory escapes the supplied repository root")
	}
	return nil
}

func controlledGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+7)
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
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_ATTR_NOSYSTEM=1",
		"LC_ALL=C",
	)
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(contents []byte) (int, error) {
	if len(contents) > b.limit-b.buffer.Len() {
		remaining := b.limit - b.buffer.Len()
		if remaining > 0 {
			_, _ = b.buffer.Write(contents[:remaining])
		}
		return 0, fmt.Errorf("git output exceeds %d-byte limit", b.limit)
	}
	return b.buffer.Write(contents)
}

func (b *boundedBuffer) String() string {
	return b.buffer.String()
}

func (b *boundedBuffer) Bytes() []byte {
	return append([]byte(nil), b.buffer.Bytes()...)
}

func validateCommitTree(ctx context.Context, runner controlledGit, treeSHA, objectFormat string) error {
	listing, err := runner.runRaw(ctx, "ls-tree", "-r", "-z", "--full-tree", treeSHA)
	if err != nil {
		return fmt.Errorf("snapshot contracts: inspect declared repository tree: %w", err)
	}
	records := bytes.Split(listing, []byte{0})
	entryCount := 0
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		entryCount++
		if entryCount > int(snapshot.DefaultMaxSnapshotEntries) {
			return fmt.Errorf("snapshot contracts: repository tree exceeds entry limit")
		}
		separator := bytes.IndexByte(record, '\t')
		if separator < 0 {
			return fmt.Errorf("snapshot contracts: malformed git ls-tree record")
		}
		header := strings.Fields(string(record[:separator]))
		if len(header) != 3 {
			return fmt.Errorf("snapshot contracts: malformed git ls-tree header")
		}
		mode, objectType, objectID := header[0], header[1], header[2]
		entryPath := string(record[separator+1:])
		if err := validatePOSIXPath("repository tree path", entryPath); err != nil {
			return err
		}
		if err := validateObjectID(objectFormat, objectID); err != nil {
			return fmt.Errorf("snapshot contracts: repository tree object %q: %w", entryPath, err)
		}
		switch mode {
		case "100644", "100755":
			if objectType != "blob" {
				return fmt.Errorf("snapshot contracts: repository tree path %q has invalid object type %q", entryPath, objectType)
			}
		case "120000":
			if objectType != "blob" {
				return fmt.Errorf("snapshot contracts: repository symlink %q has invalid object type %q", entryPath, objectType)
			}
			target, err := runner.runRaw(ctx, "cat-file", "blob", objectID)
			if err != nil {
				return fmt.Errorf("snapshot contracts: read repository symlink %q: %w", entryPath, err)
			}
			if err := validateRepositorySymlink(entryPath, target); err != nil {
				return err
			}
		case "160000":
			return fmt.Errorf("snapshot contracts: repository tree contains unsupported gitlink %q", entryPath)
		default:
			return fmt.Errorf("snapshot contracts: repository tree path %q has unsupported mode %q", entryPath, mode)
		}
	}
	return nil
}

func validateRepositorySymlink(entryPath string, rawTarget []byte) error {
	target := string(rawTarget)
	if target == "" || !utf8.Valid(rawTarget) || strings.ContainsRune(target, '\x00') || strings.Contains(target, `\`) || path.IsAbs(target) {
		return fmt.Errorf("snapshot contracts: repository symlink %q has an unsafe target", entryPath)
	}
	if path.Clean(target) != target {
		return fmt.Errorf("snapshot contracts: repository symlink %q target must be canonical", entryPath)
	}
	for _, segment := range strings.Split(target, "/") {
		if driveLikeSegment(segment) {
			return fmt.Errorf("snapshot contracts: repository symlink %q has a drive-like target", entryPath)
		}
	}
	resolved := path.Clean(path.Join(path.Dir(entryPath), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || path.IsAbs(resolved) {
		return fmt.Errorf("snapshot contracts: repository symlink %q escapes the repository root", entryPath)
	}
	return nil
}
