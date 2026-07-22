package dispatch_test

import (
	"strings"
	"time"

	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/credentials/credentialsfakes"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

// fakeBackend implements the two Backend calls dispatch makes:
// platform-user resolution and credential decryption.
type fakeBackend struct {
	credentials.Backend
	platformUserID int
	creds          map[int]map[string]*credentials.Credential
}

func (f *fakeBackend) UserBySub(sub string) (int, string, bool, error) {
	if sub == credentials.PlatformUserSub {
		return f.platformUserID, credentials.PlatformUserName, true, nil
	}
	return 0, "", false, nil
}

func (f *fakeBackend) Resolve(userID int, kind string) (*credentials.Credential, bool, error) {
	cred, ok := f.creds[userID][kind]
	return cred, ok, nil
}

type fakeWorkflows struct {
	byName map[string]*workflow.Definition
}

func (f *fakeWorkflows) Live(name string) (*workflow.Definition, bool, error) {
	d, ok := f.byName[name]
	return d, ok, nil
}

func (f *fakeWorkflows) Get(name string, version int) (*workflow.Definition, bool, error) {
	d, ok := f.byName[name]
	if !ok || d.Version != version {
		return nil, false, nil
	}
	return d, true, nil
}

type fakeSaver struct {
	savedName string
	savedCfg  atc.Config
	id        int
	err       error
}

func (f *fakeSaver) SaveTemplate(name string, cfg atc.Config) (int, error) {
	f.savedName, f.savedCfg = name, cfg
	if f.err != nil {
		return 0, f.err
	}
	return f.id, nil
}

func smokeDefinition() *workflow.Definition {
	return &workflow.Definition{
		Name: "smoke", Version: 3, SchemaVersion: 2, ContentHash: "abc123", Live: true,
		Config: workflow.Config{
			SchemaVersion: 2,
			Name:          "smoke",
			SpecDelivery:  "files",
			Defaults:      workflow.Defaults{Model: "claude-sonnet-5", MaxTurns: 5},
			Prompts:       map[string]string{"do": "Do it."},
			Steps: []workflow.Step{
				{Agent: "implement", Prompt: "do", Inputs: []string{"ticket"}, Outputs: []string{"workspace"}},
			},
		},
	}
}

func TestDispatchOneRejectsSchemaThreeBeforeLegacySideEffects(t *testing.T) {
	deps, store, saver, runs := dispatchDeps(t)
	definition := deps.Workflows.(*fakeWorkflows).byName["smoke"]
	definition.SchemaVersion = 3
	definition.Config.SchemaVersion = 3
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if !errors.Is(err, dispatch.ErrRenderRefused) || !strings.Contains(err.Error(), "schema_version 3") {
		t.Fatalf("error = %v, want explicit schema-version legacy-path refusal", err)
	}
	if saver.savedName != "" || runs.CreateRunCallCount() != 0 {
		t.Fatalf("v3 reached legacy persistence: saver=%q create-runs=%d", saver.savedName, runs.CreateRunCallCount())
	}
	got, _, _ := store.Get(id)
	if got.WorkflowVersion != nil || got.State != tickets.StateQueued {
		t.Fatalf("v3 refusal must precede version freeze/state changes: %+v", got)
	}
}

func TestDispatchOneAcceptsSchemaOneOnLegacyPath(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	definition := deps.Workflows.(*fakeWorkflows).byName["smoke"]
	definition.SchemaVersion = 1
	definition.Config.SchemaVersion = 1
	id := queuedTicket(t, store, "smoke")
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("schema-version-1 legacy dispatch: %v", err)
	}
}

func dispatchDeps(t *testing.T) (dispatch.Deps, *tickets.MemoryStore, *fakeSaver, *dbfakes.FakePipelineRunFactory) {
	t.Helper()
	store := tickets.NewMemoryStore()
	saver := &fakeSaver{id: 77}
	runs := new(dbfakes.FakePipelineRunFactory)
	run := new(dbfakes.FakePipelineRun)
	run.IDReturns(555)
	runs.CreateRunReturns(run, nil)

	deps := dispatch.Deps{
		Tickets:    store,
		Workflows:  &fakeWorkflows{byName: map[string]*workflow.Definition{"smoke": smokeDefinition()}},
		Templates:  saver,
		Runs:       runs,
		Principals: principals.NewMemoryStore(),
		Credentials: &fakeBackend{
			platformUserID: 9,
			creds: map[int]map[string]*credentials.Credential{
				9: {credentials.KindAnthropicOAuth: {Kind: credentials.KindAnthropicOAuth, Token: "platform-tok"}},
			},
		},
		Secrets:        new(credentialsfakes.FakeSecretAttacher),
		ATCExternalURL: "http://concourse.home",
		RepoBaseURL:    "https://github.com",
	}
	return deps, store, saver, runs
}

