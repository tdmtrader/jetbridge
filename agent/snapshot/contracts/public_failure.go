package contracts

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
)

// publicRecordFailure classifies one contract-rule rejection under a closed,
// safe-to-publish reason while keeping the detailed message private.
//
// Why every rule needs one: a record contract is a set of rules the platform
// asks an agent — or a person hand-authoring a record — to satisfy, and the
// node-authoring guide names them one by one. Answering a violation with a bare
// `validation_failed` tells an author that SOMETHING about their record was
// wrong and nothing else, which is the least useful thing the platform knows.
// The reason names the rule family; the private cause, which reaches the server
// log, names the exact field.
//
// The classification is deliberately by RULE FAMILY rather than by message. A
// reason per message would be a second copy of the validator, drifting the
// moment anyone rewords an error, and a client cannot branch on thirty near
// synonyms anyway.
//
// Rejections that are platform defects rather than author errors stay
// UNCLASSIFIED on purpose — a missing schema declaration, a declared kind with
// no Go representation, an entity set whose elements have no id field. Those
// mean the declaration and the Go type have drifted apart. Publishing a reason
// for one would invite a caller to try to fix their record, which cannot work.
func publicRecordFailure(reason snapshot.ValidationFailureReason, format string, args ...any) error {
	return snapshot.NewPublicValidationFailure(reason, fmt.Errorf(format, args...))
}

// publicRecordCause classifies an existing error without restating it, for the
// call sites that delegate to a shared helper and only add the rule family the
// delegation belongs to.
func publicRecordCause(reason snapshot.ValidationFailureReason, cause error) error {
	return snapshot.NewPublicValidationFailure(reason, cause)
}

// classifiedRule is the generic core validator's version of the same thing: a
// rejection that knows its closed reason but has not been published yet.
//
// The core needs its own form because of a property this package asserts about
// it — every core rejection LEADS with the field path in the frozen declaration
// grammar, so a reader can look the field up in either table from the message
// alone (TestGenericCoreValidatorEnforcesTheCoreAndNamesTheFieldPath). A
// PublicValidationFailure necessarily leads with its fixed public message
// instead, so wrapping at the point of rejection would quietly destroy that.
//
// The reason therefore travels BESIDE the message through the core, which stays
// transparent in Error(), and becomes a public failure at validateDeclaredBody
// — the single boundary where a core rejection leaves the core.
type classifiedRule struct {
	reason snapshot.ValidationFailureReason
	cause  error
}

func (rule classifiedRule) Error() string { return rule.cause.Error() }

func (rule classifiedRule) Unwrap() error { return rule.cause }

func classifiedRuleFailure(reason snapshot.ValidationFailureReason, format string, args ...any) error {
	return classifiedRule{reason: reason, cause: fmt.Errorf(format, args...)}
}

// publishClassifiedRule turns a carried reason into a public failure. An
// unclassified error — a schema/Go drift defect, which is a platform bug and not
// something a caller can fix — passes through untouched and reaches a client as
// the unqualified validation failure it has always been.
func publishClassifiedRule(err error) error {
	var rule classifiedRule
	if err == nil || !errors.As(err, &rule) {
		return err
	}
	return snapshot.NewPublicValidationFailure(rule.reason, err)
}
