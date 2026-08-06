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

	// Refs names every ref in the tree, so a snapshot can identify a revision
	// other than the one checked out. Without it a repository advertises exactly
	// one commit, which is why carrying both sides of a pull request has meant
	// cloning the same history twice.
	//
	// It is derived from the sealed bytes -- .git/refs and packed-refs are inside
	// the canonical archive -- never supplied by a caller. The snapshot store
	// requires identical (type, digest) pairs to produce byte-identical intrinsic
	// metadata, and a caller-supplied revision would break that. encoding/json
	// sorts map keys, so the encoding is deterministic.
	Refs map[string]string `json:"refs,omitempty"`
}

// DecodeRepositoryMetadata decodes the exact current intrinsic metadata shape
// sealed for repository/v1. It is used by authority components that must
// derive repository identity from an already-owned base snapshot.
func DecodeRepositoryMetadata(raw []byte) (RepositoryMetadata, error) {
	return decodeExactJSONDocument[RepositoryMetadata](raw)
}

type RepositoryChangeBody struct {
	RepositoryID   string     `json:"repository_id"`
	BaseSHA        string     `json:"base_sha"`
	Representation string     `json:"representation"`
	Payload        ContentRef `json:"payload"`
	ResultTree     string     `json:"result_tree"`
	ResultCommit   string     `json:"result_commit,omitempty"`
}

type repositoryValidator struct{}

var (
	errUnsupportedRepositoryObjectFormat = errors.New("unsupported repository object format")
	errRepositoryGitlink                 = errors.New("repository contains a gitlink")
)

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
		if errors.Is(err, fs.ErrNotExist) {
			return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryMetadataMissing, fmt.Errorf("snapshot contracts: repository .git directory is required: %w", err))
		}
		return RepositoryMetadata{}, fmt.Errorf("snapshot contracts: repository .git directory is required: %w", err)
	}
	if !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryMetadataUnsafe, fmt.Errorf("snapshot contracts: repository .git must be a real contained directory"))
	}
	if err := validateGitAdministrativeFiles(ctx, root); err != nil {
		return RepositoryMetadata{}, err
	}

	runner := controlledGit{dir: root.Name()}
	inside, err := runner.run(ctx, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository work tree is invalid: %w", err))
	}
	if inside != "true" {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository work tree is invalid"))
	}
	if err := runner.verifyConfinement(ctx); err != nil {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryMetadataUnsafe, err)
	}
	objectFormat, err := runner.run(ctx, "rev-parse", "--show-object-format=storage")
	if err != nil {
		return RepositoryMetadata{}, publicRepositorySemanticFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: determine repository object format: %w", err))
	}
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryObjectFormatUnsupported, fmt.Errorf("snapshot contracts: unsupported repository object format %q", objectFormat))
	}
	shallow, err := runner.run(ctx, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return RepositoryMetadata{}, publicRepositorySemanticFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: determine shallow repository state: %w", err))
	}
	if shallow != "false" {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryHistoryIncomplete, fmt.Errorf("snapshot contracts: shallow repositories are not allowed"))
	}
	headSHA, err := runner.run(ctx, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return RepositoryMetadata{}, publicRepositorySemanticFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository HEAD must resolve to a full commit: %w", err))
	}
	if err := validateObjectID(objectFormat, headSHA); err != nil {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository HEAD: %w", err))
	}
	treeSHA, err := runner.run(ctx, "rev-parse", "--verify", headSHA+"^{tree}")
	if err != nil {
		return RepositoryMetadata{}, publicRepositorySemanticFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: resolve repository tree: %w", err))
	}
	if err := validateObjectID(objectFormat, treeSHA); err != nil {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository tree: %w", err))
	}
	if err := validateCommitTree(ctx, runner, treeSHA, objectFormat); err != nil {
		if errors.Is(err, errRepositoryGitlink) {
			return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryGitlinksUnsupported, err)
		}
		return RepositoryMetadata{}, publicRepositorySemanticFailure(ctx, snapshot.RepositoryInvalid, err)
	}
	rootOutput, err := runner.run(ctx, "rev-list", "--max-parents=0", headSHA)
	if err != nil {
		return RepositoryMetadata{}, publicRepositorySemanticFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: resolve repository root commits: %w", err))
	}
	rootCommits := strings.Fields(rootOutput)
	if len(rootCommits) == 0 {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository has no reachable root commit"))
	}
	for _, commit := range rootCommits {
		if err := validateObjectID(objectFormat, commit); err != nil {
			return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository root commit: %w", err))
		}
	}
	sort.Strings(rootCommits)
	if _, err := runner.run(ctx, "fsck", "--full", "--strict", "--no-reflogs"); err != nil {
		return RepositoryMetadata{}, publicRepositorySemanticFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository is broken: %w", err))
	}
	gitlinks, err := runner.run(ctx, "ls-files", "--stage")
	if err != nil {
		return RepositoryMetadata{}, publicRepositorySemanticFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: inspect repository index: %w", err))
	}
	for _, line := range strings.Split(gitlinks, "\n") {
		if strings.HasPrefix(line, "160000 ") {
			return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryGitlinksUnsupported, fmt.Errorf("snapshot contracts: repositories with submodule gitlinks are not supported"))
		}
	}
	dirty, err := runner.run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return RepositoryMetadata{}, publicRepositorySemanticFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: inspect repository cleanliness: %w", err))
	}
	if dirty != "" {
		return RepositoryMetadata{}, publicRepositoryFailure(ctx, snapshot.RepositoryDirty, fmt.Errorf("snapshot contracts: repository work tree and index must be clean"))
	}

	refs, err := repositoryRefs(ctx, runner, objectFormat)
	if err != nil {
		return RepositoryMetadata{}, err
	}

	repositoryID := repositoryIdentity(objectFormat, rootCommits)
	return RepositoryMetadata{
		RepositoryID: repositoryID,
		ObjectFormat: objectFormat,
		HeadSHA:      headSHA,
		TreeSHA:      treeSHA,
		RootCommits:  append([]string(nil), rootCommits...),
		Refs:         refs,
	}, nil
}

