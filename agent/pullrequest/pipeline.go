package pullrequest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/mitchellh/copystructure"
)

const (
	MonitorResourceTypeName = "forge-pr"
	MonitorResourceName     = "pull-request"
	MonitorSourceName       = "pull-request"
	MonitorJobName          = "admit"

	monitorPipelineConfigHashDomain   = "pull-request-monitor-pipeline/config/v1\x00"
	monitorPipelineIdentityHashDomain = "pull-request-monitor-pipeline/identity/v1\x00"
)

var exactMonitorResourceImage = regexp.MustCompile(
	`^[^[:space:]@]+@sha256:[0-9a-f]{64}$`,
)

// MonitorCheckState is the protected binding projection consumed by the
// forge-pr resource. The resource's previous version is never an
// acknowledgement authority.
type MonitorCheckState struct {
	BindingID            int64     `json:"binding_id"`
	BindingRevision      int64     `json:"binding_revision"`
	AcknowledgedCursor   string    `json:"acknowledged_cursor"`
	LastReconciledTarget string    `json:"last_reconciled_target"`
	LastReconciledAt     time.Time `json:"last_reconciled_at"`
	ActiveActionDigest   string    `json:"active_action_digest"`
	AttentionRequired    bool      `json:"attention_required"`
	Paused               bool      `json:"paused"`
	OperatorTerminated   bool      `json:"operator_terminated"`
}

// MonitorPipelinePolicy contains only trusted destination policy. Binding
// identity and mutable acknowledgement state are derived from the binding row,
// never supplied by policy.
type MonitorPipelinePolicy struct {
	APIBaseURL        string
	RepositoryURL     string
	ReadCredential    string
	PollInterval      time.Duration
	FreshnessInterval time.Duration
	ResourceType      atc.ResourceType
}

// MonitorPipelineTarget is the complete immutable authority for one standing
// binding-owned source pipeline.
type MonitorPipelineTarget struct {
	TeamID             int
	BindingID          int64
	WorkflowDefinition int
	WorkflowVersion    int
	Provider           string
	Repository         string
	ExternalID         string
	APIBaseURL         string
	RepositoryURL      string
	ReadCredential     string
	PollInterval       time.Duration
	FreshnessInterval  time.Duration
	ResourceType       atc.ResourceType
	CheckState         MonitorCheckState
}

// RenderedMonitorPipeline exposes a diagnostic copy while retaining an
// unexported authority. Persistence must call ProtectedForBinding so caller
// mutation cannot substitute config, hash, or physical identity.
type RenderedMonitorPipeline struct {
	PipelineName  string
	Config        atc.Config
	CanonicalJSON []byte
	ConfigHash    string

	authority *monitorPipelineAuthority
}

type monitorPipelineAuthority struct {
	target        MonitorPipelineTarget
	pipelineName  string
	config        atc.Config
	canonicalJSON []byte
	configHash    string
}

// MonitorBindingLister is the smallest binding projection needed by the
// standing-pipeline reconciler. The durable BindingStore satisfies it.
type MonitorBindingLister interface {
	ListActive(context.Context, int) ([]Binding, error)
}

// MonitorPipelinePolicyResolver supplies trusted provider destination policy
// for one binding. It cannot supply binding identity or acknowledgement state.
type MonitorPipelinePolicyResolver interface {
	ResolveMonitorPipelinePolicy(
		context.Context,
		Binding,
	) (MonitorPipelinePolicy, error)
}

// MonitorPipelineConverger persists the protected render for the exact
// binding. The DB adapter intentionally hides physical pipeline records from
// this package.
type MonitorPipelineConverger interface {
	ConvergeMonitorPipeline(
		context.Context,
		Binding,
		RenderedMonitorPipeline,
	) (bool, error)
}

// MonitorPipelineReconciler converges one standing pipeline for every active
// binding in the trusted team. Scheduling remains owned by the existing
// resource-source lifecycle tick.
type MonitorPipelineReconciler struct {
	teamID    int
	bindings  MonitorBindingLister
	policies  MonitorPipelinePolicyResolver
	pipelines MonitorPipelineConverger
}

