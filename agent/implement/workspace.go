package implement

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/session"
)

// The workspace is the writable scratch copy of a snapshot's base tree. It
// lives in the session's tmpfs, beside but disjoint from the provider's
// credential home, and is destroyed with the session.

// populateWorkspace copies the verified base tree into dir. Captured symlinks
// stay inert text; only the owner-execute bit carries a file's Git mode.
func populateWorkspace(dir string, base Tree) error {
	for p, f := range base {
		name := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		perm := os.FileMode(0600)
		if f.Mode == "100755" {
			perm = 0700
		}
		if err := os.WriteFile(name, f.Data, perm); err != nil {
			return err
		}
		// WriteFile applies the umask; the mode must be exact.
		if err := os.Chmod(name, perm); err != nil {
			return err
		}
	}
	return nil
}

// readWorkspace reads the edited workspace back. Only regular files are
// publishable: a link or special file anywhere refuses the whole change.
func readWorkspace(dir string) (Tree, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	tree := Tree{}
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("workspace contains a link or special file: %s", name)
		}
		if !capture.SafePath(name) {
			return fmt.Errorf("unsupported path in workspace: %q", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := capture.ReadRootFile(root, name, capture.MaxFileBytes)
		if err != nil {
			return fmt.Errorf("%s: %w (at most 16 MiB per file)", name, err)
		}
		total += int64(len(data))
		if total > capture.MaxTotalBytes {
			return errors.New("workspace exceeds 256 MiB")
		}
		mode := "100644"
		if info.Mode().Perm()&0100 != 0 {
			mode = "100755"
		}
		tree[name] = TreeFile{Data: data, Mode: mode}
		return nil
	})
	return tree, err
}

var errBinaryWorkspace = errors.New("binary file: its content cannot be read or edited in this session; report this limitation")

// WorkspaceReader serves the live workspace to the provider through list,
// read and search. It never opens a path outside the workspace root, never
// follows a link, and has no write operation: edits go through the
// provider's own file-edit tool, which the session's trace confines.
type WorkspaceReader struct{ root *os.Root }

func NewWorkspaceReader(dir string) (*WorkspaceReader, error) {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() || !filepath.IsAbs(dir) {
		return nil, errors.New("workspace must be an absolute directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &WorkspaceReader{root: root}, nil
}

func (w *WorkspaceReader) Close() error { return w.root.Close() }

func (w *WorkspaceReader) paths() ([]string, error) {
	var names []string
	err := fs.WalkDir(w.root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() && capture.SafePath(name) {
			names = append(names, name)
		}
		return nil
	})
	sort.Strings(names)
	return names, err
}

func (w *WorkspaceReader) read(name string) ([]byte, error) {
	if !capture.SafePath(name) {
		return nil, errors.New("path is outside the workspace")
	}
	st, err := w.root.Lstat(name)
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("path is not a workspace file")
	}
	b, err := capture.ReadRootFile(w.root, name, capture.MaxFileBytes)
	if err != nil {
		return nil, errors.New("cannot read workspace file")
	}
	if !capture.Text(b) {
		return nil, errBinaryWorkspace
	}
	return b, nil
}

func (w *WorkspaceReader) Call(tool string, args json.RawMessage) (any, error) {
	return session.TextFiles{Paths: w.paths, Read: w.read, Binary: errBinaryWorkspace}.Call(tool, args)
}

var workspaceTools = session.TextTools(session.TextToolDescriptions{
	List:   "List files in the writable workspace: the repository at the snapshot's base commit, including your edits so far.",
	Read:   "Read numbered UTF-8 lines from a workspace file, reflecting your edits so far. Binary files return a limitation. Use next_line to continue.",
	Search: "Find literal text in workspace files; returns file/line locations. Binary files are skipped. Use prefix to narrow and next_offset when more is true.",
})

// ServeWorkspaceTools is the private stdio MCP server the provider uses to
// inspect the workspace. It has no credentials, execution or write operations.
func ServeWorkspaceTools(dir string, in io.Reader, out io.Writer) error {
	w, err := NewWorkspaceReader(dir)
	if err != nil {
		return err
	}
	defer w.Close()
	return session.ServeTools("jetbridge-implement-workspace", workspaceTools, w.Call, in, out)
}
