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

// secretsManagerService stands in for the AWS Secrets Manager endpoint. The SDK
// client talking to it is the production one, so lookups are serialized,
// signed, sent and deserialized exactly as they are against AWS.
type secretsManagerService struct {
	*httptest.Server

	mu        sync.Mutex
	requested []string
	respond   func(secretID string) (int, any)
}

func newSecretsManagerService() *secretsManagerService {
	service := &secretsManagerService{}
	service.Server = httptest.NewServer(http.HandlerFunc(service.serve))
	return service
}

func (s *secretsManagerService) serve(w http.ResponseWriter, r *http.Request) {
	defer GinkgoRecover()

	Expect(r.Header.Get("X-Amz-Target")).To(Equal("secretsmanager.GetSecretValue"))

	var input struct {
		SecretId string
	}
	Expect(json.NewDecoder(r.Body).Decode(&input)).To(Succeed())

	s.mu.Lock()
	s.requested = append(s.requested, input.SecretId)
	respond := s.respond
	s.mu.Unlock()

	status, body := respond(input.SecretId)

	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	Expect(json.NewEncoder(w).Encode(body)).To(Succeed())
}

func (s *secretsManagerService) Respond(respond func(secretID string) (int, any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.respond = respond
}

func (s *secretsManagerService) RequestedSecretIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requested...)
}

func (s *secretsManagerService) Client() *awssecretsmanager.Client {
	return awssecretsmanager.NewFromConfig(aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("access-key", "secret-key", ""),
		BaseEndpoint: aws.String(s.URL),
		Retryer:      func() aws.Retryer { return aws.NopRetryer{} },
	})
}

func secretString(value string) (int, any) {
	return http.StatusOK, map[string]any{
		"ARN":          "arn:aws:secretsmanager:us-east-1:123456789012:secret:concourse",
		"Name":         "concourse",
		"SecretString": value,
	}
}

func secretBinary(value []byte) (int, any) {
	return http.StatusOK, map[string]any{
		"ARN":          "arn:aws:secretsmanager:us-east-1:123456789012:secret:concourse",
		"Name":         "concourse",
		"SecretBinary": value,
	}
}

func awsError(status int, code string, message string) (int, any) {
	return status, map[string]any{
		"__type":  code,
		"message": message,
	}
}

func notFound() (int, any) {
	return awsError(http.StatusBadRequest, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
}

var _ = Describe("SecretsManager", func() {
	var secretAccess *SecretsManager
	var variables vars.Variables
	var varRef vars.Reference
	var service *secretsManagerService

	BeforeEach(func() {
		service = newSecretsManagerService()
		service.Respond(func(secretID string) (int, any) {
			if secretID == "/concourse/alpha/bogus/cheery" {
				return secretString("secret value")
			}
			return notFound()
		})
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
		It("should get parameter if exists", func() {
			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("secret value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("should get complex parameter", func() {
			service.Respond(func(secretID string) (int, any) {
				return secretBinary([]byte(`{"name": "yours", "pass": "truely"}`))
			})
			value, found, err := variables.Get(vars.Reference{Path: "user"})
			Expect(err).To(BeNil())
			Expect(found).To(BeTrue())
			Expect(value).To(BeEquivalentTo(map[string]any{
				"name": "yours",
				"pass": "truely",
			}))
		})

		It("should get json string parameter", func() {
			service.Respond(func(secretID string) (int, any) {
				return secretString(`{"name": "yours", "pass": "truely"}`)
			})
			value, found, err := variables.Get(vars.Reference{Path: "user"})
			Expect(err).To(BeNil())
			Expect(found).To(BeTrue())
			Expect(value).To(BeEquivalentTo(map[string]any{
				"name": "yours",
				"pass": "truely",
			}))
		})

		It("should get team parameter if exists", func() {
			service.Respond(func(secretID string) (int, any) {
				if secretID != "/concourse/alpha/cheery" {
					return notFound()
				}
				return secretString("team decrypted value")
			})
			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("team decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("should return shared parameter if exists", func() {
			service.Respond(func(secretID string) (int, any) {
				if secretID != "/concourse/cheery" {
					return notFound()
				}
				return secretString("shared decrypted value")
			})
			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("shared decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("should return not found on error", func() {
			service.Respond(func(secretID string) (int, any) {
				return awsError(http.StatusInternalServerError, "InternalServiceError", "some error")
			})
			value, found, err := variables.Get(varRef)
			Expect(value).To(BeNil())
			Expect(found).To(BeFalse())
			Expect(err).NotTo(BeNil())
		})

		It("should allow empty pipeline name", func() {
			variables := creds.NewVariables(secretAccess, creds.SecretLookupParams{Team: "alpha", Pipeline: ""}, false)
			service.Respond(func(secretID string) (int, any) {
				return secretString("team power")
			})
			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("team power"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
			Expect(service.RequestedSecretIDs()).To(Equal([]string{"/concourse/alpha/cheery"}))
		})

		It("should treat marked for deletion as deleted", func() {
			service.Respond(func(secretID string) (int, any) {
				return awsError(http.StatusBadRequest, "InvalidRequestException", "secret is scheduled for deletion")
			})
			value, found, err := variables.Get(varRef)
			Expect(value).To(BeNil())
			Expect(found).To(BeFalse())
			Expect(err).To(BeNil())
		})
	})
})
