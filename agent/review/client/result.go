package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/concourse/concourse/agent/review"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar"
)

// Result verifies the archive against its immutable Run binding before parsing
// the typed report. No caller-supplied tree, claim or credential is accepted.
func (c *Client) Result(ctx context.Context, handle Handle, name string) (*review.Report, error) {
	run, err := c.Status(ctx, handle)
	if err != nil {
		return nil, err
	}
	if run.Status == atc.RunStatusRunning {
		return nil, errors.New("review Run is still running")
	}
	if run.Terminal == nil || run.Terminal.Status != atc.RunStatusSucceeded {
		return nil, errors.New("review Run has no successful terminal result")
	}
	binding, found := run.Terminal.Results[name]
	if !found {
		return nil, errors.New("review Run has no result with that name")
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
		return nil, errors.New("review result archive has an invalid or excessive size")
	}
	tree, err := (hangar.Canonicalizer{MaxContentBytes: 32 << 20, MaxEntries: 32}).Capture(ctx, io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	if tree.Digest != binding.Ref.Digest || tree.ByteSize != response.ContentLength {
		return nil, errors.New("review result archive does not match the retained binding")
	}
	root, err := os.OpenRoot(tree.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("review.json")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, errors.New("review.json must be a bounded regular file")
	}
	file, err := root.Open("review.json")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	report, err := review.ParsePublishedReport(body)
	if err != nil {
		return nil, err
	}
	if report.RunID == nil || *report.RunID != run.ID {
		return nil, errors.New("review report belongs to a different Run")
	}
	return report, nil
}
