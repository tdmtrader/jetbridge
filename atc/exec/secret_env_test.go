package exec_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("BuildSecretEnv", func() {
	var (
		fakeState *execfakes.FakeRunState
		params    atc.TaskEnv
	)

	BeforeEach(func() {
		fakeState = new(execfakes.FakeRunState)
	})

	It("returns nil when no secret refs are tracked", func() {
		params = atc.TaskEnv{"DB_PASS": "s3cret"}

		fakeState.IterateInterpolatedCredsStub = func(iter vars.TrackedVarsIterator) {
			iter.YieldCred("db-password", "s3cret")
		}
		fakeState.IterateSecretRefsStub = func(iter vars.TrackedSecretRefsIterator) {
			// no refs
		}

		result := exec.BuildSecretEnv(params, fakeState)
		Expect(result).To(BeNil())
	})

	It("maps param env vars to secret refs when values match", func() {
		params = atc.TaskEnv{"DB_PASS": "s3cret", "API_KEY": "k3y"}

		fakeState.IterateInterpolatedCredsStub = func(iter vars.TrackedVarsIterator) {
			iter.YieldCred("db-password", "s3cret")
			iter.YieldCred("api-key", "k3y")
		}
		fakeState.IterateSecretRefsStub = func(iter vars.TrackedSecretRefsIterator) {
			iter.YieldSecretRef("db-password", vars.SecretRef{Namespace: "concourse-main", Name: "db-password", Key: "value"})
			iter.YieldSecretRef("api-key", vars.SecretRef{Namespace: "concourse-main", Name: "api-key", Key: "value"})
		}

		result := exec.BuildSecretEnv(params, fakeState)
		Expect(result).To(HaveLen(2))
		Expect(result["DB_PASS"]).To(Equal(vars.SecretRef{Namespace: "concourse-main", Name: "db-password", Key: "value"}))
		Expect(result["API_KEY"]).To(Equal(vars.SecretRef{Namespace: "concourse-main", Name: "api-key", Key: "value"}))
	})

	It("only maps params whose values match tracked secrets", func() {
		params = atc.TaskEnv{"DB_PASS": "s3cret", "STATIC_VAR": "not-a-secret"}

		fakeState.IterateInterpolatedCredsStub = func(iter vars.TrackedVarsIterator) {
			iter.YieldCred("db-password", "s3cret")
		}
		fakeState.IterateSecretRefsStub = func(iter vars.TrackedSecretRefsIterator) {
			iter.YieldSecretRef("db-password", vars.SecretRef{Namespace: "concourse-main", Name: "db-password", Key: "value"})
		}

		result := exec.BuildSecretEnv(params, fakeState)
		Expect(result).To(HaveLen(1))
		Expect(result["DB_PASS"]).To(Equal(vars.SecretRef{Namespace: "concourse-main", Name: "db-password", Key: "value"}))
	})

	It("returns nil when params is empty", func() {
		params = atc.TaskEnv{}

		fakeState.IterateSecretRefsStub = func(iter vars.TrackedSecretRefsIterator) {
			iter.YieldSecretRef("db-password", vars.SecretRef{Namespace: "ns", Name: "s", Key: "value"})
		}

		result := exec.BuildSecretEnv(params, fakeState)
		Expect(result).To(BeNil())
	})

	It("returns nil when no param values match tracked secrets", func() {
		params = atc.TaskEnv{"FOO": "bar"}

		fakeState.IterateInterpolatedCredsStub = func(iter vars.TrackedVarsIterator) {
			iter.YieldCred("db-password", "s3cret")
		}
		fakeState.IterateSecretRefsStub = func(iter vars.TrackedSecretRefsIterator) {
			iter.YieldSecretRef("db-password", vars.SecretRef{Namespace: "ns", Name: "s", Key: "value"})
		}

		result := exec.BuildSecretEnv(params, fakeState)
		Expect(result).To(BeNil())
	})
})