func queuedTicket(t *testing.T, store *tickets.MemoryStore, workflowName string) int {
	t.Helper()
	id, err := store.Create(&tickets.Ticket{
		Title: "fix X", Body: "details", Origin: "fly",
		Repo: "tdmtrader/jetbridge", TargetBranch: "main",
		WorkflowName: workflowName, UserName: "tdm", CreatedBy: "tdm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDispatchOneHappyPath(t *testing.T) {
	deps, store, saver, runs := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")

	res, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	if res.RunID != 555 || res.PipelineName != fmt.Sprintf("agent-ticket-%d", id) {
		t.Errorf("result = %+v", res)
	}

	if saver.savedName != fmt.Sprintf("agent-ticket-%d", id) || !saver.savedCfg.Template {
		t.Errorf("template save wrong: name=%q template=%v", saver.savedName, saver.savedCfg.Template)
	}

	templateID, params, createdBy := runs.CreateRunArgsForCall(0)
	if templateID != 77 || params != nil || createdBy != "admin" {
		t.Errorf("CreateRun args = %d %v %q", templateID, params, createdBy)
	}

	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning || got.PipelineRunID == nil || *got.PipelineRunID != 555 {
		t.Errorf("ticket after dispatch = %+v", got)
	}
	if got.WorkflowVersion == nil || *got.WorkflowVersion != 3 {
		t.Errorf("live workflow version must be frozen onto the ticket at dispatch, got %+v", got.WorkflowVersion)
	}

	// §8.2: the run secret is the ONLY token path into a ticketed agent
	// pod — dispatch must attach agent-run-<id> before the step's pod
	// can start (live finding: CreateContainerConfigError without it).
	att := deps.Secrets.(*credentialsfakes.FakeSecretAttacher)
	if att.AttachCallCount() != 1 {
		t.Fatalf("Attach calls = %d, want 1", att.AttachCallCount())
	}
	_, runID, cred, principalToken := att.AttachArgsForCall(0)
	if runID != 555 || cred == nil || cred.Token != "platform-tok" {
		t.Errorf("Attach args: runID=%d cred=%+v", runID, cred)
	}
	if principalToken == "" {
		t.Error("a per-run principal token must be minted into the secret")
	}
}

func TestDispatchOneAttachFailureLeavesTicketQueued(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	deps.Secrets.(*credentialsfakes.FakeSecretAttacher).AttachReturns("", errors.New("k8s down"))

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err == nil {
		t.Fatal("attach failure must surface")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("ticket must stay queued for a retry, state = %s", got.State)
	}
}

func TestDispatchOneNoCredentialFailsBeforeTransition(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	deps.Credentials.(*fakeBackend).creds = map[int]map[string]*credentials.Credential{}

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err == nil {
		t.Fatal("missing vaulted credential must surface")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("ticket must stay queued, state = %s", got.State)
	}
}

func TestDispatchOnePinnedVersion(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	pin := 3
	store.Update(id, tickets.Update{WorkflowVersion: &pin})

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("pinned dispatch: %v", err)
	}

	deps2, store2, _, _ := dispatchDeps(t)
	id2 := queuedTicket(t, store2, "smoke")
	missing := 9
	store2.Update(id2, tickets.Update{WorkflowVersion: &missing})
	if _, err := dispatch.DispatchOne(context.Background(), deps2, id2, "admin"); !errors.Is(err, dispatch.ErrWorkflowNotFound) {
		t.Errorf("missing pinned version: got %v, want ErrWorkflowNotFound", err)
	}
}

func TestDispatchOneRefusals(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)

	if _, err := dispatch.DispatchOne(context.Background(), deps, 999, "admin"); !errors.Is(err, tickets.ErrTicketNotFound) {
		t.Errorf("missing ticket: got %v", err)
	}

	draft, _ := store.Create(&tickets.Ticket{Title: "d", Repo: "r", WorkflowName: "smoke"})
	if _, err := dispatch.DispatchOne(context.Background(), deps, draft, "admin"); !errors.Is(err, dispatch.ErrNotQueued) {
		t.Errorf("draft ticket: got %v, want ErrNotQueued", err)
	}

	noWF := queuedTicket(t, store, "")
	if _, err := dispatch.DispatchOne(context.Background(), deps, noWF, "admin"); !errors.Is(err, dispatch.ErrNoWorkflow) {
		t.Errorf("no workflow name: got %v, want ErrNoWorkflow", err)
	}

	unknown := queuedTicket(t, store, "nope")
	if _, err := dispatch.DispatchOne(context.Background(), deps, unknown, "admin"); !errors.Is(err, dispatch.ErrWorkflowNotFound) {
		t.Errorf("unknown workflow: got %v, want ErrWorkflowNotFound", err)
	}
}

