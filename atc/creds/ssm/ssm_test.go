package ssm_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/creds"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	. "github.com/concourse/concourse/atc/creds/ssm"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("Ssm", func() {
	var ssmAccess *Ssm
	var variables vars.Variables
	var varRef vars.Reference
	var awsSsm *fakeAWS

	JustBeforeEach(func() {
		varRef = vars.Reference{Path: "cheery"}
		t1, err := creds.BuildSecretTemplate("t1", DefaultPipelineSecretTemplate)
		Expect(t1).NotTo(BeNil())
		Expect(err).To(BeNil())
		t2, err := creds.BuildSecretTemplate("t2", DefaultTeamSecretTemplate)
		Expect(t2).NotTo(BeNil())
		Expect(err).To(BeNil())

		awsSsm = startFakeAWS()
		awsSsm.getParameter(func(name string) (*ssm.GetParameterOutput, error) {
			if name == "/concourse/alpha/bogus/cheery" {
				val := "ssm decrypted value"
				return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: &val}}, nil
			}
			return nil, &types.ParameterNotFound{}
		})

		awsSsm.getParametersByPath(func(path string) (*ssm.GetParametersByPathOutput, error) {
			return &ssm.GetParametersByPathOutput{}, nil
		})

		ssmAccess = NewSsm(lagertest.NewTestLogger("ssm_test"), awsSsm.client, []*creds.SecretTemplate{t1, t2}, "/concourse/shared")

		variables = creds.NewVariables(ssmAccess, creds.SecretLookupParams{Team: "alpha", Pipeline: "bogus"}, false)
		Expect(ssmAccess).NotTo(BeNil())
	})

	Describe("Get()", func() {
		It("should get parameter if exists", func() {
			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("ssm decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("should get complex parameter", func() {
			awsSsm.getParametersByPath(func(path string) (*ssm.GetParametersByPathOutput, error) {
				return &ssm.GetParametersByPathOutput{Parameters: []types.Parameter{
					{Name: aws.String("/concourse/alpha/bogus/user/name"), Value: aws.String("yours")},
					{Name: aws.String("/concourse/alpha/bogus/user/pass"), Value: aws.String("truely")},
				}}, nil
			})

			value, found, err := variables.Get(vars.Reference{Path: "user"})
			Expect(value).To(BeEquivalentTo(map[string]any{
				"name": "yours",
				"pass": "truely",
			}))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("should return numbers as strings", func() {
			awsSsm.getParameter(func(name string) (*ssm.GetParameterOutput, error) {
				return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String("101")}}, nil
			})

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("101"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("should get team parameter if exists", func() {
			awsSsm.getParameter(func(name string) (*ssm.GetParameterOutput, error) {
				if name != "/concourse/alpha/bogus/cheery" {
					return nil, &types.ParameterNotFound{}
				}
				return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String("team decrypted value")}}, nil
			})

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("team decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("should get shared parameter if exists", func() {
			awsSsm.getParameter(func(name string) (*ssm.GetParameterOutput, error) {
				if name != "/concourse/shared/cheery" {
					return nil, &types.ParameterNotFound{}
				}
				return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String("shared decrypted value")}}, nil
			})

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("shared decrypted value"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})

		It("should return not found on error", func() {
			awsSsm.getParameter(func(name string) (*ssm.GetParameterOutput, error) {
				return nil, fmt.Errorf("some error")
			})

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeNil())
			Expect(found).To(BeFalse())
			Expect(err).NotTo(BeNil())
		})

		It("should allow empty pipeline name", func() {
			variables := creds.NewVariables(ssmAccess, creds.SecretLookupParams{Team: "alpha", Pipeline: ""}, false)
			awsSsm.getParameter(func(name string) (*ssm.GetParameterOutput, error) {
				Expect(name).To(Equal("/concourse/alpha/cheery"))
				return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String("team power")}}, nil
			})

			value, found, err := variables.Get(varRef)
			Expect(value).To(BeEquivalentTo("team power"))
			Expect(found).To(BeTrue())
			Expect(err).To(BeNil())
		})
	})
})

// fakeAWS speaks the AWS JSON 1.1 wire protocol so that the specs drive a real
// *ssm.Client, rather than a stand-in for one.
type fakeAWS struct {
	client *ssm.Client

	mutex          sync.Mutex
	parameter      func(name string) (*ssm.GetParameterOutput, error)
	parametersPath func(path string) (*ssm.GetParametersByPathOutput, error)
}

func startFakeAWS() *fakeAWS {
	f := new(fakeAWS)

	server := httptest.NewServer(f)
	DeferCleanup(server.Close)

	f.client = ssm.New(ssm.Options{
		Region:       "test-region",
		Credentials:  credentials.NewStaticCredentialsProvider("access-key", "secret-key", ""),
		BaseEndpoint: aws.String(server.URL),
		Retryer:      aws.NopRetryer{},
	})

	return f
}

func (f *fakeAWS) getParameter(fn func(name string) (*ssm.GetParameterOutput, error)) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.parameter = fn
}

func (f *fakeAWS) getParametersByPath(fn func(path string) (*ssm.GetParametersByPathOutput, error)) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.parametersPath = fn
}

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer GinkgoRecover()

	f.mutex.Lock()
	parameter, parametersPath := f.parameter, f.parametersPath
	f.mutex.Unlock()

	switch target := r.Header.Get("X-Amz-Target"); target {
	case "AmazonSSM.GetParameter":
		var input ssm.GetParameterInput
		Expect(json.NewDecoder(r.Body).Decode(&input)).To(Succeed())
		Expect(input.Name).NotTo(BeNil())
		Expect(input.WithDecryption).To(PointTo(Equal(true)))
		output, err := parameter(*input.Name)
		writeAWSResponse(w, output, err)

	case "AmazonSSM.GetParametersByPath":
		var input ssm.GetParametersByPathInput
		Expect(json.NewDecoder(r.Body).Decode(&input)).To(Succeed())
		Expect(input.Path).NotTo(BeNil())
		Expect(input.Recursive).To(PointTo(Equal(true)))
		Expect(input.WithDecryption).To(PointTo(Equal(true)))
		Expect(input.MaxResults).To(PointTo(BeEquivalentTo(10)))
		output, err := parametersPath(*input.Path)
		writeAWSResponse(w, output, err)

	default:
		Fail(fmt.Sprintf("unexpected ssm operation %q", target))
	}
}

func writeAWSResponse(w http.ResponseWriter, output any, err error) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")

	if err == nil {
		Expect(json.NewEncoder(w).Encode(output)).To(Succeed())
		return
	}

	errorType, status := "InternalServerError", http.StatusInternalServerError

	var notFound *types.ParameterNotFound
	if errors.As(err, &notFound) {
		errorType, status = "ParameterNotFound", http.StatusBadRequest
	}

	w.Header().Set("X-Amzn-Errortype", errorType)
	w.WriteHeader(status)
	Expect(json.NewEncoder(w).Encode(map[string]string{"message": err.Error()})).To(Succeed())
}