func NewMonitorPipelineReconciler(
	trustedTeamID int,
	bindings MonitorBindingLister,
	policies MonitorPipelinePolicyResolver,
	pipelines MonitorPipelineConverger,
) (*MonitorPipelineReconciler, error) {
	if trustedTeamID <= 0 {
		return nil, fmt.Errorf(
			"pullrequest: monitor pipeline team ID must be positive",
		)
	}
	if bindings == nil || policies == nil || pipelines == nil {
		return nil, fmt.Errorf(
			"pullrequest: monitor pipeline reconciliation dependencies are required",
		)
	}
	return &MonitorPipelineReconciler{
		teamID: trustedTeamID, bindings: bindings,
		policies: policies, pipelines: pipelines,
	}, nil
}

func (reconciler *MonitorPipelineReconciler) Reconcile(
	ctx context.Context,
) error {
	if ctx == nil {
		return fmt.Errorf(
			"pullrequest: monitor pipeline reconciliation requires context",
		)
	}
	bindings, err := reconciler.bindings.ListActive(ctx, reconciler.teamID)
	if err != nil {
		return fmt.Errorf("pullrequest: list active monitor bindings: %w", err)
	}
	for _, binding := range bindings {
		if binding.TeamID != reconciler.teamID ||
			binding.State != BindingActive ||
			binding.Paused || binding.OperatorTerminated {
			return fmt.Errorf(
				"pullrequest: active monitor binding %d crossed its trusted scope",
				binding.ID,
			)
		}
		policy, err := reconciler.policies.ResolveMonitorPipelinePolicy(
			ctx, binding,
		)
		if err != nil {
			return fmt.Errorf(
				"pullrequest: resolve monitor pipeline policy for binding %d: %w",
				binding.ID, err,
			)
		}
		target, err := MonitorPipelineTargetForBinding(binding, policy)
		if err != nil {
			return fmt.Errorf(
				"pullrequest: target monitor pipeline for binding %d: %w",
				binding.ID, err,
			)
		}
		rendered, err := RenderMonitorPipeline(target)
		if err != nil {
			return fmt.Errorf(
				"pullrequest: render monitor pipeline for binding %d: %w",
				binding.ID, err,
			)
		}
		if _, err := reconciler.pipelines.ConvergeMonitorPipeline(
			ctx, binding, rendered,
		); err != nil {
			return fmt.Errorf(
				"pullrequest: converge monitor pipeline for binding %d: %w",
				binding.ID, err,
			)
		}
	}
	return nil
}

// MonitorPipelineTargetForBinding combines the authoritative binding
// projection with trusted destination policy.
func MonitorPipelineTargetForBinding(
	binding Binding,
	policy MonitorPipelinePolicy,
) (MonitorPipelineTarget, error) {
	if err := validateMonitorPipelineBinding(binding); err != nil {
		return MonitorPipelineTarget{}, err
	}
	if err := validateMonitorPipelinePolicy(policy); err != nil {
		return MonitorPipelineTarget{}, err
	}
	activeDigest := ""
	if binding.Active != nil {
		activeDigest = binding.Active.ActionDigest
	}
	resourceType, err := cloneMonitorResourceType(policy.ResourceType)
	if err != nil {
		return MonitorPipelineTarget{}, fmt.Errorf(
			"pullrequest: clone monitor resource type: %w", err,
		)
	}
	return MonitorPipelineTarget{
		TeamID:             binding.TeamID,
		BindingID:          binding.ID,
		WorkflowDefinition: binding.MonitorWorkflowDefinitionID,
		WorkflowVersion:    binding.MonitorWorkflowVersion,
		Provider:           string(binding.Locator.Provider),
		Repository:         binding.Locator.Repository,
		ExternalID:         binding.Locator.ExternalID,
		APIBaseURL:         policy.APIBaseURL,
		RepositoryURL:      policy.RepositoryURL,
		ReadCredential:     policy.ReadCredential,
		PollInterval:       policy.PollInterval,
		FreshnessInterval:  policy.FreshnessInterval,
		ResourceType:       resourceType,
		CheckState: MonitorCheckState{
			BindingID:            binding.ID,
			BindingRevision:      binding.Revision,
			AcknowledgedCursor:   string(binding.AcknowledgedCursor),
			LastReconciledTarget: binding.LastReconciledTargetSHA,
			LastReconciledAt:     binding.LastReconciledAt,
			ActiveActionDigest:   activeDigest,
			AttentionRequired:    binding.State == BindingAttentionRequired,
			Paused:               binding.Paused,
			OperatorTerminated:   binding.OperatorTerminated,
		},
	}, nil
}

