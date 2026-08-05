# Full-State PR Monitor — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `forge-pr` resource affordable and correct, and replace its consuming-delta observation with a bounded full-state one.

**Architecture:** Two independent tracks. Tasks 1–3 fix defects and cost in the resource and GitHub client — valuable even if the wider redesign is abandoned. Tasks 4–6 replace the single-review delta with an activity-ordered full-state window and repoint the consumers that assumed exactly one review batch.

**Tech Stack:** Go 1.25, Ginkgo/Gomega for `atc/db`, plain `testing` for `agent/*`, PostgreSQL for DB suites.

**Spec:** [2026-08-05-full-state-pr-monitor-design.md](../specs/2026-08-05-full-state-pr-monitor-design.md)

**Out of scope — needs its own plan:** generic source instances, the skip guard, server-side re-launch, `resource_sources:` declaration, closing the authority-spine gaps, removing the boot gate.

**Test environment note.** `postgresrunner` binds `5433 + GinkgoParallelProcess()`, i.e. ports **5434–5442**. Other worktrees running suites collide. Before any DB suite, drain that whole range with **SIGTERM** — `kill -9` leaks SysV shared-memory segments and eventually exhausts SHMMNI so `initdb` fails outright.

---

## File structure

| File | Responsibility | Change |
|---|---|---|
| `agent/pullrequest/resource/in.go` | materialize one selected version | delete the stale-version gate; single-fetch materialization |
| `agent/pullrequest/resource/dependencies.go` | controlled git seam | add a two-worktree fetch operation |
| `agent/pullrequest/github/client.go` | GitHub HTTP transport | ETag / `If-None-Match` |
| `agent/pullrequest/github/observe.go` | provider → normalized observation | full state, activity window, truncation marker |
| `agent/pullrequest/types.go` | provider-neutral observation | `Truncated` flag |
| `agent/pullrequest/revision_executor.go` | publish a revision for a batch | select batch by ID, not index 0 |
| `agent/pullrequest/monitor_run_inspector.go` | classify a monitor run's evidence | match batch by ID, not index 0 |

---

### Task 1: Delete the stale-version gate in `in`

`in.go:88-95` re-observes at get time, re-derives the action, and errors `"forge-pr: selected version does not match current pull request"` unless the recomputed version is byte-equal. Any build whose PR moved between `check` and `get` fails — and burns its Concourse version anyway, because `AdoptInputsAndPipes` records the input at build start. `Serial: true` on the `admit` job guarantees a queue, so this fires routinely under load.

**Files:**
- Modify: `agent/pullrequest/resource/in.go:80-95`
- Test: `agent/pullrequest/resource/in_test.go`

- [ ] **Step 1: Write the failing test**

Add to `agent/pullrequest/resource/in_test.go`:

```go
func TestInMaterializesWhenObservationMovedSinceCheck(t *testing.T) {
	fixture := newInFixture(t)
	// The version was selected against an earlier observation; the provider has
	// since gained another review. Materialization must still succeed.
	fixture.observation.ReviewBatches = append(fixture.observation.ReviewBatches,
		pullrequest.ReviewBatch{
			ID: "review-99", ReviewID: "99", CommitSHA: fixture.observation.SourceSHA,
			Reviewer: "carol", Ready: true, ThreadIDs: []string{},
		})

	err := resource.In(context.Background(), fixture.request(), fixture.destination, fixture.deps())

	if err != nil {
		t.Fatalf("In() with a moved observation = %v, want success", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/pullrequest/resource/ -run TestInMaterializesWhenObservationMovedSinceCheck -v`
Expected: FAIL with `forge-pr: selected version does not match current pull request`

- [ ] **Step 3: Delete the gate**

In `agent/pullrequest/resource/in.go`, replace the block that begins at the `pullrequest.ActionFor` call and ends after the `equalVersion` check with:

