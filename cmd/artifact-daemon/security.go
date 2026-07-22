package main

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func createRootTempDir(root *os.Root, prefix string) (string, error) {
	var random [16]byte
	for range 128 {
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("generate temporary directory name: %w", err)
		}
		name := prefix + hex.EncodeToString(random[:])
		if err := root.Mkdir(name, 0700); err == nil {
			return name, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate a unique temporary directory")
}

func pathsOverlap(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	return first == second ||
		strings.HasPrefix(first, second+string(filepath.Separator)) ||
		strings.HasPrefix(second, first+string(filepath.Separator))
}

func validateCanonicalRelativeKey(key string) error {
	if key == "" || strings.ContainsAny(key, "\\\x00") || strings.HasPrefix(key, "/") || path.Clean(key) != key {
		return fmt.Errorf("key is not a canonical relative slash path")
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("key contains an unsafe path segment")
		}
	}
	return nil
}

func canonicalRequestKey(rPath, escapedPath, prefix string) (string, error) {
	if !strings.HasPrefix(rPath, prefix) || !strings.HasPrefix(escapedPath, prefix) {
		return "", fmt.Errorf("request path does not match prefix")
	}
	key := strings.TrimPrefix(rPath, prefix)
	escapedKey := strings.TrimPrefix(escapedPath, prefix)
	// Artifact keys are generated opaque identifiers. Requiring their escaped
	// and decoded spellings to agree removes ambiguous encoded separators and
	// dot segments before net/http has an opportunity to canonicalize them.
	if strings.Contains(escapedKey, "%") || escapedKey != key {
		return "", fmt.Errorf("encoded artifact key is not canonical")
	}
	if err := validateCanonicalRelativeKey(key); err != nil {
		return "", err
	}
	return key, nil
}

func snapshotNamespaceKey(key string) bool {
	return key == "snapshots" || strings.HasPrefix(key, "snapshots/")
}

func pathBelow(base, candidate string) (string, error) {
	if candidate == "" || strings.ContainsRune(candidate, '\x00') || !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate {
		return "", fmt.Errorf("path is not canonical and absolute")
	}
	base = filepath.Clean(base)
	rel, err := filepath.Rel(base, candidate)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path is not strictly below %q", base)
	}
	return rel, nil
}

// verifyExistingPathComponents rejects every symlink in the existing prefix
// of base/rel. Missing suffixes are permitted for destinations, but a source
// can require the final component to already exist.
func verifyExistingPathComponents(base, rel string, requireFinal bool) error {
	baseInfo, err := os.Lstat(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !requireFinal {
			return nil
		}
		return err
	}
	if baseInfo.Mode()&os.ModeSymlink != 0 || !baseInfo.IsDir() {
		return fmt.Errorf("base path is not a real directory")
	}

	current := base
	segments := strings.Split(filepath.ToSlash(rel), "/")
	for i, segment := range segments {
		current = filepath.Join(current, filepath.FromSlash(segment))
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if requireFinal {
				return err
			}
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path contains symlink component %q", current)
		}
		if i < len(segments)-1 && !info.IsDir() {
			return fmt.Errorf("path contains non-directory parent %q", current)
		}
	}
	return nil
}

func (s *Server) validateStepPath(candidate string, requireFinal bool) (string, error) {
	stepsRoot := filepath.Join(s.storagePath, "steps")
	rel, err := pathBelow(stepsRoot, candidate)
	if err != nil {
		return "", err
	}
	if err := verifyExistingPathComponents(stepsRoot, rel, requireFinal); err != nil {
		return "", err
	}
	return filepath.Join("steps", rel), nil
}

func (s *Server) validateResolveBoundary(key, dest string) error {
	if err := validateCanonicalRelativeKey(key); err != nil {
		return fmt.Errorf("invalid source key: %w", err)
	}
	if snapshotNamespaceKey(key) {
		return fmt.Errorf("snapshot namespace is not a resolvable artifact key")
	}
	if _, err := s.validateStepPath(dest, false); err != nil {
		return fmt.Errorf("invalid destination: %w", err)
	}
	return nil
}

func (s *Server) validateGenericArtifactSource(candidate string) error {
	resolved, err := resolveExistingPathPrefix(candidate)
	if err != nil {
		return err
	}
	resolvedStorageRoot, err := filepath.EvalSymlinks(s.storagePath)
	if err != nil {
		return err
	}
	if resolved != resolvedStorageRoot && !strings.HasPrefix(resolved, resolvedStorageRoot+string(filepath.Separator)) {
		return fmt.Errorf("artifact resolves outside storage root")
	}
	resolvedSnapshotRoot := filepath.Join(resolvedStorageRoot, "snapshots")
	if resolved == resolvedSnapshotRoot || strings.HasPrefix(resolved, resolvedSnapshotRoot+string(filepath.Separator)) {
		return fmt.Errorf("artifact resolves into snapshot namespace")
	}
	return nil
}

