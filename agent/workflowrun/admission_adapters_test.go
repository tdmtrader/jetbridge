package workflowrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
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

func TestBoundedWorkflowBudgetUSDCountsOnlyAgentSlices(t *testing.T) {
	amount, agents, err := boundedWorkflowBudgetUSD(workflowBudgetConfig(
		atc.Step{Config: &atc.AgentStep{Name: "implement", BudgetSliceUSD: 1.25}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if amount != 1.25 || agents != 1 {
		t.Fatalf("bound = (%.6f, %d), want (1.250000, 1)", amount, agents)
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

type credentialBackendStub struct {
	resolve  func(int, string) (*credentials.Credential, bool, error)
	platform func(string) (int, string, bool, error)
	put      func(int, string, string, string, time.Time) error
	status   func(int) ([]credentials.Credential, error)
	expiring func(time.Duration) ([]credentials.Credential, error)
	delete   func(int, string) error
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

func newCredentialBackendStub() *credentialBackendStub {
	return &credentialBackendStub{
		put:    func(int, string, string, string, time.Time) error { panic("unexpected Put") },
		status: func(int) ([]credentials.Credential, error) { panic("unexpected Status") },
		expiring: func(time.Duration) ([]credentials.Credential, error) {
			panic("unexpected ExpiringWithin")
		},
		delete: func(int, string) error { panic("unexpected Delete") },
	}
}

// platformVault builds a backend stub whose platform user owns the given
// credentials.
func platformVault(t *testing.T, platformUserID int, owned ...credentials.Credential) *credentialBackendStub {
	t.Helper()
	vault := newCredentialBackendStub()
	vault.platform = func(sub string) (int, string, bool, error) {
		if sub != credentials.PlatformUserSub {
			t.Fatalf("resolved sub = %q, want the platform service user", sub)
		}
		return platformUserID, credentials.PlatformUserName, platformUserID > 0, nil
	}
	vault.resolve = func(userID int, kind string) (*credentials.Credential, bool, error) {
		for i := range owned {
			if owned[i].UserID == userID && owned[i].Kind == kind {
				credential := owned[i]
				return &credential, true, nil
			}
		}
		return nil, false, nil
	}
	return vault
}

func mustPlatformCredentialAdmitter(t *testing.T, vault RunCredentialVault, secretName string) *PlatformCredentialAdmitter {
	t.Helper()
	admitter, err := NewPlatformCredentialAdmitter(vault, secretName)
	if err != nil {
		t.Fatalf("NewPlatformCredentialAdmitter: %v", err)
	}
	admitter.now = func() time.Time { return time.Unix(1_784_800_000, 0) }
	return admitter
}

func TestPlatformCredentialAdmitterAcceptsAVaultedUnexpiredCredential(t *testing.T) {
	for _, kind := range []string{credentials.KindAnthropicOAuth, credentials.KindAnthropicAPIKey} {
		t.Run(kind, func(t *testing.T) {
			vault := platformVault(t, 99, credentials.Credential{
				UserID: 99, UserName: "platform", Kind: kind,
				ExpiresAt: time.Unix(1_784_800_000, 0).Add(time.Hour).Unix(),
			})
			admitter := mustPlatformCredentialAdmitter(t, vault, credentials.PlatformSecretName)

			if err := admitter.AdmitModelCredential(context.Background()); err != nil {
				t.Fatalf("AdmitModelCredential = %v, want admitted", err)
			}
		})
	}
}

// The syncer deletes the secret when the credential is unvaulted, so admitting
// a run against the default secret name with an empty (or expired) vault would
// start agent pods that cannot authenticate.
func TestPlatformCredentialAdmitterFailsClosedWithoutAUsableCredential(t *testing.T) {
	expired := credentials.Credential{
		UserID: 99, UserName: "platform", Kind: credentials.KindAnthropicOAuth,
		ExpiresAt: time.Unix(1_784_800_000, 0).Add(-time.Hour).Unix(),
	}
	tests := []struct {
		name  string
		vault *credentialBackendStub
	}{
		{name: "no platform user", vault: platformVault(t, 0)},
		{name: "no credential", vault: platformVault(t, 99)},
		{name: "expired credential", vault: platformVault(t, 99, expired)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			admitter := mustPlatformCredentialAdmitter(t, test.vault, credentials.PlatformSecretName)

			err := admitter.AdmitModelCredential(context.Background())
			if !errors.Is(err, ErrRunCredentialUnavailable) {
				t.Fatalf("AdmitModelCredential = %v, want %v", err, ErrRunCredentialUnavailable)
			}
		})
	}
}

// An operator-named secret is maintained out of band; the binder never reads
// Kubernetes, so the vault has nothing to say about it.
func TestPlatformCredentialAdmitterAcceptsAnOperatorSuppliedSecretWithoutReadingTheVault(t *testing.T) {
	vault := newCredentialBackendStub()
	vault.platform = func(string) (int, string, bool, error) {
		t.Fatal("an out-of-band secret must not be validated against the vault")
		return 0, "", false, nil
	}
	vault.resolve = func(int, string) (*credentials.Credential, bool, error) {
		t.Fatal("an out-of-band secret must not be validated against the vault")
		return nil, false, nil
	}
	admitter := mustPlatformCredentialAdmitter(t, vault, "operator-managed-token")

	if err := admitter.AdmitModelCredential(context.Background()); err != nil {
		t.Fatalf("AdmitModelCredential = %v, want admitted", err)
	}
}

// --agent-platform-token-secret can be cleared, and then no agent pod has any
// token path at all.
func TestPlatformCredentialAdmitterFailsClosedWithoutASecretName(t *testing.T) {
	admitter := mustPlatformCredentialAdmitter(t, platformVault(t, 99), "")

	if err := admitter.AdmitModelCredential(context.Background()); !errors.Is(err, ErrRunCredentialUnavailable) {
		t.Fatalf("AdmitModelCredential = %v, want %v", err, ErrRunCredentialUnavailable)
	}
}

func TestPlatformCredentialAdmitterIsContextAwareAndBoundsStoreFailures(t *testing.T) {
	vault := platformVault(t, 99)
	secret := errors.New("vault said credential=super-secret")
	vault.platform = func(string) (int, string, bool, error) { return 0, "", false, secret }
	admitter := mustPlatformCredentialAdmitter(t, vault, credentials.PlatformSecretName)

	err := admitter.AdmitModelCredential(context.Background())
	if !errors.Is(err, ErrModelCredentialCheckFailure) || errors.Is(err, secret) ||
		err.Error() != ErrModelCredentialCheckFailure.Error() {
		t.Fatalf("store failure = %v, want a bounded %v that never carries backend detail", err, ErrModelCredentialCheckFailure)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mustPlatformCredentialAdmitter(t, platformVault(t, 99), credentials.PlatformSecretName).
		AdmitModelCredential(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context = %v, want context.Canceled", err)
	}
}
