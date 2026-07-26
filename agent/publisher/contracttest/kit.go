package contracttest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

// Config describes the gateway under test. Endpoint, TokenFile, TeamName and
// ApprovalPolicyVersion are always required; the Git fields are required for
// the current-base check and for writes.
type Config struct {
	Endpoint              string
	TokenFile             string
	CACertificateFile     string
	TeamName              string
	ApprovalPolicyVersion string
	GitDestination        string
	GitTargetBranch       string

	// Change enables the write checks. Leave it nil to run the read-only
	// protocol checks, which create no external effect.
	Change *ChangeFixture

	// Timeout bounds each individual check (default 2m).
	Timeout time.Duration
}

// Run executes the gateway protocol conformance checks. Every check names an
// obligation from docs/agentic/README.md; an implementation that fails any of
// them can duplicate or lose an external side effect.
//
// Each check is its own named sub-test, so a live gate can select or exclude
// one by name (`-run 'TestX/publish_is_idempotent_under_one_key'`) without
// editing the kit, and the write checks stay behind Config.Change because they
// create a real external effect.
func Run(t *testing.T, config Config) {
	t.Helper()
	requireKitConfig(t, config)
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Minute
	}
	token := readToken(t, config.TokenFile)
	client := newKitClient(t, config)

	run := func(name string, check func(ctx context.Context) error) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
			defer cancel()
			if err := check(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}

	// Both auth checks send an otherwise well-formed lookup — matching header
	// and body key — so the only thing wrong with the request is its
	// authorization. A gateway that answers 400 here validated the body before
	// the token, and the failure says so instead of blaming the kit.
	run("auth_is_required", func(ctx context.Context) error {
		key := randomKey()
		status, _, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup", "", key, lookupBody(key))
		if err != nil {
			return err
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			return fmt.Errorf("an unauthenticated lookup answered %d; it must answer 401 or 403, because an unauthenticated request must never be served", status)
		}
		return nil
	})

	// A rejected (wrong) token must answer 401, specifically — not 403 and not
	// a 5xx. The publisher's taxonomy (agent/publisher/gateway.go,
	// gatewayStatusRetryable) classifies 401 as retryable, on purpose: the
	// Authorization header is out-of-band from the operation key, so a bad
	// token can be corrected by rotation or a secret-remount lag WITHOUT the
	// semantic operation changing, and the client retries it on the next
	// lease. A gateway that answered 403 here would be classified terminal —
	// permanently poisoning the operation key over what may be a transient
	// credential problem, for every future run sharing that key. A 5xx would
	// make the client retry a permanently-bad token forever instead of
	// surfacing it.
	run("bad_token_is_retryable", func(ctx context.Context) error {
		key := randomKey()
		status, _, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup", token+"-wrong", key, lookupBody(key))
		if err != nil {
			return err
		}
		if status != http.StatusUnauthorized {
			return fmt.Errorf("a rejected token answered %d, want 401: 401 is retryable in the client's taxonomy, so a corrected token recovers on the next lease instead of the publication failing permanently", status)
		}
		return nil
	})

	run("unknown_operation_key_is_not_found", func(ctx context.Context) error {
		key := randomKey()
		status, body, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup", token, key, lookupBody(key))
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("lookup of an unknown key answered %d, want 200", status)
		}
		response, err := decodeLookup(body)
		if err != nil {
			return err
		}
		if response.Found || response.Result.ExternalID != "" {
			return fmt.Errorf("an unknown operation key answered %+v, want found:false with no result", response)
		}
		return nil
	})

	// Tightened to require a 4xx, not just "not 200": a 5xx here would be
	// classified retryable by the client's own taxonomy, so a gateway that
	// answered 500 would pass this check while making the client retry a
	// permanently malformed request forever instead of learning it is
	// malformed.
	run("lookup_requires_the_idempotency_key", func(ctx context.Context) error {
		key := randomKey()
		status, _, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup", token, "", lookupBody(key))
		if err != nil {
			return err
		}
		if status < 400 || status >= 500 {
			return fmt.Errorf("lookup without an Idempotency-Key answered %d, want a 4xx: the key is how the durable operation is identified, and this request can never succeed by repeating it", status)
		}
		return nil
	})

	// Registered only when a destination is configured: never call t.Skip from
	// inside a check closure — it holds the parent *testing.T and would skip
	// the whole kit.
	if config.GitDestination != "" && config.GitTargetBranch != "" {
		run("current_base_needs_no_idempotency_key", func(ctx context.Context) error {
			return checkCurrentBase(ctx, client, config, token)
		})
	}

	run("unknown_endpoint_is_terminal", func(ctx context.Context) error {
		status, _, err := post(ctx, client, config.Endpoint, "/v1/definitely-not-an-endpoint", token, randomKey(), `{}`)
		if err != nil {
			return err
		}
		if status < 400 || status >= 500 {
			return fmt.Errorf("an unknown endpoint answered %d; it must answer a terminal 4xx so the client stops instead of retrying forever", status)
		}
		return nil
	})

	if config.Change == nil {
		return
	}
	// The write checks share the first publication so the second can prove
	// byte-identical recovery. They run in order because t.Run is sequential.
	directory := t.TempDir()
	var published publisher.Result

	run("publish_then_lookup_is_visible", func(ctx context.Context) error {
		result, err := publishOnce(ctx, config, directory)
		if err != nil {
			return err
		}
		published = result
		key := operationKeyFor(config)
		status, body, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup",
			token, key, lookupBody(key))
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("lookup after a landed publish answered %d, want 200", status)
		}
		response, err := decodeLookup(body)
		if err != nil {
			return err
		}
		if !response.Found {
			return fmt.Errorf("a landed publish is invisible to lookup; every crash-recovery guarantee rests on this")
		}
		if response.Result.ExternalID != published.ExternalID || response.Result.HeadSHA != config.Change.ResultSHA {
			return fmt.Errorf("recovered result %+v does not match the published one %+v", response.Result, published)
		}
		return nil
	})

	// What this proves: a second INDEPENDENT publication attempt under one
	// operation key — fresh store, no durable memory of the first — converges on
	// the first result and adds no external effect.
	//
	// What it cannot prove: that the gateway's publish endpoint itself
	// deduplicates by Idempotency-Key. The real client looks up before it
	// writes, so on a gateway whose lookup answers, the second attempt recovers
	// at lookup and never issues a second publish request. A gateway with a
	// working lookup and a non-deduplicating publish passes this check and still
	// double-publishes the moment its read side lags its write side. Staging
	// that disagreement needs fault injection, so it is pinned in-repo against
	// the reference instead: see
	// TestGatewayRetriesCarryBytewiseIdenticalIdempotencyKeyWhenLookupLagsThePublish
	// in agent/publisher/gateway_idempotency_test.go (two publish requests, one
	// external effect).
	run("publish_is_idempotent_under_one_key", func(ctx context.Context) error {
		result, err := publishOnce(ctx, config, directory)
		if err != nil {
			return err
		}
		if result.ExternalID != published.ExternalID || result.URL != published.URL {
			return fmt.Errorf("a second publish under one operation key produced %+v, want the first result %+v — the gateway created a second external effect",
				result, published)
		}
		return nil
	})
}