func resolveExistingPathPrefix(candidate string) (string, error) {
	cleaned := filepath.Clean(candidate)
	missing := make([]string, 0, 4)
	current := cleaned
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

type extractedPathKind uint8

const (
	extractedDirectory extractedPathKind = iota + 1
	extractedRegular
	extractedSymlink
)

func normalizedTarPath(hdr *tar.Header) (string, error) {
	name := hdr.Name
	if hdr.Typeflag == tar.TypeDir {
		name = strings.TrimSuffix(name, "/")
	}
	if err := validateCanonicalRelativeKey(name); err != nil {
		return "", fmt.Errorf("unsafe tar path %q: %w", hdr.Name, err)
	}
	return name, nil
}

func validateArchiveSymlink(name, target string) error {
	if target == "" || strings.ContainsAny(target, "\\\x00") || path.IsAbs(target) || path.Clean(target) != target {
		return fmt.Errorf("unsafe symlink target %q", target)
	}
	for _, segment := range strings.Split(target, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("unsafe symlink target %q", target)
		}
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || path.IsAbs(resolved) {
		return fmt.Errorf("symlink target escapes extraction root")
	}
	return nil
}

func ensureArchiveParents(root *os.Root, name string, materialized map[string]extractedPathKind) error {
	parent := path.Dir(name)
	if parent == "." {
		return nil
	}
	current := ""
	for _, segment := range strings.Split(parent, "/") {
		if current == "" {
			current = segment
		} else {
			current = path.Join(current, segment)
		}
		if kind, ok := materialized[current]; ok {
			if kind != extractedDirectory {
				return fmt.Errorf("tar path %q has a non-directory parent %q", name, current)
			}
			continue
		}
		if err := root.Mkdir(current, 0755); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			info, statErr := root.Lstat(current)
			if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("tar parent %q is not a real directory", current)
			}
		}
		materialized[current] = extractedDirectory
	}
	return nil
}

func copyTarEntryContext(ctx context.Context, dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := src.Read(buffer)
		if n > 0 {
			if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// extractTarAnchored extracts only directories, regular files, and safe
// relative symlinks into a caller-owned empty os.Root. It never follows a tar
// symlink while materializing a later member.
func extractTarAnchored(ctx context.Context, root *os.Root, source io.Reader) error {
	tr := tar.NewReader(source)
	materialized := make(map[string]extractedPathKind)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar header: %w", err)
		}
		name, err := normalizedTarPath(hdr)
		if err != nil {
			return err
		}
		if err := ensureArchiveParents(root, name, materialized); err != nil {
			return fmt.Errorf("prepare tar path %q: %w", name, err)
		}
		if existing, ok := materialized[name]; ok {
			if hdr.Typeflag == tar.TypeDir && existing == extractedDirectory {
				continue
			}
			return fmt.Errorf("duplicate or conflicting tar path %q", name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if hdr.Size != 0 {
				return fmt.Errorf("directory %q declares content", name)
			}
			mode := sanitizeMode(hdr.Typeflag, os.FileMode(hdr.Mode&0777))
			if err := root.Mkdir(name, mode); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create directory %q: %w", name, err)
			}
			materialized[name] = extractedDirectory
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 {
				return fmt.Errorf("regular file %q has negative size", name)
			}
			mode := sanitizeMode(tar.TypeReg, os.FileMode(hdr.Mode&0777))
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return fmt.Errorf("create regular file %q: %w", name, err)
			}
			copyErr := copyTarEntryContext(ctx, file, tr)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return fmt.Errorf("write regular file %q: %w", name, errors.Join(copyErr, closeErr))
			}
			materialized[name] = extractedRegular
		case tar.TypeSymlink:
			if hdr.Size != 0 {
				return fmt.Errorf("symlink %q declares content", name)
			}
			if err := validateArchiveSymlink(name, hdr.Linkname); err != nil {
				return fmt.Errorf("symlink %q: %w", name, err)
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return fmt.Errorf("create symlink %q: %w", name, err)
			}
			materialized[name] = extractedSymlink
		default:
			return fmt.Errorf("unsupported tar entry type %d for %q", hdr.Typeflag, name)
		}
	}
}
