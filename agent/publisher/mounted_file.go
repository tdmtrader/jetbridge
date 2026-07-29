package publisher

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// mountedFileRoot binds all publisher configuration and secret reads to the
// same startup-observed directory identity.
type mountedFileRoot struct {
	path  string
	info  os.FileInfo
	scope string
}

type mountedFileBinding struct {
	relativePath    string
	stableAncestors []credentialAncestor
	atomicWriter    *atomicWriterBinding
	label           string
	maximumBytes    int64
	requireNonEmpty bool
	requirePrivate  bool
}

type atomicWriterBinding struct {
	mountPath   string
	visibleName string
	payloadPath string
}

type atomicWriterSnapshot struct {
	visibleTarget  string
	generation     string
	generationInfo os.FileInfo
	targetDirs     []credentialAncestor
	fileInfo       os.FileInfo
}

func newMountedFileRoot(path, scope string) (*mountedFileRoot, error) {
	root, info, err := openTrustedCredentialRoot(path)
	if err != nil {
		return nil, err
	}
	root.Close()
	return &mountedFileRoot{path: path, info: info, scope: scope}, nil
}

func (rootBinding *mountedFileRoot) bind(
	filePath string,
	label string,
	maximumBytes int64,
	requireNonEmpty bool,
	requirePrivate bool,
) (mountedFileBinding, error) {
	relativePath, err := relativeMountedFilePath(rootBinding, filePath, label)
	if err != nil {
		return mountedFileBinding{}, err
	}
	root, rootInfo, err := openTrustedCredentialRoot(rootBinding.path)
	if err != nil {
		return mountedFileBinding{}, err
	}
	defer root.Close()
	if !os.SameFile(rootBinding.info, rootInfo) {
		return mountedFileBinding{}, fmt.Errorf("%s: trusted root changed while binding %s", rootBinding.scope, label)
	}

	binding, err := inspectMountedFileLayout(root, relativePath, label)
	if err != nil {
		return mountedFileBinding{}, fmt.Errorf("%s: %w", rootBinding.scope, err)
	}
	binding.maximumBytes = maximumBytes
	binding.requireNonEmpty = requireNonEmpty
	binding.requirePrivate = requirePrivate
	if _, err := readMountedFileFromRoot(root, binding, rootBinding.scope); err != nil {
		return mountedFileBinding{}, err
	}
	if err := revalidateCredentialAncestors(root, label, binding.stableAncestors); err != nil {
		return mountedFileBinding{}, err
	}
	if err := revalidateTrustedCredentialRoot(rootBinding.path, rootBinding.info, label); err != nil {
		return mountedFileBinding{}, err
	}
	return binding, nil
}

