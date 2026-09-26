package review

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/session"
)

// InputReader exposes only the sealed bundle inventory. It never opens a path
// outside that inventory, even if the model knows the runtime-home location.
type InputReader struct {
	root  *os.Root
	files map[string]string
	paths []string
}

var errBinaryInput = errors.New("binary input: inspect its manifest metadata and report this limitation")

func NewInputReader(b *Bundle) (*InputReader, error) {
	root, err := os.OpenRoot(b.Dir)
	if err != nil {
		return nil, err
	}
	r := &InputReader{root: root, files: map[string]string{"change.diff": b.Manifest.DiffDigest}}
	for _, f := range b.Manifest.Files {
		r.files[f.Side+"/"+f.Path] = f.Digest
	}
	if b.Manifest.PlanDigest != nil {
		r.files["plan.md"] = *b.Manifest.PlanDigest
	}
	m, err := readRootFile(root, "manifest.json", maxFileBytes)
	if err != nil {
		root.Close()
		return nil, err
	}
	r.files["manifest.json"] = digest(m)
	for p := range r.files {
		r.paths = append(r.paths, p)
	}
	sort.Strings(r.paths)
	return r, nil
}
func (r *InputReader) Close() error { return r.root.Close() }

func (r *InputReader) read(name string) ([]byte, error) {
	want, ok := r.files[name]
	if !ok || !safePath(name) {
		return nil, errors.New("path is outside the review input")
	}
	limit := int64(maxFileBytes)
	if name == "change.diff" {
		limit = maxBundleBytes
	}
	b, err := readRootFile(r.root, name, limit)
	if err != nil {
		return nil, errors.New("cannot read input file")
	}
	if digest(b) != want {
		return nil, errors.New("input changed after capture")
	}
	if !capture.Text(b) {
		return nil, errBinaryInput
	}
	return b, nil
}

func (r *InputReader) Call(tool string, args json.RawMessage) (any, error) {
	return session.TextFiles{
		Paths:  func() ([]string, error) { return r.paths, nil },
		Read:   r.read,
		Binary: errBinaryInput,
	}.Call(tool, args)
}

var inputTools = session.TextTools(session.TextToolDescriptions{
	List:   "List captured input paths, including manifest.json, change.diff, optional plan.md, and base/head trees.",
	Read:   "Read numbered UTF-8 lines from a captured file. Symlink targets are inert text; binary files return a limitation. Use next_line to continue.",
	Search: "Find literal text in captured files; returns file/line locations. Binary files are skipped. Use prefix to narrow and next_offset when more is true.",
})

// ServeInputTools is a private stdio MCP server for the worker's Codex child.
// It has no credentials, remote endpoints, execution operations or Run records.
// It is distinct from the public submit/status/result MCP surface.
func ServeInputTools(b *Bundle, in io.Reader, out io.Writer) error {
	r, err := NewInputReader(b)
	if err != nil {
		return err
	}
	defer r.Close()
	return session.ServeTools("jetbridge-review-input", inputTools, r.Call, in, out)
}
