package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const privateRecordName = ".forge-pr-record"

type ownedDirectory struct {
	name string
	info os.FileInfo
	root *os.Root
}

type ownedFile struct {
	name    string
	info    os.FileInfo
	guard   *os.File
	present bool
}

type destinationOwnership struct {
	directories []*ownedDirectory
	files       []*ownedFile
}

func In(ctx context.Context, destination string, stdin io.Reader, stdout, _ io.Writer, dependencies Dependencies) (resultErr error) {
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
	destinationRoot, destinationInfo, err := openDestination(destination)
	if err != nil {
		return err
	}
	defer destinationRoot.Close()

	ownership := &destinationOwnership{}
	completed := false
	defer func() {
		if !completed {
			resultErr = errors.Join(resultErr, ownership.cleanup(destinationRoot))
		}
		ownership.close()
	}()

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
	// The version identifies which observation the server selected; it is not a
	// promise the provider stood still. The admit job is Serial, so builds queue,
	// and Concourse consumes a version at build start regardless of outcome --
	// refusing a moved observation here burns the event instead of retrying it.
	// Materialize what is current and let the server reject it downstream against
	// durable state.
	action, actionable, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{Now: now, PollInterval: poll, FreshnessInterval: fresh, LastCursor: pullrequest.Cursor(request.Source.Monitor.AcknowledgedCursor), LastTargetSHA: request.Source.Monitor.LastReconciledTarget, LastReconciledAt: request.Source.Monitor.LastReconciledAt, ActiveActionDigest: request.Source.Monitor.ActiveActionDigest})
	if err != nil || !actionable {
		return fmt.Errorf("forge-pr: pull request has no actionable state")
	}
	if err := verifyDestination(destinationRoot, destination, destinationInfo); err != nil {
		return err
	}
	if err := requireEmptyRoot(destinationRoot); err != nil {
		return err
	}
	if dependencies.BeforeMaterialize != nil {
		if err := dependencies.BeforeMaterialize(); err != nil {
			return fmt.Errorf("forge-pr: prepare materialization")
		}
	}
	if err := verifyDestination(destinationRoot, destination, destinationInfo); err != nil {
		return err
	}
	for _, name := range []string{"source-repository", "target-repository"} {
		output, claimErr := claimDirectory(destinationRoot, name)
		if output != nil {
			ownership.directories = append(ownership.directories, output)
		}
		if claimErr != nil {
			return claimErr
		}
	}

	git := dependencies.GitRunner
	if git == nil {
		git, err = defaultGitRunner()
		if err != nil {
			return err
		}
	}
	credential := []byte(request.Source.ReadToken)
	defer wipe(credential)
	// Both revisions live in the same remote and share nearly all of their
	// history, so the remote is contacted once. The second checkout copies what
	// it needs from the first over the filesystem: no alternates, no hardlinks,
	// no saved remote, so each materialized repository is still independently
	// valid evidence.
	sourceDirectory := filepath.Join(destination, "source-repository")
	for index, checkout := range []struct{ name, ref, sha string }{{"source-repository", observation.SourceRef, observation.SourceSHA}, {"target-repository", observation.TargetRef, observation.TargetSHA}} {
		mode, ref := GitFetchNamedRef, checkout.ref
		if action.Kind == pullrequest.ActionCompleted || action.Kind == pullrequest.ActionAbandoned {
			mode, ref = GitFetchExactObject, ""
		}
		output := ownership.directories[index]
		command := GitCommand{Operation: "checkout", FetchMode: mode, Directory: filepath.Join(destination, checkout.name), RemoteURL: request.Source.RepositoryURL, Ref: ref, SHA: checkout.sha, Credential: credential}
		if index == 0 {
			// Bring the other revision down in the same fetch so the second
			// checkout has a local source for it.
			command.SecondRef, command.SecondSHA = observation.TargetRef, observation.TargetSHA
			if mode == GitFetchExactObject {
				command.SecondRef = ""
			}
		} else {
			command.RemoteURL, command.Ref, command.Credential = "", "", nil
			command.FetchMode = GitFetchExactObject
			command.LocalSource = sourceDirectory
		}
		command.verifyDirectory = func() error {
			return verifyOwnedDirectory(destinationRoot, destination, destinationInfo, output)
		}
		if dependencies.BeforeGit != nil {
			if err := dependencies.BeforeGit(command); err != nil {
				return fmt.Errorf("forge-pr: prepare git materialization")
			}
		}
		if err := command.verifyDirectory(); err != nil {
			return err
		}
		if err := git.Run(ctx, command); err != nil {
			return fmt.Errorf("forge-pr: materialize repository")
		}
		if err := command.verifyDirectory(); err != nil {
			return err
		}
		if err := validateMaterializationRoot(ctx, output.root, snapshot.DefaultMaxSnapshotEntries, snapshot.DefaultMaxSnapshotContentBytes, snapshot.MaxSnapshotSymlinkTargetBytes); err != nil {
			return err
		}
		if err := command.verifyDirectory(); err != nil {
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
	for _, output := range ownership.directories {
		if err := verifyOwnedDirectory(destinationRoot, destination, destinationInfo, output); err != nil {
			return err
		}
	}
	if err := publishRecord(destinationRoot, destination, destinationInfo, ownership, raw); err != nil {
		return err
	}
	if err := verifyCompletedDestination(destinationRoot, destination, destinationInfo, ownership); err != nil {
		return err
	}
	if err := writeJSON(stdout, InResult{Version: *request.Version}); err != nil {
		return err
	}
	if err := verifyCompletedDestination(destinationRoot, destination, destinationInfo, ownership); err != nil {
		return err
	}
	completed = true
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

func openDestination(destination string) (*os.Root, os.FileInfo, error) {
	if destination == "" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return nil, nil, fmt.Errorf("forge-pr: destination is invalid")
	}
	parent := filepath.Dir(destination)
	parentInfo, parentErr := os.Lstat(parent)
	if parentErr != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("forge-pr: destination parent is unsafe")
	}
	if _, resolveErr := filepath.EvalSymlinks(parent); resolveErr != nil {
		return nil, nil, fmt.Errorf("forge-pr: destination parent is unsafe")
	}
	pathInfo, err := os.Lstat(destination)
	if err != nil || !realDirectory(pathInfo) {
		return nil, nil, fmt.Errorf("forge-pr: destination must be an existing real directory")
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return nil, nil, fmt.Errorf("forge-pr: open destination")
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !realDirectory(rootInfo) || !os.SameFile(pathInfo, rootInfo) {
		_ = root.Close()
		return nil, nil, fmt.Errorf("forge-pr: destination identity changed")
	}
	afterInfo, err := os.Lstat(destination)
	if err != nil || !realDirectory(afterInfo) || !os.SameFile(rootInfo, afterInfo) {
		_ = root.Close()
		return nil, nil, fmt.Errorf("forge-pr: destination identity changed")
	}
	if err := requireEmptyRoot(root); err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	return root, rootInfo, nil
}

func requireEmptyRoot(root *os.Root) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil || len(entries) != 0 {
		return fmt.Errorf("forge-pr: destination is not empty")
	}
	return nil
}

func realDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func verifyDestination(root *os.Root, destination string, expected os.FileInfo) error {
	rootInfo, rootErr := root.Stat(".")
	pathInfo, pathErr := os.Lstat(destination)
	if rootErr != nil || pathErr != nil || !realDirectory(rootInfo) || !realDirectory(pathInfo) || !os.SameFile(expected, rootInfo) || !os.SameFile(expected, pathInfo) {
		return fmt.Errorf("forge-pr: destination identity changed")
	}
	return nil
}

func claimDirectory(root *os.Root, name string) (*ownedDirectory, error) {
	if err := root.Mkdir(name, 0700); err != nil {
		return nil, fmt.Errorf("forge-pr: claim repository output")
	}
	output := &ownedDirectory{name: name}
	pathInfo, err := root.Lstat(name)
	if err != nil || !realDirectory(pathInfo) {
		return output, fmt.Errorf("forge-pr: verify claimed repository output")
	}
	output.info = pathInfo
	output.root, err = root.OpenRoot(name)
	if err != nil {
		return output, fmt.Errorf("forge-pr: retain repository output")
	}
	rootInfo, err := output.root.Stat(".")
	if err != nil || !realDirectory(rootInfo) || !os.SameFile(output.info, rootInfo) {
		return output, fmt.Errorf("forge-pr: repository output identity changed")
	}
	return output, nil
}

