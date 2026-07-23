package workflowrun

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// ErrBudgetCheckFailure bounds ledger/backend failures at the admission seam.
var ErrBudgetCheckFailure = errors.New("workflow run: budget check failed")

// WorkflowBudgetReserver atomically reserves worst-case LLM spend against the
// shared global daily liability. The DB implementation serializes workflow
// and experiment reservations with cost-ledger inserts.
type WorkflowBudgetReserver interface {
	ReserveWorkflowBudget(context.Context, snapshot.WorkflowRunID, float64) (bool, error)
}

// GlobalDailyBudgetAdmitter derives a hard upper bound from the exact durable
// executable, then reserves that amount before any template, secret, build, or
// agent side effect. When the cap is enabled, every agent must have a finite,
// positive six-decimal budget slice and dynamic repetition is rejected.
type GlobalDailyBudgetAdmitter struct {
	reserver WorkflowBudgetReserver
	capUSD   float64
}

func NewGlobalDailyBudgetAdmitter(
	reserver WorkflowBudgetReserver,
	globalDailyCapUSD float64,
) (*GlobalDailyBudgetAdmitter, error) {
	if nilInterface(reserver) || math.IsNaN(globalDailyCapUSD) ||
		math.IsInf(globalDailyCapUSD, 0) || globalDailyCapUSD < 0 {
		return nil, ErrBudgetCheckFailure
	}
	return &GlobalDailyBudgetAdmitter{reserver: reserver, capUSD: globalDailyCapUSD}, nil
}

func (a *GlobalDailyBudgetAdmitter) Admit(ctx context.Context, admission BudgetAdmission) error {
	if ctx == nil {
		return ErrBudgetCheckFailure
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.capUSD == 0 {
		return nil
	}
	if admission.WorkflowRunID.Validate() != nil {
		return ErrBudgetCheckFailure
	}
	amount, agents, err := boundedWorkflowBudgetUSD(admission.Config)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBudgetDenied, err)
	}
	if agents == 0 {
		return nil
	}
	if admission.ExperimentAdmission != nil {
		// Experiment start has already proved that every candidate/evaluator
		// slice fits the cell envelope, and the runner reserves that envelope
		// against the same global cap before allocating either child. Reserving
		// each child again would double-count one immutable liability.
		return nil
	}
	reserved, err := a.reserver.ReserveWorkflowBudget(ctx, admission.WorkflowRunID, amount)
	if err != nil {
		return ErrBudgetCheckFailure
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !reserved {
		return ErrBudgetDenied
	}
	return nil
}

func boundedWorkflowBudgetUSD(config atc.Config) (float64, int, error) {
	var totalMicroUSD int64
	spenders := 0
	addSlice := func(label string, slice float64) error {
		if math.IsNaN(slice) || math.IsInf(slice, 0) || slice <= 0 {
			return fmt.Errorf("%s requires a finite positive budget while the global cap is enabled", label)
		}
		scaled := slice * 1_000_000
		if math.Abs(scaled-math.Round(scaled)) > 0.0000001 {
			return fmt.Errorf("%s budget supports at most six decimal places", label)
		}
		microUSD := int64(math.Round(scaled))
		if microUSD <= 0 || totalMicroUSD > math.MaxInt64-microUSD {
			return fmt.Errorf("agent budget slices overflow")
		}
		totalMicroUSD += microUSD
		spenders++
		return nil
	}
	recursor := atc.StepRecursor{
		OnAgent: func(step *atc.AgentStep) error {
			return addSlice(fmt.Sprintf("agent %q budget_slice_usd", step.Name), step.BudgetSliceUSD)
		},
		OnHarvest: func(step *atc.HarvestStep) error {
			if step.Judge == nil {
				return nil
			}
			return addSlice(fmt.Sprintf("harvest %q judge", step.Name), step.Judge.BudgetUSD)
		},
		OnRetry: func(*atc.RetryStep) error {
			return fmt.Errorf("attempts cannot be statically reserved under the global cap")
		},
		OnAcross: func(*atc.AcrossStep) error {
			return fmt.Errorf("across cannot be statically reserved under the global cap")
		},
	}
	for _, job := range config.Jobs {
		for _, step := range job.PlanSequence {
			if step.Config == nil {
				return 0, 0, fmt.Errorf("workflow plan contains an empty step")
			}
			if err := step.Config.Visit(recursor); err != nil {
				return 0, 0, err
			}
		}
	}
	return float64(totalMicroUSD) / 1_000_000, spenders, nil
}