```go
	// The version identifies which observation the server selected; it is not a
	// promise the provider has stood still. A build may be queued behind others
	// (the admit job is Serial), and Concourse consumes a version at build start
	// regardless of outcome -- so failing here would burn the version and lose
	// the event. Materialize the current observation and let the server reject
	// it downstream if it no longer matches durable state.
	action, actionable, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{
		Now: now, PollInterval: poll, FreshnessInterval: fresh,
		LastCursor:         pullrequest.Cursor(request.Source.Monitor.AcknowledgedCursor),
		LastTargetSHA:      request.Source.Monitor.LastReconciledTarget,
		LastReconciledAt:   request.Source.Monitor.LastReconciledAt,
		ActiveActionDigest: request.Source.Monitor.ActiveActionDigest,
	})
	if err != nil {
		return fmt.Errorf("forge-pr: derive pull request action")
	}
	if !actionable {
		return fmt.Errorf("forge-pr: pull request has no actionable state")
	}
```

Delete the two lines that computed `expected` and compared it with `equalVersion`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/pullrequest/resource/ -v`
Expected: PASS, including the new test. If `equalVersion` is now unused, the compiler will not complain (it is a function, not an import) — leave it; Task 6 removes it.

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/resource/in.go agent/pullrequest/resource/in_test.go
git commit -m "fix(forge-pr): stop failing a materialization because the PR moved

in re-observed at get time and demanded byte-equality with the version check
selected. A build queued behind others -- the admit job is Serial -- routinely
sees a moved PR, and Concourse consumes the version at build start regardless
of outcome, so the failure burned the event rather than retrying it."
```

---

### Task 2: Conditional requests in the GitHub client

At a 5-minute poll each watched PR issues ~288 polls/day × 3 endpoints against a 5,000/hr ceiling. GitHub returns `304 Not Modified` for free against an `ETag`, and does not charge it to the rate limit.

**Files:**
- Modify: `agent/pullrequest/github/client.go`
- Test: `agent/pullrequest/github/client_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestClientReusesCachedBodyOnNotModified(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("If-None-Match") == `"etag-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"etag-1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":7}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	first, err := client.getJSON(context.Background(), server.URL+"/x")
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.getJSON(context.Background(), server.URL+"/x")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("304 response body = %s, want the cached %s", second, first)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/pullrequest/github/ -run TestClientReusesCachedBodyOnNotModified -v`
Expected: FAIL — either the body is empty on the second call, or `getJSON` errors on a 304 status.

- [ ] **Step 3: Add a bounded ETag cache**

In `agent/pullrequest/github/client.go`, add to the client struct and its request path:

```go
type cachedResponse struct {
	etag string
	body []byte
}

// maxCachedResponses bounds the cache so a long-lived observer watching many
// paginated endpoints cannot grow without limit.
const maxCachedResponses = 64

// cacheKey is the exact request URL. Bodies are immutable once stored.
func (client *client) cachedFor(url string) (cachedResponse, bool) {
	client.cacheMu.RLock()
	defer client.cacheMu.RUnlock()
	entry, found := client.cache[url]
	return entry, found
}

func (client *client) storeCached(url, etag string, body []byte) {
	if etag == "" {
		return
	}
	client.cacheMu.Lock()
	defer client.cacheMu.Unlock()
	if client.cache == nil {
		client.cache = make(map[string]cachedResponse, maxCachedResponses)
	}
	if len(client.cache) >= maxCachedResponses {
		for key := range client.cache {
			delete(client.cache, key)
			break
		}
	}
	client.cache[url] = cachedResponse{etag: etag, body: append([]byte(nil), body...)}
}
```

In the GET path, before issuing the request set `If-None-Match` from any cached entry; on `http.StatusNotModified` return the cached body; on `200` call `storeCached` with the response `ETag`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/pullrequest/github/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/github/client.go agent/pullrequest/github/client_test.go
git commit -m "perf(forge-pr): serve unchanged GitHub reads from an ETag cache

At a 5m poll each watched PR spends ~864 requests/day against a 5,000/hr
ceiling. GitHub answers If-None-Match with a 304 it does not bill."
```

---

### Task 3: Materialize both worktrees from one fetch

`in` claims `source-repository` and `target-repository` and runs a full, unshallow, unfiltered fetch for each — measured at **352 MiB and ~21s CPU per build**, with no object reuse even though both refs come from the same remote. One fetch into one object store with two worktrees halves the transfer.

