package composition_test

import (
	"fmt"

	"github.com/concourse/concourse/atc/agent/composition"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DigestConflictError", func() {
	conflict := composition.DigestConflictError{Recorded: "abc", Presented: "def"}

	// The consumer that has to act on this error -- the run_pipeline step --
	// cannot import this package, and core cannot name it either. The marker
	// is how the classification reaches them anyway, so this asserts the thing
	// they actually call rather than the method's presence.
	It("is a refusal, recognizable through the port without naming this package", func() {
		Expect(runs.IsRefusal(conflict)).To(BeTrue())
	})

	It("stays one when a caller wraps it", func() {
		Expect(runs.IsRefusal(fmt.Errorf("admitting child run: %w", conflict))).To(BeTrue())
	})
})