var _ BudgetAdmitter = (*GlobalDailyBudgetAdmitter)(nil)

var (
	ErrRunCredentialUnavailable    = errors.New("workflow run: no usable Anthropic credential")
	ErrRunSecretPreparationFailure = errors.New("workflow run: secret preparation failed")
	ErrRunSecretAttachFailure      = errors.New("workflow run: secret attachment failed")
)

const defaultRunPrincipalTimeout = 24 * time.Hour

// CreatorUserResolver maps the authenticated creator copied into the durable
// workflow run to Concourse's current users row. atc/db.AgentUserLookup
// satisfies this interface.
type CreatorUserResolver interface {
	FindByUsername(string) (int, bool, error)
}

// RunCredentialVault is the read-only credential surface needed at
// admission. credentials.Backend and atc/db.AgentUserCredentialsFactory both
// satisfy it without granting this component any vault mutation methods.
type RunCredentialVault interface {
	Resolve(int, string) (*credentials.Credential, bool, error)
	UserBySub(string) (int, string, bool, error)
}

// RunPrincipalStore is the mint/rollback surface for a per-run principal.
// principals.Store satisfies it directly.
type RunPrincipalStore interface {
	Create(principals.CreateSpec) (principals.Principal, string, error)
	Revoke(int) (bool, error)
}

// VaultedRunSecretPreparer resolves and clones a usable credential before any
// execution allocation. It deliberately defers principal creation and secret
// attachment until the pipeline-run transaction supplies its allocated ID.
type VaultedRunSecretPreparer struct {
	users          CreatorUserResolver
	vault          RunCredentialVault
	principalStore RunPrincipalStore
	attacher       credentials.SecretAttacher
	runTimeout     time.Duration
	now            func() time.Time
}

func NewVaultedRunSecretPreparer(
	users CreatorUserResolver,
	vault RunCredentialVault,
	principalStore RunPrincipalStore,
	attacher credentials.SecretAttacher,
	runTimeout time.Duration,
) (*VaultedRunSecretPreparer, error) {
	if nilInterface(users) || nilInterface(vault) || nilInterface(principalStore) || nilInterface(attacher) {
		return nil, ErrRunSecretPreparationFailure
	}
	if runTimeout <= 0 {
		runTimeout = defaultRunPrincipalTimeout
	}
	return &VaultedRunSecretPreparer{
		users: users, vault: vault, principalStore: principalStore,
		attacher: attacher, runTimeout: runTimeout, now: time.Now,
	}, nil
}

