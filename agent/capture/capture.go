// Package capture seals committed Git trees into closed, content-addressed
// directories and verifies them again on load. Every sealed input format
// (review-input/v1, implement-input/v1) is built from these helpers, so all
// of them share one path policy, one size policy and one leaf digest.
//
// It has no dependency on JetBridge's scheduler, database or artifact runtime.
package capture

import (
	"bufio"
	"bytes"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// MaxFileBytes bounds every captured leaf file.
	MaxFileBytes = 16 << 20
	// MaxTotalBytes bounds a whole sealed input.
	MaxTotalBytes = 256 << 20
)

// CommitPattern matches a full SHA-1 or SHA-256 object name.
var CommitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// File is one captured leaf. Its JSON shape is part of every input digest and
// must not change.
type File struct {
	Side   string `json:"side"`
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// SupportedMode reports whether a captured Git mode is accepted: regular,
// executable, or a symlink captured as inert target text.
func SupportedMode(mode string) bool {
	return mode == "100644" || mode == "100755" || mode == "120000"
}

// Digest is the leaf digest: lowercase hex SHA-256.
func Digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// SafePath accepts only clean, relative, slash-separated UTF-8 names with no
// traversal, backslash, NUL or line break.
func SafePath(name string) bool {
	return name != "." && name != ".." && name != "" && path.Clean(name) == name &&
		!strings.HasPrefix(name, "../") && !path.IsAbs(name) &&
		!strings.ContainsAny(name, "\\\x00\r\n") && utf8.ValidString(name)
}

// Text reports whether data is UTF-8 without NUL bytes.
func Text(data []byte) bool { return utf8.Valid(data) && !bytes.ContainsRune(data, 0) }

// GitEnvironment gives Git no caller-supplied GIT_* variables, global config,
// hooks, credential helpers or replacement objects. Capture reads raw objects,
// never a checkout or git archive (which applies export-ignore/export-subst).
func GitEnvironment() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"}
}

// GitCommand runs git in repo with hooks and fsmonitor disabled.
func GitCommand(ctx context.Context, repo string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	c.Env = GitEnvironment()
	return c
}

// GitOutput runs GitCommand and returns bounded stdout.
func GitOutput(ctx context.Context, repo string, args ...string) ([]byte, error) {
	c := GitCommand(ctx, repo, args...)
	var out limitedBuffer
	c.Stdout = &out
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return out.Bytes(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxTotalBytes {
		return 0, errors.New("captured data exceeds 256 MiB")
	}
	return b.Buffer.Write(p)
}

// Within reports whether child is parent or below it.
func Within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Repository resolves the top level of the worktree containing repo.
func Repository(ctx context.Context, repo string) (string, error) {
	b, err := GitOutput(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(b)))
}

// Output resolves a new publication path. It must not exist and must be
// outside the repository, so a capture never becomes part of its own input.
// It returns the absolute output and its resolved parent directory.
func Output(repo, output string) (string, string, error) {
	output, err := filepath.Abs(output)
	if err != nil {
		return "", "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return "", "", err
	}
	output = filepath.Join(parent, filepath.Base(output))
	if Within(repo, output) {
		return "", "", errors.New("output must be outside the repository")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		return "", "", errors.New("output must not exist")
	}
	return output, parent, nil
}

// RequireClean refuses a worktree with any staged, unstaged or untracked
// change, including inside submodules.
func RequireClean(ctx context.Context, repo string) error {
	status, err := GitOutput(ctx, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return errors.New("repository has uncommitted changes; commit them before capture")
	}
	return nil
}

// ResolveCommit resolves ref to a full commit object name.
func ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	b, err := GitOutput(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	id := strings.TrimSpace(string(b))
	if err != nil || !CommitPattern.MatchString(id) {
		return "", fmt.Errorf("%q does not resolve to a commit", ref)
	}
	return id, nil
}

