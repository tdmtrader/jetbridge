package workflowrun

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type workflowBudgetReserverStub struct {
	reserve func(context.Context, snapshot.WorkflowRunID, float64) (bool, error)
}

func (s *workflowBudgetReserverStub) ReserveWorkflowBudget(
	ctx context.Context,
	runID snapshot.WorkflowRunID,
	amount float64,
) (bool, error) {
	return s.reserve(ctx, runID, amount)
}

func TestGlobalDailyBudgetAdmitterAtomicallyReservesExactExecutableBound(t *testing.T) {
	tests := []struct {
		name     string
		reserved bool
		denied   bool
	}{
		{name: "reservation accepted", reserved: true},
		{name: "reservation exceeds shared cap", reserved: false, denied: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			admitter, err := NewGlobalDailyBudgetAdmitter(&workflowBudgetReserverStub{reserve: func(_ context.Context, runID snapshot.WorkflowRunID, amount float64) (bool, error) {
				calls++
				if runID != 17 || amount != 3.5 {
					t.Fatalf("reservation = (%s, %.6f)", runID.String(), amount)
				}
				return test.reserved, nil
			}}, 100)
			if err != nil {
				t.Fatal(err)
			}
			err = admitter.Admit(context.Background(), BudgetAdmission{
				WorkflowRunID: 17,
				Config: workflowBudgetConfig(
					atc.Step{Config: &atc.AgentStep{Name: "implement", BudgetSliceUSD: 2.25}},
					atc.Step{Config: &atc.AgentStep{Name: "review", BudgetSliceUSD: 1.25}},
				),
			})
			if test.denied != errors.Is(err, ErrBudgetDenied) {
				t.Fatalf("Admit error = %v, denied = %t", err, test.denied)
			}
			if calls != 1 {
				t.Fatalf("reservation calls = %d", calls)
			}
		})
	}
}

func TestGlobalDailyBudgetAdmitterUsesTheEnclosingExperimentCellReservation(t *testing.T) {
	admitter, err := NewGlobalDailyBudgetAdmitter(&workflowBudgetReserverStub{reserve: func(context.Context, snapshot.WorkflowRunID, float64) (bool, error) {
		t.Fatal("experiment child must not reserve the same global liability twice")
		return false, nil
	}}, 100)
	if err != nil {
		t.Fatal(err)
	}
	admission := BudgetAdmission{
		WorkflowRunID: 17,
		Config: workflowBudgetConfig(
			atc.Step{Config: &atc.AgentStep{Name: "candidate", BudgetSliceUSD: 0.75}},
		),
		ExperimentAdmission: &ExperimentAdmissionGate{ExperimentID: 11, CellID: 13, Phase: "candidate"},
	}
	if err := admitter.Admit(context.Background(), admission); err != nil {
		t.Fatalf("experiment child admission = %v", err)
	}
	admission.Config = workflowBudgetConfig(atc.Step{Config: &atc.AgentStep{Name: "unbounded"}})
	if err := admitter.Admit(context.Background(), admission); !errors.Is(err, ErrBudgetDenied) {
		t.Fatalf("unbounded experiment child admission = %v, want denied", err)
	}
}