// requireKitConfig fails the whole kit before any check runs: a missing field
// would otherwise surface as an unrelated protocol failure against a gateway
// that is behaving correctly.
func requireKitConfig(t *testing.T, config Config) {
	t.Helper()
	for name, value := range map[string]string{
		"Endpoint": config.Endpoint, "TokenFile": config.TokenFile,
		"TeamName": config.TeamName, "ApprovalPolicyVersion": config.ApprovalPolicyVersion,
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("contracttest: Config.%s is required", name)
		}
	}
	if config.Change != nil && (config.GitDestination == "" || config.GitTargetBranch == "") {
		t.Fatal("contracttest: the write checks need Config.GitDestination and Config.GitTargetBranch")
	}
}

// lookupResponse is the client's view of a lookup answer. The publisher decodes
// gateway responses with DisallowUnknownFields, so a gateway that adds a field
// here breaks the real client and must break this kit too.
type lookupResponse struct {
	Found  bool `json:"found"`
	Result struct {
		ExternalID string `json:"external_id"`
		URL        string `json:"url"`
		HeadSHA    string `json:"head_sha"`
	} `json:"result"`
}

func decodeLookup(body []byte) (lookupResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var response lookupResponse
	if err := decoder.Decode(&response); err != nil {
		return lookupResponse{}, fmt.Errorf("lookup response carries fields the client rejects (it decodes with DisallowUnknownFields): %w", err)
	}
	return response, nil
}

func readToken(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// newKitClient mirrors the publisher's own transport policy: the mounted CA
// bundle when one is configured, TLS 1.2 or better, and no redirects — a
// redirected write is a write whose destination the client never authorized.
func newKitClient(t *testing.T, config Config) *http.Client {
	t.Helper()
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if config.CACertificateFile != "" {
		certificate, err := os.ReadFile(config.CACertificateFile)
		if err != nil {
			t.Fatal(err)
		}
		if !roots.AppendCertsFromPEM(certificate) {
			t.Fatalf("contracttest: %s contains no certificates", config.CACertificateFile)
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: 10 * time.Second,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("contracttest: the gateway must not redirect")
		},
	}
}

func post(
	ctx context.Context,
	client *http.Client,
	endpoint, path, token, key, body string,
) (int, []byte, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, strings.TrimSuffix(endpoint, "/")+path, strings.NewReader(body),
	)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, payload, nil
}

func lookupBody(key string) string {
	return fmt.Sprintf(`{"publisher":%q,"operation_key":%q}`, publisher.GitPublisher, key)
}

