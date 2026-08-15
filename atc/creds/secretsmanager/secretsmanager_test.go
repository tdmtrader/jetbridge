package secretsmanager_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssecretsmanager "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/vars"

	. "github.com/concourse/concourse/atc/creds/secretsmanager"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type secretsManagerSecret struct {
	stringValue          string
	binaryValue          []byte
	binary               bool
	scheduledForDeletion bool
}

type secretsManagerAvailability struct {
	available bool
	status    int
	errorCode string
	message   string
}

type secretsManagerRequest struct {
	secretID string
}

// secretsManagerService speaks AWS JSON 1.1 to a production SDK client while
// exposing only service state and immutable protocol requests to the specs.
type secretsManagerService struct {
	*httptest.Server

	mu           sync.Mutex
	secrets      map[string]secretsManagerSecret
	availability secretsManagerAvailability
	requests     []secretsManagerRequest
}

func newSecretsManagerService() *secretsManagerService {
	service := &secretsManagerService{
		secrets:      map[string]secretsManagerSecret{},
		availability: secretsManagerAvailability{available: true},
	}
	service.Server = httptest.NewServer(http.HandlerFunc(service.serve))
	return service
}

func (service *secretsManagerService) serve(w http.ResponseWriter, r *http.Request) {
	defer GinkgoRecover()

	Expect(r.Method).To(Equal(http.MethodPost))
	target := r.Header.Get("X-Amz-Target")
	mediaType := r.Header.Get("Content-Type")
	authorization := r.Header.Get("Authorization")
	Expect(target).To(Equal("secretsmanager.GetSecretValue"))
	Expect(mediaType).To(ContainSubstring("application/x-amz-json-1.1"))
	Expect(authorization).To(ContainSubstring("AWS4-HMAC-SHA256 Credential=access-key/"))
	Expect(authorization).To(ContainSubstring("/secretsmanager/aws4_request"))

	var input struct {
		SecretID string `json:"SecretId"`
	}
	Expect(json.NewDecoder(r.Body).Decode(&input)).To(Succeed())

	service.mu.Lock()
	service.requests = append(service.requests, secretsManagerRequest{
		secretID: input.SecretID,
	})
	availability := service.availability
	secret, found := service.secrets[input.SecretID]
	secret.binaryValue = append([]byte(nil), secret.binaryValue...)
	service.mu.Unlock()

	switch {
	case !availability.available:
		writeSecretsManagerError(w, availability.status, availability.errorCode, availability.message)
	case !found:
		writeSecretsManagerError(w, http.StatusBadRequest, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
	case secret.scheduledForDeletion:
		writeSecretsManagerError(w, http.StatusBadRequest, "InvalidRequestException", "secret is scheduled for deletion")
	case secret.binary:
		writeSecretsManagerSecret(w, map[string]any{"SecretBinary": secret.binaryValue})
	default:
		writeSecretsManagerSecret(w, map[string]any{"SecretString": secret.stringValue})
	}
}

func writeSecretsManagerSecret(w http.ResponseWriter, value map[string]any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	value["ARN"] = "arn:aws:secretsmanager:us-east-1:123456789012:secret:concourse"
	value["Name"] = "concourse"
	Expect(json.NewEncoder(w).Encode(value)).To(Succeed())
}

func writeSecretsManagerError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	Expect(json.NewEncoder(w).Encode(map[string]any{
		"__type":  code,
		"message": message,
	})).To(Succeed())
}

func (service *secretsManagerService) clearSecrets() {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.secrets = map[string]secretsManagerSecret{}
}

func (service *secretsManagerService) putString(secretID, value string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.secrets[secretID] = secretsManagerSecret{stringValue: value}
}

func (service *secretsManagerService) putBinary(secretID string, value []byte) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.secrets[secretID] = secretsManagerSecret{binary: true, binaryValue: append([]byte(nil), value...)}
}

func (service *secretsManagerService) scheduleDeletion(secretID string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.secrets[secretID] = secretsManagerSecret{scheduledForDeletion: true}
}

func (service *secretsManagerService) setUnavailable(status int, code, message string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.availability = secretsManagerAvailability{
		status:    status,
		errorCode: code,
		message:   message,
	}
}

func (service *secretsManagerService) requestsSnapshot() []secretsManagerRequest {
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]secretsManagerRequest(nil), service.requests...)
}

func (service *secretsManagerService) requestedSecretIDs() []string {
	requests := service.requestsSnapshot()
	secretIDs := make([]string, len(requests))
	for i, request := range requests {
		secretIDs[i] = request.secretID
	}
	return secretIDs
}

func (service *secretsManagerService) Client() *awssecretsmanager.Client {
	return awssecretsmanager.NewFromConfig(aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("access-key", "secret-key", ""),
		BaseEndpoint: aws.String(service.URL),
		Retryer:      func() aws.Retryer { return aws.NopRetryer{} },
	})
}