func TestGlobalDailyBudgetAdmitterRequiresStaticallyBoundedAgents(t *testing.T) {
	reserver := &workflowBudgetReserverStub{reserve: func(context.Context, snapshot.WorkflowRunID, float64) (bool, error) {
		t.Fatal("invalid executable must not reach the reservation store")
		return false, nil
	}}
	admitter, err := NewGlobalDailyBudgetAdmitter(reserver, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		step atc.Step
	}{
		{name: "zero slice", step: atc.Step{Config: &atc.AgentStep{Name: "uncapped"}}},
		{name: "retry", step: atc.Step{Config: &atc.RetryStep{Step: &atc.AgentStep{Name: "retry", BudgetSliceUSD: 1}, Attempts: 2}}},
		{name: "across", step: atc.Step{Config: &atc.AcrossStep{Vars: []atc.AcrossVarConfig{{Var: "x", Values: []any{"a"}}}, Step: &atc.AgentStep{Name: "across", BudgetSliceUSD: 1}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := admitter.Admit(context.Background(), BudgetAdmission{WorkflowRunID: 17, Config: workflowBudgetConfig(test.step)})
			if !errors.Is(err, ErrBudgetDenied) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestGlobalDailyBudgetAdmitterSkipsReservationWhenUncapped(t *testing.T) {
	admitter, err := NewGlobalDailyBudgetAdmitter(&workflowBudgetReserverStub{reserve: func(context.Context, snapshot.WorkflowRunID, float64) (bool, error) {
		t.Fatal("uncapped admission must not reserve")
		return false, nil
	}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitter.Admit(context.Background(), BudgetAdmission{}); err != nil {
		t.Fatalf("uncapped admission = %v", err)
	}
}

func TestGlobalDailyBudgetAdmitterPropagatesContextAndBoundsStoreFailure(t *testing.T) {
	secret := errors.New("ledger password swordfish")
	store := &workflowBudgetReserverStub{reserve: func(context.Context, snapshot.WorkflowRunID, float64) (bool, error) {
		return false, secret
	}}
	admitter, err := NewGlobalDailyBudgetAdmitter(store, 100)
	if err != nil {
		t.Fatal(err)
	}
	admission := BudgetAdmission{WorkflowRunID: 17, Config: workflowBudgetConfig(atc.Step{Config: &atc.AgentStep{Name: "agent", BudgetSliceUSD: 1}})}
	if err := admitter.Admit(context.Background(), admission); !errors.Is(err, ErrBudgetCheckFailure) || errors.Is(err, secret) || err.Error() != ErrBudgetCheckFailure.Error() {
		t.Fatalf("Admit store error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := admitter.Admit(canceled, admission); !errors.Is(err, context.Canceled) {
		t.Fatalf("Admit context error = %v", err)
	}
}

func workflowBudgetConfig(steps ...atc.Step) atc.Config {
	return atc.Config{Jobs: atc.JobConfigs{{Name: "run", PlanSequence: steps}}}
}

type creatorUserResolverStub struct {
	find func(string) (int, bool, error)
}

func (s *creatorUserResolverStub) FindByUsername(name string) (int, bool, error) {
	return s.find(name)
}

type credentialBackendStub struct {
	resolve   func(int, string) (*credentials.Credential, bool, error)
	platform  func(string) (int, string, bool, error)
	put       func(int, string, string, string, time.Time) error
	status    func(int) ([]credentials.Credential, error)
	expiring  func(time.Duration) ([]credentials.Credential, error)
	delete    func(int, string) error
	setJiraID func(int, string) error
}

func (s *credentialBackendStub) Resolve(userID int, kind string) (*credentials.Credential, bool, error) {
	return s.resolve(userID, kind)
}

func (s *credentialBackendStub) UserBySub(sub string) (int, string, bool, error) {
	return s.platform(sub)
}

func (s *credentialBackendStub) Put(userID int, userName, kind, token string, expiresAt time.Time) error {
	return s.put(userID, userName, kind, token, expiresAt)
}

func (s *credentialBackendStub) Status(userID int) ([]credentials.Credential, error) {
	return s.status(userID)
}

func (s *credentialBackendStub) ExpiringWithin(within time.Duration) ([]credentials.Credential, error) {
	return s.expiring(within)
}

func (s *credentialBackendStub) Delete(userID int, kind string) error {
	return s.delete(userID, kind)
}

func (s *credentialBackendStub) SetJiraAccountID(userID int, accountID string) error {
	return s.setJiraID(userID, accountID)
}

type secretAttacherStub struct {
	attach  func(context.Context, int, *credentials.Credential) (string, error)
	cleanup func(context.Context, int) error
}

func (s *secretAttacherStub) Attach(ctx context.Context, runID int, credential *credentials.Credential) (string, error) {
	return s.attach(ctx, runID, credential)
}

func (s *secretAttacherStub) Cleanup(ctx context.Context, runID int) error {
	return s.cleanup(ctx, runID)
}

func TestVaultedRunSecretPreparerAttachesOnlyResolvedCredentialAfterAllocation(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_784_800_000, 0)
	credential := &credentials.Credential{
		UserID: 17, UserName: "alice", Kind: credentials.KindAnthropicOAuth,
		ExpiresAt: now.Add(time.Hour).Unix(), Token: "user-oauth-token",
	}
	var order []string
	users := &creatorUserResolverStub{find: func(name string) (int, bool, error) {
		order = append(order, "find-user")
		if name != "alice" {
			t.Fatalf("creator = %q", name)
		}
		return 17, true, nil
	}}
	vault := newCredentialBackendStub()
	vault.resolve = func(userID int, kind string) (*credentials.Credential, bool, error) {
		order = append(order, "resolve-user")
		if userID != 17 || kind != credentials.KindAnthropicOAuth {
			t.Fatalf("Resolve = (%d, %q)", userID, kind)
		}
		return credential, true, nil
	}
	vault.platform = func(string) (int, string, bool, error) {
		t.Fatal("usable creator credential must not fall back to platform")
		return 0, "", false, nil
	}
	attacher := &secretAttacherStub{
		attach: func(got context.Context, pipelineRunID int, gotCredential *credentials.Credential) (string, error) {
			order = append(order, "attach-secret")
			if got != ctx || pipelineRunID != 313 || gotCredential == credential ||
				gotCredential.Token != "user-oauth-token" {
				t.Fatalf("Attach = (%v, %d, %+v)", got, pipelineRunID, gotCredential)
			}
			return credentials.RunSecretName(pipelineRunID), nil
		},
		cleanup: func(context.Context, int) error {
			t.Fatal("successful attach must not clean up")
			return nil
		},
	}
	preparer, err := NewVaultedRunSecretPreparer(users, vault, attacher)
	if err != nil {
		t.Fatal(err)
	}
	preparer.now = func() time.Time { return now }
	run := cancellationRun(snapshot.WorkflowRunID(9007199254740993), 7, db.AgentWorkflowRunStatusAdmitting)
	run.CreatedBy = "alice"
	prepared, err := preparer.Prepare(ctx, AdmissionContext{TeamID: 7, TeamName: "main", CreatedBy: "alice"}, run)
	if err != nil {
		t.Fatal(err)
	}
	credential.Token = "mutated-after-prepare"
	if err := prepared.Attach(ctx, 313); err != nil {
		t.Fatal(err)
	}
	if want := []string{"find-user", "resolve-user", "attach-secret"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
	if err := prepared.Attach(ctx, 313); err != nil {
		t.Fatalf("same-run replay: %v", err)
	}
	if err := prepared.Attach(ctx, 314); !errors.Is(err, ErrRunSecretAttachFailure) {
		t.Fatalf("different run replay error = %v", err)
	}
}

func TestVaultedRunSecretPreparerFallsBackFromExpiredCreatorToPlatformAPIKey(t *testing.T) {
	now := time.Unix(1_784_900_000, 0)
	var resolutions []string
	users := &creatorUserResolverStub{find: func(string) (int, bool, error) { return 17, true, nil }}
	vault := newCredentialBackendStub()
	vault.resolve = func(userID int, kind string) (*credentials.Credential, bool, error) {
		resolutions = append(resolutions, string(rune(userID))+":"+kind)
		switch {
		case userID == 17 && kind == credentials.KindAnthropicOAuth:
			return &credentials.Credential{UserID: 17, Kind: kind, Token: "expired", ExpiresAt: now.Unix()}, true, nil
		case userID == 17 && kind == credentials.KindAnthropicAPIKey:
			return nil, false, nil
		case userID == 99 && kind == credentials.KindAnthropicOAuth:
			return nil, false, nil
		case userID == 99 && kind == credentials.KindAnthropicAPIKey:
			return &credentials.Credential{UserID: 99, UserName: "platform", Kind: kind, Token: "platform-key"}, true, nil
		default:
			t.Fatalf("unexpected Resolve = (%d, %q)", userID, kind)
			return nil, false, nil
		}
	}
	vault.platform = func(sub string) (int, string, bool, error) {
		if sub != credentials.PlatformUserSub {
			t.Fatalf("platform sub = %q", sub)
		}
		return 99, credentials.PlatformUserName, true, nil
	}
	var attached *credentials.Credential
	attacher := &secretAttacherStub{
		attach: func(_ context.Context, runID int, credential *credentials.Credential) (string, error) {
			attached = credential
			return credentials.RunSecretName(runID), nil
		},
		cleanup: func(context.Context, int) error { return nil },
	}
	preparer, err := NewVaultedRunSecretPreparer(users, vault, attacher)
	if err != nil {
		t.Fatal(err)
	}
	preparer.now = func() time.Time { return now }
	run := cancellationRun(151, 7, db.AgentWorkflowRunStatusAdmitting)
	run.CreatedBy = "alice"
	prepared, err := preparer.Prepare(context.Background(), AdmissionContext{TeamID: 7, TeamName: "main", CreatedBy: "alice"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Attach(context.Background(), 419); err != nil {
		t.Fatal(err)
	}
	if attached == nil || attached.UserID != 99 || attached.Kind != credentials.KindAnthropicAPIKey || attached.Token != "platform-key" {
		t.Fatalf("attached credential = %+v", attached)
	}
	if len(resolutions) != 4 {
		t.Fatalf("resolution count = %d", len(resolutions))
	}
}

func TestVaultedRunSecretPreparerCleansUpWhenAttachmentFails(t *testing.T) {
	secret := errors.New("kubernetes bearer token")
	users := &creatorUserResolverStub{find: func(string) (int, bool, error) { return 17, true, nil }}
	vault := newCredentialBackendStub()
	vault.resolve = func(userID int, kind string) (*credentials.Credential, bool, error) {
		return &credentials.Credential{UserID: userID, Kind: kind, Token: "oauth"}, true, nil
	}
	vault.platform = func(string) (int, string, bool, error) { return 0, "", false, nil }
	cleanupCalls := 0
	attacher := &secretAttacherStub{
		attach: func(context.Context, int, *credentials.Credential) (string, error) { return "", secret },
		cleanup: func(_ context.Context, runID int) error {
			cleanupCalls++
			if runID != 521 {
				t.Fatalf("cleanup run = %d", runID)
			}
			return nil
		},
	}
	preparer, err := NewVaultedRunSecretPreparer(users, vault, attacher)
	if err != nil {
		t.Fatal(err)
	}
	run := cancellationRun(161, 7, db.AgentWorkflowRunStatusAdmitting)
	run.CreatedBy = "alice"
	prepared, err := preparer.Prepare(context.Background(), AdmissionContext{TeamID: 7, TeamName: "main", CreatedBy: "alice"}, run)
	if err != nil {
		t.Fatal(err)
	}
	err = prepared.Attach(context.Background(), 521)
	if !errors.Is(err, ErrRunSecretAttachFailure) || err.Error() != ErrRunSecretAttachFailure.Error() || errors.Is(err, secret) {
		t.Fatalf("Attach error = %v", err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d", cleanupCalls)
	}
}

func TestVaultedRunSecretPreparerBoundsUnavailableAndStoreFailures(t *testing.T) {
	users := &creatorUserResolverStub{find: func(string) (int, bool, error) { return 0, false, nil }}
	vault := newCredentialBackendStub()
	vault.resolve = func(int, string) (*credentials.Credential, bool, error) { return nil, false, nil }
	vault.platform = func(string) (int, string, bool, error) { return 0, "", false, nil }
	attacher := &secretAttacherStub{}
	preparer, err := NewVaultedRunSecretPreparer(users, vault, attacher)
	if err != nil {
		t.Fatal(err)
	}
	run := cancellationRun(171, 7, db.AgentWorkflowRunStatusAdmitting)
	run.CreatedBy = "alice"
	_, err = preparer.Prepare(context.Background(), AdmissionContext{TeamID: 7, TeamName: "main", CreatedBy: "alice"}, run)
	if !errors.Is(err, ErrRunCredentialUnavailable) || err.Error() != ErrRunCredentialUnavailable.Error() {
		t.Fatalf("unavailable error = %v", err)
	}

	secret := errors.New("users table connection password")
	users.find = func(string) (int, bool, error) { return 0, false, secret }
	_, err = preparer.Prepare(context.Background(), AdmissionContext{TeamID: 7, TeamName: "main", CreatedBy: "alice"}, run)
	if !errors.Is(err, ErrRunSecretPreparationFailure) || errors.Is(err, secret) || err.Error() != ErrRunSecretPreparationFailure.Error() {
		t.Fatalf("store error = %v", err)
	}
}

func TestVaultedRunSecretPreparerIsContextAwareAndRejectsInvalidAllocatedRunID(t *testing.T) {
	resolveCalls := 0
	users := &creatorUserResolverStub{find: func(string) (int, bool, error) {
		resolveCalls++
		return 17, true, nil
	}}
	vault := newCredentialBackendStub()
	vault.resolve = func(userID int, kind string) (*credentials.Credential, bool, error) {
		return &credentials.Credential{UserID: userID, Kind: kind, Token: "oauth"}, true, nil
	}
	vault.platform = func(string) (int, string, bool, error) { return 0, "", false, nil }
	attacher := &secretAttacherStub{}
	preparer, err := NewVaultedRunSecretPreparer(users, vault, attacher)
	if err != nil {
		t.Fatal(err)
	}
	run := cancellationRun(181, 7, db.AgentWorkflowRunStatusAdmitting)
	run.CreatedBy = "alice"
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := preparer.Prepare(canceled, AdmissionContext{TeamID: 7, TeamName: "main", CreatedBy: "alice"}, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare canceled error = %v", err)
	}
	if resolveCalls != 0 {
		t.Fatal("canceled preparation touched user store")
	}

	prepared, err := preparer.Prepare(context.Background(), AdmissionContext{TeamID: 7, TeamName: "main", CreatedBy: "alice"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Attach(context.Background(), 0); !errors.Is(err, ErrRunSecretAttachFailure) {
		t.Fatalf("invalid run ID error = %v", err)
	}
}

func newCredentialBackendStub() *credentialBackendStub {
	return &credentialBackendStub{
		put:    func(int, string, string, string, time.Time) error { panic("unexpected Put") },
		status: func(int) ([]credentials.Credential, error) { panic("unexpected Status") },
		expiring: func(time.Duration) ([]credentials.Credential, error) {
			panic("unexpected ExpiringWithin")
		},
		delete:    func(int, string) error { panic("unexpected Delete") },
		setJiraID: func(int, string) error { panic("unexpected SetJiraAccountID") },
	}
}
