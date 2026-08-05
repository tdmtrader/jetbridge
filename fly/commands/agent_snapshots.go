package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	snapshotsapi "github.com/concourse/concourse/agent/api/snapshots"
	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type AgentSnapshotsCommand struct {
	Create          AgentSnapshotsCreateCommand          `command:"create" description:"Create a durable typed snapshot from a directory"`
	CaptureResource AgentSnapshotsCaptureResourceCommand `command:"capture-resource" description:"Capture one exact pipeline resource version as a typed snapshot"`
	List            AgentSnapshotsListCommand            `command:"list" description:"List snapshots visible to the selected team"`
	Show            AgentSnapshotsShowCommand            `command:"show" description:"Show snapshot metadata and provenance"`
	Download        AgentSnapshotsDownloadCommand        `command:"download" description:"Download and verify canonical snapshot content"`
	Pin             AgentSnapshotsPinCommand             `command:"pin" description:"Retain a snapshot for the current user"`
	Unpin           AgentSnapshotsUnpinCommand           `command:"unpin" description:"Release the current user's snapshot pin"`
}

const agentResourceCapturePollInterval = 250 * time.Millisecond

type AgentSnapshotsCaptureResourceCommand struct {
	Pipeline flaghelpers.PipelineFlag `short:"p" long:"pipeline" required:"true" description:"Pipeline containing the resource"`
	Resource string                   `short:"r" long:"resource" required:"true" description:"Resource name"`
	Version  *atc.Version             `short:"v" long:"version" required:"true" value-name:"KEY:VALUE" description:"Exact resource version (repeat for every field)"`
	Type     string                   `long:"type" description:"Versioned snapshot type; defaults to repository/v1 for git resources"`
	NoWait   bool                     `long:"no-wait" description:"Return after starting the durable capture execution"`
	Json     bool                     `long:"json" description:"Print command result as JSON"`
}

func (command *AgentSnapshotsCaptureResourceCommand) Validate() error {
	if _, err := command.Pipeline.Validate(); err != nil {
		return err
	}
	if command.Version == nil || len(*command.Version) == 0 {
		return fmt.Errorf("agent snapshot capture-resource: --version is required")
	}
	if strings.TrimSpace(command.Resource) == "" {
		return fmt.Errorf("agent snapshot capture-resource: --resource is required")
	}
	if command.Type != "" {
		if _, err := snapshot.ParseTypeRef(command.Type); err != nil {
			return err
		}
	}
	return nil
}

func (command *AgentSnapshotsCaptureResourceCommand) Execute([]string) error {
	if err := command.Validate(); err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	wire := snapshotsapi.ResourceCaptureRequest{
		Pipeline: command.Pipeline.Ref(), Resource: command.Resource,
		Version: cloneAgentResourceVersion(*command.Version), Type: command.Type,
	}
	path := agentSnapshotsPath(target) + "/capture-resource"
	retriedTerminalGeneration := false
	for {
		payload, err := json.Marshal(wire)
		if err != nil {
			return err
		}
		response, err := agentSnapshotRequest(context.Background(), target, http.MethodPost, path, bytes.NewReader(payload), map[string]string{
			"Content-Type": "application/json",
		})
		if err != nil {
			return err
		}
		var result resourcecapture.CaptureResult
		if err := decodeAgentSnapshotResponse(response, &result); err != nil {
			return err
		}
		terminalFailure := result.Execution.Status == db.PipelineRunFailed ||
			result.Execution.Status == db.PipelineRunErrored ||
			result.Execution.Status == db.PipelineRunAborted
		if !command.NoWait && terminalFailure && !retriedTerminalGeneration {
			wire.RetryPipelineRunID = strconv.FormatInt(result.Execution.PipelineRunID, 10)
			retriedTerminalGeneration = true
			continue
		}
		if command.NoWait || result.Snapshot != nil || result.Execution.Status != db.PipelineRunRunning {
			if err := printAgentResourceCapture(result, command.Json); err != nil {
				return err
			}
			return agentResourceCaptureOutcomeError(result)
		}
		time.Sleep(agentResourceCapturePollInterval)
	}
}

func cloneAgentResourceVersion(version atc.Version) atc.Version {
	cloned := make(atc.Version, len(version))
	for key, value := range version {
		cloned[key] = value
	}
	return cloned
}

func printAgentResourceCapture(result resourcecapture.CaptureResult, jsonOutput bool) error {
	if jsonOutput {
		return displayhelpers.JsonPrint(result)
	}
	fmt.Printf("resource capture %s\npipeline run: %d\nstatus: %s\n", result.OperationKey, result.Execution.PipelineRunID, result.Execution.Status)
	if result.Snapshot != nil {
		fmt.Printf("snapshot: %s (%s)\n", result.Snapshot.ID.String(), result.Snapshot.Type)
	}
	return nil
}

