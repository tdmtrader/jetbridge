package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func In(ctx context.Context, destination string, stdin io.Reader, stdout, _ io.Writer, dependencies Dependencies) error {
	request, err := decodeRequest(stdin)
	if err != nil {
		return err
	}
	if request.Version == nil {
		return fmt.Errorf("forge-pr: selected version is required")
	}
	if err := request.Version.validate(); err != nil {
		return err
	}
	locator, poll, fresh, err := request.Source.validate()
	if err != nil {
		return err
	}
	if request.Source.Monitor.AttentionRequired || request.Source.Monitor.Paused || request.Source.Monitor.OperatorTerminated {
		return fmt.Errorf("forge-pr: binding is not materializable")
	}
	if request.Version.BindingRevision != bindingRevision(request.Source) {
		return fmt.Errorf("forge-pr: selected version binding revision is stale")
	}
	if err := safeDestination(destination); err != nil {
		return err
	}
	observer, err := observerFor(request.Source, dependencies)
	if err != nil {
		return err
	}
	observation, err := observer.Observe(ctx, locator, pullrequest.Cursor(request.Source.Monitor.AcknowledgedCursor))
	if err != nil {
		return fmt.Errorf("forge-pr: observe pull request")
	}
	now := time.Now().UTC()
	if dependencies.Clock != nil {
		now = dependencies.Clock().UTC()
	}
	action, actionable, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{Now: now, PollInterval: poll, FreshnessInterval: fresh, LastCursor: pullrequest.Cursor(request.Source.Monitor.AcknowledgedCursor), LastTargetSHA: request.Source.Monitor.LastReconciledTarget, LastReconciledAt: request.Source.Monitor.LastReconciledAt, ActiveActionDigest: request.Source.Monitor.ActiveActionDigest})
	if err != nil || !actionable {
		return fmt.Errorf("forge-pr: selected version is stale")
	}
	expected := versionFor(request.Source, action)
	if !equalVersion(expected, *request.Version) {
		return fmt.Errorf("forge-pr: selected version does not match current pull request")
	}
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("forge-pr: open destination")
	}
	defer destinationRoot.Close()
	stage, err := os.MkdirTemp(destination, ".forge-pr-")
	if err != nil {
		return fmt.Errorf("forge-pr: create staging output")
	}
	stageName := filepath.Base(stage)
	installed := false
	defer func() {
		_ = destinationRoot.RemoveAll(stageName)
		if !installed {
			_ = destinationRoot.RemoveAll("source-repository")
			_ = destinationRoot.RemoveAll("target-repository")
			_ = destinationRoot.RemoveAll("record.json")
		}
	}()
	git := dependencies.GitRunner
	if git == nil {
		git, err = defaultGitRunner()
		if err != nil {
			return err
		}
	}
	credential := []byte(request.Source.ReadToken)
	defer wipe(credential)
	for _, checkout := range []struct{ name, ref, sha string }{{"source-repository", observation.SourceRef, observation.SourceSHA}, {"target-repository", observation.TargetRef, observation.TargetSHA}} {
		mode, ref := GitFetchNamedRef, checkout.ref
		if action.Kind == pullrequest.ActionCompleted || action.Kind == pullrequest.ActionAbandoned {
			mode, ref = GitFetchExactObject, ""
		}
		if err := git.Run(ctx, GitCommand{Operation: "checkout", FetchMode: mode, Directory: filepath.Join(stage, checkout.name), RemoteURL: request.Source.RepositoryURL, Ref: ref, SHA: checkout.sha, Credential: credential}); err != nil {
			return fmt.Errorf("forge-pr: materialize repository")
		}
		if err := validateMaterializationBounds(ctx, filepath.Join(stage, checkout.name), snapshot.DefaultMaxSnapshotEntries, snapshot.DefaultMaxSnapshotContentBytes, snapshot.MaxSnapshotSymlinkTargetBytes); err != nil {
			return err
		}
	}
	body, err := pullRequestBody(observation, action.Kind)
	if err != nil {
		return err
	}
	record, err := contracts.NewRecord(snapshot.TypeRef("pull-request/v1"), nil, body)
	if err != nil {
		return fmt.Errorf("forge-pr: normalize pull request record")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("forge-pr: encode pull request record")
	}
	if strings.Contains(string(raw), request.Source.ReadToken) {
		return fmt.Errorf("forge-pr: refused credential-bearing record")
	}
	if err := os.WriteFile(filepath.Join(stage, "record.json"), raw, 0600); err != nil {
		return fmt.Errorf("forge-pr: write record")
	}
	for _, name := range []string{"source-repository", "target-repository"} {
		if err := destinationRoot.Rename(stageName+"/"+name, name); err != nil {
			return fmt.Errorf("forge-pr: install repository output")
		}
	}
	// record.json is the authoritative marker and is intentionally published
	// only after both repository trees are fully installed.
	if err := destinationRoot.Rename(stageName+"/record.json", "record.json"); err != nil {
		return fmt.Errorf("forge-pr: install record output")
	}
	if err := writeJSON(stdout, InResult{Version: *request.Version}); err != nil {
		return err
	}
	installed = true
	return nil
}

func pullRequestBody(observation pullrequest.Observation, kind pullrequest.ActionKind) (contracts.PullRequestBody, error) {
	trigger := contracts.PullRequestTrigger(kind)
	body := contracts.PullRequestBody{Provider: string(observation.Provider), Repository: observation.Repository, ExternalID: observation.ExternalID, URL: observation.URL, State: observation.State, Mergeability: observation.Mergeability, SourceRef: observation.SourceRef, SourceSHA: observation.SourceSHA, ExpectedSource: observation.ExpectedSource, TargetRef: observation.TargetRef, TargetSHA: observation.TargetSHA, Iteration: observation.Iteration, Trigger: trigger, ReviewBatches: observation.ReviewBatches, Threads: observation.Threads}
	if err := body.Validate(nil); err != nil {
		return contracts.PullRequestBody{}, fmt.Errorf("forge-pr: normalize pull request body")
	}
	return body, nil
}

func safeDestination(destination string) error {
	if destination == "" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return fmt.Errorf("forge-pr: destination is invalid")
	}
	parent := filepath.Dir(destination)
	parentInfo, parentErr := os.Lstat(parent)
	if parentErr != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("forge-pr: destination parent is unsafe")
	}
	if _, resolveErr := filepath.EvalSymlinks(parent); resolveErr != nil {
		return fmt.Errorf("forge-pr: destination parent is unsafe")
	}
	info, err := os.Lstat(destination)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("forge-pr: destination must be an existing real directory")
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil || len(entries) != 0 {
		return fmt.Errorf("forge-pr: destination is not empty")
	}
	return nil
}
func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