**Files:**
- Modify: `agent/pullrequest/resource/dependencies.go`
- Modify: `agent/pullrequest/resource/in.go` (materialization loop)
- Test: `agent/pullrequest/resource/materialization_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestMaterializeFetchesTheRemoteOnce(t *testing.T) {
	fixture := newInFixture(t)
	var fetches int
	fixture.gitRunner = recordingGit{onRun: func(cmd resource.GitCommand) {
		if cmd.Operation == "fetch" {
			fetches++
		}
	}}

	if err := resource.In(context.Background(), fixture.request(), fixture.destination, fixture.deps()); err != nil {
		t.Fatal(err)
	}

	if fetches != 1 {
		t.Fatalf("fetch count = %d, want 1 for two worktrees from one remote", fetches)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/pullrequest/resource/ -run TestMaterializeFetchesTheRemoteOnce -v`
Expected: FAIL with `fetch count = 2, want 1`

- [ ] **Step 3: Add a shared-object-store materialization**

In `agent/pullrequest/resource/dependencies.go`, add an operation that fetches both refs in one invocation and then creates two worktrees:

```go
// materializePair fetches both refs from one remote into a single private
// object store, then checks out each into its own destination. Both refs come
// from the same repository, so a second fetch re-transfers near-identical
// history for no benefit.
func (git controlledGit) materializePair(ctx context.Context, command GitPairCommand) error {
	if command.RemoteURL == "" || command.SourceSHA == "" || command.TargetSHA == "" {
		return fmt.Errorf("forge-pr: invalid paired materialization command")
	}
	if err := git.runner.Run(ctx, directgit.Command{
		Dir: command.ObjectStore, Credential: command.Credential,
		Args: []string{"init", "--bare", "--initial-branch=concourse-materialized"},
	}); err != nil {
		return fmt.Errorf("forge-pr: initialize object store")
	}
	if err := git.runner.Run(ctx, directgit.Command{
		Dir: command.ObjectStore, Credential: command.Credential,
		Args: []string{
			"fetch", "--no-tags", "--no-recurse-submodules", command.RemoteURL,
			"+" + command.SourceSHA + ":refs/concourse/materialized/source",
			"+" + command.TargetSHA + ":refs/concourse/materialized/target",
		},
	}); err != nil {
		return fmt.Errorf("forge-pr: fetch pull request objects")
	}
	for _, worktree := range []struct {
		directory string
		sha       string
	}{
		{command.SourceDirectory, command.SourceSHA},
		{command.TargetDirectory, command.TargetSHA},
	} {
		if err := git.runner.Run(ctx, directgit.Command{
			Dir: command.ObjectStore, Credential: nil,
			Args: []string{"worktree", "add", "--detach", worktree.directory, worktree.sha},
		}); err != nil {
			return fmt.Errorf("forge-pr: materialize worktree")
		}
	}
	return nil
}
```

Add the matching typed command:

```go
type GitPairCommand struct {
	ObjectStore     string
	SourceDirectory string
	TargetDirectory string
	RemoteURL       string
	SourceSHA       string
	TargetSHA       string
	Credential      []byte
}
```

In `in.go`, replace the per-repository loop with one `materializePair` call, keeping every existing post-condition: `validateMaterializationRoot` and `validateRepositoryEvidence` still run per destination.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/pullrequest/resource/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/resource/dependencies.go agent/pullrequest/resource/in.go agent/pullrequest/resource/materialization_test.go
git commit -m "perf(forge-pr): fetch the remote once for both worktrees