func verifyOwnedDirectory(destinationRoot *os.Root, destination string, destinationInfo os.FileInfo, output *ownedDirectory) error {
	if output == nil || output.root == nil || output.info == nil {
		return fmt.Errorf("forge-pr: repository output identity is unavailable")
	}
	if err := verifyDestination(destinationRoot, destination, destinationInfo); err != nil {
		return err
	}
	relativeInfo, relativeErr := destinationRoot.Lstat(output.name)
	retainedInfo, retainedErr := output.root.Stat(".")
	pathInfo, pathErr := os.Lstat(filepath.Join(destination, output.name))
	if relativeErr != nil || retainedErr != nil || pathErr != nil ||
		!realDirectory(relativeInfo) || !realDirectory(retainedInfo) || !realDirectory(pathInfo) ||
		!os.SameFile(output.info, relativeInfo) || !os.SameFile(output.info, retainedInfo) || !os.SameFile(output.info, pathInfo) {
		return fmt.Errorf("forge-pr: repository output identity changed")
	}
	return nil
}

func verifyCompletedDestination(root *os.Root, destination string, destinationInfo os.FileInfo, ownership *destinationOwnership) error {
	if err := verifyDestination(root, destination, destinationInfo); err != nil {
		return err
	}

	expectedNames := map[string]struct{}{
		"record.json":       {},
		"source-repository": {},
		"target-repository": {},
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil || len(entries) != len(expectedNames) {
		return fmt.Errorf("forge-pr: completed destination membership changed")
	}
	for _, entry := range entries {
		if _, expected := expectedNames[entry.Name()]; !expected {
			return fmt.Errorf("forge-pr: completed destination membership changed")
		}
		delete(expectedNames, entry.Name())
	}
	if len(expectedNames) != 0 {
		return fmt.Errorf("forge-pr: completed destination membership changed")
	}

	for _, output := range ownership.directories {
		if err := verifyOwnedDirectory(root, destination, destinationInfo, output); err != nil {
			return err
		}
	}

	var record *ownedFile
	for _, file := range ownership.files {
		if file != nil && file.name == "record.json" && file.present {
			record = file
			break
		}
	}
	if record == nil || record.info == nil || record.guard == nil {
		return fmt.Errorf("forge-pr: record identity is unavailable")
	}
	retainedInfo, retainedErr := record.guard.Stat()
	recordInfo, recordErr := root.Lstat(record.name)
	if retainedErr != nil || recordErr != nil ||
		!retainedInfo.Mode().IsRegular() || !recordInfo.Mode().IsRegular() ||
		!os.SameFile(record.info, retainedInfo) ||
		!os.SameFile(retainedInfo, recordInfo) {
		return fmt.Errorf("forge-pr: record identity changed")
	}
	return nil
}

func publishRecord(root *os.Root, destination string, destinationInfo os.FileInfo, ownership *destinationOwnership, raw []byte) error {
	if err := verifyDestination(root, destination, destinationInfo); err != nil {
		return err
	}
	file, err := root.OpenFile(privateRecordName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("forge-pr: claim private record")
	}
	private := &ownedFile{name: privateRecordName, present: true}
	ownership.files = append(ownership.files, private)
	private.info, err = file.Stat()
	if err == nil {
		var written int
		written, err = file.Write(raw)
		if err == nil && written != len(raw) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("forge-pr: write private record")
	}
	guard, err := root.Open(privateRecordName)
	if err != nil {
		return fmt.Errorf("forge-pr: retain private record")
	}
	private.guard = guard
	retainedInfo, err := guard.Stat()
	if err != nil || private.info == nil || !retainedInfo.Mode().IsRegular() ||
		!os.SameFile(private.info, retainedInfo) {
		return fmt.Errorf("forge-pr: private record identity changed")
	}
	if err := verifyDestination(root, destination, destinationInfo); err != nil {
		return err
	}
	if err := root.Link(privateRecordName, "record.json"); err != nil {
		return fmt.Errorf("forge-pr: publish record")
	}
	record := &ownedFile{name: "record.json", info: private.info, present: true}
	ownership.files = append(ownership.files, record)
	retainedInfo, retainedErr := private.guard.Stat()
	recordInfo, recordErr := root.Lstat(record.name)
	if retainedErr != nil || recordErr != nil ||
		!retainedInfo.Mode().IsRegular() || !recordInfo.Mode().IsRegular() ||
		!os.SameFile(record.info, retainedInfo) ||
		!os.SameFile(retainedInfo, recordInfo) {
		return fmt.Errorf("forge-pr: record identity changed")
	}
	if err := cleanupOwnedFile(root, private); err != nil {
		return fmt.Errorf("forge-pr: remove private record")
	}
	record.guard = private.guard
	private.guard = nil
	if err := verifyDestination(root, destination, destinationInfo); err != nil {
		return err
	}
	return nil
}

func (ownership *destinationOwnership) cleanup(root *os.Root) error {
	var cleanupErrors []error
	for index := len(ownership.files) - 1; index >= 0; index-- {
		if err := cleanupOwnedFile(root, ownership.files[index]); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	for index := len(ownership.directories) - 1; index >= 0; index-- {
		if err := cleanupOwnedDirectory(root, ownership.directories[index]); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func (ownership *destinationOwnership) close() {
	for _, file := range ownership.files {
		if file.guard != nil {
			_ = file.guard.Close()
		}
	}
	for _, output := range ownership.directories {
		if output.root != nil {
			_ = output.root.Close()
		}
	}
}

func cleanupOwnedFile(root *os.Root, file *ownedFile) error {
	if file == nil || !file.present {
		return nil
	}
	expected := file.info
	if file.guard != nil {
		retained, guardErr := file.guard.Stat()
		if guardErr != nil || file.info == nil ||
			!retained.Mode().IsRegular() ||
			!os.SameFile(file.info, retained) {
			return fmt.Errorf("forge-pr: refused to remove changed owned file")
		}
		expected = retained
	}
	current, err := root.Lstat(file.name)
	if os.IsNotExist(err) {
		file.present = false
		return nil
	}
	if err != nil || expected == nil || !current.Mode().IsRegular() || !os.SameFile(expected, current) {
		return fmt.Errorf("forge-pr: refused to remove changed owned file")
	}
	if err := root.Remove(file.name); err != nil {
		return fmt.Errorf("forge-pr: remove owned file")
	}
	file.present = false
	return nil
}

func cleanupOwnedDirectory(root *os.Root, output *ownedDirectory) error {
	if output == nil {
		return nil
	}
	var cleanupErrors []error
	if output.root != nil {
		entries, err := fs.ReadDir(output.root.FS(), ".")
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("forge-pr: inspect owned repository output"))
		} else {
			for _, entry := range entries {
				if err := output.root.RemoveAll(entry.Name()); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("forge-pr: clean owned repository output"))
				}
			}
		}
	}
	current, err := root.Lstat(output.name)
	switch {
	case os.IsNotExist(err):
		return errors.Join(cleanupErrors...)
	case err != nil:
		cleanupErrors = append(cleanupErrors, fmt.Errorf("forge-pr: inspect owned repository output"))
	case output.info == nil || !realDirectory(current) || !os.SameFile(output.info, current):
		cleanupErrors = append(cleanupErrors, fmt.Errorf("forge-pr: refused to remove changed repository output"))
	default:
		if err := root.Remove(output.name); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("forge-pr: remove owned repository output"))
		}
	}
	return errors.Join(cleanupErrors...)
}

func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