var _ = Describe("SecretsManager", func() {
	var secretAccess *SecretsManager
	var variables vars.Variables
	var varRef vars.Reference
	var service *secretsManagerService

	BeforeEach(func() {
		service = newSecretsManagerService()
		service.putString("/concourse/alpha/bogus/cheery", "secret value")
	})

	AfterEach(func() {
		service.Close()
	})

	JustBeforeEach(func() {
		varRef = vars.Reference{Path: "cheery"}
		t1, err := creds.BuildSecretTemplate("t1", DefaultPipelineSecretTemplate)
		Expect(t1).NotTo(BeNil())
		Expect(err).To(BeNil())
		t2, err := creds.BuildSecretTemplate("t2", DefaultTeamSecretTemplate)
		Expect(t2).NotTo(BeNil())
		Expect(err).To(BeNil())
		t3, err := creds.BuildSecretTemplate("t3", DefaultSharedSecretTemplate)
		Expect(t3).NotTo(BeNil())
		Expect(err).To(BeNil())

		secretAccess = NewSecretsManager(lagertest.NewTestLogger("secretsmanager_test"), service.Client(), []*creds.SecretTemplate{t1, t2, t3})
		variables = creds.NewVariables(secretAccess, creds.SecretLookupParams{Team: "alpha", Pipeline: "bogus"}, false)
		Expect(secretAccess).NotTo(BeNil())
	})

	Describe("Get()", func() {
		It("gets the pipeline secret before lower-precedence paths", func() {
			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("secret value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
			Expect(service.requestedSecretIDs()).To(Equal([]string{"/concourse/alpha/bogus/cheery"}))
		})

		It("decodes a binary JSON secret", func() {
			service.putBinary("/concourse/alpha/bogus/user", []byte(`{"name": "yours", "pass": "truely"}`))

			value, found, err := variables.Get(vars.Reference{Path: "user"})
			Expect(err).To(BeNil())
			Expect(found).To(BeTrue())
			Expect(value).To(BeEquivalentTo(map[string]any{
				"name": "yours",
				"pass": "truely",
			}))
		})

		It("decodes a JSON string secret", func() {
			service.putString("/concourse/alpha/bogus/user", `{"name": "yours", "pass": "truely"}`)

			value, found, err := variables.Get(vars.Reference{Path: "user"})
			Expect(err).To(BeNil())
			Expect(found).To(BeTrue())
			Expect(value).To(BeEquivalentTo(map[string]any{
				"name": "yours",
				"pass": "truely",
			}))
		})

		It("falls back from pipeline to team", func() {
			service.clearSecrets()
			service.putString("/concourse/alpha/cheery", "team decrypted value")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("team decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
			Expect(service.requestedSecretIDs()).To(Equal([]string{
				"/concourse/alpha/bogus/cheery",
				"/concourse/alpha/cheery",
			}))
		})

		It("falls back from pipeline and team to shared", func() {
			service.clearSecrets()
			service.putString("/concourse/cheery", "shared decrypted value")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("shared decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
			Expect(service.requestedSecretIDs()).To(Equal([]string{
				"/concourse/alpha/bogus/cheery",
				"/concourse/alpha/cheery",
				"/concourse/cheery",
			}))
		})

		It("returns not found when no lookup path contains the secret", func() {
			service.clearSecrets()

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeNil())
			Expect(found).To(BeFalse())
			Expect(err).To(BeNil())
		})

		It("returns an unavailable service error", func() {
			service.setUnavailable(http.StatusServiceUnavailable, "ServiceUnavailableException", "Secrets Manager is unavailable")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeNil())
			Expect(found).To(BeFalse())
			Expect(err).To(HaveOccurred())
		})

		It("uses only the team path when the pipeline name is empty", func() {
			variables := creds.NewVariables(secretAccess, creds.SecretLookupParams{Team: "alpha", Pipeline: ""}, false)
			service.clearSecrets()
			service.putString("/concourse/alpha/cheery", "team power")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("team power"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
			Expect(service.requestedSecretIDs()).To(Equal([]string{"/concourse/alpha/cheery"}))
		})

		It("treats a secret scheduled for deletion as absent", func() {
			service.clearSecrets()
			service.scheduleDeletion("/concourse/alpha/bogus/cheery")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeNil())
			Expect(found).To(BeFalse())
			Expect(err).To(BeNil())
		})

		It("serves concurrent signed lookups from stable secret state", func() {
			lookups := []struct {
				path string
				want string
			}{
				{path: "/concurrent/alpha", want: "first"},
				{path: "/concurrent/bravo", want: "second"},
				{path: "/concurrent/charlie", want: "third"},
				{path: "/concurrent/delta", want: "fourth"},
			}
			service.clearSecrets()
			for _, lookup := range lookups {
				service.putString(lookup.path, lookup.want)
			}

			type lookupResult struct {
				path  string
				value any
				found bool
				err   error
			}
			results := make(chan lookupResult, len(lookups))
			for _, lookup := range lookups {
				go func(path string) {
					value, _, found, err := secretAccess.Get(path)
					results <- lookupResult{path: path, value: value, found: found, err: err}
				}(lookup.path)
			}

			expected := map[string]string{
				"/concurrent/alpha":   "first",
				"/concurrent/bravo":   "second",
				"/concurrent/charlie": "third",
				"/concurrent/delta":   "fourth",
			}
			for range lookups {
				var result lookupResult
				Eventually(results).Should(Receive(&result))
				Expect(result.err).NotTo(HaveOccurred())
				Expect(result.found).To(BeTrue())
				Expect(result.value).To(Equal(expected[result.path]))
			}
		})
	})
})
