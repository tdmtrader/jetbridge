package detached

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar"
)

// RunIdentified is a parsed result that names the Run which produced it.
type RunIdentified interface{ RunID() int }

// Result verifies the archive against its immutable Run binding before the
// workload parses it, then requires the parsed result to name the same Run. No
// caller-supplied tree, claim or credential is accepted. The tree passed to
// parse is valid only for the duration of the call.
func (c *Client) Result(ctx context.Context, handle Handle, name string, parse func(tree fs.FS) (RunIdentified, error)) (RunIdentified, error) {
	if parse == nil {
		return nil, errors.New("a result parser is required")
	}
	run, err := c.Status(ctx, handle)
	if err != nil {
		return nil, err
	}
	if run.Status == atc.RunStatusRunning {
		return nil, errors.New("Run is still running")
	}
	if run.Terminal == nil || run.Terminal.Status != atc.RunStatusSucceeded {
		return nil, errors.New("Run has no successful terminal result")
	}
	binding, found := run.Terminal.Results[name]
	if !found {
		return nil, errors.New("Run has no result with that name")
	}
	endpoint, err := c.runEndpoint(handle)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/results/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/x-tar")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JetBridge returned HTTP %d", response.StatusCode)
	}
	const maxArchiveBytes int64 = 64 << 20
	if response.ContentLength < 0 || response.ContentLength > maxArchiveBytes {
		return nil, errors.New("result archive has an invalid or excessive size")
	}
	tree, err := (hangar.Canonicalizer{MaxContentBytes: 32 << 20, MaxEntries: 32}).Capture(ctx, io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	if tree.Digest != binding.Ref.Digest || tree.ByteSize != response.ContentLength {
		return nil, errors.New("result archive does not match the retained binding")
	}
	root, err := os.OpenRoot(tree.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	parsed, err := parse(root.FS())
	if err != nil {
		return nil, err
	}
	if parsed == nil || parsed.RunID() != run.ID {
		return nil, errors.New("result belongs to a different Run")
	}
	return parsed, nil
}

// ReadResultFile reads one bounded regular file from a verified result tree.
// Symlinks and other non-regular entries are refused, never followed.
func ReadResultFile(tree fs.FS, name string, limit int64) ([]byte, error) {
	info, err := fs.Lstat(tree, name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%s must be a bounded regular file", name)
	}
	file, err := tree.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s must be a bounded regular file", name)
	}
	return body, nil
}
