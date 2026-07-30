package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/concourse/concourse/agent/broker/workspace"
)

// MaxWorkspacePatchBytes leaves bounded headroom for base64 expansion and the
// rest of the JSON envelope beneath the private authority API's 4 MiB limit.
const MaxWorkspacePatchBytes = 11 << 18 // 2.75 MiB
const workspaceCapturePolicyRevision = "git-workspace-capture/v2"

var gitObjectIDPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

// WorkspaceCapture is the complete, bounded candidate crossing from the
// broker-worker to ATC. Repository identity and local paths stay authority-
// owned/local respectively.
type WorkspaceCapture struct {
	BaseCommit     string `json:"base_commit"`
	BaseTree       string `json:"base_tree"`
	ResultTree     string `json:"result_tree"`
	Patch          []byte `json:"patch"`
	PatchDigest    string `json:"patch_digest"`
	EntryCount     int    `json:"entry_count"`
	PolicyRevision string `json:"policy_revision"`
}

func WorkspaceCaptureFromResult(result workspace.Result) (WorkspaceCapture, error) {
	capture := WorkspaceCapture{
		BaseCommit: result.BaseCommit, BaseTree: result.BaseTree,
		ResultTree: result.ResultTree, Patch: append([]byte(nil), result.Patch...),
		PatchDigest: result.PatchDigest, EntryCount: result.EntryCount,
		PolicyRevision: result.PolicyRevision,
	}
	if err := capture.Validate(); err != nil {
		return WorkspaceCapture{}, err
	}
	return capture, nil
}

func (capture WorkspaceCapture) Validate() error {
	if !gitObjectIDPattern.MatchString(capture.BaseCommit) ||
		!gitObjectIDPattern.MatchString(capture.BaseTree) ||
		!gitObjectIDPattern.MatchString(capture.ResultTree) ||
		len(capture.BaseCommit) != len(capture.BaseTree) ||
		len(capture.BaseCommit) != len(capture.ResultTree) {
		return fmt.Errorf("broker workspace capture: exact Git object identities are required")
	}
	if len(capture.Patch) > MaxWorkspacePatchBytes {
		return fmt.Errorf("broker workspace capture: patch exceeds %d-byte authority limit", MaxWorkspacePatchBytes)
	}
	sum := sha256.Sum256(capture.Patch)
	if capture.PatchDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		return fmt.Errorf("broker workspace capture: patch digest does not match bytes")
	}
	if capture.EntryCount < 0 || capture.EntryCount > 10_000_000 ||
		capture.PolicyRevision != workspaceCapturePolicyRevision {
		return fmt.Errorf("broker workspace capture: bounded entry count and policy revision are required")
	}
	return nil
}

type WorkspacePreparer interface {
	CaptureWorkspace(context.Context) (workspace.Result, error)
}
