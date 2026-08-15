package ssm_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/concourse/concourse/atc/creds"
	. "github.com/concourse/concourse/atc/creds/ssm"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type ssmServiceAvailability struct {
	available bool
	status    int
	errorType string
	message   string
}

type ssmParameterPage struct {
	requestToken string
	nextToken    string
	parameters   map[string]string
}

type ssmRequest struct {
	target    string
	name      string
	path      string
	nextToken string
}

// ssmProtocolService speaks AWS JSON 1.1 to a production SDK client and owns
// deterministic Parameter Store state, pagination, and immutable requests.
type ssmProtocolService struct {
	client *ssm.Client
	server *httptest.Server

	mu           sync.Mutex
	parameters   map[string]string
	pathPages    map[string][]ssmParameterPage
	availability ssmServiceAvailability
	requests     []ssmRequest
}

func startSSMProtocolService() *ssmProtocolService {
	service := &ssmProtocolService{
		parameters:   map[string]string{},
		pathPages:    map[string][]ssmParameterPage{},
		availability: ssmServiceAvailability{available: true},
	}
	service.server = httptest.NewServer(service)
	DeferCleanup(service.server.Close)

	service.client = ssm.New(ssm.Options{
		Region:       "test-region",
		Credentials:  credentials.NewStaticCredentialsProvider("access-key", "secret-key", ""),
		BaseEndpoint: aws.String(service.server.URL),
		Retryer:      aws.NopRetryer{},
	})

	return service
}

func (service *ssmProtocolService) putParameter(name, value string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.parameters[name] = value
}

func (service *ssmProtocolService) clearValues() {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.parameters = map[string]string{}
	service.pathPages = map[string][]ssmParameterPage{}
}

func (service *ssmProtocolService) setParameterPages(path string, pages ...ssmParameterPage) {
	service.mu.Lock()
	defer service.mu.Unlock()

	copiedPages := make([]ssmParameterPage, len(pages))
	for i, page := range pages {
		copiedPages[i] = ssmParameterPage{
			requestToken: page.requestToken,
			nextToken:    page.nextToken,
			parameters:   make(map[string]string, len(page.parameters)),
		}
		for name, value := range page.parameters {
			copiedPages[i].parameters[name] = value
		}
	}
	service.pathPages[path] = copiedPages
}

func (service *ssmProtocolService) setUnavailable(status int, errorType, message string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.availability = ssmServiceAvailability{
		status:    status,
		errorType: errorType,
		message:   message,
	}
}

func (service *ssmProtocolService) requestsSnapshot() []ssmRequest {
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]ssmRequest(nil), service.requests...)
}

func (service *ssmProtocolService) requestKeys() []string {
	requests := service.requestsSnapshot()
	keys := make([]string, len(requests))
	for i, request := range requests {
		switch request.target {
		case "AmazonSSM.GetParameter":
			keys[i] = "parameter:" + request.name
		case "AmazonSSM.GetParametersByPath":
			keys[i] = "path:" + request.path + "?token=" + request.nextToken
		}
	}
	return keys
}

func (service *ssmProtocolService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer GinkgoRecover()

	Expect(r.Method).To(Equal(http.MethodPost))
	target := r.Header.Get("X-Amz-Target")
	mediaType := r.Header.Get("Content-Type")
	authorization := r.Header.Get("Authorization")
	Expect(mediaType).To(ContainSubstring("application/x-amz-json-1.1"))
	Expect(authorization).To(ContainSubstring("AWS4-HMAC-SHA256 Credential=access-key/"))
	Expect(authorization).To(ContainSubstring("/ssm/aws4_request"))

	switch target {
	case "AmazonSSM.GetParameter":
		service.serveGetParameter(w, r, target, mediaType, authorization)
	case "AmazonSSM.GetParametersByPath":
		service.serveGetParametersByPath(w, r, target, mediaType, authorization)
	default:
		Fail(fmt.Sprintf("unexpected ssm operation %q", target))
	}
}