func (rootBinding *mountedFileRoot) read(binding mountedFileBinding) ([]byte, error) {
	root, rootInfo, err := openTrustedCredentialRoot(rootBinding.path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if !os.SameFile(rootBinding.info, rootInfo) {
		return nil, fmt.Errorf("%s: trusted root changed while reading %s", rootBinding.scope, binding.label)
	}
	if err := revalidateCredentialAncestors(root, binding.label, binding.stableAncestors); err != nil {
		return nil, err
	}
	body, err := readMountedFileFromRoot(root, binding, rootBinding.scope)
	if err != nil {
		return nil, err
	}
	if err := revalidateCredentialAncestors(root, binding.label, binding.stableAncestors); err != nil {
		return nil, err
	}
	if err := revalidateTrustedCredentialRoot(rootBinding.path, rootBinding.info, binding.label); err != nil {
		return nil, err
	}
	return body, nil
}

func relativeMountedFilePath(
	rootBinding *mountedFileRoot,
	filePath string,
	label string,
) (string, error) {
	if filePath == "" || !filepath.IsAbs(filePath) || filepath.Clean(filePath) != filePath {
		return "", fmt.Errorf("%s: %s must use an absolute clean file path", rootBinding.scope, label)
	}
	relativePath, err := filepath.Rel(rootBinding.path, filePath)
	if err != nil || relativePath == "." || !filepath.IsLocal(relativePath) {
		return "", fmt.Errorf("%s: %s must lie strictly beneath the trusted root", rootBinding.scope, label)
	}
	return relativePath, nil
}

func inspectMountedFileLayout(
	root *os.Root,
	relativePath string,
	label string,
) (mountedFileBinding, error) {
	components := strings.Split(relativePath, string(filepath.Separator))
	currentPath := ""
	for index, component := range components {
		currentPath = filepath.Join(currentPath, component)
		info, err := root.Lstat(currentPath)
		if err != nil {
			return mountedFileBinding{}, fmt.Errorf("inspect %s: %w", label, err)
		}
		last := index == len(components)-1
		if info.Mode()&os.ModeSymlink != 0 {
			mountPath := filepath.Dir(currentPath)
			if mountPath == "." {
				mountPath = ""
			}
			stableAncestors, err := inspectStableMountedAncestors(root, label, mountPath)
			if err != nil {
				return mountedFileBinding{}, err
			}
			return mountedFileBinding{
				relativePath:    relativePath,
				stableAncestors: stableAncestors,
				atomicWriter: &atomicWriterBinding{
					mountPath:   mountPath,
					visibleName: component,
					payloadPath: filepath.Join(components[index:]...),
				},
				label: label,
			}, nil
		}
		if !last && !info.IsDir() {
			return mountedFileBinding{}, fmt.Errorf("%s must not have non-directory ancestors", label)
		}
		if last && !info.Mode().IsRegular() {
			return mountedFileBinding{}, fmt.Errorf("%s must be a regular file", label)
		}
	}
	stableAncestors, err := inspectStableMountedAncestors(root, label, filepath.Dir(relativePath))
	if err != nil {
		return mountedFileBinding{}, err
	}
	return mountedFileBinding{
		relativePath:    relativePath,
		stableAncestors: stableAncestors,
		label:           label,
	}, nil
}

func inspectStableMountedAncestors(
	root *os.Root,
	label string,
	directory string,
) ([]credentialAncestor, error) {
	if directory == "" || directory == "." {
		return nil, nil
	}
	return inspectCredentialAncestorDirectory(root, label, directory)
}

func readMountedFileFromRoot(
	root *os.Root,
	binding mountedFileBinding,
	scope string,
) ([]byte, error) {
	if binding.atomicWriter != nil {
		return readAtomicWriterMountedFile(root, binding, scope)
	}
	parent, closeParent, err := openCredentialParent(root, binding.label, binding.stableAncestors)
	if err != nil {
		return nil, err
	}
	if closeParent {
		defer parent.Close()
	}
	name := filepath.Base(binding.relativePath)
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("%s: inspect %s: %w", scope, binding.label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: %s changed to a symlink while reading", scope, binding.label)
	}
	return readRegularMountedFile(parent, name, info, binding, scope)
}

func readRegularMountedFile(
	root *os.Root,
	path string,
	pathInfo os.FileInfo,
	binding mountedFileBinding,
	scope string,
) ([]byte, error) {
	if err := validateMountedFileInfo(pathInfo, binding, scope); err != nil {
		return nil, err
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: open %s: %w", scope, binding.label, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: inspect opened %s: %w", scope, binding.label, err)
	}
	if !os.SameFile(pathInfo, openedInfo) {
		return nil, fmt.Errorf("%s: %s changed while opening", scope, binding.label)
	}
	body, err := readOpenedMountedFile(file, openedInfo, binding, scope)
	if err != nil {
		return nil, err
	}
	currentInfo, err := root.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: re-inspect %s: %w", scope, binding.label, err)
	}
	if !os.SameFile(pathInfo, currentInfo) {
		return nil, fmt.Errorf("%s: %s changed while reading", scope, binding.label)
	}
	return body, nil
}