func agentResourceCaptureOutcomeError(result resourcecapture.CaptureResult) error {
	switch result.Execution.Status {
	case db.PipelineRunFailed, db.PipelineRunErrored, db.PipelineRunAborted:
		return fmt.Errorf("agent snapshot capture-resource: pipeline run %d ended %s", result.Execution.PipelineRunID, result.Execution.Status)
	case db.PipelineRunSucceeded:
		if result.Snapshot == nil {
			return fmt.Errorf("agent snapshot capture-resource: pipeline run %d completed without a finalized snapshot", result.Execution.PipelineRunID)
		}
	default:
		return nil
	}
	return nil
}

type AgentSnapshotsCreateCommand struct {
	Type string   `long:"type" required:"true" description:"Versioned snapshot type (for example review/v1)"`
	From string   `long:"from" required:"true" description:"Directory to archive"`
	Base []string `long:"base" description:"Declare a base input as NAME=SNAPSHOT-ID (repeatable); required for types whose seal gate reopens an input, such as repository-change/v1"`
	Json bool     `long:"json" description:"Print command result as JSON"`
}

func (command *AgentSnapshotsCreateCommand) Execute([]string) error {
	typeRef, err := snapshot.ParseTypeRef(command.Type)
	if err != nil {
		return err
	}
	if err := validateAgentSnapshotBaseFlags(command.Base); err != nil {
		return err
	}
	root, err := openAgentSnapshotDirectory(command.From)
	if err != nil {
		return err
	}
	defer root.Close()
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	idempotencyKey, err := newAgentSnapshotIdempotencyKey()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	tarResult := make(chan error, 1)
	go func() {
		tarErr := writeAgentSnapshotTarFromRoot(ctx, root, writer)
		_ = writer.CloseWithError(tarErr)
		tarResult <- tarErr
	}()

	query := url.Values{"type": []string{typeRef.String()}}
	if len(command.Base) != 0 {
		query["base"] = append([]string(nil), command.Base...)
	}
	path := agentSnapshotsPath(target) + "?" + query.Encode()
	response, requestErr := agentSnapshotRequest(ctx, target, http.MethodPost, path, reader, map[string]string{
		"Content-Type":    "application/x-tar",
		"Idempotency-Key": idempotencyKey,
	})
	_ = reader.CloseWithError(requestErr)
	tarErr := <-tarResult

	var manifest snapshot.Snapshot
	if err := completeAgentSnapshotCreate(response, requestErr, tarErr, &manifest); err != nil {
		return err
	}
	return printAgentSnapshotManifest(manifest, command.Json)
}