func (service *ssmProtocolService) serveGetParameter(w http.ResponseWriter, r *http.Request, target, mediaType, authorization string) {
	var input ssm.GetParameterInput
	Expect(json.NewDecoder(r.Body).Decode(&input)).To(Succeed())
	Expect(input.Name).NotTo(BeNil())
	Expect(input.WithDecryption).NotTo(BeNil())

	request := ssmRequest{
		target: target,
		name:   aws.ToString(input.Name),
	}
	Expect(aws.ToBool(input.WithDecryption)).To(BeTrue())

	service.mu.Lock()
	service.requests = append(service.requests, request)
	availability := service.availability
	value, found := service.parameters[request.name]
	service.mu.Unlock()

	if !availability.available {
		writeSSMError(w, availability.status, availability.errorType, availability.message)
		return
	}
	if !found {
		writeSSMError(w, http.StatusBadRequest, "ParameterNotFound", "parameter not found")
		return
	}

	writeSSMResponse(w, &ssm.GetParameterOutput{Parameter: &types.Parameter{
		ARN:      aws.String("arn:aws:ssm:test-region:123456789012:parameter" + request.name),
		DataType: aws.String("text"),
		Name:     aws.String(request.name),
		Type:     types.ParameterTypeString,
		Value:    aws.String(value),
		Version:  1,
	}})
}

func (service *ssmProtocolService) serveGetParametersByPath(w http.ResponseWriter, r *http.Request, target, mediaType, authorization string) {
	var input ssm.GetParametersByPathInput
	Expect(json.NewDecoder(r.Body).Decode(&input)).To(Succeed())
	Expect(input.Path).NotTo(BeNil())
	Expect(input.Recursive).NotTo(BeNil())
	Expect(input.WithDecryption).NotTo(BeNil())
	Expect(input.MaxResults).NotTo(BeNil())

	request := ssmRequest{
		target:    target,
		path:      aws.ToString(input.Path),
		nextToken: aws.ToString(input.NextToken),
	}
	Expect(aws.ToBool(input.WithDecryption)).To(BeTrue())
	Expect(aws.ToBool(input.Recursive)).To(BeTrue())
	Expect(aws.ToInt32(input.MaxResults)).To(BeEquivalentTo(10))

	service.mu.Lock()
	service.requests = append(service.requests, request)
	availability := service.availability
	page := service.pageForRequest(request.path, request.nextToken)
	service.mu.Unlock()

	if !availability.available {
		writeSSMError(w, availability.status, availability.errorType, availability.message)
		return
	}

	output := &ssm.GetParametersByPathOutput{}
	for name, value := range page.parameters {
		output.Parameters = append(output.Parameters, types.Parameter{
			ARN:      aws.String("arn:aws:ssm:test-region:123456789012:parameter" + name),
			DataType: aws.String("text"),
			Name:     aws.String(name),
			Type:     types.ParameterTypeString,
			Value:    aws.String(value),
			Version:  1,
		})
	}
	if page.nextToken != "" {
		output.NextToken = aws.String(page.nextToken)
	}
	writeSSMResponse(w, output)
}

func (service *ssmProtocolService) pageForRequest(path, token string) ssmParameterPage {
	for _, page := range service.pathPages[path] {
		if page.requestToken == token {
			copied := ssmParameterPage{
				requestToken: page.requestToken,
				nextToken:    page.nextToken,
				parameters:   make(map[string]string, len(page.parameters)),
			}
			for name, value := range page.parameters {
				copied.parameters[name] = value
			}
			return copied
		}
	}
	return ssmParameterPage{parameters: map[string]string{}}
}

func writeSSMResponse(w http.ResponseWriter, output any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	Expect(json.NewEncoder(w).Encode(output)).To(Succeed())
}

func writeSSMError(w http.ResponseWriter, status int, errorType, message string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("X-Amzn-Errortype", errorType)
	w.WriteHeader(status)
	Expect(json.NewEncoder(w).Encode(map[string]string{
		"__type":  errorType,
		"message": message,
	})).To(Succeed())
}

