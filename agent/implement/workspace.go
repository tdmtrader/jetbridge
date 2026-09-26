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

// The read-only inputs a snapshot may carry are served beside the workspace
// under absolute names. A workspace path is always relative, so no
// repository file can shadow them, and the file-edit tool, confined to the
// workspace, cannot change them.
const (
	PriorInputPath    = "/input/" + PriorFile
	FindingsInputPath = "/input/" + FindingsFile
)

// ReadOnlyInputs returns the snapshot's prior change and review findings,
// verified again, keyed by the names the workspace tools serve them under.
func (s *Snapshot) ReadOnlyInputs() (map[string][]byte, error) {
	inputs := map[string][]byte{}
	prior, err := s.Prior()
	if err != nil {
		return nil, err
	}
	if prior != nil {
		inputs[PriorInputPath] = prior
	}
	findings, err := s.Findings()
	if err != nil {
		return nil, err
	}
	if findings != nil {
		inputs[FindingsInputPath] = findings
	}
	return inputs, nil
}

// WorkspaceReader serves the live workspace to the provider through list,
// read and search. It never opens a path outside the workspace root, never
// follows a link, and has no write operation: edits go through the
// provider's own file-edit tool, which the session's trace confines. It also
// serves the snapshot's read-only inputs, held in memory.
type WorkspaceReader struct {
	root   *os.Root
	inputs map[string][]byte
}

// NewWorkspaceReader serves dir and, under their absolute names, inputs.
func NewWorkspaceReader(dir string, inputs map[string][]byte) (*WorkspaceReader, error) {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() || !filepath.IsAbs(dir) {
		return nil, errors.New("workspace must be an absolute directory")
	}
	for name, data := range inputs {
		if name != PriorInputPath && name != FindingsInputPath {
			return nil, fmt.Errorf("unknown read-only input %q", name)
		}
		if !capture.Text(data) {
			return nil, fmt.Errorf("read-only input %s is not text", name)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &WorkspaceReader{root: root, inputs: inputs}, nil
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
	for name := range w.inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, err
}

func (w *WorkspaceReader) read(name string) ([]byte, error) {
	if data, ok := w.inputs[name]; ok {
		return data, nil
	}
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
	List:   "List files in the writable workspace: the repository at the snapshot's base commit, including your edits so far. Read-only inputs, when present, are listed under /input/ and are not in the workspace.",
	Read:   "Read numbered UTF-8 lines from a workspace file, reflecting your edits so far, or from a read-only input under /input/. Binary files return a limitation. Use next_line to continue.",
	Search: "Find literal text in workspace files and read-only inputs; returns file/line locations. Binary files are skipped. Use prefix to narrow and next_offset when more is true.",
})

// ServeWorkspaceTools is the private stdio MCP server the provider uses to
// inspect the workspace and the snapshot's read-only inputs. It has no
// credentials, execution or write operations.
func ServeWorkspaceTools(dir string, inputs map[string][]byte, in io.Reader, out io.Writer) error {
	w, err := NewWorkspaceReader(dir, inputs)
	if err != nil {
		return err
	}
	defer w.Close()
	return session.ServeTools("jetbridge-implement-workspace", workspaceTools, w.Call, in, out)
}