// maxRepositoryRefs bounds the named-revision map. A materialized tree carries a
// handful of refs; a pathological one must not be able to grow the intrinsic
// metadata without limit.
const maxRepositoryRefs = 256

// repositoryRefs reads every ref in the tree so the snapshot can name revisions
// other than the checked-out one. Peeled tag targets are deliberately excluded:
// show-ref emits them as a separate "^{}" entry, and a ref must resolve to a
// single object for the map to mean anything.
func repositoryRefs(ctx context.Context, runner controlledGit, objectFormat string) (map[string]string, error) {
	listing, err := runner.run(ctx, "show-ref")
	if err != nil {
		// A repository with no refs at all is legitimate; show-ref exits non-zero
		// for it, and there is nothing to name.
		return nil, nil
	}
	if strings.TrimSpace(listing) == "" {
		return nil, nil
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository ref listing is malformed"))
		}
		object, name := fields[0], fields[1]
		if strings.HasSuffix(name, "^{}") {
			continue
		}
		if err := validateObjectID(objectFormat, object); err != nil {
			return nil, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository ref %q: %w", name, err))
		}
		if _, duplicate := refs[name]; duplicate {
			return nil, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository ref %q is duplicated", name))
		}
		refs[name] = object
		if len(refs) > maxRepositoryRefs {
			return nil, publicRepositoryFailure(ctx, snapshot.RepositoryInvalid, fmt.Errorf("snapshot contracts: repository declares more than %d refs", maxRepositoryRefs))
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	return refs, nil
}

// publicRepositoryFailure only exposes conditions established from the
// repository itself. Context cancellation, filesystem faults, and failure to
// start Git remain ordinary internal errors so the HTTP boundary cannot turn
// operational detail into a public diagnostic.
func publicRepositoryFailure(ctx context.Context, reason snapshot.ValidationFailureReason, cause error) error {
	return repositoryFailure(ctx, reason, cause, false)
}

// publicRepositorySemanticFailure marks failures from a Git command whose
// nonzero result is itself the repository condition being validated (for
// example, an unresolved HEAD or a failed fsck). The initial Git invocation
// still uses publicRepositoryFailure so a broken or substituted Git process is
// never published as a repository diagnostic.
func publicRepositorySemanticFailure(ctx context.Context, reason snapshot.ValidationFailureReason, cause error) error {
	return repositoryFailure(ctx, reason, cause, true)
}

func repositoryFailure(ctx context.Context, reason snapshot.ValidationFailureReason, cause error, semanticGitExit bool) error {
	if cause == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	var startErr *exec.Error
	if errors.As(cause, &startErr) {
		return cause
	}
	var exitErr *exec.ExitError
	if errors.As(cause, &exitErr) && !semanticGitExit {
		return cause
	}
	var pathErr *fs.PathError
	if errors.As(cause, &pathErr) && !errors.Is(cause, fs.ErrNotExist) {
		return cause
	}
	return snapshot.NewPublicValidationFailure(reason, cause)
}

func validateGitAdministrativeFiles(ctx context.Context, root *os.Root) error {
	info, err := root.Lstat(".git/config")
	if errors.Is(err, fs.ErrNotExist) {
		return publicRepositoryFailure(ctx, snapshot.RepositoryMetadataMissing, fmt.Errorf("snapshot contracts: repository config is required: %w", err))
	}
	if err != nil {
		return fmt.Errorf("snapshot contracts: repository config: %w", err)
	}
	if !info.Mode().IsRegular() {
		return publicRepositoryFailure(ctx, snapshot.RepositoryMetadataUnsafe, fmt.Errorf("snapshot contracts: repository config must be a regular file"))
	}
	config, err := readRegularFile(ctx, root, ".git/config", maxJSONDocumentBytes)
	if err != nil {
		return fmt.Errorf("snapshot contracts: repository config: %w", err)
	}
	if err := validateRepositoryConfig(config); err != nil {
		if errors.Is(err, errUnsupportedRepositoryObjectFormat) {
			return publicRepositoryFailure(ctx, snapshot.RepositoryObjectFormatUnsupported, fmt.Errorf("snapshot contracts: repository config: %w", err))
		}
		return publicRepositoryFailure(ctx, snapshot.RepositoryMetadataUnsafe, fmt.Errorf("snapshot contracts: repository config: %w", err))
	}
	if info, err := root.Lstat(".git/shallow"); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 0 {
			return publicRepositoryFailure(ctx, snapshot.RepositoryHistoryIncomplete, fmt.Errorf("snapshot contracts: shallow repositories are not allowed"))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("snapshot contracts: inspect shallow marker: %w", err)
	}
	if err := validateContainedGitReference(ctx, root, ".git/commondir", ".git"); err != nil {
		return publicRepositoryFailure(ctx, snapshot.RepositoryMetadataUnsafe, fmt.Errorf("snapshot contracts: commondir: %w", err))
	}
	if err := validateAlternates(ctx, root); err != nil {
		return publicRepositoryFailure(ctx, snapshot.RepositoryMetadataUnsafe, err)
	}
	return nil
}

type repositoryConfigEntry struct {
	section    string
	subsection string
	name       string
	value      string
}

func validateRepositoryConfig(config []byte) error {
	entries, err := parseRepositoryConfig(config)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.section {
		case "core":
			if entry.subsection != "" {
				return unsupportedRepositoryConfig(entry)
			}
			switch entry.name {
			case "repositoryformatversion":
				if entry.value != "0" && entry.value != "1" {
					return fmt.Errorf("unsupported core.repositoryformatversion %q", entry.value)
				}
			case "filemode", "bare", "logallrefupdates", "ignorecase", "precomposeunicode", "symlinks":
				value := strings.ToLower(entry.value)
				if value != "true" && value != "false" && value != "yes" && value != "no" && value != "on" && value != "off" && value != "1" && value != "0" {
					return fmt.Errorf("core.%s must be a boolean", entry.name)
				}
				if entry.name == "bare" && value != "false" && value != "no" && value != "off" && value != "0" {
					return fmt.Errorf("core.bare must be false")
				}
			default:
				return unsupportedRepositoryConfig(entry)
			}
		case "extensions":
			if entry.subsection != "" || entry.name != "objectformat" {
				return unsupportedRepositoryConfig(entry)
			}
			if entry.value != "sha1" && entry.value != "sha256" {
				return fmt.Errorf("%w %q", errUnsupportedRepositoryObjectFormat, entry.value)
			}
		case "user":
			if entry.subsection != "" || (entry.name != "name" && entry.name != "email") {
				return unsupportedRepositoryConfig(entry)
			}
		case "remote":
			if entry.subsection == "" {
				return unsupportedRepositoryConfig(entry)
			}
			switch entry.name {
			case "url":
				if err := validateInertRemoteURL(entry.value); err != nil {
					return fmt.Errorf("remote %q URL: %w", entry.subsection, err)
				}
			case "fetch":
				if err := validateInertConfigValue(entry.value); err != nil {
					return fmt.Errorf("remote %q fetch refspec: %w", entry.subsection, err)
				}
			default:
				return unsupportedRepositoryConfig(entry)
			}
		case "branch":
			if entry.subsection == "" || (entry.name != "remote" && entry.name != "merge") {
				return unsupportedRepositoryConfig(entry)
			}
			if err := validateInertConfigValue(entry.value); err != nil {
				return fmt.Errorf("branch %q %s: %w", entry.subsection, entry.name, err)
			}
		default:
			return unsupportedRepositoryConfig(entry)
		}
	}
	return nil
}

