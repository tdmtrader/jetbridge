package conjur_test

import (
	"errors"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/creds"
	. "github.com/concourse/concourse/atc/creds/conjur"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type conjurSecrets map[string][]byte

func (secrets conjurSecrets) RetrieveSecret(path string) ([]byte, error) {
	value, found := secrets[path]
	if !found {
		return nil, errors.New("secret not found")
	}
	return value, nil
}

var _ = Describe("Conjur", func() {
	var secretAccess *Conjur
	var variables vars.Variables
	var varRef vars.Reference
	var secrets conjurSecrets

	BeforeEach(func() {
		varRef = vars.Reference{Path: "cheery"}
		secrets = conjurSecrets{
			"concourse/alpha/bogus/cheery": []byte("secret value"),
		}
	})

	JustBeforeEach(func() {
		pipelineTemplate, err := creds.BuildSecretTemplate("pipeline", DefaultPipelineSecretTemplate)
		Expect(err).NotTo(HaveOccurred())
		Expect(pipelineTemplate).NotTo(BeNil())

		teamTemplate, err := creds.BuildSecretTemplate("team", DefaultTeamSecretTemplate)
		Expect(err).NotTo(HaveOccurred())
		Expect(teamTemplate).NotTo(BeNil())

		secretAccess = NewConjur(
			lager.NewLogger("conjur_test"),
			secrets,
			[]*creds.SecretTemplate{pipelineTemplate, teamTemplate},
		)
		variables = creds.NewVariables(
			secretAccess,
			creds.SecretLookupParams{Team: "alpha", Pipeline: "bogus"},
			false,
		)
	})

	Describe("Get()", func() {
		It("gets a pipeline secret", func() {
			value, found, err := variables.Get(varRef)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("secret value"))
		})

		Context("when only the team secret exists", func() {
			BeforeEach(func() {
				secrets = conjurSecrets{
					"concourse/alpha/cheery": []byte("team secret"),
				}
			})

			It("falls back to the team path", func() {
				value, found, err := variables.Get(varRef)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(value).To(Equal("team secret"))
			})
		})

		Context("when the secret does not exist", func() {
			BeforeEach(func() {
				secrets = conjurSecrets{}
			})

			It("returns not found", func() {
				value, found, err := variables.Get(varRef)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(value).To(BeNil())
			})
		})

		Context("when the pipeline name is empty", func() {
			BeforeEach(func() {
				secrets = conjurSecrets{
					"concourse/alpha/cheery": []byte("team secret"),
				}
			})

			It("uses the team path", func() {
				variables := creds.NewVariables(
					secretAccess,
					creds.SecretLookupParams{Team: "alpha"},
					false,
				)

				value, found, err := variables.Get(varRef)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(value).To(Equal("team secret"))
			})
		})

		It("uses the full variable path when no templates are configured", func() {
			secretAccess := NewConjur(
				lager.NewLogger("conjur_test"),
				conjurSecrets{"cheery": []byte("full path secret")},
				nil,
			)
			variables := creds.NewVariables(secretAccess, creds.SecretLookupParams{}, false)

			value, found, err := variables.Get(varRef)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("full path secret"))
		})
	})
})
