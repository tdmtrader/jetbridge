package workflowrun

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
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
	ErrRunCredentialUnavailable    = errors.New("workflow run: no usable platform Anthropic credential")
	ErrModelCredentialCheckFailure = errors.New("workflow run: model credential check failed")
)

// RunCredentialVault is the read-only credential surface needed at
// admission. credentials.Backend and atc/db.AgentUserCredentialsFactory both
// satisfy it without granting this component any vault mutation methods.
type RunCredentialVault interface {
	Resolve(int, string) (*credentials.Credential, bool, error)
	UserBySub(string) (int, string, bool, error)
}

// PlatformCredentialAdmitter fails a run closed, before any execution side
// effect, when this web node has no model credential for the agent pods the
// run will start. The platform token is the ONLY model-credential path
// (§8.2): pods mount secretName themselves, so admission never attaches,
// clones, or even reads a token — it checks that a source exists.
//
// Two sources qualify. The default secretName is the syncer's own secret, so
// the vault's platform credential IS the source and must be present and
// unexpired. An operator who points --agent-platform-token-secret at a secret
// they maintain by hand owns its contents out of band; the binder does not
// read Kubernetes, so that configuration is taken at face value.
type PlatformCredentialAdmitter struct {
	vault      RunCredentialVault
	secretName string
	now        func() time.Time
}

func NewPlatformCredentialAdmitter(
	vault RunCredentialVault,
	secretName string,
) (*PlatformCredentialAdmitter, error) {
	if nilInterface(vault) {
		return nil, ErrModelCredentialCheckFailure
	}
	return &PlatformCredentialAdmitter{
		vault: vault, secretName: strings.TrimSpace(secretName), now: time.Now,
	}, nil
}

func (a *PlatformCredentialAdmitter) AdmitModelCredential(ctx context.Context) error {
	if ctx == nil {
		return ErrModelCredentialCheckFailure
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.secretName == "" {
		return ErrRunCredentialUnavailable
	}
	if a.secretName != credentials.PlatformSecretName {
		return nil
	}

	userID, _, found, err := a.vault.UserBySub(credentials.PlatformUserSub)
	if err != nil {
		return ErrModelCredentialCheckFailure
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !found {
		return ErrRunCredentialUnavailable
	}
	if userID <= 0 {
		return ErrModelCredentialCheckFailure
	}

	_, found, err = credentials.ResolveUsableAnthropicCredential(a.vault, userID, a.now())
	if err != nil {
		return ErrModelCredentialCheckFailure
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !found {
		return ErrRunCredentialUnavailable
	}
	return nil
}

var _ ModelCredentialAdmitter = (*PlatformCredentialAdmitter)(nil)
