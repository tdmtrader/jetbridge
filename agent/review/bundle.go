// Package review captures committed changes and validates inspection-only reviews.
// It has no dependency on JetBridge's scheduler, database or artifact runtime.
package review

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
	inputVersion   = "review-input/v1"
	maxFileBytes   = 16 << 20
	maxBundleBytes = 256 << 20
)

var commitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

type CaptureOptions struct{ Repo, Base, Head, Plan, Output string }

type File struct {
	Side   string `json:"side"`
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Version    string  `json:"version"`
	BaseCommit string  `json:"base_commit"`
	HeadCommit string  `json:"head_commit"`
	Files      []File  `json:"files"`
	DiffDigest string  `json:"diff_digest"`
	PlanDigest *string `json:"plan_digest"`
}

type Bundle struct {
	Dir      string
	Manifest Manifest
	Digest   string
}

func digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func safePath(name string) bool {
	return name != "." && name != ".." && name != "" && path.Clean(name) == name &&
		!strings.HasPrefix(name, "../") && !path.IsAbs(name) &&
		!strings.ContainsAny(name, "\\\x00\r\n") && utf8.ValidString(name)
}

// Git gets no caller-supplied GIT_* variables, global config, hooks, credential
// helpers or replacement objects. Capture reads raw objects, never a checkout or
// git archive (which applies export-ignore/export-subst attributes).
func gitEnvironment() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"}
}

func gitCommand(ctx context.Context, repo string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	c.Env = gitEnvironment()
	return c
}