func (p *VaultedRunSecretPreparer) Prepare(
	ctx context.Context,
	admission AdmissionContext,
	run db.AgentWorkflowRun,
) (PreparedRunSecret, error) {
	if ctx == nil {
		return nil, ErrRunSecretPreparationFailure
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if admission.TeamID <= 0 || strings.TrimSpace(admission.TeamName) == "" ||
		strings.TrimSpace(admission.CreatedBy) == "" || run.ID.Validate() != nil ||
		run.TeamID != admission.TeamID || run.TeamName != admission.TeamName ||
		run.CreatedBy != admission.CreatedBy || run.Status != db.AgentWorkflowRunStatusAdmitting {
		return nil, ErrRunSecretPreparationFailure
	}

	userID, found, err := p.users.FindByUsername(admission.CreatedBy)
	if err != nil {
		return nil, ErrRunSecretPreparationFailure
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if found && userID <= 0 {
		return nil, ErrRunSecretPreparationFailure
	}

	now := p.now()
	if found {
		credential, usable, err := p.resolveOwner(ctx, userID, now)
		if err != nil {
			return nil, err
		}
		if usable {
			return p.prepared(admission, run, credential), nil
		}
	}

	platformID, _, platformFound, err := p.vault.UserBySub(credentials.PlatformUserSub)
	if err != nil {
		return nil, ErrRunSecretPreparationFailure
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !platformFound {
		return nil, ErrRunCredentialUnavailable
	}
	if platformID <= 0 {
		return nil, ErrRunSecretPreparationFailure
	}
	credential, usable, err := p.resolveOwner(ctx, platformID, now)
	if err != nil {
		return nil, err
	}
	if !usable {
		return nil, ErrRunCredentialUnavailable
	}
	return p.prepared(admission, run, credential), nil
}

func (p *VaultedRunSecretPreparer) resolveOwner(
	ctx context.Context,
	userID int,
	now time.Time,
) (*credentials.Credential, bool, error) {
	for _, kind := range []string{credentials.KindAnthropicOAuth, credentials.KindAnthropicAPIKey} {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		credential, found, err := p.vault.Resolve(userID, kind)
		if err != nil {
			return nil, false, ErrRunSecretPreparationFailure
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if !found {
			continue
		}
		if credential == nil || credential.UserID != userID || credential.Kind != kind || credential.Token == "" {
			return nil, false, ErrRunSecretPreparationFailure
		}
		if credential.ExpiresAt > 0 && credential.ExpiresAt <= now.Unix() {
			continue
		}
		cloned := *credential
		return &cloned, true, nil
	}
	return nil, false, nil
}

func (p *VaultedRunSecretPreparer) prepared(
	admission AdmissionContext,
	run db.AgentWorkflowRun,
	credential *credentials.Credential,
) *vaultedPreparedRunSecret {
	return &vaultedPreparedRunSecret{
		credential: *credential, admission: admission, workflowRunID: run.ID,
		principalStore: p.principalStore, attacher: p.attacher,
		runTimeout: p.runTimeout, now: p.now,
	}
}

type vaultedPreparedRunSecret struct {
	mu             sync.Mutex
	credential     credentials.Credential
	admission      AdmissionContext
	workflowRunID  snapshot.WorkflowRunID
	principalStore RunPrincipalStore
	attacher       credentials.SecretAttacher
	runTimeout     time.Duration
	now            func() time.Time
	attached       bool
	pipelineRunID  int
}

func (p *vaultedPreparedRunSecret) Attach(ctx context.Context, pipelineRunID int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if ctx == nil {
		return ErrRunSecretAttachFailure
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if pipelineRunID <= 0 {
		return ErrRunSecretAttachFailure
	}
	if p.attached {
		if p.pipelineRunID == pipelineRunID {
			return nil
		}
		return ErrRunSecretAttachFailure
	}
	if p.credential.ExpiresAt > 0 && p.credential.ExpiresAt <= p.now().Unix() {
		return ErrRunCredentialUnavailable
	}

	expires := p.now().Add(p.runTimeout).Unix()
	principal, token, err := p.principalStore.Create(principals.CreateSpec{
		Name: credentials.RunSecretName(pipelineRunID),
		Description: fmt.Sprintf(
			"per-run principal for workflow run %s (pipeline run %d)",
			p.workflowRunID.String(), pipelineRunID,
		),
		Scopes: []string{
			principals.ScopeTicketsRead,
			principals.ScopeTicketsWrite,
			principals.ScopeMetricsWrite,
			principals.ScopeCostsWrite,
			principals.ScopeQuestionsAnswer,
		},
		TeamName:  p.admission.TeamName,
		CreatedBy: "workflow-run",
		ExpiresAt: &expires,
	})
	if err != nil {
		return ErrRunSecretAttachFailure
	}
	if principal.ID <= 0 || token == "" {
		if principal.ID > 0 {
			_, _ = p.principalStore.Revoke(principal.ID)
		}
		return ErrRunSecretAttachFailure
	}
	if err := ctx.Err(); err != nil {
		_, _ = p.principalStore.Revoke(principal.ID)
		return err
	}

	name, attachErr := p.attacher.Attach(ctx, pipelineRunID, &p.credential, token)
	if attachErr != nil || name != credentials.RunSecretName(pipelineRunID) {
		if ctx.Err() == nil {
			_ = p.attacher.Cleanup(ctx, pipelineRunID)
		}
		_, _ = p.principalStore.Revoke(principal.ID)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return ErrRunSecretAttachFailure
	}
	p.attached = true
	p.pipelineRunID = pipelineRunID
	return nil
}

var _ RunSecretPreparer = (*VaultedRunSecretPreparer)(nil)
var _ PreparedRunSecret = (*vaultedPreparedRunSecret)(nil)