func TestDispatchOneRunCreationFailureLeavesTicketQueued(t *testing.T) {
	deps, store, _, runs := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	runs.CreateRunReturns(nil, errors.New("boom"))

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err == nil {
		t.Fatal("run-creation failure must surface")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("ticket must stay queued for a retry, state = %s", got.State)
	}
}

func TestDispatchOneDefersWhenTicketBudgetExhausted(t *testing.T) {
	deps, store, _, runs := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{LimitUSD: 5, SpentUSD: 6, RemainingUSD: -1, Exhausted: true}, nil)
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if !errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("want ErrBudgetExhausted, got %v", err)
	}
	if runs.CreateRunCallCount() != 0 {
		t.Error("over-cap admission must run BEFORE CreateRun")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("over-cap ticket must STAY queued (never failed), state=%s", got.State)
	}
}

func TestDispatchOneDefersWhenGlobalDailyCapExhausted(t *testing.T) {
	deps, store, _, runs := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{}, nil) // uncapped ticket
	checker.GlobalDailyRemainingReturns(budget.Remaining{LimitUSD: 50, SpentUSD: 50, Exhausted: true}, nil)
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if !errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("want ErrBudgetExhausted, got %v", err)
	}
	if runs.CreateRunCallCount() != 0 {
		t.Error("daily-cap admission must run BEFORE CreateRun")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("daily-capped ticket must stay queued, state=%s", got.State)
	}
}

func TestDispatchOneBudgetCheckerErrorIsPlatformFaultNotDeferral(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{}, errors.New("ledger down"))
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if err == nil || errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("checker error must surface as a platform fault, got %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("platform fault leaves ticket queued, state=%s", got.State)
	}
}

func TestDispatchOneNilBudgetSkipsAdmission(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t) // deps.Budget nil
	id := queuedTicket(t, store, "smoke")
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("nil Budget must preserve landed behavior: %v", err)
	}
}

var _ dispatch.RunCreator = db.PipelineRunFactory(nil)

type fakeUserLookup struct{ ids map[string]int }

func (f fakeUserLookup) FindByUsername(name string) (int, bool, error) {
	id, ok := f.ids[name]
	return id, ok, nil
}

func TestDispatchOneResolvesAndPersistsUserID(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.Users = fakeUserLookup{ids: map[string]int{"tdm": 42}}
	// Give user 42 a vaulted credential so user-first resolution is provable.
	deps.Credentials = &fakeBackend{
		platformUserID: 9,
		creds: map[int]map[string]*credentials.Credential{
			9:  {credentials.KindAnthropicOAuth: {UserID: 9, Kind: credentials.KindAnthropicOAuth, Token: "platform-tok"}},
			42: {credentials.KindAnthropicOAuth: {UserID: 42, UserName: "tdm", Kind: credentials.KindAnthropicOAuth, Token: "tdm-tok"}},
		},
	}
	attacher := new(credentialsfakes.FakeSecretAttacher)
	deps.Secrets = attacher
	id := queuedTicket(t, store, "smoke") // UserName "tdm", UserID nil

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.UserID == nil || *got.UserID != 42 {
		t.Fatalf("user_id must be resolved+persisted at dispatch, got %v", got.UserID)
	}
	if attacher.AttachCallCount() != 1 {
		t.Fatal("expected one Attach")
	}
	_, _, cred, _ := attacher.AttachArgsForCall(0)
	if cred.Token != "tdm-tok" {
		t.Errorf("user-first credential must fund the run once user_id resolves, got token %q", cred.Token)
	}
}