// RenderMonitorPipeline creates one ordinary, initially server-paused
// Concourse source pipeline. The registry owns activation after persistence.
func RenderMonitorPipeline(
	target MonitorPipelineTarget,
) (RenderedMonitorPipeline, error) {
	cloned, err := cloneMonitorPipelineTarget(target)
	if err != nil {
		return RenderedMonitorPipeline{}, fmt.Errorf(
			"pullrequest: clone monitor pipeline target: %w", err,
		)
	}
	if err := validateMonitorPipelineTarget(cloned); err != nil {
		return RenderedMonitorPipeline{}, err
	}
	config := atc.Config{
		ResourceTypes: atc.ResourceTypes{cloned.ResourceType},
		Resources: atc.ResourceConfigs{{
			Name: MonitorResourceName,
			Type: cloned.ResourceType.Name,
			Source: atc.Source{
				"provider":           cloned.Provider,
				"repository":         cloned.Repository,
				"external_id":        cloned.ExternalID,
				"api_base_url":       cloned.APIBaseURL,
				"repository_url":     cloned.RepositoryURL,
				"read_token":         "((" + cloned.ReadCredential + "))",
				"poll_interval":      cloned.PollInterval.String(),
				"freshness_interval": cloned.FreshnessInterval.String(),
				"monitor":            monitorCheckSource(cloned.CheckState),
			},
			CheckEvery: &atc.CheckEvery{Interval: cloned.PollInterval},
		}},
		Jobs: atc.JobConfigs{{
			Name: MonitorJobName, Serial: true, DisableManualTrigger: true,
			PlanSequence: []atc.Step{{Config: &atc.GetStep{
				Name: MonitorSourceName, Resource: MonitorResourceName,
				Trigger: true, Version: &atc.VersionConfig{Every: true},
			}}},
		}},
	}
	warnings, validationErrors := configvalidate.Validate(config)
	if len(validationErrors) > 0 {
		return RenderedMonitorPipeline{}, fmt.Errorf(
			"pullrequest: invalid rendered monitor pipeline:\n%s",
			strings.Join(validationErrors, "\n"),
		)
	}
	for _, warning := range warnings {
		if warning.Type == "invalid_identifier" {
			return RenderedMonitorPipeline{}, fmt.Errorf(
				"pullrequest: unsafe rendered monitor identifier: %s",
				warning.Message,
			)
		}
	}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		return RenderedMonitorPipeline{}, fmt.Errorf(
			"pullrequest: canonicalize monitor pipeline: %w", err,
		)
	}
	configIdentity, err := atc.CanonicalJSON(struct {
		CanonicalJSONVersion int                   `json:"canonical_json_version"`
		Target               MonitorPipelineTarget `json:"target"`
		Config               atc.Config            `json:"config"`
	}{
		CanonicalJSONVersion: atc.CanonicalJSONVersion,
		Target:               *cloned,
		Config:               config,
	})
	if err != nil {
		return RenderedMonitorPipeline{}, fmt.Errorf(
			"pullrequest: canonicalize monitor config identity: %w", err,
		)
	}
	configHash := monitorPipelineHash(
		monitorPipelineConfigHashDomain, configIdentity,
	)
	physicalIdentity, err := atc.CanonicalJSON(struct {
		CanonicalJSONVersion int    `json:"canonical_json_version"`
		TeamID               int    `json:"team_id"`
		BindingID            int64  `json:"binding_id"`
		WorkflowDefinition   int    `json:"workflow_definition"`
		WorkflowVersion      int    `json:"workflow_version"`
		Provider             string `json:"provider"`
		Repository           string `json:"repository"`
		ExternalID           string `json:"external_id"`
	}{
		CanonicalJSONVersion: atc.CanonicalJSONVersion,
		TeamID:               cloned.TeamID,
		BindingID:            cloned.BindingID,
		WorkflowDefinition:   cloned.WorkflowDefinition,
		WorkflowVersion:      cloned.WorkflowVersion,
		Provider:             cloned.Provider,
		Repository:           cloned.Repository,
		ExternalID:           cloned.ExternalID,
	})
	if err != nil {
		return RenderedMonitorPipeline{}, fmt.Errorf(
			"pullrequest: canonicalize monitor physical identity: %w", err,
		)
	}
	identityHash := monitorPipelineHash(
		monitorPipelineIdentityHashDomain, physicalIdentity,
	)
	name := fmt.Sprintf(
		"agent-pr-monitor-%d-%s", cloned.BindingID, identityHash[:12],
	)
	if warning, err := atc.ValidateIdentifier(name); err != nil || warning != nil {
		return RenderedMonitorPipeline{}, fmt.Errorf(
			"pullrequest: generated monitor pipeline name is unsafe",
		)
	}
	authorityConfig, err := cloneMonitorConfig(config)
	if err != nil {
		return RenderedMonitorPipeline{}, err
	}
	publicConfig, err := cloneMonitorConfig(config)
	if err != nil {
		return RenderedMonitorPipeline{}, err
	}
	authority := &monitorPipelineAuthority{
		target: *cloned, pipelineName: name, config: authorityConfig,
		canonicalJSON: append([]byte(nil), canonical...), configHash: configHash,
	}
	return RenderedMonitorPipeline{
		PipelineName: name, Config: publicConfig,
		CanonicalJSON: append([]byte(nil), canonical...), ConfigHash: configHash,
		authority: authority,
	}, nil
}

