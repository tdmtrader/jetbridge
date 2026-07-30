package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func validPullRequestResponseBody() contracts.PullRequestResponseBody {
	return contracts.PullRequestResponseBody{
		BatchID: "batch-1",
		Summary: "I have addressed the requested change.",
		Replies: []contracts.PullRequestThreadResponse{{
			ThreadID: "thread-1", Body: "Updated in the latest revision.",
		}},
	}
}

func pullRequestWithAuthorizedThreads(threads ...string) contracts.PullRequestBody {
	body := validPullRequestBody()
	body.Threads = nil
	for index, threadID := range threads {
		body.Threads = append(body.Threads, contracts.PullRequestThread{
			ID: threadID, Iteration: "iteration-1", Comments: []contracts.PullRequestComment{{
				ID: "comment-" + string(rune('1'+index)), Author: "reviewer-1", Body: "Please revise this.", CommitSHA: strings.Repeat("a", 40),
			}},
		})
	}
	body.ReviewBatches[0].ThreadIDs = append([]string(nil), threads...)
	return body
}

func TestPullRequestResponseRejectsThreadOutsideAuthorizedObservation(t *testing.T) {
	body := validPullRequestResponseBody()
	body.Replies[0].ThreadID = "thread-not-in-subject"
	if err := contracts.ValidatePullRequestResponseAgainst(
		body,
		pullRequestWithAuthorizedThreads("thread-1"),
	); err == nil {
		t.Fatal("accepted reply to an unauthorized thread")
	}
}

func TestPullRequestResponseRejectsDuplicateReplies(t *testing.T) {
	body := validPullRequestResponseBody()
	body.Replies = append(body.Replies, body.Replies[0])
	if err := body.Validate(nil); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Validate() error = %v, want duplicate", err)
	}
}
