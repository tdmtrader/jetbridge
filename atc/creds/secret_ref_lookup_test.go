package creds_test

import (
	"time"

	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VariableLookupFromSecrets SecretRefResolver", func() {
	var (
		fakeSecrets *fakeSecretsWithRef
		variables   vars.Variables
	)

	Describe("when Secrets implements SecretRefProvider", func() {
		BeforeEach(func() {
			fakeSecrets = &fakeSecretsWithRef{
				data: map[string]any{},
			}
		})

		Context("with lookup paths", func() {
			BeforeEach(func() {
				variables = creds.NewVariables(fakeSecrets, creds.SecretLookupParams{Team: "main", Pipeline: "my-pipeline"}, false)
			})

			It("implements SecretRefResolver", func() {
				_, ok := variables.(vars.SecretRefResolver)
				Expect(ok).To(BeTrue())
			})

			It("returns the secret ref for the path where the secret was found (pipeline-scoped)", func() {
				fakeSecrets.data["concourse-main/my-pipeline.db-password"] = "s3cret"

				resolver := variables.(vars.SecretRefResolver)
				ref, found := resolver.GetSecretRef(vars.Reference{Path: "db-password"})
				Expect(found).To(BeTrue())
				Expect(ref).To(Equal(&vars.SecretRef{
					Namespace: "concourse-main",
					Name:      "my-pipeline.db-password",
					Key:       "value",
				}))
			})

			It("returns the secret ref for team-scoped secret when pipeline-scoped is not found", func() {
				fakeSecrets.data["concourse-main/db-password"] = "s3cret"

				resolver := variables.(vars.SecretRefResolver)
				ref, found := resolver.GetSecretRef(vars.Reference{Path: "db-password"})
				Expect(found).To(BeTrue())
				Expect(ref).To(Equal(&vars.SecretRef{
					Namespace: "concourse-main",
					Name:      "db-password",
					Key:       "value",
				}))
			})

			It("returns false when the secret is not found at any path", func() {
				resolver := variables.(vars.SecretRefResolver)
				ref, found := resolver.GetSecretRef(vars.Reference{Path: "nonexistent"})
				Expect(found).To(BeFalse())
				Expect(ref).To(BeNil())
			})
		})

		Context("without lookup paths", func() {
			BeforeEach(func() {
				fakeSecrets = &fakeSecretsWithRef{
					data:          map[string]any{},
					noLookupPaths: true,
				}
				variables = creds.NewVariables(fakeSecrets, creds.SecretLookupParams{}, false)
			})

			It("returns the secret ref using the direct path", func() {
				fakeSecrets.data["concourse-main/direct-secret"] = "val"

				resolver := variables.(vars.SecretRefResolver)
				ref, found := resolver.GetSecretRef(vars.Reference{Path: "concourse-main/direct-secret"})
				Expect(found).To(BeTrue())
				Expect(ref).To(Equal(&vars.SecretRef{
					Namespace: "concourse-main",
					Name:      "direct-secret",
					Key:       "value",
				}))
			})

			It("returns false when direct path secret is not found", func() {
				resolver := variables.(vars.SecretRefResolver)
				ref, found := resolver.GetSecretRef(vars.Reference{Path: "missing"})
				Expect(found).To(BeFalse())
				Expect(ref).To(BeNil())
			})
		})
	})

	Describe("when Secrets does not implement SecretRefProvider", func() {
		It("returns false from GetSecretRef", func() {
			plainSecrets := &fakeSecretsNoRef{data: map[string]any{"prefix-main/some-var": "val"}}
			variables = creds.NewVariables(plainSecrets, creds.SecretLookupParams{Team: "main"}, false)

			resolver := variables.(vars.SecretRefResolver)
			ref, found := resolver.GetSecretRef(vars.Reference{Path: "some-var"})
			Expect(found).To(BeFalse())
			Expect(ref).To(BeNil())
		})
	})
})

// fakeSecretsWithRef implements both Secrets and SecretRefProvider
type fakeSecretsWithRef struct {
	data          map[string]any
	noLookupPaths bool
}

func (f *fakeSecretsWithRef) Get(path string) (any, *time.Time, bool, error) {
	val, ok := f.data[path]
	if !ok {
		return nil, nil, false, nil
	}
	return val, nil, true, nil
}

func (f *fakeSecretsWithRef) NewSecretLookupPaths(teamName string, pipelineName string, allowRootPath bool) []creds.SecretLookupPath {
	if f.noLookupPaths {
		return nil
	}
	lookupPaths := []creds.SecretLookupPath{}
	if len(pipelineName) > 0 {
		lookupPaths = append(lookupPaths, creds.NewSecretLookupWithPrefix("concourse-"+teamName+"/"+pipelineName+"."))
	}
	lookupPaths = append(lookupPaths, creds.NewSecretLookupWithPrefix("concourse-"+teamName+"/"))
	return lookupPaths
}

func (f *fakeSecretsWithRef) GetSecretRef(path string) (*vars.SecretRef, bool) {
	parts := splitPath(path)
	if len(parts) != 2 {
		return nil, false
	}
	return &vars.SecretRef{
		Namespace: parts[0],
		Name:      parts[1],
		Key:       "value",
	}, true
}

// fakeSecretsNoRef implements Secrets but NOT SecretRefProvider
type fakeSecretsNoRef struct {
	data map[string]any
}

func (f *fakeSecretsNoRef) Get(path string) (any, *time.Time, bool, error) {
	val, ok := f.data[path]
	if !ok {
		return nil, nil, false, nil
	}
	return val, nil, true, nil
}

func (f *fakeSecretsNoRef) NewSecretLookupPaths(teamName string, pipelineName string, allowRootPath bool) []creds.SecretLookupPath {
	return []creds.SecretLookupPath{creds.NewSecretLookupWithPrefix("prefix-" + teamName + "/")}
}

func splitPath(path string) []string {
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			return []string{path[:i], path[i+1:]}
		}
	}
	return []string{path}
}
