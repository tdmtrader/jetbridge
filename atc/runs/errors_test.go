package runs_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// consumerRefusal is a refusal raised beyond this package.
//
// atc/agent/composition's digest conflict is the real one, and its own suite
// asserts that IsRefusal answers for it. This stand-in is here because core
// cannot import that package -- which is the whole reason the marker interface
// exists rather than a fixed list -- and because the rule is about any
// consumer's refusal, not about that one.
type consumerRefusal struct{}

func (consumerRefusal) Error() string     { return "the consumer refuses" }
func (consumerRefusal) AdmissionRefusal() {}

var _ = Describe("IsRefusal", func() {
	// A refusal is a fact about what was asked for. A consumer that reports
	// one to a human and stops is right to; a consumer that retries one is
	// waiting for a pipeline config to fix itself.
	DescribeTable("a refusal",
		func(err error) {
			Expect(runs.IsRefusal(err)).To(BeTrue())
		},
		Entry("no such template", runs.ErrTemplateNotFound),
		Entry("not a template", runs.ErrNotATemplate),
		Entry("an instanced pipeline", runs.ErrTemplateInstanced),
		Entry("an archived template", runs.ErrTemplateArchived),
		Entry("a paused template", runs.ErrTemplatePaused),
		Entry("another team's template", runs.ErrUnauthorized),
		Entry("params the template refuses",
			runs.InvalidParamsError{Err: errors.New("unknown parameter: nope")}),
		Entry("a stored template config that no longer validates",
			runs.TemplateConfigInvalidError{Err: errors.New("jobs: identifier is empty")}),
		Entry("a consumer's own refusal, recognized through the marker",
			consumerRefusal{}),
	)

	// %w is how a consumer says where it was when the port refused. It has
	// not stopped being refused, so every one of the above survives being
	// wrapped -- including the marker, which errors.As unwraps for.
	DescribeTable("a refusal a consumer wrapped with context",
		func(err error) {
			Expect(runs.IsRefusal(fmt.Errorf("admitting child run: %w", err))).To(BeTrue())
		},
		Entry("a sentinel", runs.ErrTemplatePaused),
		Entry("a wrapping type",
			runs.InvalidParamsError{Err: errors.New("unknown parameter: nope")}),
		Entry("a marked consumer refusal", consumerRefusal{}),
	)

	// The default is "fault", and these are the ones it matters for: each of
	// them either goes away on a retry or is a bug someone has to be told
	// about. Reporting one as a refusal buries it in a build log as though the
	// pipeline's author had written something wrong.
	DescribeTable("a fault",
		func(err error) {
			Expect(runs.IsRefusal(err)).To(BeFalse())
		},
		Entry("a principal presenting both forms or neither", runs.ErrPrincipalAmbiguous),
		Entry("a missing contract key", runs.ErrMissingContractKey),
		Entry("a run id that names no row", runs.ErrRunNotFound),
		Entry("an operator role mapping the port will not honour",
			runs.CustomRolesInvalidError{Err: errors.New("viewer may create runs")}),
		Entry("a transaction the port did not open", runs.ForeignTransactionError{}),
		Entry("a cancelled context", context.Canceled),
		Entry("a deadline that passed", context.DeadlineExceeded),
		Entry("a wrapped cancelled context",
			fmt.Errorf("admitting child run: %w", context.Canceled)),
		Entry("a database error", sql.ErrConnDone),
		Entry("an error from nowhere in particular", errors.New("pool exhausted")),
		Entry("no error at all", nil),
	)
})