func gitOutput(ctx context.Context, repo string, args ...string) ([]byte, error) {
	c := gitCommand(ctx, repo, args...)
	var out limitedBuffer
	c.Stdout = &out
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return out.Bytes(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxBundleBytes {
		return 0, errors.New("review data exceeds 256 MiB")
	}
	return b.Buffer.Write(p)
}

// Capture publishes a new directory only after both trees and optional plan are
// captured. Output must not exist and must be outside the submitted worktree.
func Capture(ctx context.Context, opts CaptureOptions) (*Bundle, error) {
	if opts.Base == "" || opts.Repo == "" || opts.Output == "" {
		return nil, errors.New("repo, base and output are required")
	}
	if opts.Head == "" {
		opts.Head = "HEAD"
	}
	repoBytes, err := gitOutput(ctx, opts.Repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	repo, err := filepath.EvalSymlinks(strings.TrimSpace(string(repoBytes)))
	if err != nil {
		return nil, err
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return nil, err
	}
	output = filepath.Join(parent, filepath.Base(output))
	if within(repo, output) {
		return nil, errors.New("output must be outside the repository")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		return nil, errors.New("output must not exist")
	}
	status, err := gitOutput(ctx, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return nil, err
	}
	if len(status) != 0 {
		return nil, errors.New("repository has uncommitted changes; commit them before capture")
	}
	resolve := func(ref string) (string, error) {
		b, err := gitOutput(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		id := strings.TrimSpace(string(b))
		if err != nil || !commitPattern.MatchString(id) {
			return "", errors.New("base and head must resolve to commits")
		}
		return id, nil
	}
	base, err := resolve(opts.Base)
	if err != nil {
		return nil, err
	}
	head, err := resolve(opts.Head)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(parent, ".review-input-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	m := Manifest{Version: inputVersion, BaseCommit: base, HeadCommit: head, Files: []File{}}
	var total int64
	for _, tree := range []struct{ side, commit string }{{"base", base}, {"head", head}} {
		files, err := captureTree(ctx, repo, tree.commit, tree.side, stage, &total)
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, files...)
	}
	diff, err := gitOutput(ctx, repo, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", "--full-index", "--no-color", "--diff-algorithm=myers", "--src-prefix=a/", "--dst-prefix=b/", base, head, "--")
	if err != nil {
		return nil, err
	}
	m.DiffDigest = digest(diff)
	if err := os.WriteFile(filepath.Join(stage, "change.diff"), diff, 0600); err != nil {
		return nil, err
	}
	if opts.Plan != "" {
		b, err := readFileBounded(opts.Plan, maxFileBytes)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
			return nil, errors.New("plan must be UTF-8 text")
		}
		d := digest(b)
		m.PlanDigest = &d
		if err := os.WriteFile(filepath.Join(stage, "plan.md"), b, 0600); err != nil {
			return nil, err
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), data, 0600); err != nil {
		return nil, err
	}
	bundle, err := LoadBundle(stage)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(stage, output); err != nil {
		return nil, err
	}
	bundle.Dir = output
	return bundle, nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func captureTree(ctx context.Context, repo, commit, side, dest string, total *int64) ([]File, error) {
	listing, err := gitOutput(ctx, repo, "ls-tree", "-rz", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(dest, side), 0700); err != nil {
		return nil, err
	}
	c := gitCommand(ctx, repo, "cat-file", "--batch")
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
		if len(header) != 3 || !safePath(name) {
			return nil, errors.New("unsupported git path")
		}
		mode, oid := header[0], header[2]
		if mode == "160000" {
			return nil, fmt.Errorf("submodules are unsupported: %s", name)
		}
		if header[1] != "blob" || (mode != "100644" && mode != "100755" && mode != "120000") {
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
		if err != nil || size < 0 || size > maxFileBytes || *total+size > maxBundleBytes {
			return nil, errors.New("review input exceeds 16 MiB per file or 256 MiB total")
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
		files = append(files, File{side, name, mode, digest(b), size})
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

func readFileBounded(name string, limit int64) ([]byte, error) {
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

// LoadBundle verifies the entire closed input tree, including its inventory,
// hashes and types. A bundle cannot smuggle auxiliary files into the worker.
func LoadBundle(dir string) (*Bundle, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := readRootFile(root, "manifest.json", maxFileBytes)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := decodeStrict(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Version != inputVersion || !commitPattern.MatchString(m.BaseCommit) || !commitPattern.MatchString(m.HeadCommit) || m.Files == nil {
		return nil, errors.New("invalid manifest")
	}
	expected := map[string]string{"change.diff": m.DiffDigest}
	if m.PlanDigest != nil {
		expected["plan.md"] = *m.PlanDigest
	}
	last := ""
	for _, file := range m.Files {
		key := file.Side + "/" + file.Path
		if (file.Side != "base" && file.Side != "head") || !safePath(file.Path) || key <= last || file.Size < 0 || file.Size > maxFileBytes || (file.Mode != "100644" && file.Mode != "100755" && file.Mode != "120000") {
			return nil, errors.New("invalid manifest file")
		}
		last = key
		expected[key] = file.Digest
	}
	seen := map[string]bool{}
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if name == "." || name == "base" || name == "head" {
				return nil
			}
			for file := range expected {
				if strings.HasPrefix(file, name+"/") {
					return nil
				}
			}
			return errors.New("unexpected directory in bundle")
		}
		if !entry.Type().IsRegular() {
			return errors.New("bundle contains a non-regular file")
		}
		if name == "manifest.json" {
			return nil
		}
		want, ok := expected[name]
		if !ok {
			return errors.New("unexpected file in bundle")
		}
		limit := int64(maxFileBytes)
		if name == "change.diff" {
			limit = maxBundleBytes
		}
		b, err := readRootFile(root, name, limit)
		if err != nil {
			return err
		}
		total += int64(len(b))
		if total > maxBundleBytes {
			return errors.New("bundle exceeds 256 MiB")
		}
		if digest(b) != want {
			return fmt.Errorf("digest mismatch: %s", name)
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(seen) != len(expected) {
		return nil, errors.New("bundle is missing files")
	}
	for _, file := range m.Files {
		st, err := root.Stat(file.Side + "/" + file.Path)
		if err != nil || st.Size() != file.Size {
			return nil, errors.New("file size mismatch")
		}
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return &Bundle{Dir: dir, Manifest: m, Digest: digest(canonical)}, nil
}

func readRootFile(root *os.Root, name string, limit int64) ([]byte, error) {
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

func decodeStrict(data []byte, value any) error {
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
