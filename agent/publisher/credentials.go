package publisher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxCredentialBytes int64 = 64 << 10

var atomicWriterGenerationName = regexp.MustCompile(
	`^\.\.[0-9]{4}_[0-9]{2}_[0-9]{2}_[0-9]{2}_[0-9]{2}_[0-9]{2}\.[0-9]+$`,
)

// FileCredentialProvider applies an exact Policy before opening a
// destination-scoped credential beneath an explicit trusted root. The root,
// each real directory ancestor, and the file are rechecked on every
// authorization so namespace or file rotations cannot weaken the boundary.
type FileCredentialProvider struct {
	policy          Policy
	root            *mountedFileRoot
	credentialFiles map[string]mountedFileBinding
}

type credentialAncestor struct {
	path string
	info os.FileInfo
}

// NewFileCredentialProvider validates a complete one-to-one mapping for the
// credential references used by policy. The trusted root must be absolute,
// clean, non-root, and symlink-free. Unknown mappings are rejected so a
// process does not gain access to credentials outside its active policy.
func NewFileCredentialProvider(
	policy Policy,
	trustedRootPath string,
	credentialFiles map[string]string,
) (*FileCredentialProvider, error) {
	root, err := newMountedFileRoot(trustedRootPath, "publisher credentials")
	if err != nil {
		return nil, err
	}
	return newFileCredentialProvider(policy, root, credentialFiles)
}

func newFileCredentialProvider(
	policy Policy,
	root *mountedFileRoot,
	credentialFiles map[string]string,
) (*FileCredentialProvider, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}

	required := make(map[string]struct{}, len(policy.Rules))
	for _, rule := range policy.Rules {
		required[rule.CredentialReference] = struct{}{}
	}
	if len(credentialFiles) != len(required) {
		return nil, fmt.Errorf("publisher credentials: file mapping must exactly cover policy references")
	}
	files := make(map[string]mountedFileBinding, len(credentialFiles))
	for reference, filePath := range credentialFiles {
		if _, found := required[reference]; !found {
			return nil, fmt.Errorf("publisher credentials: reference %q is not used by policy", reference)
		}
		binding, err := root.bind(
			filePath,
			fmt.Sprintf("reference %q", reference),
			maxCredentialBytes,
			true,
			true,
		)
		if err != nil {
			return nil, err
		}
		files[reference] = binding
	}
	return &FileCredentialProvider{
		policy: clonePolicy(policy), root: root, credentialFiles: files,
	}, nil
}