// ProtectedForBinding reopens the privately retained render only when the
// binding projection is exact.
func (rendered RenderedMonitorPipeline) ProtectedForBinding(
	binding Binding,
) (RenderedMonitorPipeline, error) {
	if rendered.authority == nil {
		return RenderedMonitorPipeline{}, fmt.Errorf(
			"pullrequest: monitor pipeline has no protected authority",
		)
	}
	policy := MonitorPipelinePolicy{
		APIBaseURL:        rendered.authority.target.APIBaseURL,
		RepositoryURL:     rendered.authority.target.RepositoryURL,
		ReadCredential:    rendered.authority.target.ReadCredential,
		PollInterval:      rendered.authority.target.PollInterval,
		FreshnessInterval: rendered.authority.target.FreshnessInterval,
		ResourceType:      rendered.authority.target.ResourceType,
	}
	expected, err := MonitorPipelineTargetForBinding(binding, policy)
	if err != nil {
		return RenderedMonitorPipeline{}, err
	}
	if !reflect.DeepEqual(expected, rendered.authority.target) {
		return RenderedMonitorPipeline{}, fmt.Errorf(
			"pullrequest: protected monitor pipeline binding changed",
		)
	}
	config, err := cloneMonitorConfig(rendered.authority.config)
	if err != nil {
		return RenderedMonitorPipeline{}, err
	}
	return RenderedMonitorPipeline{
		PipelineName:  rendered.authority.pipelineName,
		Config:        config,
		CanonicalJSON: append([]byte(nil), rendered.authority.canonicalJSON...),
		ConfigHash:    rendered.authority.configHash,
		authority:     rendered.authority,
	}, nil
}