Measured 352 MiB and ~21s CPU per admit build, fetching near-identical
history twice from the same remote. One bare object store plus two worktrees
halves the transfer and keeps every existing validation post-condition."
```

**Note for the implementer:** `git fsck --full --strict --no-reflogs` (`agent/snapshot/contracts/repository.go:146`) runs per checkout inside `repository/v1` resealing and costs ~5.7s of the ~21s. Removing or scoping it weakens a seal validation and is **deliberately out of scope here** — it needs its own security review.

---

### Task 4: Full-state observation

`Observe` already fully paginates reviews and comments; `selectReview` then discards all but one review. Deleting that selection is the whole change, and it also fixes a live bug: `normalizeThreads` builds its root index from one review's comments only, so a reply in review B to a root comment in review A fails with `"github review reply has no root"` permanently.

**Files:**
- Modify: `agent/pullrequest/github/observe.go`
- Test: `agent/pullrequest/github/observe_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestObserveResolvesRepliesAcrossReviews(t *testing.T) {
	server := newObserveServer(t, observeFixture{
		reviews: []string{
			`{"id":10,"user":{"id":1,"login":"alice"},"body":"first","commit_id":"` + fortyHex('a') + `","submitted_at":"2026-08-01T10:00:00Z","state":"COMMENTED"}`,
			`{"id":20,"user":{"id":2,"login":"bob"},"body":"","commit_id":"` + fortyHex('a') + `","submitted_at":"2026-08-01T11:00:00Z","state":"COMMENTED"}`,
		},
		comments: []string{
			`{"id":100,"pull_request_review_id":10,"user":{"id":1,"login":"alice"},"body":"root","commit_id":"` + fortyHex('a') + `","path":"a.go","line":1}`,
			`{"id":101,"pull_request_review_id":20,"user":{"id":2,"login":"bob"},"body":"reply","commit_id":"` + fortyHex('a') + `","path":"a.go","line":1,"in_reply_to_id":100}`,
		},
	})
	defer server.Close()

	observer, err := github.NewObserver(server.URL, staticToken("t"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	observation, err := observer.Observe(context.Background(), testLocator(), "")
	if err != nil {
		t.Fatalf("Observe with a cross-review reply = %v, want success", err)
	}
	if len(observation.ReviewBatches) != 2 {
		t.Fatalf("review batches = %d, want 2 (full state)", len(observation.ReviewBatches))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/pullrequest/github/ -run TestObserveResolvesRepliesAcrossReviews -v`
Expected: FAIL with `github review reply has no root`

- [ ] **Step 3: Replace selection with enumeration**

In `observe.go`, replace the `selected := selectReview(...)` block inside `Observe` with:

```go
		batches, threads, truncated, err := normalizeFullState(reviews, comments, observer.windowSize)
		if err != nil {
			return pullrequest.Observation{}, err
		}
		observation.ReviewBatches = batches
		observation.Threads = threads
		observation.Truncated = truncated
```

Add `normalizeFullState`, which groups every comment once — so a reply resolves against any review's root — and builds one batch per submitted review:

```go
// normalizeFullState enumerates every submitted review and every review comment
// in one pass. Grouping threads across all comments (rather than per review) is
// what allows a reply submitted in one review to resolve a root comment left in
// another, which GitHub's pull_request_review_id makes routine.
func normalizeFullState(reviews []review, comments []reviewComment, window int) ([]pullrequest.ReviewBatch, []pullrequest.Thread, bool, error) {
	threads, threadIDsByReview, err := normalizeAllThreads(comments)
	if err != nil {
		return nil, nil, false, err
	}
	batches := make([]pullrequest.ReviewBatch, 0, len(reviews))
	for _, value := range reviews {
		if value.SubmittedAt == nil {
			continue
		}
		if value.ID <= 0 || value.User.ID <= 0 || strings.TrimSpace(value.CommitID) == "" {
			return nil, nil, false, fmt.Errorf("github submitted review is invalid")
		}
		if err := validateObjectID(value.CommitID); err != nil {
			return nil, nil, false, fmt.Errorf("github submitted review commit is invalid")
		}
		if strings.TrimSpace(value.Body) != "" {
			id := "review-" + strconv.FormatInt(value.ID, 10) + "-body"
			threads = append(threads, pullrequest.Thread{
				ID: id, Iteration: strconv.FormatInt(value.ID, 10),
				Comments: []contracts.PullRequestComment{{
					ID:     id + "-comment",
					Author: githubUser(value.User), Body: value.Body, CommitSHA: value.CommitID,
				}},
				lastActivity: value.SubmittedAt.UTC(),
			})
			threadIDsByReview[value.ID] = append(threadIDsByReview[value.ID], id)
		}
		authority := append([]string(nil), threadIDsByReview[value.ID]...)
		sort.Strings(authority)
		batches = append(batches, pullrequest.ReviewBatch{
			ID: "review-" + strconv.FormatInt(value.ID, 10), ReviewID: strconv.FormatInt(value.ID, 10),
			CommitSHA: value.CommitID, Reviewer: githubUser(value.User), Ready: true, ThreadIDs: authority,
		})
	}
	sort.Slice(batches, func(i, j int) bool { return batches[i].ID < batches[j].ID })
	windowed, truncated := applyThreadWindow(threads, window)
	return batches, windowed, truncated, nil
}
```

Add `normalizeAllThreads` — `normalizeThreads` with its root index built from **all** comments rather than one review's:

```go
// normalizeAllThreads groups every review comment into threads in a single
// pass. The root index spans all comments, so a reply resolves regardless of
// which review submitted it. It returns thread IDs keyed by the review that
// submitted each thread's ROOT comment, which is what a batch's ThreadIDs
// must reference.
func normalizeAllThreads(comments []reviewComment) ([]pullrequest.Thread, map[int64][]string, error) {
	byID := make(map[int64]reviewComment, len(comments))
	for _, comment := range comments {
		if comment.ID <= 0 || comment.User.ID <= 0 || strings.TrimSpace(comment.Body) == "" {
			return nil, nil, fmt.Errorf("github review comment is invalid")
		}
		if _, exists := byID[comment.ID]; exists {
			return nil, nil, fmt.Errorf("github review comment id is duplicate")
		}
		byID[comment.ID] = comment
	}
	groups := make(map[int64][]reviewComment)
	for _, original := range comments {
		current, root := original, original.ID
		seen := map[int64]struct{}{original.ID: {}}
		for current.InReplyToID != nil {
			parent, found := byID[*current.InReplyToID]
			if !found {
				return nil, nil, fmt.Errorf("github review reply has no root")
			}
			if _, cycled := seen[parent.ID]; cycled {
				return nil, nil, fmt.Errorf("github review reply cycles")
			}
			seen[parent.ID] = struct{}{}
			current, root = parent, parent.ID
		}
		groups[root] = append(groups[root], original)
	}
	threads := make([]pullrequest.Thread, 0, len(groups))
	idsByReview := make(map[int64][]string, len(groups))
	for root, members := range groups {
		sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
		id := "thread-" + strconv.FormatInt(root, 10)
		thread := pullrequest.Thread{ID: id, Iteration: strconv.FormatInt(root, 10)}
		for _, member := range members {
			thread.Comments = append(thread.Comments, contracts.PullRequestComment{
				ID:     "comment-" + strconv.FormatInt(member.ID, 10),
				Author: githubUser(member.User), Body: member.Body, CommitSHA: member.CommitID,
			})
			if member.UpdatedAt.After(thread.lastActivity) {
				thread.lastActivity = member.UpdatedAt.UTC()
			}
		}
		if anchor := anchorFor(byID[root]); anchor != nil {
			thread.Anchor = anchor
		}
		threads = append(threads, thread)
		if owner := byID[root].ReviewID; owner != nil {
			idsByReview[*owner] = append(idsByReview[*owner], id)
		}
	}
	return threads, idsByReview, nil
}
```

Two supporting changes belong in this task, because the code above depends on them:

- Add `UpdatedAt time.Time \`json:"updated_at"\`` to the `reviewComment` wire struct. GitHub already returns it; it is decoded here purely to order the window in Task 5.
- Add an unexported `lastActivity time.Time` field to `pullrequest.Thread`. It never serializes — it is build-time ordering state only, and the sealed record shape is unchanged.

Delete `selectReview`, `afterWatermark` and `watermarkFor`, and delete the `cursor.Watermark` / `cursor.BatchDigest` writes.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/pullrequest/github/ -v`
Expected: PASS. Existing delta-shaped assertions in `observe_test.go` that expect exactly one batch must be updated to expect full state — that is the point of the change, not a regression.

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/github/observe.go agent/pullrequest/github/observe_test.go
git commit -m "feat(forge-pr): observe complete PR state instead of one review

Observe already paginated everything and then discarded all but one review.
Enumerating instead also fixes a permanent failure: a reply submitted in one
review against a root comment from another could not resolve its root, and
GitHub assigns pull_request_review_id to the submitting review, so any
threaded back-and-forth bricked the observer."
```

---

### Task 5: Activity-ordered window with a truncation marker

`maxThreads = 512` and `maxPullRequestThreads = 512` **reject rather than truncate**, and `observe.go:140-142` turns rejection into a permanent `Observe` failure — so an unbounded full state bricks a long-lived PR. Bounding by *last activity* keeps any thread that just received a comment inside the window by construction.

`PullRequestComment` carries no timestamp and `reviewComment` does not decode one, so the wire type gains `UpdatedAt` for ordering only. The sealed record shape is unchanged.

**Files:**
- Modify: `agent/pullrequest/github/observe.go`
- Modify: `agent/pullrequest/types.go` (add `Truncated`)
- Test: `agent/pullrequest/github/observe_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestObserveWindowKeepsRecentlyActiveThreadsAndMarksTruncation(t *testing.T) {
	fixture := observeFixtureWithThreads(t, 200) // 200 root comments, oldest first
	fixture.bumpActivity(threadIndex(0), "2026-08-05T12:00:00Z") // oldest thread, newest reply
	server := newObserveServer(t, fixture)
	defer server.Close()

	observer, err := github.NewObserverWithWindow(server.URL, staticToken("t"), server.Client(), 150)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := observer.Observe(context.Background(), testLocator(), "")
	if err != nil {
		t.Fatal(err)
	}

	if len(observation.Threads) != 150 {
		t.Fatalf("threads = %d, want the 150 most recently active", len(observation.Threads))
	}
	if !observation.Truncated {
		t.Fatal("Truncated = false, want true when threads were dropped")
	}
	if !containsThread(observation.Threads, threadIDFor(0)) {
		t.Fatal("the oldest thread received the newest reply and must be inside the window")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/pullrequest/github/ -run TestObserveWindowKeepsRecentlyActiveThreadsAndMarksTruncation -v`
Expected: FAIL — `NewObserverWithWindow` undefined.

- [ ] **Step 3: Implement the window**

`reviewComment.UpdatedAt` and `Thread.lastActivity` were added in Task 4 and are already populated — comment threads take the maximum `UpdatedAt` across their comments, body threads take the review's `SubmittedAt`. This task only consumes them:

```go
// defaultThreadWindow bounds what a single observation carries. The sealed
// record permits 512, but the consumer is an agent: handing an LLM every
// thread a long-lived PR ever had costs tokens and degrades its judgement.
const defaultThreadWindow = 150

// applyThreadWindow keeps the most recently active threads. Ordering by last
// activity is the safety property -- a thread receiving a new comment sorts
// into the window by construction, so a human replying on an ancient thread is
// never dropped. The retained set is then sorted by ID, which Observation
// .Validate requires.
func applyThreadWindow(threads []pullrequest.Thread, window int) ([]pullrequest.Thread, bool) {
	if window <= 0 {
		window = defaultThreadWindow
	}
	if len(threads) <= window {
		sort.Slice(threads, func(i, j int) bool { return threads[i].ID < threads[j].ID })
		return threads, false
	}
	sort.Slice(threads, func(i, j int) bool {
		if threads[i].lastActivity.Equal(threads[j].lastActivity) {
			return threads[i].ID > threads[j].ID
		}
		return threads[i].lastActivity.After(threads[j].lastActivity)
	})
	retained := threads[:window]
	sort.Slice(retained, func(i, j int) bool { return retained[i].ID < retained[j].ID })
	return retained, true
}
```

Add to `pullrequest.Observation` in `types.go`:

```go
	// Truncated reports that the observation carries only the most recently
	// active window of threads. The agent must not infer that an absent thread
	// does not exist.
	Truncated bool `json:"truncated,omitempty"`
```

Add `NewObserverWithWindow`, and have `NewObserver` delegate to it with `defaultThreadWindow`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/pullrequest/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/github/observe.go agent/pullrequest/types.go agent/pullrequest/github/observe_test.go
git commit -m "feat(forge-pr): bound observation to the most recently active threads

The sealed bounds reject rather than truncate and Observe turns rejection into
a permanent failure, so unbounded full state bricks a long-lived PR. Ordering
by last activity keeps any thread that just received a comment inside the
window by construction, and Truncated tells the agent its view is partial."
```

---

### Task 6: Select the review batch by ID

Six sites assume `ReviewBatches` holds exactly one element and index it positionally. Under full state all six are permanently wrong. Selecting by the response's `BatchID` restores the safety property — and the batch must exist in the sealed observation, so the agent cannot name one that was never observed.

**Files:**
- Modify: `agent/pullrequest/revision_executor.go:592`, `:693`, `:700`, `:705`
- Modify: `agent/pullrequest/monitor_run_inspector.go:310`, `:503-504`
- Test: `agent/pullrequest/revision_executor_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestRevisionExecutorPublishesTheNamedBatchNotTheFirst(t *testing.T) {
	fixture := newRevisionFixture(t)
	fixture.observation.ReviewBatches = []pullrequest.ReviewBatch{
		{ID: "review-10", ReviewID: "10", CommitSHA: fixture.sourceSHA, Reviewer: "alice", Ready: true},
		{ID: "review-20", ReviewID: "20", CommitSHA: fixture.sourceSHA, Reviewer: "bob", Ready: true},
	}
	fixture.response.BatchID = "review-20"

	published, err := fixture.executor.Execute(context.Background(), fixture.request())
	if err != nil {
		t.Fatal(err)
	}

	if published.Batch.ID != "review-20" {
		t.Fatalf("published batch = %q, want the named review-20", published.Batch.ID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/pullrequest/ -run TestRevisionExecutorPublishesTheNamedBatchNotTheFirst -v`
Expected: FAIL with `published batch = "review-10"` — index 0 was published.

- [ ] **Step 3: Select by ID**

Add a shared helper in `agent/pullrequest/types.go`:

```go
// BatchByID returns the exact observed batch a response names. The batch must
// exist in the sealed observation: an agent may choose which batch to answer,
// but never invent one.
func (observation Observation) BatchByID(id string) (ReviewBatch, bool) {
	for _, batch := range observation.ReviewBatches {
		if batch.ID == id {
			return batch, true
		}
	}
	return ReviewBatch{}, false
}
```

Replace every `observation.ReviewBatches[0]` with a `BatchByID(response.BatchID)` lookup that errors when absent. Replace the `len(observation.ReviewBatches) != 1` guards for the `review_batch` trigger with "the named batch exists"; the `len(...) != 0` guards for conflict and freshness triggers become "the response names no batch".

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/pullrequest/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/revision_executor.go agent/pullrequest/monitor_run_inspector.go agent/pullrequest/types.go agent/pullrequest/revision_executor_test.go
git commit -m "fix(pullrequest): publish the review batch the response names

Six sites indexed ReviewBatches[0], which was safe only while an observation
carried exactly one review. Selecting by BatchID, and requiring that ID to
exist in the sealed observation, keeps the agent from naming a batch that was
never observed."
```

---

## Verification

- [ ] `go build ./...` — clean
- [ ] `go vet ./...` — clean
- [ ] `go test ./agent/pullrequest/... ./agent/publisher/... ./atc/atccmd/...` — pass
- [ ] Drain ports 5434–5442 with SIGTERM, then `ginkgo -p ./atc/db` — pass
- [ ] `go test ./deploy/...` — pass

## Self-review notes

Spec sections covered by this plan: full-state observation (Task 4), activity window and truncation marker (Task 5), the `len==1` consumers (Task 6), and all three cost prerequisites (Tasks 1–3). Spec sections **not** covered, each needing its own plan: generic source instances, the skip guard, server-side re-launch, `resource_sources:` declaration, authority-spine gaps, boot gate, live proof.

`body.Trigger` still comes from `ActionFor` after this phase. Computing it from full state is part of the skip-guard plan, not this one — Task 1 deliberately keeps `ActionFor` in `in` so that this phase changes observation shape without also changing trigger semantics.
