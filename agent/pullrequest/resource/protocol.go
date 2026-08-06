// Package resource implements the provider-native, read-only Concourse PR
// resource protocol. The server-projected monitor binding is the only
// acknowledgement authority; a Concourse version is merely an ordering token.
package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/pullrequest"
)

const (
	maxInputBytes  = 256 << 10
	maxOutputBytes = 256 << 10
)

// MonitorCheckState is the server-projected, acknowledged state of the PR
// binding. It must not be constructed from a prior Concourse version.
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

// UnmarshalJSON rejects offset spellings and non-canonical timestamps before
// time.Time has a chance to normalize them. The projected value is part of
// resource configuration, so accepting a second byte representation would
// make audit/digest comparisons ambiguous.
func (state *MonitorCheckState) UnmarshalJSON(raw []byte) error {
	type wire struct {
		BindingID            int64  `json:"binding_id"`
		BindingRevision      int64  `json:"binding_revision"`
		AcknowledgedCursor   string `json:"acknowledged_cursor"`
		LastReconciledTarget string `json:"last_reconciled_target"`
		LastReconciledAt     string `json:"last_reconciled_at"`
		ActiveActionDigest   string `json:"active_action_digest"`
		AttentionRequired    bool   `json:"attention_required"`
		Paused               bool   `json:"paused"`
		OperatorTerminated   bool   `json:"operator_terminated"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value wire
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing data")
	}
	instant := time.Time{}
	if value.LastReconciledAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value.LastReconciledAt)
		if err != nil || !strings.HasSuffix(value.LastReconciledAt, "Z") || parsed.UTC().Format(time.RFC3339Nano) != value.LastReconciledAt {
			return fmt.Errorf("last_reconciled_at is not canonical UTC")
		}
		instant = parsed.UTC()
	}
	*state = MonitorCheckState{BindingID: value.BindingID, BindingRevision: value.BindingRevision, AcknowledgedCursor: value.AcknowledgedCursor, LastReconciledTarget: value.LastReconciledTarget, LastReconciledAt: instant, ActiveActionDigest: value.ActiveActionDigest, AttentionRequired: value.AttentionRequired, Paused: value.Paused, OperatorTerminated: value.OperatorTerminated}
	return nil
}

// Source is deliberately a closed, policy-resolved source document. ReadToken
// is never serialized after decoding and errors must not reflect this value.
type Source struct {
	Provider          string            `json:"provider"`
	Repository        string            `json:"repository"`
	ExternalID        string            `json:"external_id"`
	APIBaseURL        string            `json:"api_base_url"`
	RepositoryURL     string            `json:"repository_url"`
	ReadToken         string            `json:"read_token"`
	PollInterval      string            `json:"poll_interval"`
	FreshnessInterval string            `json:"freshness_interval"`
	Monitor           MonitorCheckState `json:"monitor"`
}

// Version is the complete Concourse version identity. BindingRevision stays a
// string so JSON decoders cannot silently accept a float or exponent.
type Version struct {
	Provider        string `json:"provider"`
	ExternalID      string `json:"external_id"`
	SourceSHA       string `json:"source_sha"`
	TargetSHA       string `json:"target_sha"`
	ActionKind      string `json:"action_kind"`
	ActionDigest    string `json:"action_digest"`
	Cursor          string `json:"cursor"`
	BindingRevision string `json:"binding_revision"`
}

type Request struct {
	Source  Source   `json:"source"`
	Version *Version `json:"version,omitempty"`
}

type InResult struct {
	Version  Version `json:"version"`
	Metadata []any   `json:"metadata,omitempty"`
}

// ObserverFactory makes provider selection explicit and testable.
type ObserverFactory func(Source) (pullrequest.Observer, error)

// GitCommand contains no literal argument vector: repository URLs, refs and
// credentials cross the git seam as typed fields so the implementation can
// keep untrusted values out of options and the credential out of logs.
type GitFetchMode string

const (
	GitFetchNamedRef    GitFetchMode = "named_ref"
	GitFetchExactObject GitFetchMode = "exact_object"
)

type GitCommand struct {
	Operation  string
	FetchMode  GitFetchMode
	Directory  string
	RemoteURL  string
	Ref        string
	SHA        string
	Credential []byte

	// SecondRef and SecondSHA bring the other pull-request revision down in the
	// same fetch. Both revisions live in one remote and their object sets overlap
	// almost entirely, so fetching them separately re-transfers the same history.
	SecondRef string
	SecondSHA string

	// LocalSource populates this checkout from an already-materialized repository
	// instead of the remote, once that fetch has brought both revisions down.
	// Objects are copied by an ordinary fetch over a filesystem path -- never
	// shared through alternates and never hardlinked -- so each materialized
	// repository remains independently valid evidence, which is what makes
	// repository/v1 resealing mean anything.
	LocalSource string

	// verifyDirectory is installed by In when Directory is backed by a
	// retained os.Root. The production runner calls it around each pathname-
	// based Git process so a moved or replaced destination fails closed.
	verifyDirectory func() error
}

type GitRunner interface {
	Run(context.Context, GitCommand) error
}

type Dependencies struct {
	ObserverFactory   ObserverFactory
	GitRunner         GitRunner
	Clock             func() time.Time
	BeforeMaterialize func() error
	BeforeGit         func(GitCommand) error
}

func decodeRequest(reader io.Reader) (Request, error) {
	if reader == nil {
		return Request{}, fmt.Errorf("forge-pr: input is required")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxInputBytes+1))
	if err != nil {
		return Request{}, fmt.Errorf("forge-pr: read input")
	}
	if len(raw) > maxInputBytes {
		return Request{}, fmt.Errorf("forge-pr: input exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("forge-pr: invalid input")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Request{}, fmt.Errorf("forge-pr: invalid trailing input")
	}
	return request, nil
}

func (source Source) validate() (pullrequest.Locator, time.Duration, time.Duration, error) {
	locator := pullrequest.Locator{Provider: pullrequest.Provider(source.Provider), Repository: source.Repository, ExternalID: source.ExternalID}
	if source.Provider != string(pullrequest.ProviderGitHub) {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: unsupported provider")
	}
	// Validate through the observation boundary without permitting a fake state.
	if !safeText(source.Repository, 512) || !safeText(source.ExternalID, 256) || strings.ContainsAny(source.Repository, " \t\r\n") || strings.ContainsAny(source.ExternalID, " \t\r\n") {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: source locator is invalid")
	}
	if err := safeURL(source.APIBaseURL); err != nil {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: api base URL is invalid")
	}
	if err := safeURL(source.RepositoryURL); err != nil {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: repository URL is invalid")
	}
	if strings.TrimSpace(source.ReadToken) == "" || len(source.ReadToken) > 16<<10 || !utf8.ValidString(source.ReadToken) {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: read credential is unavailable")
	}
	poll, err := parseDuration(source.PollInterval)
	if err != nil {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: poll interval is invalid")
	}
	fresh, err := parseDuration(source.FreshnessInterval)
	if err != nil || fresh%poll != 0 {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: freshness interval is invalid")
	}
	if source.Monitor.BindingID <= 0 || source.Monitor.BindingRevision <= 0 {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: monitor binding is invalid")
	}
	if err := pullrequest.Cursor(source.Monitor.AcknowledgedCursor).Validate(); err != nil {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: acknowledged cursor is invalid")
	}
	if source.Monitor.LastReconciledTarget != "" && !objectID(source.Monitor.LastReconciledTarget) {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: reconciled target is invalid")
	}
	if source.Monitor.ActiveActionDigest != "" && !digest(source.Monitor.ActiveActionDigest) {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: active action digest is invalid")
	}
	if !source.Monitor.LastReconciledAt.IsZero() && (source.Monitor.LastReconciledAt.Location() != time.UTC || source.Monitor.LastReconciledAt.Format(time.RFC3339Nano) != source.Monitor.LastReconciledAt.UTC().Format(time.RFC3339Nano)) {
		return pullrequest.Locator{}, 0, 0, fmt.Errorf("forge-pr: reconciled time is not canonical UTC")
	}
	return locator, poll, fresh, nil
}

func safeText(value string, maximum int) bool {
	if strings.TrimSpace(value) == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, rune := range value {
		if unicode.IsControl(rune) {
			return false
		}
	}
	return true
}

func safeURL(raw string) error {
	if len(raw) == 0 || len(raw) > 2048 {
		return fmt.Errorf("invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("invalid")
	}
	return nil
}

func parseDuration(raw string) (time.Duration, error) {
	if raw == "" || len(raw) > 32 || strings.TrimSpace(raw) != raw {
		return 0, fmt.Errorf("invalid")
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid")
	}
	return duration, nil
}

func (version Version) validate() error {
	if version.Provider != string(pullrequest.ProviderGitHub) {
		return fmt.Errorf("forge-pr: version provider is invalid")
	}
	if strings.TrimSpace(version.ExternalID) == "" || !objectID(version.SourceSHA) || !objectID(version.TargetSHA) || !validActionKind(version.ActionKind) || !digest(version.ActionDigest) {
		return fmt.Errorf("forge-pr: version is invalid")
	}
	if err := pullrequest.Cursor(version.Cursor).Validate(); err != nil || version.Cursor == "" {
		return fmt.Errorf("forge-pr: version cursor is invalid")
	}
	if _, err := positiveDecimal(version.BindingRevision); err != nil {
		return fmt.Errorf("forge-pr: version binding revision is invalid")
	}
	return nil
}

func positiveDecimal(raw string) (int64, error) {
	if raw == "" || raw[0] == '0' || strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "-") {
		return 0, fmt.Errorf("invalid")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid")
		}
	}
	return strconv.ParseInt(raw, 10, 64)
}
func objectID(raw string) bool {
	if len(raw) != 40 && len(raw) != 64 {
		return false
	}
	for _, r := range raw {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func digest(raw string) bool {
	return len(raw) == 71 && strings.HasPrefix(raw, "sha256:") && objectID(raw[len("sha256:"):]) && len(raw[len("sha256:"):]) == 64
}
func validActionKind(raw string) bool {
	switch pullrequest.ActionKind(raw) {
	case pullrequest.ActionReviewBatch, pullrequest.ActionConflict, pullrequest.ActionFreshness, pullrequest.ActionCompleted, pullrequest.ActionAbandoned:
		return true
	}
	return false
}

func bindingRevision(source Source) string {
	return strconv.FormatInt(source.Monitor.BindingRevision, 10)
}
func versionFor(source Source, action pullrequest.Action) Version {
	return Version{Provider: source.Provider, ExternalID: source.ExternalID, SourceSHA: action.Observation.SourceSHA, TargetSHA: action.Observation.TargetSHA, ActionKind: string(action.Kind), ActionDigest: action.Digest, Cursor: string(action.Cursor), BindingRevision: bindingRevision(source)}
}
func equalVersion(left, right Version) bool { return left == right }
func writeJSON(writer io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("forge-pr: encode output")
	}
	if len(raw) > maxOutputBytes {
		return fmt.Errorf("forge-pr: output exceeds limit")
	}
	if _, err := writer.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("forge-pr: write output")
	}
	return nil
}
