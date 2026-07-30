package pullrequestresponse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestProtectedTreeDetectsDescendantDirectoryIdentity(t *testing.T) {
	protectedPath := t.TempDir()
	descendantPath := filepath.Join(protectedPath, "empty-descendant")
	if err := os.Mkdir(descendantPath, 0700); err != nil {
		t.Fatal(err)
	}
	protected, err := openResponseOutput(protectedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer protected.root.Close()
	descendant, err := openResponseOutput(descendantPath)
	if err != nil {
		t.Fatal(err)
	}
	defer descendant.root.Close()

	found, err := protected.containsIdentity(descendant.identity)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("protected tree did not detect a descendant directory identity")
	}
}

func TestWriteFailureNeverRemovesAReplacementItDidNotCreate(t *testing.T) {
	output := t.TempDir()
	ownedPath := filepath.Join(output, "owned-record")
	recordPath := filepath.Join(output, recordFileName)
	replacement := []byte("replacement owned by another writer")
	ctx := &replaceRecordOnSecondErr{
		output:      output,
		ownedPath:   ownedPath,
		recordPath:  recordPath,
		replacement: replacement,
	}

	err := Write(ctx, output, validResponseRecord(t))
	if err == nil {
		t.Fatal("Write succeeded after the context was canceled")
	}
	actual, readErr := os.ReadFile(recordPath)
	if readErr != nil {
		t.Fatalf("replacement was removed during rollback: %v", readErr)
	}
	if string(actual) != string(replacement) {
		t.Fatalf("record replacement = %q, want %q", actual, replacement)
	}
	if _, statErr := os.Stat(ownedPath); statErr != nil {
		t.Fatalf("owned record was not retained after replacement: %v", statErr)
	}
}

type replaceRecordOnSecondErr struct {
	calls       int
	output      string
	ownedPath   string
	recordPath  string
	replacement []byte
}

func (ctx *replaceRecordOnSecondErr) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (ctx *replaceRecordOnSecondErr) Done() <-chan struct{} {
	return nil
}

func (ctx *replaceRecordOnSecondErr) Err() error {
	ctx.calls++
	if ctx.calls != 2 {
		return nil
	}
	if err := os.Rename(ctx.recordPath, ctx.ownedPath); err != nil {
		panic(err)
	}
	if err := os.WriteFile(ctx.recordPath, ctx.replacement, 0600); err != nil {
		panic(err)
	}
	return context.Canceled
}

func (ctx *replaceRecordOnSecondErr) Value(any) any {
	return nil
}

func validResponseRecord(t *testing.T) contracts.Record[contracts.PullRequestResponseBody] {
	t.Helper()
	sum := sha256.Sum256([]byte("exact pull request observation"))
	ref := snapshot.SnapshotRef{
		ID:     1,
		Type:   observationType,
		Digest: snapshot.Digest("sha256:" + hex.EncodeToString(sum[:])),
	}
	record, err := contracts.NewRecord(
		responseType,
		[]contracts.Subject{contracts.SubjectFromInput(
			"pull-request",
			contracts.SubjectRolePrimary,
			"pull-request",
			ref,
		)},
		contracts.PullRequestResponseBody{
			BatchID: "batch-1",
			Summary: "Addressed the completed review.",
			Replies: []contracts.PullRequestThreadResponse{{
				ThreadID: "thread-1",
				Body:     "Updated in the latest revision.",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