func (provider *FileCredentialProvider) AuthorizeDestination(ctx context.Context, request Request) (Credential, error) {
	if ctx == nil {
		return Credential{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	rule, err := provider.policy.Resolve(ctx, request)
	if err != nil {
		return Credential{}, err
	}
	secret, err := provider.readCredential(rule.CredentialReference)
	if err != nil {
		return Credential{}, err
	}
	return Credential{
		Reference:   rule.CredentialReference,
		adapterKind: rule.Adapter,
		remoteURL:   rule.RemoteURL,
		secret:      append([]byte(nil), secret...),
	}, nil
}

func (provider *FileCredentialProvider) readCredential(reference string) ([]byte, error) {
	credential, found := provider.credentialFiles[reference]
	if !found {
		return nil, fmt.Errorf("publisher credentials: authorized reference is unavailable")
	}
	return provider.root.read(credential)
}

func openTrustedCredentialRoot(trustedRootPath string) (*os.Root, os.FileInfo, error) {
	if trustedRootPath == "" ||
		!filepath.IsAbs(trustedRootPath) ||
		filepath.Clean(trustedRootPath) != trustedRootPath ||
		trustedRootPath == string(filepath.Separator) {
		return nil, nil, fmt.Errorf("publisher credentials: trusted root must be an absolute clean non-root path")
	}

	current, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, nil, fmt.Errorf("publisher credentials: open filesystem root: %w", err)
	}
	components := strings.Split(strings.TrimPrefix(trustedRootPath, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		before, err := current.Lstat(component)
		if err != nil {
			current.Close()
			return nil, nil, fmt.Errorf("publisher credentials: inspect trusted root: %w", err)
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			current.Close()
			return nil, nil, fmt.Errorf("publisher credentials: trusted root must not contain symlink components")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			current.Close()
			return nil, nil, fmt.Errorf("publisher credentials: open trusted root: %w", err)
		}
		opened, openedErr := next.Lstat(".")
		after, afterErr := current.Lstat(component)
		current.Close()
		if openedErr != nil || afterErr != nil {
			next.Close()
			if openedErr != nil {
				return nil, nil, fmt.Errorf("publisher credentials: inspect opened trusted root: %w", openedErr)
			}
			return nil, nil, fmt.Errorf("publisher credentials: re-inspect trusted root: %w", afterErr)
		}
		if !os.SameFile(before, opened) || !os.SameFile(before, after) {
			next.Close()
			return nil, nil, fmt.Errorf("publisher credentials: trusted root changed while opening")
		}
		current = next
	}
	info, err := current.Lstat(".")
	if err != nil {
		current.Close()
		return nil, nil, fmt.Errorf("publisher credentials: inspect trusted root: %w", err)
	}
	return current, info, nil
}

func revalidateTrustedCredentialRoot(trustedRootPath string, expected os.FileInfo, reference string) error {
	current, currentInfo, err := openTrustedCredentialRoot(trustedRootPath)
	if err != nil {
		return err
	}
	defer current.Close()
	if !os.SameFile(expected, currentInfo) {
		return fmt.Errorf("publisher credentials: trusted root changed while reading reference %q", reference)
	}
	return nil
}

func inspectCredentialAncestorDirectory(
	root *os.Root,
	reference string,
	directory string,
) ([]credentialAncestor, error) {
	components := strings.Split(directory, string(filepath.Separator))
	ancestors := make([]credentialAncestor, 0, len(components))
	currentPath := ""
	for _, component := range components {
		currentPath = filepath.Join(currentPath, component)
		before, err := root.Lstat(currentPath)
		if err != nil {
			return nil, fmt.Errorf("publisher credentials: inspect ancestor for reference %q: %w", reference, err)
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			return nil, fmt.Errorf("publisher credentials: reference %q must not have symlink ancestors", reference)
		}
		openedRoot, err := root.OpenRoot(currentPath)
		if err != nil {
			return nil, fmt.Errorf("publisher credentials: open ancestor for reference %q: %w", reference, err)
		}
		opened, openedErr := openedRoot.Lstat(".")
		openedRoot.Close()
		after, afterErr := root.Lstat(currentPath)
		if openedErr != nil || afterErr != nil {
			if openedErr != nil {
				return nil, fmt.Errorf("publisher credentials: inspect opened ancestor for reference %q: %w", reference, openedErr)
			}
			return nil, fmt.Errorf("publisher credentials: re-inspect ancestor for reference %q: %w", reference, afterErr)
		}
		if !os.SameFile(before, opened) || !os.SameFile(before, after) {
			return nil, fmt.Errorf("publisher credentials: ancestor for reference %q changed while opening", reference)
		}
		ancestors = append(ancestors, credentialAncestor{path: currentPath, info: before})
	}
	return ancestors, nil
}

func revalidateCredentialAncestors(
	root *os.Root,
	reference string,
	expected []credentialAncestor,
) error {
	if len(expected) == 0 {
		return nil
	}
	current, err := inspectCredentialAncestorDirectory(root, reference, expected[len(expected)-1].path)
	if err != nil {
		return err
	}
	if len(current) != len(expected) {
		return fmt.Errorf("publisher credentials: ancestors for reference %q changed while reading", reference)
	}
	for index := range expected {
		if current[index].path != expected[index].path || !os.SameFile(current[index].info, expected[index].info) {
			return fmt.Errorf("publisher credentials: ancestor for reference %q changed while reading", reference)
		}
	}
	return nil
}

func openCredentialParent(
	root *os.Root,
	reference string,
	expected []credentialAncestor,
) (*os.Root, bool, error) {
	if len(expected) == 0 {
		return root, false, nil
	}
	current := root
	currentOwned := false
	for _, ancestor := range expected {
		component := filepath.Base(ancestor.path)
		before, err := current.Lstat(component)
		if err != nil {
			if currentOwned {
				current.Close()
			}
			return nil, false, fmt.Errorf("publisher credentials: inspect ancestor for reference %q: %w", reference, err)
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() || !os.SameFile(ancestor.info, before) {
			if currentOwned {
				current.Close()
			}
			return nil, false, fmt.Errorf("publisher credentials: ancestor for reference %q changed while opening", reference)
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			if currentOwned {
				current.Close()
			}
			return nil, false, fmt.Errorf("publisher credentials: open ancestor for reference %q: %w", reference, err)
		}
		opened, openedErr := next.Lstat(".")
		after, afterErr := current.Lstat(component)
		if currentOwned {
			current.Close()
		}
		if openedErr != nil || afterErr != nil {
			next.Close()
			if openedErr != nil {
				return nil, false, fmt.Errorf("publisher credentials: inspect opened ancestor for reference %q: %w", reference, openedErr)
			}
			return nil, false, fmt.Errorf("publisher credentials: re-inspect ancestor for reference %q: %w", reference, afterErr)
		}
		if !os.SameFile(ancestor.info, opened) || !os.SameFile(ancestor.info, after) {
			next.Close()
			return nil, false, fmt.Errorf("publisher credentials: ancestor for reference %q changed while opening", reference)
		}
		current = next
		currentOwned = true
	}
	return current, true, nil
}

func isAtomicWriterGeneration(target string) bool {
	return filepath.Clean(target) == target &&
		filepath.Base(target) == target &&
		atomicWriterGenerationName.MatchString(target)
}

func clonePolicy(policy Policy) Policy {
	copy := Policy{SchemaVersion: policy.SchemaVersion, Rules: make([]PolicyRule, len(policy.Rules))}
	copy.Rules = append(copy.Rules[:0], policy.Rules...)
	return copy
}
