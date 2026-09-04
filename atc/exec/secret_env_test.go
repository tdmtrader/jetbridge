package exec_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// credVars resolves variables from a fixed set and, like the Kubernetes
// credential manager, reports which secret each one was read from.
type credVars struct {
	vars.StaticVariables
	refs map[string]vars.SecretRef
}

func (c credVars) GetSecretRef(ref vars.Reference) (*vars.SecretRef, bool) {
	secretRef, found := c.refs[ref.Path]
	if !found {
		return nil, false
	}
	// Like the Kubernetes credential manager, the key within the secret is the
	// field the variable reference selected, defaulting to "value".
	if len(ref.Fields) > 0 {
		secretRef.Key = ref.Fields[len(ref.Fields)-1]
	}
	return &secretRef, true
}

var _ = Describe("BuildSecretEnv", func() {
	var (
		state  exec.RunState
		params atc.TaskEnv
	)

	BeforeEach(func() {
		state = exec.NewRunState(noopStepper, credVars{
			StaticVariables: vars.StaticVariables{
				"db-password": "s3cret",
				"api-key":     "k3y",
				"plain-var":   "not-a-secret",
				"db":          map[string]any{"password": "s3cret"},
			},
			refs: map[string]vars.SecretRef{
				"db-password": {Namespace: "concourse-main", Name: "db-password", Key: "value"},
				"api-key":     {Namespace: "concourse-main", Name: "api-key", Key: "value"},
				"db":          {Namespace: "concourse-main", Name: "db", Key: "value"},
			},
		})
	})

	resolve := func(ref vars.Reference) {
		GinkgoHelper()
		_, found, err := state.Get(ref)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
	}

	It("returns nil when no secret refs are tracked", func() {
		params = atc.TaskEnv{"PLAIN": "not-a-secret"}

		resolve(vars.Reference{Path: "plain-var"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(BeNil())
	})

	It("maps param env vars to secret refs when values match", func() {
		params = atc.TaskEnv{"DB_PASS": "s3cret", "API_KEY": "k3y"}

		resolve(vars.Reference{Path: "db-password"})
		resolve(vars.Reference{Path: "api-key"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(HaveLen(2))
		Expect(result["DB_PASS"]).To(Equal(vars.SecretRef{Namespace: "concourse-main", Name: "db-password", Key: "value"}))
		Expect(result["API_KEY"]).To(Equal(vars.SecretRef{Namespace: "concourse-main", Name: "api-key", Key: "value"}))
	})

	It("only maps params whose values match tracked secrets", func() {
		params = atc.TaskEnv{"DB_PASS": "s3cret", "STATIC_VAR": "not-a-secret"}

		resolve(vars.Reference{Path: "db-password"})
		resolve(vars.Reference{Path: "plain-var"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(HaveLen(1))
		Expect(result["DB_PASS"]).To(Equal(vars.SecretRef{Namespace: "concourse-main", Name: "db-password", Key: "value"}))
	})

	It("returns nil when params is empty", func() {
		params = atc.TaskEnv{}

		resolve(vars.Reference{Path: "db-password"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(BeNil())
	})

	It("returns nil when no param values match tracked secrets", func() {
		params = atc.TaskEnv{"FOO": "bar"}

		resolve(vars.Reference{Path: "db-password"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(BeNil())
	})

	It("maps a param resolved from a field of a credential to that field's key", func() {
		params = atc.TaskEnv{"DB_PASS": "s3cret"}

		resolve(vars.Reference{Path: "db", Fields: []string{"password"}})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(HaveLen(1))
		Expect(result["DB_PASS"]).To(Equal(vars.SecretRef{
			Namespace: "concourse-main",
			Name:      "db",
			Key:       "password",
		}))
	})
})