// randomKey is a syntactically valid operation key that identifies nothing.
func randomKey() string {
	var raw [32]byte
	// crypto/rand.Read never reports an error; it panics if the system source
	// is unusable, which no caller could recover from anyway.
	_, _ = rand.Read(raw[:])
	return "sha256:" + hex.EncodeToString(raw[:])
}

// validObjectID mirrors validGitObjectID in agent/publisher/gateway.go: a base
// the publisher cannot parse is a base it cannot compare, so it fails the
// publication rather than publishing onto an unknown head.
func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

// checkCurrentBase pins the idempotency-key exemption. current-base identifies
// no durable operation, so gatewayGitBackend.CurrentBase sends an empty
// operation key (agent/publisher/gateway.go) and the header is absent; a
// gateway that demands one here fails every publication before its first write.
func checkCurrentBase(ctx context.Context, client *http.Client, config Config, token string) error {
	body, err := json.Marshal(map[string]string{
		"destination": config.GitDestination, "target_branch": config.GitTargetBranch,
	})
	if err != nil {
		return err
	}
	status, payload, err := post(ctx, client, config.Endpoint, "/v1/git/current-base", token, "", string(body))
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("current-base answered %d without an Idempotency-Key; the client never sends one here (gatewayGitBackend.CurrentBase in agent/publisher/gateway.go)", status)
	}
	var response struct {
		BaseSHA string `json:"base_sha"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("current-base response carries fields the client rejects (it decodes with DisallowUnknownFields): %w", err)
	}
	if !validObjectID(response.BaseSHA) {
		return fmt.Errorf("current-base answered base_sha %q; it must be 40 or 64 lowercase hexadecimal characters or the publisher cannot compare it to the sealed change", response.BaseSHA)
	}
	return nil
}

// gitRequest is the publication the write checks perform. It is only reachable
// with Config.Change set, which requireKitConfig pairs with the Git fields.
func gitRequest(config Config) publisher.Request {
	return publisher.Request{
		Publisher:   publisher.GitPublisher,
		Input:       config.Change.ref(),
		Destination: config.GitDestination,
		Mode:        publisher.ModePullRequest,
		Parameters: map[string]string{
			"source_branch": "agent/contracttest", "target_branch": config.GitTargetBranch,
		},
		ApprovalPolicyVersion: config.ApprovalPolicyVersion,
		Authority: publisher.Authority{
			TeamID: 9, TeamName: config.TeamName, BuildID: 12, WorkflowRunID: 17, Actor: "contracttest",
		},
	}
}

// operationKeyFor discards the error: gitRequest is constructed valid, and
// requireKitConfig already rejected the inputs that could make it otherwise.
func operationKeyFor(config Config) string {
	key, _ := gitRequest(config).OperationKey()
	return key
}

// publishOnce runs one complete publication through the real publisher against
// the gateway under test. The store is FRESH per call, which is what makes a
// second call a real second request rather than a durable replay: the
// exactly-once guarantee then has to come from the gateway, which is the thing
// being tested.
func publishOnce(ctx context.Context, config Config, directory string) (publisher.Result, error) {
	policyPath := filepath.Join(directory, "policy.json")
	policy, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"rules": []map[string]any{{
			"team":                     config.TeamName,
			"publisher":                publisher.GitPublisher,
			"modes":                    []publisher.Mode{publisher.ModePullRequest},
			"approval_policy_versions": []string{config.ApprovalPolicyVersion},
			"target_branches":          []string{config.GitTargetBranch},
			"destinations":             []string{config.GitDestination},
		}},
	})
	if err != nil {
		return publisher.Result{}, err
	}
	if err := os.WriteFile(policyPath, policy, 0600); err != nil {
		return publisher.Result{}, err
	}
	metadata, content := config.Change.stores()
	executor, err := publisher.NewGatewayExecutor(
		publisher.NewMemoryStore(time.Now), metadata, content,
		publisher.GatewayConfig{
			Endpoint:          config.Endpoint,
			PolicyFile:        policyPath,
			TokenFile:         config.TokenFile,
			CACertificateFile: config.CACertificateFile,
			RequestTimeout:    30 * time.Second,
			LeaseDuration:     5 * time.Minute,
			MaxResponseBytes:  1 << 20,
			// The cluster-wide action switch is platform state, not gateway
			// protocol. The kit holds it open so a suppressed deployment cannot
			// make a conforming gateway look broken.
			ActionsMode: actionsAlwaysActive{},
		},
	)
	if err != nil {
		return publisher.Result{}, err
	}
	publication, err := executor.Execute(ctx, gitRequest(config))
	if err != nil {
		return publisher.Result{}, err
	}
	if publication.Status != publisher.StatusSucceeded {
		return publisher.Result{}, fmt.Errorf("publication completed %q (%s), want succeeded",
			publication.Status, publication.Result.Detail)
	}
	return publication.Result, nil
}

type actionsAlwaysActive struct{}

func (actionsAlwaysActive) GetActionsMode() (string, bool, error) {
	return publisher.ActionsModeActive, true, nil
}
