package contracttest_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher/contracttest"
)

func referenceClient(t *testing.T, reference *contracttest.ReferenceServer) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(reference.Server.Certificate())
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

func referencePost(t *testing.T, reference *contracttest.ReferenceServer, path, token, key, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, reference.Server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := referenceClient(t, reference).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestReferenceServerEnforcesAuthKeysAndTheCurrentBaseExemption(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{})
	key := "sha256:" + strings.Repeat("a", 64)
	lookupBody := `{"publisher":"git-publisher/v1","operation_key":"` + key + `"}`

	if got := referencePost(t, reference, "/v1/publications/lookup", "", key, lookupBody).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated lookup = %d, want 401", got)
	}
	if got := referencePost(t, reference, "/v1/publications/lookup", "mounted-token", "", lookupBody).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("lookup without Idempotency-Key = %d, want 400", got)
	}
	response := referencePost(t, reference, "/v1/publications/lookup", "mounted-token", key, lookupBody)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("lookup = %d, want 200", response.StatusCode)
	}
	var found struct {
		Found bool `json:"found"`
	}
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil || found.Found {
		t.Fatalf("unknown key must answer found:false (%+v, %v)", found, err)
	}
	// The current-base exemption is a rule, not an option: the client never
	// sends a key here (agent/publisher/gateway.go:473).
	base := referencePost(t, reference, "/v1/git/current-base", "mounted-token", "",
		`{"destination":"git.example/acme/widget","target_branch":"main"}`)
	if base.StatusCode != http.StatusOK {
		t.Fatalf("current-base without Idempotency-Key = %d, want 200", base.StatusCode)
	}
	if got := referencePost(t, reference, "/v1/nope", "mounted-token", key, `{}`).StatusCode; got != http.StatusNotFound {
		t.Fatalf("unknown endpoint = %d, want 404", got)
	}
	if got := referencePost(t, reference, "/v1/publications/lookup", "mounted-token", key,
		`{"publisher":"git-publisher/v1","operation_key":"`+key+`","surprise":1}`).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("unknown request field = %d, want 400", got)
	}
}