func readAtomicWriterMountedFile(
	root *os.Root,
	binding mountedFileBinding,
	scope string,
) ([]byte, error) {
	snapshot, err := inspectAtomicWriterMountedFile(root, binding, scope)
	if err != nil {
		return nil, err
	}
	resolvedPath := filepath.Join(
		binding.atomicWriter.mountPath,
		snapshot.generation,
		binding.atomicWriter.payloadPath,
	)
	file, err := root.Open(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("%s: open %s: %w", scope, binding.label, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: inspect opened %s: %w", scope, binding.label, err)
	}
	if !os.SameFile(snapshot.fileInfo, openedInfo) {
		return nil, fmt.Errorf("%s: %s changed while opening", scope, binding.label)
	}
	if err := revalidateAtomicWriterMountedFile(root, binding, scope, snapshot); err != nil {
		return nil, err
	}
	body, err := readOpenedMountedFile(file, openedInfo, binding, scope)
	if err != nil {
		return nil, err
	}
	if err := revalidateAtomicWriterMountedFile(root, binding, scope, snapshot); err != nil {
		return nil, err
	}
	return body, nil
}

func inspectAtomicWriterMountedFile(
	root *os.Root,
	binding mountedFileBinding,
	scope string,
) (atomicWriterSnapshot, error) {
	atomic := binding.atomicWriter
	visiblePath := filepath.Join(atomic.mountPath, atomic.visibleName)
	visibleInfo, err := root.Lstat(visiblePath)
	if err != nil {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: inspect %s: %w", scope, binding.label, err)
	}
	if visibleInfo.Mode()&os.ModeSymlink == 0 {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: %s changed while inspecting", scope, binding.label)
	}
	visibleTarget, err := root.Readlink(visiblePath)
	if err != nil {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: inspect %s link: %w", scope, binding.label, err)
	}
	if visibleTarget != filepath.Join("..data", atomic.visibleName) {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: %s has an unsupported symlink layout", scope, binding.label)
	}

	dataPath := filepath.Join(atomic.mountPath, "..data")
	dataInfo, err := root.Lstat(dataPath)
	if err != nil {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: inspect %s data link: %w", scope, binding.label, err)
	}
	if dataInfo.Mode()&os.ModeSymlink == 0 {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: %s data link must be a symlink", scope, binding.label)
	}
	generation, err := root.Readlink(dataPath)
	if err != nil {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: inspect %s data target: %w", scope, binding.label, err)
	}
	if !isAtomicWriterGeneration(generation) {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: %s data link escapes its mount", scope, binding.label)
	}
	generationPath := filepath.Join(atomic.mountPath, generation)
	generationInfo, err := root.Lstat(generationPath)
	if err != nil {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: inspect %s generation: %w", scope, binding.label, err)
	}
	if generationInfo.Mode()&os.ModeSymlink != 0 || !generationInfo.IsDir() {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: %s generation must be a directory", scope, binding.label)
	}

	targetDirectory := filepath.Join(generationPath, filepath.Dir(atomic.payloadPath))
	targetDirs, err := inspectStableMountedAncestors(root, binding.label, targetDirectory)
	if err != nil {
		return atomicWriterSnapshot{}, err
	}
	targetPath := filepath.Join(generationPath, atomic.payloadPath)
	fileInfo, err := root.Lstat(targetPath)
	if err != nil {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: inspect %s target: %w", scope, binding.label, err)
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 {
		return atomicWriterSnapshot{}, fmt.Errorf("%s: %s target must not be a symlink", scope, binding.label)
	}
	if err := validateMountedFileInfo(fileInfo, binding, scope); err != nil {
		return atomicWriterSnapshot{}, err
	}
	return atomicWriterSnapshot{
		visibleTarget:  visibleTarget,
		generation:     generation,
		generationInfo: generationInfo,
		targetDirs:     targetDirs,
		fileInfo:       fileInfo,
	}, nil
}

func revalidateAtomicWriterMountedFile(
	root *os.Root,
	binding mountedFileBinding,
	scope string,
	expected atomicWriterSnapshot,
) error {
	current, err := inspectAtomicWriterMountedFile(root, binding, scope)
	if err != nil {
		return err
	}
	if current.visibleTarget != expected.visibleTarget ||
		current.generation != expected.generation ||
		!os.SameFile(current.generationInfo, expected.generationInfo) ||
		!os.SameFile(current.fileInfo, expected.fileInfo) ||
		!sameMountedAncestors(current.targetDirs, expected.targetDirs) {
		return fmt.Errorf("%s: %s changed while reading", scope, binding.label)
	}
	return nil
}

func sameMountedAncestors(left, right []credentialAncestor) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].path != right[index].path || !os.SameFile(left[index].info, right[index].info) {
			return false
		}
	}
	return true
}

func validateMountedFileInfo(
	info os.FileInfo,
	binding mountedFileBinding,
	scope string,
) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: %s must resolve to a regular file", scope, binding.label)
	}
	if binding.requirePrivate && info.Mode().Perm()&0007 != 0 {
		return fmt.Errorf("%s: %s must not be accessible by other users", scope, binding.label)
	}
	if info.Size() < 0 || info.Size() > binding.maximumBytes ||
		(binding.requireNonEmpty && info.Size() == 0) {
		return mountedFileSizeError(binding, scope)
	}
	return nil
}

func readOpenedMountedFile(
	file *os.File,
	openedInfo os.FileInfo,
	binding mountedFileBinding,
	scope string,
) ([]byte, error) {
	if err := validateMountedFileInfo(openedInfo, binding, scope); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(file, binding.maximumBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read %s: %w", scope, binding.label, err)
	}
	if int64(len(body)) > binding.maximumBytes ||
		(binding.requireNonEmpty && len(body) == 0) {
		return nil, mountedFileSizeError(binding, scope)
	}
	return body, nil
}

func mountedFileSizeError(binding mountedFileBinding, scope string) error {
	if binding.requireNonEmpty {
		return fmt.Errorf("%s: %s must contain 1-%d bytes", scope, binding.label, binding.maximumBytes)
	}
	return fmt.Errorf("%s: %s must contain at most %d bytes", scope, binding.label, binding.maximumBytes)
}
