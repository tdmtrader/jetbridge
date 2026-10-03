package session

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/agent/capture"
)

// CheckRequest refuses a worker invocation that lacks what every workload's
// session needs: a model, the owner's credential stream, a positive timeout,
// and a positive Run ID when one is given.
func CheckRequest(model string, auth io.ReadCloser, timeout time.Duration, runID *int) error {
	if timeout <= 0 || model == "" || auth == nil {
		return errors.New("model, credential stream and positive timeout are required")
	}
	if runID != nil && *runID < 1 {
		return errors.New("run ID must be positive")
	}
	return nil
}

// Paths are a worker's input, output and runtime directories, resolved
// through symlinks and proven disjoint.
type Paths struct {
	Input, Output, Runtime string
	// parent is Output's resolved parent: the result is staged there, on
	// the same filesystem, so publishing it is one rename.
	parent string
}

// ResolvePaths resolves a worker's directories and refuses any two that
// overlap, so the provider session can neither read the result it is about
// to produce nor leave anything in the sealed input. The output must not
// exist or be an empty directory: a published result is never replaced.
func ResolvePaths(input, output, runtime string) (Paths, error) {
	// Every path is made absolute before it is resolved: the overlap check
	// cannot relate a relative path to an absolute one.
	resolve := func(path string) (string, error) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(abs)
	}
	var p Paths
	var err error
	if p.Input, err = resolve(input); err != nil {
		return Paths{}, err
	}
	if output, err = filepath.Abs(output); err != nil {
		return Paths{}, err
	}
	if p.parent, err = resolve(filepath.Dir(output)); err != nil {
		return Paths{}, err
	}
	p.Output = filepath.Join(p.parent, filepath.Base(output))
	if p.Runtime, err = resolve(runtime); err != nil {
		return Paths{}, err
	}
	dirs := []string{p.Input, p.Output, p.Runtime}
	for i, a := range dirs {
		for _, b := range dirs[i+1:] {
			if capture.Within(a, b) || capture.Within(b, a) {
				return Paths{}, errors.New("input, output and runtime must be separate directories")
			}
		}
	}
	if st, err := os.Lstat(p.Output); err == nil {
		if !st.IsDir() {
			return Paths{}, errors.New("output must be a new or empty directory")
		}
		entries, err := os.ReadDir(p.Output)
		if err != nil {
			return Paths{}, err
		}
		if len(entries) > 0 {
			return Paths{}, errors.New("output must be empty; a published result is immutable")
		}
	} else if !os.IsNotExist(err) {
		return Paths{}, err
	}
	return p, nil
}

// Publish writes files into a private stage beside the output and renames
// the stage into place, so a reader sees every file or none.
func (p Paths) Publish(files map[string][]byte) error {
	stage, err := os.MkdirTemp(p.parent, ".jb-output-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, data := range files {
		if name == "" || filepath.Base(name) != name {
			return errors.New("a published result file must be a plain name")
		}
		if err := os.WriteFile(filepath.Join(stage, name), data, 0600); err != nil {
			return err
		}
	}
	return os.Rename(stage, p.Output)
}