var _ = Describe("Ssm", func() {
	var ssmAccess *Ssm
	var variables vars.Variables
	var varRef vars.Reference
	var service *ssmProtocolService

	JustBeforeEach(func() {
		varRef = vars.Reference{Path: "cheery"}
		t1, err := creds.BuildSecretTemplate("t1", DefaultPipelineSecretTemplate)
		Expect(t1).NotTo(BeNil())
		Expect(err).To(BeNil())
		t2, err := creds.BuildSecretTemplate("t2", DefaultTeamSecretTemplate)
		Expect(t2).NotTo(BeNil())
		Expect(err).To(BeNil())

		service = startSSMProtocolService()
		service.putParameter("/concourse/alpha/bogus/cheery", "ssm decrypted value")

		ssmAccess = NewSsm(lagertest.NewTestLogger("ssm_test"), service.client, []*creds.SecretTemplate{t1, t2}, "/concourse/shared")
		variables = creds.NewVariables(ssmAccess, creds.SecretLookupParams{Team: "alpha", Pipeline: "bogus"}, false)
		Expect(ssmAccess).NotTo(BeNil())
	})

	Describe("Get()", func() {
		It("gets the pipeline parameter before lower-precedence paths", func() {
			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("ssm decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
			Expect(service.requestKeys()).To(Equal([]string{"parameter:/concourse/alpha/bogus/cheery"}))
		})

		It("assembles a complex parameter from all paginated path data", func() {
			service.setParameterPages("/concourse/alpha/bogus/user",
				ssmParameterPage{
					nextToken: "second-page",
					parameters: map[string]string{
						"/concourse/alpha/bogus/user/name": "yours",
					},
				},
				ssmParameterPage{
					requestToken: "second-page",
					parameters: map[string]string{
						"/concourse/alpha/bogus/user/pass": "truely",
					},
				},
			)

			value, found, err := variables.Get(vars.Reference{Path: "user"})
			Expect(value).To(BeEquivalentTo(map[string]any{
				"name": "yours",
				"pass": "truely",
			}))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
			Expect(service.requestKeys()).To(Equal([]string{
				"parameter:/concourse/alpha/bogus/user",
				"path:/concourse/alpha/bogus/user?token=",
				"path:/concourse/alpha/bogus/user?token=second-page",
			}))
		})

		It("returns numbers as strings", func() {
			service.putParameter("/concourse/alpha/bogus/cheery", "101")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("101"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("falls back from pipeline to team", func() {
			service.clearValues()
			service.putParameter("/concourse/alpha/cheery", "team decrypted value")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("team decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
			Expect(service.requestKeys()).To(Equal([]string{
				"parameter:/concourse/alpha/bogus/cheery",
				"path:/concourse/alpha/bogus/cheery?token=",
				"parameter:/concourse/alpha/cheery",
			}))
		})

		It("falls back from pipeline and team to shared", func() {
			service.clearValues()
			service.putParameter("/concourse/shared/cheery", "shared decrypted value")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("shared decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("returns not found when no lookup path contains the parameter", func() {
			service.clearValues()

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeNil())
			Expect(found).To(BeFalse())
			Expect(err).To(BeNil())
		})

		It("returns an unavailable service error", func() {
			service.setUnavailable(http.StatusServiceUnavailable, "ServiceUnavailable", "Parameter Store is unavailable")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeNil())
			Expect(found).To(BeFalse())
			Expect(err).To(HaveOccurred())
		})

		It("uses only the team path when the pipeline name is empty", func() {
			variables := creds.NewVariables(ssmAccess, creds.SecretLookupParams{Team: "alpha", Pipeline: ""}, false)
			service.clearValues()
			service.putParameter("/concourse/alpha/cheery", "team power")

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("team power"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
			Expect(service.requestKeys()).To(Equal([]string{"parameter:/concourse/alpha/cheery"}))
		})

		It("serves concurrent signed lookups from stable parameter state", func() {
			lookups := []struct {
				path string
				want string
			}{
				{path: "/concurrent/alpha", want: "first"},
				{path: "/concurrent/bravo", want: "second"},
				{path: "/concurrent/charlie", want: "third"},
				{path: "/concurrent/delta", want: "fourth"},
			}
			service.clearValues()
			for _, lookup := range lookups {
				service.putParameter(lookup.path, lookup.want)
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
					value, _, found, err := ssmAccess.Get(path)
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