func validateMonitorPipelineBinding(binding Binding) error {
	if binding.ID <= 0 || binding.TeamID <= 0 ||
		binding.MonitorWorkflowDefinitionID <= 0 ||
		binding.MonitorWorkflowVersion <= 0 || binding.Revision <= 0 {
		return fmt.Errorf("pullrequest: monitor binding identities are invalid")
	}
	if err := binding.Locator.validate(contracts.PullRequestActive); err != nil {
		return fmt.Errorf("pullrequest: monitor binding locator: %w", err)
	}
	if err := binding.AcknowledgedCursor.Validate(); err != nil ||
		binding.AcknowledgedCursor == "" {
		return fmt.Errorf("pullrequest: monitor binding cursor is invalid")
	}
	if err := validateObjectID(
		"last reconciled target sha", binding.LastReconciledTargetSHA,
	); err != nil {
		return err
	}
	if binding.LastReconciledAt.IsZero() ||
		binding.LastReconciledAt.Location() != time.UTC ||
		!binding.LastReconciledAt.Equal(
			binding.LastReconciledAt.Truncate(time.Microsecond),
		) {
		return fmt.Errorf(
			"pullrequest: monitor reconciliation time must be canonical UTC microseconds",
		)
	}
	if err := binding.State.Validate(); err != nil {
		return err
	}
	if binding.Active != nil && !isDigest(binding.Active.ActionDigest) {
		return fmt.Errorf("pullrequest: monitor active action is invalid")
	}
	return nil
}

func validateMonitorPipelinePolicy(policy MonitorPipelinePolicy) error {
	if err := validateMonitorURL(policy.APIBaseURL); err != nil {
		return fmt.Errorf("pullrequest: monitor API base URL: %w", err)
	}
	if err := validateMonitorURL(policy.RepositoryURL); err != nil {
		return fmt.Errorf("pullrequest: monitor repository URL: %w", err)
	}
	if strings.TrimSpace(policy.ReadCredential) != policy.ReadCredential ||
		policy.ReadCredential == "" || len(policy.ReadCredential) > 512 ||
		!utf8.ValidString(policy.ReadCredential) ||
		strings.Contains(policy.ReadCredential, "((") ||
		strings.Contains(policy.ReadCredential, "))") {
		return fmt.Errorf("pullrequest: monitor read credential reference is invalid")
	}
	for _, character := range policy.ReadCredential {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return fmt.Errorf(
				"pullrequest: monitor read credential reference is invalid",
			)
		}
	}
	if policy.PollInterval <= 0 ||
		policy.FreshnessInterval <= 0 ||
		policy.FreshnessInterval%policy.PollInterval != 0 {
		return fmt.Errorf("pullrequest: monitor intervals are invalid")
	}
	resourceType := policy.ResourceType
	if resourceType.Name != MonitorResourceTypeName ||
		!exactMonitorResourceImage.MatchString(resourceType.Image) ||
		resourceType.Type != "" || len(resourceType.Source) != 0 ||
		len(resourceType.Defaults) != 0 || resourceType.Privileged ||
		resourceType.CheckEvery != nil || len(resourceType.Tags) != 0 ||
		len(resourceType.Params) != 0 {
		return fmt.Errorf(
			"pullrequest: monitor resource type must be an exact unprivileged image",
		)
	}
	return nil
}