// validateAgentSnapshotBaseFlags rejects a malformed --base before any
// network call, directory walk, or archive build: the server authoritatively
// re-validates the same NAME=SNAPSHOT-ID shape and duplicate-name rule, but a
// caller who mistyped a flag should not pay for a full tar upload just to
// learn that from a 400 response.
func validateAgentSnapshotBaseFlags(bases []string) error {
	seen := make(map[string]struct{}, len(bases))
	for _, raw := range bases {
		name, id, found := strings.Cut(raw, "=")
		if !found || name == "" || id == "" {
			return fmt.Errorf("agent snapshot: --base must be NAME=SNAPSHOT-ID, got %q", raw)
		}
		if _, err := snapshot.ParseSnapshotID(id); err != nil {
			return fmt.Errorf("agent snapshot: --base %q: %w", raw, err)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("agent snapshot: --base declares %q more than once", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func openAgentSnapshotDirectory(directory string) (root *os.Root, err error) {
	opened, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("agent snapshot: open source directory: %w", err)
	}
	root = opened
	defer func() {
		if err != nil {
			err = errors.Join(err, opened.Close())
			root = nil
		}
	}()

	info, err := root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("agent snapshot: inspect source directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agent snapshot: --from must be a directory")
	}

	err = fs.WalkDir(root.FS(), ".", func(walkPath string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if walkPath == "." {
			return nil
		}
		name := filepath.ToSlash(walkPath)
		info, err := root.Lstat(walkPath)
		if err != nil {
			return fmt.Errorf("agent snapshot: inspect %q: %w", name, err)
		}
		mode := info.Mode()
		if err := validateAgentSnapshotMode(name, mode); err != nil {
			return err
		}
		switch {
		case mode.IsDir(), mode.IsRegular():
			return nil
		case mode&os.ModeSymlink != 0:
			target, err := root.Readlink(walkPath)
			if err != nil {
				return fmt.Errorf("agent snapshot: read symlink %q: %w", name, err)
			}
			if err := validateAgentSnapshotSymlink(walkPath, target); err != nil {
				return fmt.Errorf("agent snapshot: symlink %q: %w", name, err)
			}
			return nil
		default:
			return fmt.Errorf("agent snapshot: unsupported filesystem entry %q (%s)", name, mode.Type())
		}
	})
	if err != nil {
		return nil, err
	}
	return root, nil
}

func completeAgentSnapshotCreate(response *http.Response, requestErr, tarErr error, manifest *snapshot.Snapshot) error {
	if response != nil && response.StatusCode >= 400 {
		return decodeAgentSnapshotResponse(response, nil)
	}
	if tarErr != nil {
		drainAndCloseAgentSnapshotResponse(response)
		return tarErr
	}
	if requestErr != nil {
		drainAndCloseAgentSnapshotResponse(response)
		return requestErr
	}
	return decodeAgentSnapshotResponse(response, manifest)
}

func drainAndCloseAgentSnapshotResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
}

type AgentSnapshotsListCommand struct {
	Type         string `long:"type" description:"Filter by exact snapshot type"`
	ContentState string `long:"content-state" description:"Filter by available or expired content"`
	CreatedAfter string `long:"created-after" description:"Filter by RFC3339 creation time"`
	Cursor       string `long:"cursor" description:"Continue after an opaque cursor returned by an earlier page"`
	Limit        int    `long:"limit" default:"100" description:"Maximum snapshots to return (1-1000)"`
	Json         bool   `long:"json" description:"Print command result as JSON"`
}

func (command *AgentSnapshotsListCommand) Execute([]string) error {
	query := url.Values{}
	if command.Type != "" {
		ref, err := snapshot.ParseTypeRef(command.Type)
		if err != nil {
			return err
		}
		query.Set("type", ref.String())
	}
	if command.ContentState != "" {
		state := snapshot.ContentState(command.ContentState)
		if err := state.Validate(); err != nil {
			return err
		}
		query.Set("content_state", string(state))
	}
	if command.CreatedAfter != "" {
		parsed, err := time.Parse(time.RFC3339, command.CreatedAfter)
		if err != nil {
			return fmt.Errorf("agent snapshot: --created-after must be RFC3339: %w", err)
		}
		query.Set("created_after", parsed.Format(time.RFC3339))
	}
	if command.Limit < 1 || command.Limit > 1000 {
		return fmt.Errorf("agent snapshot: --limit must be between 1 and 1000")
	}
	query.Set("limit", strconv.Itoa(command.Limit))
	if err := addAgentHistoryCursor(query, command.Cursor); err != nil {
		return fmt.Errorf("agent snapshot: %w", err)
	}

	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentSnapshotRequest(context.Background(), target, http.MethodGet, agentSnapshotsPath(target)+"?"+query.Encode(), nil, nil)
	if err != nil {
		return err
	}
	nextCursor := response.Header.Get("X-Next-Cursor")
	manifests := []snapshot.Snapshot{}
	if err := decodeAgentSnapshotResponse(response, &manifests); err != nil {
		return err
	}
	if err := reportNextAgentHistoryCursor(os.Stderr, "snapshot", nextCursor); err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(manifests)
	}
	return renderAgentSnapshotList(manifests)
}

