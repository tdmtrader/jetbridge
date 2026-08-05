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

- [ ] **Step 1: Rewrite the test that asserts the behaviour being removed**

`agent/pullrequest/resource/in_test.go:20` currently has `TestForgePRInRejectsStaleVersionBeforeGit`, which asserts exactly the gate this task deletes — including `t.Fatal("git must not run")`. It must be inverted, not left alone. Replace it wholesale with:

```go
func TestForgePRInMaterializesWhenTheObservationMovedSinceCheck(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	// A version selected against an earlier observation. The provider has since
	// moved, so the recomputed cursor differs from the one recorded here.
	version := resource.Version{
		Provider: "github", ExternalID: "42", SourceSHA: sha('a'), TargetSHA: sha('b'),
		ActionKind: "review_batch", ActionDigest: digest('d'), Cursor: "stale", BindingRevision: "7",
	}
	gitRan := false
	var output bytes.Buffer
	err := resource.In(
		context.Background(),
		t.TempDir(),
		bytes.NewReader(checkInput(t, source, &version)),
		&output,
		&bytes.Buffer{},
		resource.Dependencies{
			ObserverFactory: fixedObserver(observerFunc(func(_ context.Context, l pullrequest.Locator, _ pullrequest.Cursor) (pullrequest.Observation, error) {
				return activeObservation(l, "actual"), nil
			})),
			Clock:     func() time.Time { return now },
			GitRunner: runnerFunc(func(context.Context, resource.GitCommand) error { gitRan = true; return nil }),
		},
	)

	if err != nil {
		t.Fatalf("In() with a moved observation = %v, want materialization to proceed", err)
	}
	if !gitRan {
		t.Fatal("git did not run: the moved observation was rejected before materialization")
	}
	if strings.Contains(err2String(err), source.ReadToken) {
		t.Fatal("error leaks token")
	}
}

func err2String(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
```

**Leave `TestForgePRInRejectsStaleBindingBeforeObserver` (`in_test.go:526`) alone** — it covers the binding-revision fence, which this task does not touch.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/pullrequest/resource/ -run TestForgePRInMaterializesWhenTheObservationMovedSinceCheck -v`
Expected: FAIL — `In()` returns `forge-pr: selected version does not match current pull request`.

- [ ] **Step 3: Delete the gate**

In `agent/pullrequest/resource/in.go`, the block after the `observer.Observe` call currently derives an action, then computes `expected := versionFor(request.Source, action)` and rejects unless `equalVersion(expected, *request.Version)`. Keep the derivation; delete the comparison:

```go
	// The version identifies which observation the server selected; it is not a
	// promise the provider stood still. The admit job is Serial, so builds queue,
	// and Concourse consumes a version at build start regardless of outcome --
	// failing here burns the event instead of retrying it. Materialize what is
	// current and let the server reject it downstream against durable state.
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

Delete the `expected := versionFor(...)` line and the `if !equalVersion(...)` block.

- [ ] **Step 4: Run the package**

Run: `go test ./agent/pullrequest/resource/ -v`
Expected: PASS. If the compiler reports `equalVersion` or `versionFor` unused, leave them — `versionFor` is still used by `check.go`, and `equalVersion` is removed in Task 6.

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/resource/in.go agent/pullrequest/resource/in_test.go
git commit -m "fix(forge-pr): stop failing a materialization because the PR moved

