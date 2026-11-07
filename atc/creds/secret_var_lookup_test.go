package creds_test

import (
	"fmt"
	"path"
	"time"

	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VariableLookupFromSecrets", func() {
	var (
		variables vars.Variables
	)

	BeforeEach(func() {
		secrets := dummy.NewSecretsFactory([]dummy.VarFlag{
			{
				Name: "a",
				Value: map[string]any{
					"b": map[any]any{
						"c": "foo",
					},
				},
			},
		}).NewSecrets()
		variables = creds.NewVariables(secrets, creds.SecretLookupParams{Team: "team", Pipeline: "pipeline"}, true)
	})

	Describe("Get", func() {
		It("traverses fields", func() {
			result, found, err := variables.Get(vars.Reference{Path: "a", Fields: []string{"b", "c"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(result).To(Equal("foo"))
		})

		// dummy's lookup keys are its prefixes joined with the var name, so
		// a key that spells the climb would have answered it.
		It("refuses a quoted '..' before reading the dummy's vars", func() {
			secrets := dummy.NewSecretsFactory([]dummy.VarFlag{
				{Name: "team/pipeline/../../outside", Value: "stolen"},
				{Name: "team/pipeline/foo/bar", Value: "slashed"},
			}).NewSecrets()
			variables := creds.NewVariables(secrets, creds.SecretLookupParams{Team: "team", Pipeline: "pipeline"}, true)

			_, found, err := variables.Get(vars.Reference{Path: "../../outside"})
			Expect(err).To(MatchError(ContainSubstring("path segment '..' is not allowed")))
			Expect(found).To(BeFalse())

			val, found, err := variables.Get(vars.Reference{Path: "foo/bar"})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("slashed"))
		})

		Context("when a field is missing", func() {
			It("errors", func() {
				_, _, err := variables.Get(vars.Reference{Path: "a", Fields: []string{"b", "d"}})
				Expect(err).To(HaveOccurred())
			})
		})
	})
})

// recordingSecrets answers every path in found and records every path it
// was asked for, so a spec can prove a refused lookup never reached the
// backend.
type recordingSecrets struct {
	lookupPaths func(team, pipeline string) []creds.SecretLookupPath
	found       map[string]any
	asked       []string
}

func (s *recordingSecrets) Get(path string) (any, *time.Time, bool, error) {
	s.asked = append(s.asked, path)
	val, ok := s.found[path]
	return val, nil, ok, nil
}

// GetSecretRef makes recordingSecrets a SecretRefProvider, so the native
// reference path runs its lookup too.
func (s *recordingSecrets) GetSecretRef(path string, _ []string) (*vars.SecretRef, bool) {
	return &vars.SecretRef{Name: path}, true
}

func (s *recordingSecrets) NewSecretLookupPaths(team, pipeline string, _ bool) []creds.SecretLookupPath {
	return s.lookupPaths(team, pipeline)
}

var _ = Describe("secret paths stay under the team's prefix", func() {
	var (
		secrets *recordingSecrets
		params  creds.SecretLookupParams
	)

	// The vault, ssm, secretsmanager and conjur defaults.
	templateRules := func(team, pipeline string) []creds.SecretLookupPath {
		p, err := creds.BuildSecretTemplate("p", "/concourse/{{.Team}}/{{.Pipeline}}/{{.Secret}}")
		Expect(err).NotTo(HaveOccurred())
		t, err := creds.BuildSecretTemplate("t", "/concourse/{{.Team}}/{{.Secret}}")
		Expect(err).NotTo(HaveOccurred())
		var rules []creds.SecretLookupPath
		for _, tmpl := range []*creds.SecretTemplate{p, t} {
			if rule := creds.NewSecretLookupWithTemplate(tmpl, team, pipeline); rule != nil {
				rules = append(rules, rule)
			}
		}
		return rules
	}

	// The credhub and dummy shape: prefixes built with path.Join, which has
	// already cleaned the team and pipeline names by the time a var arrives.
	joinedPrefixRules := func(team, pipeline string) []creds.SecretLookupPath {
		return []creds.SecretLookupPath{
			creds.NewSecretLookupWithPrefix(path.Join("/concourse", team, pipeline) + "/"),
			creds.NewSecretLookupWithPrefix(path.Join("/concourse", team) + "/"),
		}
	}

	BeforeEach(func() {
		secrets = &recordingSecrets{lookupPaths: templateRules, found: map[string]any{}}
		params = creds.SecretLookupParams{Team: "team", Pipeline: "pipeline"}
	})

	get := func(ref vars.Reference) (any, bool, error) {
		return creds.NewVariables(secrets, params, false).Get(ref)
	}

	It("resolves an ordinary name", func() {
		secrets.found["/concourse/team/pipeline/foo"] = "bar"

		val, found, err := get(vars.Reference{Path: "foo"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(val).To(Equal("bar"))
	})

	It("resolves a name with slashes beneath the prefix", func() {
		secrets.found["/concourse/team/foo/bar"] = "baz"

		val, found, err := get(vars.Reference{Source: "vault", Path: "foo/bar"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(val).To(Equal("baz"))
		Expect(secrets.asked).To(Equal([]string{"/concourse/team/pipeline/foo/bar", "/concourse/team/foo/bar"}))
	})

	It("resolves a name whose segments merely contain dots", func() {
		secrets.found["/concourse/team/pipeline/foo..bar/.baz"] = "qux"

		_, found, err := get(vars.Reference{Path: "foo..bar/.baz"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	DescribeTable("refuses a name with a dot segment before asking the backend",
		func(name, segment string) {
			secrets.found["/concourse/other-team/secret"] = "stolen"
			secrets.found["/concourse/secret"] = "stolen"

			_, found, err := get(vars.Reference{Path: name})
			Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("invalid var '%q'", name))))
			Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("path segment '%s' is not allowed", segment))))
			Expect(found).To(BeFalse())
			Expect(secrets.asked).To(BeEmpty())
		},
		Entry("a quoted parent escape", "../../other-team/secret", ".."),
		Entry("a parent segment mid-path", "x/../../../secret", ".."),
		Entry("a bare parent segment", "..", ".."),
		Entry("a current-directory segment", "./secret", "."),
	)

	It("refuses a name with a dot segment when the backend names no lookup paths", func() {
		secrets.lookupPaths = func(string, string) []creds.SecretLookupPath { return nil }

		_, _, err := get(vars.Reference{Path: "../secret"})
		Expect(err).To(MatchError(ContainSubstring("path segment '..' is not allowed")))
		Expect(secrets.asked).To(BeEmpty())
	})

	DescribeTable("refuses a lookup scoped by a dot-segment pipeline or team name",
		func(rules func(string, string) []creds.SecretLookupPath, team, pipeline, field string) {
			secrets.lookupPaths = rules
			params = creds.SecretLookupParams{Team: team, Pipeline: pipeline}
			secrets.found["/concourse/other-team/secret"] = "stolen"

			_, found, err := get(vars.Reference{Path: "other-team/secret"})
			Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("%s name '..' is not a valid secret path segment", field))))
			Expect(found).To(BeFalse())
			Expect(secrets.asked).To(BeEmpty())
		},
		Entry("template rules, pipeline", templateRules, "team", "..", "pipeline"),
		Entry("joined prefix rules, pipeline", joinedPrefixRules, "team", "..", "pipeline"),
		Entry("joined prefix rules, team", joinedPrefixRules, "..", "", "team"),
	)

	It("leaves a refused name without a native secret reference", func() {
		secrets.found["/concourse/team/secret"] = "stolen"
		secrets.found["/concourse/secret"] = "stolen"

		ref, found := creds.NewVariables(secrets, params, false).(creds.VariableLookupFromSecrets).GetSecretRef(vars.Reference{Path: "../secret"})
		Expect(found).To(BeFalse())
		Expect(ref).To(BeNil())
		Expect(secrets.asked).To(BeEmpty())
	})
})