type AgentSnapshotsShowCommand struct {
	Args struct {
		ID string `positional-arg-name:"ID" required:"true" description:"Exact snapshot ID"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

type agentSnapshotDetail struct {
	Manifest        snapshot.Snapshot         `json:"manifest"`
	ReplicaCount    int                       `json:"replica_count"`
	RetentionClaims []snapshot.RetentionClaim `json:"retention_claims"`
	Productions     []json.RawMessage         `json:"productions"`
	Downstream      []json.RawMessage         `json:"downstream"`
}

func (command *AgentSnapshotsShowCommand) Execute([]string) error {
	id, err := parseAgentSnapshotID(command.Args.ID)
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentSnapshotRequest(context.Background(), target, http.MethodGet, agentSnapshotPath(target, id), nil, nil)
	if err != nil {
		return err
	}
	var detail agentSnapshotDetail
	if err := decodeAgentSnapshotResponse(response, &detail); err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(detail)
	}
	if err := printAgentSnapshotManifest(detail.Manifest, false); err != nil {
		return err
	}
	fmt.Printf("replicas: %d\nretention claims: %d\nproductions: %d\ndownstream consumers: %d\n",
		detail.ReplicaCount, len(detail.RetentionClaims), len(detail.Productions), len(detail.Downstream))
	return nil
}

type AgentSnapshotsDownloadCommand struct {
	Args struct {
		ID string `positional-arg-name:"ID" required:"true" description:"Exact snapshot ID"`
	} `positional-args:"yes"`
	To string `long:"to" required:"true" description:"Destination tar file"`
}

func (command *AgentSnapshotsDownloadCommand) Execute([]string) error {
	id, err := parseAgentSnapshotID(command.Args.ID)
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentSnapshotRequest(context.Background(), target, http.MethodGet, agentSnapshotPath(target, id)+"/content", nil, nil)
	if err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		return decodeAgentSnapshotResponse(response, nil)
	}
	if err := writeAgentSnapshotDownload(response, command.To); err != nil {
		return err
	}
	fmt.Printf("downloaded snapshot %s to %s\n", id, command.To)
	return nil
}

type AgentSnapshotsPinCommand struct {
	Args struct {
		ID string `positional-arg-name:"ID" required:"true" description:"Exact snapshot ID"`
	} `positional-args:"yes"`
	Reason string `long:"reason" default:"manual pin" description:"Human-readable retention reason"`
	Json   bool   `long:"json" description:"Print command result as JSON"`
}

func (command *AgentSnapshotsPinCommand) Execute([]string) error {
	id, err := parseAgentSnapshotID(command.Args.ID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"reason": command.Reason})
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentSnapshotRequest(context.Background(), target, http.MethodPut, agentSnapshotPath(target, id)+"/pin", bytes.NewReader(payload), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	var claim snapshot.RetentionClaim
	if err := decodeAgentSnapshotResponse(response, &claim); err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(claim)
	}
	fmt.Printf("pinned snapshot %s (%s)\n", id, claim.Reason)
	return nil
}

type AgentSnapshotsUnpinCommand struct {
	Args struct {
		ID string `positional-arg-name:"ID" required:"true" description:"Exact snapshot ID"`
	} `positional-args:"yes"`
}

func (command *AgentSnapshotsUnpinCommand) Execute([]string) error {
	id, err := parseAgentSnapshotID(command.Args.ID)
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentSnapshotRequest(context.Background(), target, http.MethodDelete, agentSnapshotPath(target, id)+"/pin", nil, nil)
	if err != nil {
		return err
	}
	if err := decodeAgentSnapshotResponse(response, nil); err != nil {
		return err
	}
	fmt.Printf("unpinned snapshot %s\n", id)
	return nil
}

func agentSnapshotsPath(target rc.Target) string {
	return "/api/v1/teams/" + url.PathEscape(target.Team().Name()) + "/agent/snapshots"
}

func agentSnapshotPath(target rc.Target, id string) string {
	return agentSnapshotsPath(target) + "/" + url.PathEscape(id)
}

func agentSnapshotRequest(ctx context.Context, target rc.Target, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, target.URL()+path, body)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return target.Client().HTTPClient().Do(request)
}

func decodeAgentSnapshotResponse(response *http.Response, output any) error {
	if response == nil || response.Body == nil {
		return fmt.Errorf("agent snapshot: server returned no response")
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		var envelope struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error != "" {
			if envelope.Message != "" {
				return fmt.Errorf("%s: %s: %s", response.Status, envelope.Error, envelope.Message)
			}
			return fmt.Errorf("%s: %s", response.Status, envelope.Error)
		}
		return fmt.Errorf("%s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func newAgentSnapshotIdempotencyKey() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("agent snapshot: generate idempotency key: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func printAgentSnapshotManifest(manifest snapshot.Snapshot, jsonOutput bool) error {
	if jsonOutput {
		return displayhelpers.JsonPrint(manifest)
	}
	fmt.Printf("snapshot %s\ntype: %s\ndigest: %s\nstate: %s\nbytes: %d\nfiles: %d\ncreated: %s\n",
		manifest.ID.String(), manifest.Type, manifest.Digest, manifest.ContentState,
		manifest.ByteSize, manifest.FileCount, manifest.CreatedAt.Format(time.RFC3339))
	return nil
}

func renderAgentSnapshotList(manifests []snapshot.Snapshot) error {
	table := ui.Table{Headers: ui.TableRow{
		{Contents: "id", Color: color.New(color.Bold)},
		{Contents: "type", Color: color.New(color.Bold)},
		{Contents: "state", Color: color.New(color.Bold)},
		{Contents: "bytes", Color: color.New(color.Bold)},
		{Contents: "files", Color: color.New(color.Bold)},
		{Contents: "created", Color: color.New(color.Bold)},
	}}
	for _, manifest := range manifests {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: manifest.ID.String()},
			{Contents: manifest.Type.String()},
			{Contents: string(manifest.ContentState)},
			{Contents: strconv.FormatInt(manifest.ByteSize, 10)},
			{Contents: strconv.FormatInt(manifest.FileCount, 10)},
			{Contents: manifest.CreatedAt.Format(time.RFC3339)},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}