in re-observed at get time and demanded byte-equality with the version check
selected. The admit job is Serial so builds queue routinely, and Concourse
consumes a version at build start regardless of outcome -- so the failure
burned the event rather than retrying it. The test that asserted the old
behaviour is inverted rather than deleted."
```

---

### Task 2: Conditional requests in the GitHub client

At a 5-minute poll each watched PR issues ~288 polls/day across three endpoints against a 5,000/hr ceiling. GitHub answers `If-None-Match` with a `304` it does not bill.

There is no `client` type — the HTTP surface is `Observer` (`client.go:25`), with unexported `get` (`:58`) and `getPage` (`:98`). Note `get` currently treats anything outside 2xx as an error (`:82`), so a 304 is a hard failure today.

**Files:**
- Modify: `agent/pullrequest/github/client.go`
- Test: `agent/pullrequest/github/observe_test.go`

- [ ] **Step 1: Write the failing test**

Add to `agent/pullrequest/github/observe_test.go`, following the existing server style:

```go
func TestObserveReusesCachedBodiesOnNotModified(t *testing.T) {
	t.Parallel()
	var serverURL string
	conditional := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("If-None-Match") == `"etag-1"` {
			conditional++
			response.WriteHeader(http.StatusNotModified)
			return
		}
		response.Header().Set("ETag", `"etag-1"`)
		switch request.URL.Path {
		case "/repos/acme/widget/pulls/42":
			writeFixtureAt(t, response, "pull_request_active.json", serverURL)
		case "/repos/acme/widget/pulls/42/reviews":
			writeFixture(t, response, "reviews_page_1.json")
		case "/repos/acme/widget/pulls/42/comments":
			writeFixture(t, response, "review_comments_page_1.json")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	observer, err := github.NewObserver(server.URL, tokenFunc(func(context.Context) (string, error) {
		return "token-1", nil
	}), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	locator := pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}

	first, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatalf("second Observe = %v, want the cached bodies to be reused", err)
	}

	if conditional == 0 {
		t.Fatal("no conditional request was issued")
	}
	if first.SourceSHA != second.SourceSHA || len(first.Threads) != len(second.Threads) {
		t.Fatalf("cached observation diverged: %+v vs %+v", first, second)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/pullrequest/github/ -run TestObserveReusesCachedBodiesOnNotModified -v`
Expected: FAIL — the second `Observe` errors, because a 304 falls through to `githubStatusError` at `client.go:82`.

- [ ] **Step 3: Add a bounded response cache to `Observer`**

Add to the `Observer` struct and a new block in `client.go`:

```go
// maxCachedResponses bounds the cache. An observer watching one PR touches a
// handful of URLs; the bound stops a paginated endpoint from growing it without
// limit.
const maxCachedResponses = 64

type cachedResponse struct {
	etag string
	body []byte
}

func (observer *Observer) cachedFor(key string) (cachedResponse, bool) {
	observer.cacheMu.RLock()
	defer observer.cacheMu.RUnlock()
	entry, found := observer.cache[key]
	return entry, found
}

// storeCached retains an immutable copy. Bodies are already bounded by
// maxBodyBytes before this is reached.
func (observer *Observer) storeCached(key, etag string, body []byte) {
	if etag == "" {
		return
	}
	observer.cacheMu.Lock()
	defer observer.cacheMu.Unlock()
	if observer.cache == nil {
		observer.cache = make(map[string]cachedResponse, maxCachedResponses)
	}
	if len(observer.cache) >= maxCachedResponses {
		for existing := range observer.cache {
			delete(observer.cache, existing)
			break
		}
	}
	observer.cache[key] = cachedResponse{etag: etag, body: append([]byte(nil), body...)}
}
```

Add the fields `cacheMu sync.RWMutex` and `cache map[string]cachedResponse` to `Observer`.

In **both** `get` and `getPage`, before `observer.client.Do(request)`:

```go
	cacheKey := target.String()
	if entry, found := observer.cachedFor(cacheKey); found {
		request.Header.Set("If-None-Match", entry.etag)
	}
```

and immediately after the `Do`, before the 2xx check:

```go
	if response.StatusCode == http.StatusNotModified {
		entry, found := observer.cachedFor(cacheKey)
		if !found {
			return fmt.Errorf("github returned 304 without a cached body")
		}
		return decodeJSON(entry.body, destination)
	}
```

(in `getPage`, return `(nil, ...)` shapes to match its signature, and preserve its `Link`-header handling by caching the header alongside the body if pagination must survive a 304 — for the first cut, only cache single-page responses and skip the cache when a `Link` header is present.)

After a successful read of `raw`, call `observer.storeCached(cacheKey, response.Header.Get("ETag"), raw)`.

- [ ] **Step 4: Run the package**

Run: `go test ./agent/pullrequest/github/ -v`
Expected: PASS. `TestObserveRejectsUnsafePaginationAndOversizedResponses` and `TestObserveClassifiesOnlyProvenRateLimits` must still pass — the 304 branch sits before the status check but after the transport error check, so neither is reordered.

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/github/client.go agent/pullrequest/github/observe_test.go
git commit -m "perf(forge-pr): serve unchanged GitHub reads from an ETag cache

Each watched PR spends ~864 requests/day against a 5,000/hr ceiling. GitHub
answers If-None-Match with a 304 it does not bill -- which the client
previously treated as a hard error, since it is outside 2xx."
```

---

### Task 3: Materialize both worktrees from one fetch

`in` materializes `source-repository` and `target-repository` with a separate full, unshallow, unfiltered fetch each — measured at **352 MiB and ~21s CPU per build** — even though both refs come from the same remote.

`GitRunner` is a single-method interface (`Run(ctx, GitCommand)`) implemented by `runnerFunc` throughout the tests. **Do not add a method to it** — that breaks every existing fake. Extend `GitCommand` instead.

**Files:**
- Modify: `agent/pullrequest/resource/protocol.go` (`GitCommand` fields)
- Modify: `agent/pullrequest/resource/dependencies.go` (`controlledGit.Run`)
- Modify: `agent/pullrequest/resource/in.go` (materialization)
- Test: `agent/pullrequest/resource/in_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestForgePRInFetchesTheRemoteOnceForBothWorktrees(t *testing.T) {
	now, source, observation, version := currentInFixture(t)
	commands := 0
	var output bytes.Buffer
	err := resource.In(
		context.Background(),
		t.TempDir(),
		bytes.NewReader(checkInput(t, source, &version)),
		&output,
		&bytes.Buffer{},
		resource.Dependencies{
			ObserverFactory: fixedObserver(observerFunc(func(_ context.Context, l pullrequest.Locator, _ pullrequest.Cursor) (pullrequest.Observation, error) {
				return observation, nil
			})),
			Clock: func() time.Time { return now },
			GitRunner: runnerFunc(func(_ context.Context, command resource.GitCommand) error {
				commands++
				if command.SecondDirectory == "" || command.SecondSHA == "" {
					t.Fatalf("expected a paired materialization, got %+v", command)
				}
				return nil
			}),
		},
	)
	_ = err // materialization validation runs after git; this test asserts the git shape only

	if commands != 1 {
		t.Fatalf("git invocations = %d, want 1 paired fetch for two worktrees", commands)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/pullrequest/resource/ -run TestForgePRInFetchesTheRemoteOnceForBothWorktrees -v`
Expected: FAIL with `git invocations = 2, want 1` (and the `SecondDirectory` fatal, since the field does not exist yet — add it first if the package will not compile).

- [ ] **Step 3: Add pair fields and one paired materialization**

In `protocol.go`, extend `GitCommand`:

```go
	// SecondDirectory and SecondSHA carry the second worktree of a paired
	// materialization. Both refs come from one remote, so fetching twice
	// re-transfers near-identical history for no benefit.
	SecondDirectory string
	SecondSHA       string
```

In `dependencies.go`, `controlledGit.Run` gains a paired branch that validates both destinations exactly as the single one is validated today, then runs: `git init` in a private object store, one `fetch` carrying `+<sourceSHA>:refs/concourse/materialized/source` **and** `+<targetSHA>:refs/concourse/materialized/target`, then `git worktree add --detach` per destination — preserving every existing refusal (no saved remote, no shallow boundary, no alternates, no sparse-checkout, no `core.worktree`).

In `in.go`, replace the two-iteration materialization loop with one `GitCommand` carrying both directories and both SHAs. `validateMaterializationRoot` and `validateRepositoryEvidence` still run **per destination** afterwards — unchanged.

- [ ] **Step 4: Run the package**

Run: `go test ./agent/pullrequest/resource/ -v`
Expected: PASS, including `TestForgePRInUsesExactObjectFetchForTerminalObservation` — the terminal fetch mode must still be honoured for both refs.

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/resource/protocol.go agent/pullrequest/resource/dependencies.go agent/pullrequest/resource/in.go agent/pullrequest/resource/in_test.go
git commit -m "perf(forge-pr): fetch the remote once for both worktrees

Measured 352 MiB and ~21s CPU per admit build, fetching near-identical history
twice from one remote. A single bare object store plus two worktrees halves
the transfer. GitCommand grows pair fields rather than GitRunner growing a
method, so every existing runnerFunc fake keeps compiling."
```

**Out of scope:** `git fsck --full --strict --no-reflogs` (`agent/snapshot/contracts/repository.go:146`) costs ~5.7s of the ~21s but removing or scoping it weakens a seal validation and needs its own security review.

---

### Task 4: Full-state observation

`Observe` already fully paginates reviews and comments; `selectReview` then discards all but one review. Deleting that selection is the whole change, and it also fixes a live bug: `normalizeThreads` builds its root index from one review's comments only, so a reply in review B to a root comment in review A fails with `"github review reply has no root"` permanently.

**Files:**
- Modify: `agent/pullrequest/github/observe.go`
- Test: `agent/pullrequest/github/observe_test.go`

- [ ] **Step 1: Add testdata for a cross-review reply, then write the failing test**

Create `agent/pullrequest/github/testdata/reviews_cross_review.json`:

```json
[
  {"id":10,"user":{"id":1,"login":"alice"},"body":"first pass","commit_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","submitted_at":"2026-08-01T10:00:00Z","state":"COMMENTED"},
  {"id":20,"user":{"id":2,"login":"bob"},"body":"","commit_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","submitted_at":"2026-08-01T11:00:00Z","state":"COMMENTED"}
]
```

Create `agent/pullrequest/github/testdata/review_comments_cross_review.json`:

```json
[
  {"id":100,"pull_request_review_id":10,"user":{"id":1,"login":"alice"},"body":"root comment","commit_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","original_commit_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","path":"a.go","line":1,"original_line":1,"updated_at":"2026-08-01T10:00:00Z"},
  {"id":101,"pull_request_review_id":20,"user":{"id":2,"login":"bob"},"body":"reply from another review","commit_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","original_commit_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","path":"a.go","line":1,"original_line":1,"in_reply_to_id":100,"updated_at":"2026-08-01T11:00:00Z"}
]
```

Add to `agent/pullrequest/github/observe_test.go`:

```go
func TestObserveResolvesRepliesAcrossReviewsAndReturnsFullState(t *testing.T) {
	t.Parallel()
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/acme/widget/pulls/42":
			writeFixtureAt(t, response, "pull_request_active.json", serverURL)
		case "/repos/acme/widget/pulls/42/reviews":
			writeFixture(t, response, "reviews_cross_review.json")
		case "/repos/acme/widget/pulls/42/comments":
			writeFixture(t, response, "review_comments_cross_review.json")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	observer, err := github.NewObserver(server.URL, tokenFunc(func(context.Context) (string, error) {
		return "token-1", nil
	}), server.Client())
	if err != nil {
		t.Fatal(err)
	}

	observation, err := observer.Observe(context.Background(), pullrequest.Locator{
		Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42",
	}, "")
	if err != nil {
		t.Fatalf("Observe with a cross-review reply = %v, want success", err)
	}
	if len(observation.ReviewBatches) != 2 {
		t.Fatalf("review batches = %d, want 2 (full state, not one review)", len(observation.ReviewBatches))
	}
	// Both comments belong to one thread rooted at comment 100, plus one
	// synthetic body thread for review 10 (review 20 has an empty body).
	if len(observation.Threads) != 2 {
		t.Fatalf("threads = %d, want 2", len(observation.Threads))
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

The fixture is generated in the test rather than checked in, because 200 threads is not a reviewable testdata file. Add to `agent/pullrequest/github/observe_test.go`:

```go
func TestObserveWindowKeepsRecentlyActiveThreadsAndMarksTruncation(t *testing.T) {
	t.Parallel()
	const roots = 200
	// Comment i is authored at 10:00 + i minutes, EXCEPT the oldest root, which
	// receives a reply far later -- it must survive the window on activity.
	comments := make([]string, 0, roots+1)
	for index := 0; index < roots; index++ {
		comments = append(comments, fmt.Sprintf(
			`{"id":%d,"pull_request_review_id":10,"user":{"id":1,"login":"alice"},"body":"c%d","commit_id":%q,"original_commit_id":%q,"path":"a.go","line":1,"original_line":1,"updated_at":"2026-08-01T%02d:%02d:00Z"}`,
			1000+index, index, sha('a'), sha('a'), 10+index/60, index%60))
	}
	comments = append(comments, fmt.Sprintf(
		`{"id":9999,"pull_request_review_id":10,"user":{"id":2,"login":"bob"},"body":"late reply","commit_id":%q,"original_commit_id":%q,"path":"a.go","line":1,"original_line":1,"in_reply_to_id":1000,"updated_at":"2026-08-05T12:00:00Z"}`,
		sha('a'), sha('a')))

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/acme/widget/pulls/42":
			writeFixtureAt(t, response, "pull_request_active.json", serverURL)
		case "/repos/acme/widget/pulls/42/reviews":
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(response, `[{"id":10,"user":{"id":1,"login":"alice"},"body":"","commit_id":%q,"submitted_at":"2026-08-01T10:00:00Z","state":"COMMENTED"}]`, sha('a'))
		case "/repos/acme/widget/pulls/42/comments":
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(response, "[%s]", strings.Join(comments, ","))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	observer, err := github.NewObserverWithWindow(server.URL, tokenFunc(func(context.Context) (string, error) {
		return "token-1", nil
	}), server.Client(), 150)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := observer.Observe(context.Background(), pullrequest.Locator{
		Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42",
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(observation.Threads) != 150 {
		t.Fatalf("threads = %d, want the 150 most recently active", len(observation.Threads))
	}
	if !observation.Truncated {
		t.Fatal("Truncated = false, want true when threads were dropped")
	}
	oldestRoot := "thread-1000"
	found := false
	for _, thread := range observation.Threads {
		if thread.ID == oldestRoot {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s received the newest reply and must be inside the window", oldestRoot)
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

The fixture is `newPRRevisionExecutorFixture(t, trigger)` returning `*prRevisionExecutorFixture` (`revision_executor_test.go:1021`). Read that constructor before writing the test — it builds the observation, candidate, validation, impact and response snapshot refs, and the assertions below must set the observation's batches through whatever it exposes. Add to `agent/pullrequest/revision_executor_test.go`:

```go
func TestPRRevisionExecutorPublishesTheNamedBatchNotTheFirst(t *testing.T) {
	fixture := newPRRevisionExecutorFixture(t, contracts.PullRequestTriggerReviewBatch)
	// Full state carries every batch; the response names which one it answers.
	fixture.setObservationBatches(
		pullrequest.ReviewBatch{ID: "review-10", ReviewID: "10", CommitSHA: revisionObjectID('a'), Reviewer: "alice", Ready: true},
		pullrequest.ReviewBatch{ID: "review-20", ReviewID: "20", CommitSHA: revisionObjectID('a'), Reviewer: "bob", Ready: true},
	)
	fixture.setResponseBatchID("review-20")

	published, err := fixture.execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if published.Batch.ID != "review-20" {
		t.Fatalf("published batch = %q, want the named review-20", published.Batch.ID)
	}
}
```

`setObservationBatches`, `setResponseBatchID` and `execute` are thin helpers on the existing fixture — add whichever the constructor does not already provide, matching its established style rather than inventing a parallel one.

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