// CaptureTree writes every blob of commit below dest/side and returns the
// sorted leaf inventory. Only regular, executable and symlink entries are
// accepted; symlinks become inert text holding their target, and nothing is
// materialized executable. Submodules and Git LFS pointers are refused.
func CaptureTree(ctx context.Context, repo, commit, side, dest string, total *int64) ([]File, error) {
	listing, err := GitOutput(ctx, repo, "ls-tree", "-rz", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(dest, side), 0700); err != nil {
		return nil, err
	}
	c := GitCommand(ctx, repo, "cat-file", "--batch")
	in, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	defer func() { in.Close(); c.Process.Kill(); c.Wait() }()
	r := bufio.NewReader(out)
	files := []File{}
	for _, entry := range bytes.Split(listing, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		parts := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, errors.New("invalid git tree entry")
		}
		header := strings.Fields(string(parts[0]))
		name := string(parts[1])
		if len(header) != 3 || !SafePath(name) {
			return nil, errors.New("unsupported git path")
		}
		mode, oid := header[0], header[2]
		if mode == "160000" {
			return nil, fmt.Errorf("submodules are unsupported: %s", name)
		}
		if header[1] != "blob" || !SupportedMode(mode) {
			return nil, errors.New("unsupported tree entry")
		}
		if _, err := fmt.Fprintln(in, oid); err != nil {
			return nil, err
		}
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != oid || fields[1] != "blob" {
			return nil, errors.New("invalid git blob response")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > MaxFileBytes || *total+size > MaxTotalBytes {
			return nil, errors.New("captured input exceeds 16 MiB per file or 256 MiB total")
		}
		b := make([]byte, size+1)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		b = b[:size]
		if bytes.HasPrefix(b, []byte("version https://git-lfs.github.com/spec/v1")) {
			return nil, fmt.Errorf("Git LFS pointers are unsupported: %s", name)
		}
		file := filepath.Join(dest, side, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return nil, err
		}
		// Symlinks are inert text containing their target; mode preserves their
		// Git identity. Never materialize a link or an executable input.
		if err := os.WriteFile(file, b, 0600); err != nil {
			return nil, err
		}
		files = append(files, File{side, name, mode, Digest(b), size})
		*total += size
	}
	if err := in.Close(); err != nil {
		return nil, err
	}
	if err := c.Wait(); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// ReadFileBounded reads a regular file no larger than limit.
func ReadFileBounded(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errors.New("expected a bounded regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("file exceeds limit")
	}
	return b, err
}

// ReadRootFile reads a regular file no larger than limit without leaving root.
func ReadRootFile(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errors.New("expected bounded regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("file exceeds limit")
	}
	return b, err
}

// DecodeStrict decodes exactly one JSON value with no unknown fields.
func DecodeStrict(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

// Entry is one expected leaf of a sealed directory.
type Entry struct {
	Digest string
	// Size is the recorded size, or -1 when the manifest records none.
	Size int64
	// Limit bounds the read; zero means MaxFileBytes.
	Limit int64
}

// Inventory is the closed set of files a sealed directory may hold beside its
// manifest.json. Dirs lists top-level directories that may exist even when no
// expected file lies below them.
type Inventory struct {
	Files map[string]Entry
	Dirs  []string
}

// Verify walks root and requires exactly manifest.json plus the inventory,
// every file regular, bounded, and matching its recorded digest and size. A
// sealed input cannot smuggle auxiliary files, links or directories.
func Verify(root *os.Root, inv Inventory) error {
	allowedDirs := map[string]bool{".": true}
	for _, d := range inv.Dirs {
		allowedDirs[d] = true
	}
	seen := map[string]bool{}
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if allowedDirs[name] {
				return nil
			}
			for file := range inv.Files {
				if strings.HasPrefix(file, name+"/") {
					return nil
				}
			}
			return errors.New("unexpected directory in sealed input")
		}
		if !entry.Type().IsRegular() {
			return errors.New("sealed input contains a non-regular file")
		}
		if name == "manifest.json" {
			return nil
		}
		want, ok := inv.Files[name]
		if !ok {
			return errors.New("unexpected file in sealed input")
		}
		limit := want.Limit
		if limit == 0 {
			limit = MaxFileBytes
		}
		b, err := ReadRootFile(root, name, limit)
		if err != nil {
			return err
		}
		total += int64(len(b))
		if total > MaxTotalBytes {
			return errors.New("sealed input exceeds 256 MiB")
		}
		if Digest(b) != want.Digest {
			return fmt.Errorf("digest mismatch: %s", name)
		}
		if want.Size >= 0 && int64(len(b)) != want.Size {
			return errors.New("file size mismatch")
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(inv.Files) {
		return errors.New("sealed input is missing files")
	}
	return nil
}