func validateMonitorPipelineTarget(target *MonitorPipelineTarget) error {
	if target == nil {
		return fmt.Errorf("pullrequest: monitor pipeline target is required")
	}
	policy := MonitorPipelinePolicy{
		APIBaseURL: target.APIBaseURL, RepositoryURL: target.RepositoryURL,
		ReadCredential: target.ReadCredential, PollInterval: target.PollInterval,
		FreshnessInterval: target.FreshnessInterval,
		ResourceType:      target.ResourceType,
	}
	if err := validateMonitorPipelinePolicy(policy); err != nil {
		return err
	}
	if target.TeamID <= 0 || target.BindingID <= 0 ||
		target.WorkflowDefinition <= 0 || target.WorkflowVersion <= 0 {
		return fmt.Errorf("pullrequest: monitor pipeline identities are invalid")
	}
	locator := Locator{
		Provider: Provider(target.Provider), Repository: target.Repository,
		ExternalID: target.ExternalID,
	}
	if err := locator.validate(contracts.PullRequestActive); err != nil {
		return fmt.Errorf("pullrequest: monitor pipeline locator: %w", err)
	}
	state := target.CheckState
	if state.BindingID != target.BindingID || state.BindingRevision <= 0 {
		return fmt.Errorf("pullrequest: monitor check binding is invalid")
	}
	if err := Cursor(state.AcknowledgedCursor).Validate(); err != nil ||
		state.AcknowledgedCursor == "" {
		return fmt.Errorf("pullrequest: monitor check cursor is invalid")
	}
	if err := validateObjectID(
		"monitor reconciled target", state.LastReconciledTarget,
	); err != nil {
		return err
	}
	if state.LastReconciledAt.IsZero() ||
		state.LastReconciledAt.Location() != time.UTC ||
		!state.LastReconciledAt.Equal(
			state.LastReconciledAt.Truncate(time.Microsecond),
		) {
		return fmt.Errorf(
			"pullrequest: monitor check time must be canonical UTC microseconds",
		)
	}
	if state.ActiveActionDigest != "" && !isDigest(state.ActiveActionDigest) {
		return fmt.Errorf("pullrequest: monitor check action is invalid")
	}
	return nil
}

func validateMonitorURL(raw string) error {
	if raw == "" || len(raw) > maxURLBytes {
		return fmt.Errorf("URL is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" {
		return fmt.Errorf("URL must be an absolute credential-free HTTPS URL")
	}
	return nil
}

// monitorCheckSource uses the same map-shaped representation returned by ATC
// config persistence. This keeps CanonicalJSON byte-stable across a
// save-and-load cycle instead of changing object-key order when a typed struct
// is decoded back into an untyped resource source.
func monitorCheckSource(state MonitorCheckState) atc.Source {
	return atc.Source{
		"binding_id":             state.BindingID,
		"binding_revision":       state.BindingRevision,
		"acknowledged_cursor":    state.AcknowledgedCursor,
		"last_reconciled_target": state.LastReconciledTarget,
		"last_reconciled_at":     state.LastReconciledAt,
		"active_action_digest":   state.ActiveActionDigest,
		"attention_required":     state.AttentionRequired,
		"paused":                 state.Paused,
		"operator_terminated":    state.OperatorTerminated,
	}
}

func cloneMonitorPipelineTarget(
	target MonitorPipelineTarget,
) (*MonitorPipelineTarget, error) {
	copied, err := copystructure.Copy(&target)
	if err != nil {
		return nil, err
	}
	cloned, ok := copied.(*MonitorPipelineTarget)
	if !ok {
		return nil, fmt.Errorf(
			"unexpected monitor pipeline target clone %T", copied,
		)
	}
	return cloned, nil
}

func cloneMonitorConfig(config atc.Config) (atc.Config, error) {
	copied, err := copystructure.Copy(config)
	if err != nil {
		return atc.Config{}, fmt.Errorf(
			"pullrequest: clone monitor pipeline config: %w", err,
		)
	}
	cloned, ok := copied.(atc.Config)
	if !ok {
		return atc.Config{}, fmt.Errorf(
			"pullrequest: unexpected monitor config clone %T", copied,
		)
	}
	return cloned, nil
}

func cloneMonitorResourceType(value atc.ResourceType) (atc.ResourceType, error) {
	copied, err := copystructure.Copy(value)
	if err != nil {
		return atc.ResourceType{}, err
	}
	cloned, ok := copied.(atc.ResourceType)
	if !ok {
		return atc.ResourceType{}, fmt.Errorf(
			"unexpected monitor resource type clone %T", copied,
		)
	}
	return cloned, nil
}

func monitorPipelineHash(domain string, payload []byte) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(domain))
	_, _ = hasher.Write(payload)
	return fmt.Sprintf("%x", hasher.Sum(nil))
}
