package implement

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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
// repository file can shadow them, and no workspace edit can name them.
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

// Workspace serves the live workspace to the provider: list, read and
// search, and the only operations that change it, write, replace and
// delete. The provider itself runs read-only and has no tool that writes, so
// these handlers are where an edit is confined, and they refuse before
// anything changes: every operation goes through an os.Root on the
// workspace, names only an ordinary relative path outside .git, and touches
// only regular files. The snapshot's read-only inputs are held in memory and
// served under absolute names no edit can name.
type Workspace struct {
	root   *os.Root
	inputs map[string][]byte
	// written is the content edits have written so far, against budget,
	// which is maxWorkspaceWrites.
	written, budget int64
}

// maxWorkspaceWrites bounds the content one session's edits may write, so a
// session cannot fill the memory-backed runtime it shares with its credential.
const maxWorkspaceWrites = 4 * MaxPatchBytes

// NewWorkspace serves dir and, under their absolute names, inputs.
func NewWorkspace(dir string, inputs map[string][]byte) (*Workspace, error) {
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
	return &Workspace{root: root, inputs: inputs, budget: maxWorkspaceWrites}, nil
}

func (w *Workspace) Close() error { return w.root.Close() }

func (w *Workspace) paths() ([]string, error) {
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

func (w *Workspace) read(name string) ([]byte, error) {
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

// editable refuses a path an edit may not name: anything but an ordinary
// relative path, a .git component in any case, or a path whose existing
// components are not plain directories leading to, at most, a regular file.
// It reports whether the file exists, and its permissions if so.
func (w *Workspace) editable(name string) (exists bool, perm os.FileMode, err error) {
	if _, ok := w.inputs[name]; ok {
		return false, 0, errors.New("the read-only inputs cannot be edited")
	}
	if !capture.SafePath(name) {
		return false, 0, errors.New("path is outside the workspace")
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if strings.EqualFold(part, ".git") {
			return false, 0, errors.New("the repository's .git cannot be edited")
		}
	}
	for i := range parts {
		st, err := w.root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, fs.ErrNotExist) {
			return false, 0, nil
		}
		if err != nil {
			return false, 0, errors.New("cannot inspect workspace path")
		}
		if i < len(parts)-1 && !st.IsDir() {
			return false, 0, errors.New("a parent of the path is not a directory")
		}
		if i == len(parts)-1 {
			if !st.Mode().IsRegular() {
				return false, 0, errors.New("path is not a regular workspace file")
			}
			return true, st.Mode().Perm(), nil
		}
	}
	return false, 0, nil
}

// put replaces name with data in one rename, so a refused or failed write
// leaves the previous content. A new file is not executable; an existing
// file keeps its permissions.
func (w *Workspace) put(name string, data []byte, perm os.FileMode) error {
	if len(data) > capture.MaxFileBytes || !capture.Text(data) {
		return errors.New("content must be UTF-8 text of at most 16 MiB")
	}
	if w.written+int64(len(data)) > w.budget {
		return errors.New("this session has written all the content it may; report this limitation")
	}
	if dir := path.Dir(name); dir != "." {
		if err := w.root.MkdirAll(dir, 0700); err != nil {
			return errors.New("cannot create the file's directory")
		}
	}
	stage := path.Join(path.Dir(name), ".jb-edit-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	f, err := w.root.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return errors.New("cannot write workspace file")
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Close())
	if err == nil {
		// The file mode must be exact; OpenFile applies the umask.
		err = w.root.Chmod(stage, perm)
	}
	if err == nil {
		err = w.root.Rename(stage, name)
	}
	if err != nil {
		w.root.Remove(stage)
		return errors.New("cannot write workspace file")
	}
	w.written += int64(len(data))
	return nil
}

func (w *Workspace) edit(tool string, args json.RawMessage) (any, error) {
	var p struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
		Old     *string `json:"old"`
		New     *string `json:"new"`
	}
	if err := capture.DecodeStrict(args, &p); err != nil {
		return nil, fmt.Errorf("invalid %s arguments", tool)
	}
	exists, perm, err := w.editable(p.Path)
	if err != nil {
		return nil, err
	}
	switch tool {
	case "write":
		if p.Content == nil || p.Old != nil || p.New != nil {
			return nil, errors.New("write takes path and content")
		}
		if !exists {
			perm = 0600
		}
		if err := w.put(p.Path, []byte(*p.Content), perm); err != nil {
			return nil, err
		}
		return map[string]any{"path": p.Path, "created": !exists}, nil
	case "replace":
		if p.Old == nil || p.New == nil || p.Content != nil || *p.Old == "" {
			return nil, errors.New("replace takes path, a non-empty old and new")
		}
		if !exists {
			return nil, errors.New("path is not a workspace file")
		}
		current, err := w.read(p.Path)
		if err != nil {
			return nil, err
		}
		if n := strings.Count(string(current), *p.Old); n != 1 {
			return nil, fmt.Errorf("old occurs %d times in %s; it must occur exactly once", n, p.Path)
		}
		if err := w.put(p.Path, []byte(strings.Replace(string(current), *p.Old, *p.New, 1)), perm); err != nil {
			return nil, err
		}
		return map[string]any{"path": p.Path, "replaced": true}, nil
	case "delete":
		if p.Content != nil || p.Old != nil || p.New != nil {
			return nil, errors.New("delete takes path only")
		}
		if !exists {
			return nil, errors.New("path is not a workspace file")
		}
		if err := w.root.Remove(p.Path); err != nil {
			return nil, errors.New("cannot delete workspace file")
		}
		return map[string]any{"path": p.Path, "deleted": true}, nil
	}
	return nil, errors.New("unknown workspace tool")
}

