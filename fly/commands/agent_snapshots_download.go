package commands

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

// writeAgentSnapshotDownload verifies the complete response before atomically
// publishing it at destination. A failed request therefore cannot truncate or
// replace a previously valid local archive.
func writeAgentSnapshotDownload(response *http.Response, destination string) (err error) {
	if response == nil || response.Body == nil {
		return fmt.Errorf("agent snapshot: download response body is required")
	}
	body := response.Body
	bodyClosed := false
	defer func() {
		if !bodyClosed {
			_ = body.Close()
		}
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("agent snapshot: download returned HTTP %d", response.StatusCode)
	}
	expectedSize, err := agentSnapshotContentLength(response)
	if err != nil {
		return err
	}
	expectedDigest, err := agentSnapshotETag(response.Header)
	if err != nil {
		return err
	}

	directory := filepath.Dir(destination)
	base := filepath.Base(destination)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return fmt.Errorf("agent snapshot: download destination must name a file")
	}
	temporary, err := os.CreateTemp(directory, "."+base+".snapshot-*")
	if err != nil {
		return fmt.Errorf("agent snapshot: create private download: %w", err)
	}
	temporaryName := temporary.Name()
	published := false
	defer func() {
		if !published {
			_ = temporary.Close()
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("agent snapshot: secure private download: %w", err)
	}

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), body)
	closeBodyErr := body.Close()
	bodyClosed = true
	if copyErr != nil || closeBodyErr != nil {
		return errors.Join(
			wrapAgentSnapshotError("agent snapshot: copy download", copyErr),
			wrapAgentSnapshotError("agent snapshot: close download response", closeBodyErr),
		)
	}
	if written != expectedSize {
		return fmt.Errorf("agent snapshot: downloaded %d bytes, expected %d", written, expectedSize)
	}
	actualDigest := snapshot.Digest(fmt.Sprintf("sha256:%x", hash.Sum(nil)))
	if actualDigest != expectedDigest {
		return fmt.Errorf("agent snapshot: downloaded content digest does not match ETag")
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("agent snapshot: sync private download: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("agent snapshot: close private download: %w", err)
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("agent snapshot: publish verified download: %w", err)
	}
	published = true
	return nil
}

func agentSnapshotContentLength(response *http.Response) (int64, error) {
	values := response.Header.Values("Content-Length")
	if len(values) == 0 && response.ContentLength >= 0 {
		return response.ContentLength, nil
	}
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return 0, fmt.Errorf("agent snapshot: download is missing an exact Content-Length")
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("agent snapshot: download has an invalid Content-Length")
	}
	return value, nil
}

func agentSnapshotETag(header http.Header) (snapshot.Digest, error) {
	values := header.Values("ETag")
	if len(values) != 1 {
		return "", fmt.Errorf("agent snapshot: download is missing an exact ETag")
	}
	raw := values[0]
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' || strings.Contains(raw[1:len(raw)-1], `\`) {
		return "", fmt.Errorf("agent snapshot: download has an invalid ETag")
	}
	digest, err := snapshot.ParseDigest(raw[1 : len(raw)-1])
	if err != nil {
		return "", fmt.Errorf("agent snapshot: download has an invalid ETag")
	}
	return digest, nil
}

func wrapAgentSnapshotError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}