func TestReferenceServerDeduplicatesPublishByOperationKey(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{})
	key := "sha256:" + strings.Repeat("b", 64)
	for attempt := 0; attempt < 2; attempt++ {
		body, contentType := multipartPublish(t, key, strings.Repeat("2", 40))
		request, err := http.NewRequest(http.MethodPost, reference.Server.URL+"/v1/git/publish", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("Authorization", "Bearer mounted-token")
		request.Header.Set("Idempotency-Key", key)
		response, err := referenceClient(t, reference).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			ExternalID string `json:"external_id"`
			HeadSHA    string `json:"head_sha"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if result.ExternalID != "publication/1" || result.HeadSHA != strings.Repeat("2", 40) {
			t.Fatalf("attempt %d result = %+v", attempt, result)
		}
	}
	if reference.PublishRequests() != 2 || reference.ExternalEffects() != 1 {
		t.Fatalf("requests/effects = %d/%d, want 2/1", reference.PublishRequests(), reference.ExternalEffects())
	}
}

// TestReferenceServerMakesPublishedResultsVisibleToLookup pins the property the
// whole adapter contract rests on (docs/agentic/README.md:512-517): the gateway
// durably maps (publisher, operation_key) to the provider result, so a caller
// that acquires a lease after an ambiguous timeout recovers the landed write by
// lookup instead of publishing a second time.
func TestReferenceServerMakesPublishedResultsVisibleToLookup(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{})
	firstKey := "sha256:" + strings.Repeat("c", 64)
	secondKey := "sha256:" + strings.Repeat("d", 64)

	status, published := publishToReference(t, reference, "/v1/git/publish", firstKey, strings.Repeat("3", 40))
	if status != http.StatusOK {
		t.Fatalf("publish = %d, want 200", status)
	}

	recovered := lookupReference(t, reference, "git-publisher/v1", firstKey)
	if !recovered.Found {
		t.Fatal("lookup after publish must find the landed result: an invisible write duplicates the external effect")
	}
	if recovered.Result != published {
		t.Fatalf("lookup result = %+v, want the published result %+v", recovered.Result, published)
	}

	// The map is keyed by (publisher, operation_key), not by the key alone: the
	// same operation key under the other publisher is a different operation.
	if other := lookupReference(t, reference, "work-item-publisher/v1", firstKey); other.Found {
		t.Fatalf("work-item lookup of a Git operation key must miss, got %+v", other.Result)
	}

	// Distinct keys are distinct operations, each with its own external effect.
	if _, second := publishToReference(t, reference, "/v1/git/publish", secondKey, strings.Repeat("4", 40)); second.ExternalID == published.ExternalID {
		t.Fatalf("distinct operation keys must produce distinct results, both = %q", second.ExternalID)
	}
	if reference.ExternalEffects() != 2 {
		t.Fatalf("external effects = %d, want 2 (one per distinct key)", reference.ExternalEffects())
	}
	if got := lookupReference(t, reference, "git-publisher/v1", secondKey); !got.Found || got.Result.HeadSHA != strings.Repeat("4", 40) {
		t.Fatalf("lookup of the second key = %+v, want its own result", got)
	}
	if keys := reference.IdempotencyKeys("/v1/git/publish"); len(keys) != 2 || keys[0] != firstKey || keys[1] != secondKey {
		t.Fatalf("publish idempotency keys = %v, want [%s %s]", keys, firstKey, secondKey)
	}
	wantPaths := []string{
		"/v1/git/publish", "/v1/publications/lookup", "/v1/publications/lookup",
		"/v1/git/publish", "/v1/publications/lookup",
	}
	if paths := reference.Paths(); !slices.Equal(paths, wantPaths) {
		t.Fatalf("request log = %v, want %v", paths, wantPaths)
	}
}

// TestReferenceServerBlindLookupFaultLagsTheWriteSide pins the eventual
// consistency adversary Task 6 bends: the write is durable, only the read side
// lies, so clearing the fault reveals the result that was there all along.
func TestReferenceServerBlindLookupFaultLagsTheWriteSide(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{
		Fault: contracttest.Fault{BlindLookup: true},
	})
	key := "sha256:" + strings.Repeat("e", 64)

	if status, _ := publishToReference(t, reference, "/v1/git/publish", key, strings.Repeat("5", 40)); status != http.StatusOK {
		t.Fatalf("publish under a blind lookup = %d, want 200", status)
	}
	if blind := lookupReference(t, reference, "git-publisher/v1", key); blind.Found {
		t.Fatalf("BlindLookup must answer found:false even after the write landed, got %+v", blind.Result)
	}

	reference.SetFault(contracttest.Fault{})
	sighted := lookupReference(t, reference, "git-publisher/v1", key)
	if !sighted.Found || sighted.Result.HeadSHA != strings.Repeat("5", 40) {
		t.Fatalf("clearing the fault must reveal the durable result, got %+v", sighted)
	}
	if reference.ExternalEffects() != 1 {
		t.Fatalf("external effects = %d, want 1: a blind lookup hides a write, it does not lose one", reference.ExternalEffects())
	}
}

// TestReferenceServerFailAfterFirstPublishKeepsTheLandedWrite pins the crash
// adversary Task 7 bends: the caller never learns the answer, but the effect
// happened, so both lookup and a repeated publish must return that same result
// without a second external effect.
func TestReferenceServerFailAfterFirstPublishKeepsTheLandedWrite(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{
		Fault: contracttest.Fault{FailAfterFirstPublish: http.StatusBadGateway},
	})
	key := "sha256:" + strings.Repeat("f", 64)

	if status, _ := publishToReference(t, reference, "/v1/work-items/publish", key, ""); status != http.StatusBadGateway {
		t.Fatalf("faulted publish = %d, want 502", status)
	}
	if reference.ExternalEffects() != 1 {
		t.Fatalf("external effects = %d, want 1: the write landed before the injected failure", reference.ExternalEffects())
	}
	recovered := lookupReference(t, reference, "work-item-publisher/v1", key)
	if !recovered.Found || recovered.Result.ExternalID != "publication/1" {
		t.Fatalf("lookup after the injected failure = %+v, want the landed result", recovered)
	}
	// A work-item result never carries head_sha (agent/publisher/gateway.go:829-832).
	if recovered.Result.HeadSHA != "" {
		t.Fatalf("work-item result carries head_sha %q", recovered.Result.HeadSHA)
	}

	status, retried := publishToReference(t, reference, "/v1/work-items/publish", key, "")
	if status != http.StatusOK {
		t.Fatalf("retried publish = %d, want 200: the fault fires once", status)
	}
	if retried != recovered.Result {
		t.Fatalf("retried publish result = %+v, want the recovered result %+v", retried, recovered.Result)
	}
	if reference.PublishRequests() != 2 || reference.ExternalEffects() != 1 {
		t.Fatalf("requests/effects = %d/%d, want 2/1", reference.PublishRequests(), reference.ExternalEffects())
	}
}

type referenceResultBody struct {
	ExternalID string `json:"external_id"`
	URL        string `json:"url"`
	HeadSHA    string `json:"head_sha"`
}

type referenceLookupBody struct {
	Found  bool                `json:"found"`
	Result referenceResultBody `json:"result"`
}

func publishToReference(t *testing.T, reference *contracttest.ReferenceServer, path, operationKey, resultSHA string) (int, referenceResultBody) {
	t.Helper()
	body, contentType := multipartPublish(t, operationKey, resultSHA)
	request, err := http.NewRequest(http.MethodPost, reference.Server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer mounted-token")
	request.Header.Set("Idempotency-Key", operationKey)
	response, err := referenceClient(t, reference).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var result referenceResultBody
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode, result
}

func lookupReference(t *testing.T, reference *contracttest.ReferenceServer, publisher, operationKey string) referenceLookupBody {
	t.Helper()
	response := referencePost(t, reference, "/v1/publications/lookup", "mounted-token", operationKey,
		`{"publisher":"`+publisher+`","operation_key":"`+operationKey+`"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("lookup = %d, want 200", response.StatusCode)
	}
	var body referenceLookupBody
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func multipartPublish(t *testing.T, operationKey, resultSHA string) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	operationHeader := make(textproto.MIMEHeader)
	operationHeader.Set("Content-Disposition", `form-data; name="operation"`)
	operationHeader.Set("Content-Type", "application/json")
	operationPart, err := writer.CreatePart(operationHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operationPart.Write([]byte(`{"operation_key":"` + operationKey + `","destination":"git.example/acme/widget","result_sha":"` + resultSHA + `"}`)); err != nil {
		t.Fatal(err)
	}
	contentHeader := make(textproto.MIMEHeader)
	contentHeader.Set("Content-Disposition", `form-data; name="snapshot"; filename="snapshot.tar"`)
	contentHeader.Set("Content-Type", "application/x-tar")
	contentPart, err := writer.CreatePart(contentHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contentPart.Write([]byte("snapshot bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes(), writer.FormDataContentType()
}
