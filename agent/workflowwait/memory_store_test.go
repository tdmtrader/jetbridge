package workflowwait

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

func TestMemoryStoreCreateIsExactAndRestartStable(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	request := validCreateRequest(now.Add(time.Hour))

	created, fresh, err := store.CreateOrGet(context.Background(), request)
	if err != nil || !fresh {
		t.Fatalf("create = (%+v, %t, %v)", created, fresh, err)
	}
	replayed, fresh, err := store.CreateOrGet(context.Background(), request)
	if err != nil || fresh || replayed.ID != created.ID || !replayed.Deadline.Equal(created.Deadline) {
		t.Fatalf("replay = (%+v, %t, %v)", replayed, fresh, err)
	}

	changed := request
	changed.Deadline = request.Deadline.Add(time.Hour)
	if replayed, fresh, err := store.CreateOrGet(context.Background(), changed); err != nil || fresh || !replayed.Deadline.Equal(request.Deadline) {
		t.Fatalf("restart create = (%+v, %t, %v), want original deadline", replayed, fresh, err)
	}
	if got, _, _ := store.Get(context.Background(), request.Key.TeamID, request.Key.WorkflowRunID, created.ID); !got.Deadline.Equal(request.Deadline) {
		t.Fatalf("deadline moved from %s to %s", request.Deadline, got.Deadline)
	}
}

func TestMemoryStoreOneAuthorizedAnswerWinsConcurrentRace(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	wait, _, err := store.CreateOrGet(context.Background(), validCreateRequest(now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	answers := []snapshot.SnapshotRef{validRef(31, "human-answer/v1", 'b'), validRef(32, "human-answer/v1", 'c')}
	var group sync.WaitGroup
	wins := make(chan snapshot.SnapshotRef, 32)
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			answer := answers[index%len(answers)]
			actor := "user-" + answer.ID.String()
			resolved, found, resolveErr := reserveAndResolveMemory(
				store,
				wait,
				answer,
				answer.ID.String(),
				actor,
				"User "+answer.ID.String(),
			)
			if resolveErr == nil && found {
				wins <- *resolved.Answer
			}
		}(index)
	}
	group.Wait()
	close(wins)
	var winner snapshot.SnapshotRef
	for value := range wins {
		if winner == (snapshot.SnapshotRef{}) {
			winner = value
		}
		if value != winner {
			t.Fatalf("multiple answers won: %v and %v", winner, value)
		}
	}
	if winner == (snapshot.SnapshotRef{}) {
		t.Fatal("no answer won")
	}
	stored, found, err := store.Get(context.Background(), wait.Key.TeamID, wait.Key.WorkflowRunID, wait.ID)
	if err != nil || !found || stored.Status != StatusResolved || stored.Answer == nil || *stored.Answer != winner {
		t.Fatalf("stored winner = (%+v, %t, %v)", stored, found, err)
	}
}

func TestMemoryStoreFailsClosedForScopeTypeAndTerminalRaces(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	wait, _, _ := store.CreateOrGet(context.Background(), validCreateRequest(now.Add(time.Minute)))
	answer := validRef(31, "human-answer/v1", 'b')

	for _, request := range []ResolveRequest{
		{
			TeamID: 99, WorkflowRunID: wait.Key.WorkflowRunID, WaitID: wait.ID,
			Answer: answer, AnswerValue: "approve", Actor: "alice", DisplayName: "Alice", ReservedAt: now,
		},
		{
			TeamID: wait.Key.TeamID, WorkflowRunID: 99, WaitID: wait.ID,
			Answer: answer, AnswerValue: "approve", Actor: "alice", DisplayName: "Alice", ReservedAt: now,
		},
	} {
		if _, found, err := store.Resolve(context.Background(), request); err != nil || found {
			t.Fatalf("wrong scope = (found %t, err %v), want hidden absence", found, err)
		}
	}
	wrongType := answer
	wrongType.Type = "review/v1"
	_, intent, found, err := store.ReserveResolution(context.Background(), ReserveResolutionRequest{
		TeamID: 17, WorkflowRunID: 19, WaitID: wait.ID,
		AnswerValue: "approve", Actor: "alice", DisplayName: "Alice",
	})
	if err != nil || !found {
		t.Fatalf("reserve wrong type test = (found %t, err %v)", found, err)
	}
	if _, found, err := store.Resolve(context.Background(), ResolveRequest{
		TeamID: 17, WorkflowRunID: 19, WaitID: wait.ID, Answer: wrongType,
		AnswerValue: intent.AnswerValue, Actor: intent.Actor,
		DisplayName: intent.DisplayName, ReservedAt: intent.ReservedAt,
	}); !found || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("wrong type = (found %t, err %v)", found, err)
	}

	timeoutRequest := validCreateRequest(wait.Deadline)
	timeoutRequest.Key.PlanID = "timeout-plan"
	timeoutRequest.Key.OutputName = "timeout-answer"
	timeoutWait, _, err := store.CreateOrGet(context.Background(), timeoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	now = timeoutWait.Deadline
	timedOut, found, err := store.Expire(context.Background(), timeoutWait.Key, now)
	if err != nil || !found || timedOut.Status != StatusTimedOut || timedOut.Answer == nil || *timedOut.Answer != *wait.Default {
		t.Fatalf("timeout = (%+v, %t, %v)", timedOut, found, err)
	}
	if _, _, found, err := store.ReserveResolution(context.Background(), ReserveResolutionRequest{
		TeamID: 17, WorkflowRunID: 19, WaitID: timeoutWait.ID,
		AnswerValue: "approve", Actor: "alice", DisplayName: "Alice",
	}); !found || !errors.Is(err, ErrConflict) {
		t.Fatalf("post-timeout resolve = (found %t, err %v)", found, err)
	}
}

