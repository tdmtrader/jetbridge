package credhub_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/credhub"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CredHub", func() {
	var (
		requests  atomic.Int32
		variables vars.Variables
	)

	BeforeEach(func() {
		requests.Store(0)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}))
		DeferCleanup(server.Close)

		manager := &credhub.CredHubManager{URL: server.URL, PathPrefix: "/concourse"}
		Expect(manager.Init(lagertest.NewTestLogger("test"))).To(Succeed())
		factory, err := manager.NewSecretsFactory(lagertest.NewTestLogger("test"))
		Expect(err).NotTo(HaveOccurred())

		variables = creds.NewVariables(factory.NewSecrets(), creds.SecretLookupParams{Team: "team", Pipeline: "pipeline"}, false)
	})

	It("refuses a quoted '..' in a var name without contacting CredHub", func() {
		_, found, err := variables.Get(vars.Reference{Path: "../../other-team/secret"})
		Expect(err).To(MatchError(ContainSubstring(`invalid var '"../../other-team/secret"': path segment '..' is not allowed`)))
		Expect(found).To(BeFalse())
		Expect(requests.Load()).To(BeZero())
	})

	It("refuses a lookup scoped by a pipeline named '..' without contacting CredHub", func() {
		variables := variables.(creds.VariableLookupFromSecrets)
		variables = creds.NewVariables(variables.Secrets, creds.SecretLookupParams{Team: "team", Pipeline: ".."}, false).(creds.VariableLookupFromSecrets)

		_, _, err := variables.Get(vars.Reference{Path: "other-team/secret"})
		Expect(err).To(MatchError(ContainSubstring("pipeline name '..' is not a valid secret path segment")))
		Expect(requests.Load()).To(BeZero())
	})

	It("contacts CredHub for a var name with slashes", func() {
		_, _, _ = variables.Get(vars.Reference{Path: "foo/bar"})
		Expect(requests.Load()).NotTo(BeZero())
	})
})