func TestDispatchOneUnknownUserFallsBackToPlatform(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.Users = fakeUserLookup{ids: map[string]int{}} // "tdm" not found
	attacher := new(credentialsfakes.FakeSecretAttacher)
	deps.Secrets = attacher
	id := queuedTicket(t, store, "smoke")

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("unknown user must not block dispatch (platform funds it): %v", err)
	}
	got, _, _ := store.Get(id)
	if got.UserID != nil {
		t.Errorf("unresolvable user leaves user_id NULL, got %v", got.UserID)
	}
}

func principalByName(t *testing.T, store *principals.MemoryStore, name string) principals.Principal {
	t.Helper()
	list, err := store.List()
	if err != nil {
		t.Fatalf("list principals: %v", err)
	}
	for _, p := range list {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no principal named %q in %d principals", name, len(list))
	return principals.Principal{}
}

func TestAttachMintsContractShapedPrincipal(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.RunTimeout = 6 * time.Hour
	pstore := principals.NewMemoryStore()
	deps.Principals = pstore
	id := queuedTicket(t, store, "smoke")

	before := time.Now()
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	// Run id 555 comes from dispatchDeps' FakePipelineRun.
	p := principalByName(t, pstore, "agent-run-555")
	wantScopes := map[string]bool{
		principals.ScopeTicketsRead: true, principals.ScopeTicketsWrite: true,
		principals.ScopeMetricsWrite: true, principals.ScopeCostsWrite: true,
		principals.ScopeQuestionsAnswer: true,
	}
	if len(p.Scopes) != len(wantScopes) {
		t.Errorf("want 5 scopes incl. questions:answer, got %v", p.Scopes)
	}
	for _, s := range p.Scopes {
		if !wantScopes[s] {
			t.Errorf("unexpected scope %q", s)
		}
	}
	if p.ExpiresAt == nil {
		t.Fatal("expiry must be set")
	}
	lo := before.Add(6*time.Hour - time.Minute).Unix()
	hi := before.Add(6*time.Hour + time.Minute).Unix()
	if *p.ExpiresAt < lo || *p.ExpiresAt > hi {
		t.Errorf("expiry must be now+RunTimeout (6h), got %d not in [%d,%d]", *p.ExpiresAt, lo, hi)
	}
}

func TestResolveRunCredentialSkipsExpiredNamingOwner(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.Users = fakeUserLookup{ids: map[string]int{"tdm": 42}}
	expired := time.Now().Add(-time.Hour).Unix()
	deps.Credentials = &fakeBackend{
		platformUserID: 9,
		creds: map[int]map[string]*credentials.Credential{
			// user cred expired; platform cred valid → platform funds the run
			42: {credentials.KindAnthropicOAuth: {UserID: 42, UserName: "tdm", Kind: credentials.KindAnthropicOAuth, Token: "stale", ExpiresAt: expired}},
			9:  {credentials.KindAnthropicOAuth: {UserID: 9, Kind: credentials.KindAnthropicOAuth, Token: "platform-tok"}},
		},
	}
	attacher := new(credentialsfakes.FakeSecretAttacher)
	deps.Secrets = attacher
	id := queuedTicket(t, store, "smoke")

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("expired user cred must fall back to platform: %v", err)
	}
	_, _, cred, _ := attacher.AttachArgsForCall(0)
	if cred.Token != "platform-tok" {
		t.Errorf("expected platform fallback past expired user cred, got %q", cred.Token)
	}
}

func TestResolveRunCredentialAllExpiredErrorsWithOwner(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	expired := time.Now().Add(-time.Hour).Unix()
	deps.Credentials = &fakeBackend{
		platformUserID: 9,
		creds: map[int]map[string]*credentials.Credential{
			9: {credentials.KindAnthropicOAuth: {UserID: 9, UserName: "platform", Kind: credentials.KindAnthropicOAuth, Token: "stale", ExpiresAt: expired}},
		},
	}
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("all-expired must error naming the owner, got %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("credential failure is pre-transition: ticket stays queued, got %s", got.State)
	}
}