func TestMemoryStoreDurableIntentWinsDeadlineAndCanBeRetried(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	wait, _, err := store.CreateOrGet(context.Background(), validCreateRequest(now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	_, intent, found, err := store.ReserveResolution(context.Background(), ReserveResolutionRequest{
		TeamID:        wait.Key.TeamID,
		WorkflowRunID: wait.Key.WorkflowRunID,
		WaitID:        wait.ID,
		AnswerValue:   "approve",
		Actor:         "subject:sha256:alice",
		DisplayName:   "Alice",
	})
	if err != nil || !found {
		t.Fatalf("reserve = (found %t, err %v)", found, err)
	}
	now = wait.Deadline.Add(time.Hour)
	stillWaiting, found, err := store.Expire(context.Background(), wait.Key, now)
	if err != nil || !found || stillWaiting.Status != StatusWaiting {
		t.Fatalf("expiry raced accepted intent = (%+v, %t, %v)", stillWaiting, found, err)
	}
	_, replayedIntent, found, err := store.ReserveResolution(context.Background(), ReserveResolutionRequest{
		TeamID:        wait.Key.TeamID,
		WorkflowRunID: wait.Key.WorkflowRunID,
		WaitID:        wait.ID,
		AnswerValue:   "approve",
		Actor:         "subject:sha256:alice",
		DisplayName:   "Alice renamed after reservation",
	})
	if err != nil || !found || replayedIntent != intent {
		t.Fatalf("replay accepted intent = (%+v, %t, %v), want %+v", replayedIntent, found, err, intent)
	}
	if _, _, found, err := store.ReserveResolution(context.Background(), ReserveResolutionRequest{
		TeamID:        wait.Key.TeamID,
		WorkflowRunID: wait.Key.WorkflowRunID,
		WaitID:        wait.ID,
		AnswerValue:   "reject",
		Actor:         "subject:sha256:alice",
		DisplayName:   "Alice",
	}); !found || !errors.Is(err, ErrConflict) {
		t.Fatalf("competing answer after deadline = (found %t, err %v), want conflict", found, err)
	}
	pending, err := store.PendingResolutions(context.Background(), wait.Key.TeamID, wait.Key.WorkflowRunID, 100)
	if err != nil || len(pending) != 1 || pending[0].Wait.ID != wait.ID || pending[0].Intent != intent {
		t.Fatalf("pending accepted intent = (%+v, %v), want wait %s intent %+v", pending, err, wait.ID, intent)
	}
	resolved, found, err := store.Resolve(context.Background(), ResolveRequest{
		TeamID:        wait.Key.TeamID,
		WorkflowRunID: wait.Key.WorkflowRunID,
		WaitID:        wait.ID,
		Answer:        validRef(31, "human-answer/v1", 'b'),
		AnswerValue:   replayedIntent.AnswerValue,
		Actor:         replayedIntent.Actor,
		DisplayName:   replayedIntent.DisplayName,
		ReservedAt:    replayedIntent.ReservedAt,
	})
	if err != nil || !found || resolved.Status != StatusResolved {
		t.Fatalf("resolve accepted intent = (%+v, %t, %v)", resolved, found, err)
	}
	pending, err = store.PendingResolutions(context.Background(), wait.Key.TeamID, wait.Key.WorkflowRunID, 100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("resolved intent remained pending = (%+v, %v)", pending, err)
	}
	if _, found, err := store.Resolve(context.Background(), ResolveRequest{
		TeamID:        wait.Key.TeamID,
		WorkflowRunID: wait.Key.WorkflowRunID,
		WaitID:        wait.ID,
		Answer:        *resolved.Answer,
		AnswerValue:   intent.AnswerValue,
		Actor:         intent.Actor,
		DisplayName:   "renamed after resolution",
		ReservedAt:    intent.ReservedAt,
	}); !found || !errors.Is(err, ErrConflict) {
		t.Fatalf("mutable replay identity = (found %t, err %v), want conflict", found, err)
	}
}

func TestMemoryStoreCancelRunOnlyCancelsOpenScopedWaits(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	first, _, _ := store.CreateOrGet(context.Background(), validCreateRequest(now.Add(time.Hour)))
	secondRequest := validCreateRequest(now.Add(time.Hour))
	secondRequest.Key.PlanID = "plan-2"
	secondRequest.Key.OutputName = "answer-2"
	second, _, _ := store.CreateOrGet(context.Background(), secondRequest)
	otherRequest := validCreateRequest(now.Add(time.Hour))
	otherRequest.Key.WorkflowRunID = 20
	otherRequest.Key.PlanID = "plan-3"
	other, _, _ := store.CreateOrGet(context.Background(), otherRequest)

	if _, _, err := reserveAndResolveMemory(
		store,
		first,
		validRef(31, "human-answer/v1", 'b'),
		"approve",
		"alice",
		"Alice",
	); err != nil {
		t.Fatal(err)
	}
	count, err := store.CancelRun(context.Background(), 17, 19, "system:run-cancel", now.Add(time.Minute))
	if err != nil || count != 1 {
		t.Fatalf("cancel = (%d, %v)", count, err)
	}
	resolved, _, _ := store.Get(context.Background(), 17, 19, first.ID)
	cancelled, _, _ := store.Get(context.Background(), 17, 19, second.ID)
	untouched, _, _ := store.Get(context.Background(), 17, 20, other.ID)
	if resolved.Status != StatusResolved || cancelled.Status != StatusCancelled || untouched.Status != StatusWaiting {
		t.Fatalf("states = %s, %s, %s", resolved.Status, cancelled.Status, untouched.Status)
	}
}

func reserveAndResolveMemory(
	store *MemoryStore,
	wait Wait,
	answer snapshot.SnapshotRef,
	answerValue string,
	actor string,
	displayName string,
) (Wait, bool, error) {
	_, intent, found, err := store.ReserveResolution(context.Background(), ReserveResolutionRequest{
		TeamID:        wait.Key.TeamID,
		WorkflowRunID: wait.Key.WorkflowRunID,
		WaitID:        wait.ID,
		AnswerValue:   answerValue,
		Actor:         actor,
		DisplayName:   displayName,
	})
	if err != nil || !found {
		return Wait{}, found, err
	}
	return store.Resolve(context.Background(), ResolveRequest{
		TeamID:        wait.Key.TeamID,
		WorkflowRunID: wait.Key.WorkflowRunID,
		WaitID:        wait.ID,
		Answer:        answer,
		AnswerValue:   intent.AnswerValue,
		Actor:         intent.Actor,
		DisplayName:   intent.DisplayName,
		ReservedAt:    intent.ReservedAt,
	})
}

func validCreateRequest(deadline time.Time) CreateRequest {
	defaultRef := validRef(30, "human-answer/v1", 'a')
	return CreateRequest{
		Key:          ExecutionKey{TeamID: 17, WorkflowRunID: 19, BuildID: 23, PlanID: "plan-1", Attempt: "1", OutputName: "answer"},
		QuestionName: "question", Question: validRef(29, "question/v1", '9'), ExpectedType: "human-answer/v1",
		Deadline: deadline, TimeoutPolicy: TimeoutDefault, Default: &defaultRef,
		WorkflowPort: "approval", WorkflowDefinitionID: 7,
	}
}

func validRef(id snapshot.SnapshotID, typ snapshot.TypeRef, digestByte byte) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{ID: id, Type: typ, Digest: snapshot.Digest("sha256:" + string(make([]byte, 0)) + repeatByte(digestByte, 64))}
}

func repeatByte(value byte, count int) string {
	buffer := make([]byte, count)
	for index := range buffer {
		buffer[index] = value
	}
	return string(buffer)
}