func unsupportedRepositoryConfig(entry repositoryConfigEntry) error {
	key := entry.section
	if entry.subsection != "" {
		key += "." + entry.subsection
	}
	key += "." + entry.name
	return fmt.Errorf("unsupported active setting %q", key)
}

func parseRepositoryConfig(config []byte) ([]repositoryConfigEntry, error) {
	if !utf8.Valid(config) || bytes.IndexByte(config, 0) >= 0 {
		return nil, fmt.Errorf("must be UTF-8 text without NUL bytes")
	}
	var entries []repositoryConfigEntry
	section := ""
	subsection := ""
	for lineNumber, rawLine := range strings.Split(string(config), "\n") {
		rawLine = strings.TrimSuffix(rawLine, "\r")
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasSuffix(line, `\`) {
			return nil, fmt.Errorf("line %d uses unsupported continuation syntax", lineNumber+1)
		}
		if strings.HasPrefix(line, "[") {
			parsedSection, parsedSubsection, err := parseRepositoryConfigSection(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber+1, err)
			}
			section, subsection = parsedSection, parsedSubsection
			continue
		}
		if section == "" {
			return nil, fmt.Errorf("line %d defines a value before a section", lineNumber+1)
		}
		name, value, err := parseRepositoryConfigVariable(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber+1, err)
		}
		entries = append(entries, repositoryConfigEntry{
			section: section, subsection: subsection, name: name, value: value,
		})
	}
	return entries, nil
}

func parseRepositoryConfigSection(line string) (string, string, error) {
	if !strings.HasSuffix(line, "]") || strings.Count(line, "[") != 1 || strings.Count(line, "]") != 1 {
		return "", "", fmt.Errorf("malformed section header")
	}
	body := strings.TrimSpace(line[1 : len(line)-1])
	if body == "" {
		return "", "", fmt.Errorf("empty section header")
	}
	separator := strings.IndexAny(body, " \t")
	section := body
	remainder := ""
	if separator >= 0 {
		section = body[:separator]
		remainder = strings.TrimSpace(body[separator:])
	}
	if !validGitConfigName(section) {
		return "", "", fmt.Errorf("invalid section name")
	}
	if remainder == "" {
		return strings.ToLower(section), "", nil
	}
	subsection, err := decodeGitConfigQuotedValue(remainder)
	if err != nil || subsection == "" {
		return "", "", fmt.Errorf("invalid subsection")
	}
	if err := validateInertConfigValue(subsection); err != nil {
		return "", "", fmt.Errorf("invalid subsection: %w", err)
	}
	return strings.ToLower(section), subsection, nil
}

func parseRepositoryConfigVariable(line string) (string, string, error) {
	separator := strings.IndexAny(line, "= \t")
	name := line
	remainder := ""
	if separator >= 0 {
		name = line[:separator]
		remainder = strings.TrimSpace(line[separator:])
	}
	if !validGitConfigName(name) {
		return "", "", fmt.Errorf("invalid variable name")
	}
	if strings.HasPrefix(remainder, "=") {
		remainder = strings.TrimSpace(remainder[1:])
	}
	value := "true"
	if remainder != "" {
		var err error
		value, err = decodeGitConfigValue(remainder)
		if err != nil {
			return "", "", err
		}
	}
	return strings.ToLower(name), value, nil
}

func validGitConfigName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-') {
			return false
		}
	}
	return true
}

func decodeGitConfigValue(value string) (string, error) {
	if strings.HasPrefix(value, `"`) {
		return decodeGitConfigQuotedValue(value)
	}
	if strings.ContainsAny(value, `"#;`) {
		return "", fmt.Errorf("uses unsupported quoting or inline-comment syntax")
	}
	if err := validateInertConfigValue(value); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func decodeGitConfigQuotedValue(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("malformed quoted value")
	}
	var decoded strings.Builder
	for index := 1; index < len(value)-1; index++ {
		character := value[index]
		if character != '\\' {
			decoded.WriteByte(character)
			continue
		}
		index++
		if index >= len(value)-1 {
			return "", fmt.Errorf("malformed quoted escape")
		}
		switch value[index] {
		case '\\', '"':
			decoded.WriteByte(value[index])
		case 'n':
			decoded.WriteByte('\n')
		case 't':
			decoded.WriteByte('\t')
		case 'b':
			decoded.WriteByte('\b')
		default:
			return "", fmt.Errorf("unsupported quoted escape")
		}
	}
	result := decoded.String()
	if err := validateInertConfigValue(result); err != nil {
		return "", err
	}
	return result, nil
}

func validateInertConfigValue(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("must not be empty")
	}
	for _, character := range value {
		if character < ' ' || character == 0x7f {
			return fmt.Errorf("contains control characters")
		}
	}
	return nil
}

func validateInertRemoteURL(value string) error {
	if err := validateInertConfigValue(value); err != nil {
		return err
	}
	if strings.Contains(value, "::") {
		return fmt.Errorf("custom remote helpers are not allowed")
	}
	if schemeEnd := strings.Index(value, "://"); schemeEnd >= 0 {
		scheme := strings.ToLower(value[:schemeEnd])
		switch scheme {
		case "file", "git", "http", "https", "ssh":
		default:
			return fmt.Errorf("custom transport scheme %q is not allowed", scheme)
		}
	}
	return nil
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
			return fmt.Errorf("snapshot contracts: %w %q", errRepositoryGitlink, entryPath)
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