func (w *Workspace) Call(tool string, args json.RawMessage) (any, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	switch tool {
	case "write", "replace", "delete":
		return w.edit(tool, args)
	}
	return session.TextFiles{Paths: w.paths, Read: w.read, Binary: errBinaryWorkspace}.Call(tool, args)
}

// WorkspaceTools are the tools the workspace server offers, by name;
// workspaceWrites are those that change the workspace.
var (
	WorkspaceTools  = []string{"list", "read", "search", "write", "replace", "delete"}
	workspaceWrites = []string{"write", "replace", "delete"}
)

var workspaceTools = func() json.RawMessage {
	var tools []json.RawMessage
	if err := json.Unmarshal(session.TextTools(session.TextToolDescriptions{
		List:   "List files in the writable workspace: the repository at the snapshot's base commit, including your edits so far. Read-only inputs, when present, are listed under /input/ and are not in the workspace.",
		Read:   "Read numbered UTF-8 lines from a workspace file, reflecting your edits so far, or from a read-only input under /input/. Binary files return a limitation. Use next_line to continue.",
		Search: "Find literal text in workspace files and read-only inputs; returns file/line locations. Binary files are skipped. Use prefix to narrow and next_offset when more is true.",
	}), &tools); err != nil {
		panic(err)
	}
	edit := `{"annotations":{"readOnlyHint":false,"destructiveHint":true,"idempotentHint":false,"openWorldHint":false},`
	tools = append(tools,
		json.RawMessage(edit+`"name":"write","description":"Create a workspace file, or replace its whole content, with UTF-8 text. Paths are relative to the workspace root; .git and the read-only inputs cannot be written. A new file is not executable; an existing file keeps its mode.","inputSchema":{"type":"object","required":["path","content"],"properties":{"path":{"type":"string"},"content":{"type":"string"}},"additionalProperties":false}}`),
		json.RawMessage(edit+`"name":"replace","description":"Replace the one occurrence of old in a workspace file with new. old must occur exactly once; read the file first and include enough surrounding text to make it unique.","inputSchema":{"type":"object","required":["path","old","new"],"properties":{"path":{"type":"string"},"old":{"type":"string","minLength":1},"new":{"type":"string"}},"additionalProperties":false}}`),
		json.RawMessage(edit+`"name":"delete","description":"Delete a workspace file.","inputSchema":{"type":"object","required":["path"],"properties":{"path":{"type":"string"}},"additionalProperties":false}}`),
	)
	data, err := json.Marshal(tools)
	if err != nil {
		panic(err)
	}
	return data
}()

// ServeWorkspaceTools is the private stdio MCP server through which the
// provider inspects and edits the workspace and reads the snapshot's
// read-only inputs. It has no credentials and no execution.
func ServeWorkspaceTools(dir string, inputs map[string][]byte, in io.Reader, out io.Writer) error {
	w, err := NewWorkspace(dir, inputs)
	if err != nil {
		return err
	}
	defer w.Close()
	return session.ServeTools("jetbridge-implement-workspace", workspaceTools, w.Call, in, out)
}
