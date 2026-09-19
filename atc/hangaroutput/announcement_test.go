package hangaroutput

import (
	"errors"
	"reflect"
	"testing"

	"github.com/concourse/concourse/hangar/output"
)

// The announcement's field set is the assertion.
//
// Requirement 18 says a capture's outcome reaches existing diagnostics
// carrying the redacted disposition and reason and "no warrant, key, path or
// consumer ref". That sentence can only FAIL if the payload is a closed set of
// fields, so this walks the type: three fields, all of closed vocabularies or
// a closed reason word, and no map, slice, interface or error through which an
// object key could eventually travel.
//
// A value assertion could not say this. "This announcement contains no key" is
// true of every announcement that happens not to; "this type has nowhere to put
// one" is true of all of them, forever.
func TestAnAnnouncementCarriesNothingButAKindADispositionAndAReason(t *testing.T) {
	announcement := reflect.TypeOf(Announcement{})

	want := map[string]reflect.Kind{
		"Kind":        reflect.String,
		"Disposition": reflect.String,
		"Reason":      reflect.String,
	}
	if announcement.NumField() != len(want) {
		var got []string
		for i := 0; i < announcement.NumField(); i++ {
			got = append(got, announcement.Field(i).Name)
		}
		t.Fatalf("an announcement carries %v; requirement 18's payload is a disposition and a "+
			"reason, and a fourth field is where a warrant, key, path or consumer reference "+
			"eventually travels", got)
	}

	for i := 0; i < announcement.NumField(); i++ {
		field := announcement.Field(i)
		kind, known := want[field.Name]
		if !known {
			t.Errorf("an announcement carries an unexpected field %q", field.Name)

			continue
		}
		if field.Type.Kind() != kind {
			t.Errorf("Announcement.%s is a %s; every field is a closed-vocabulary string, "+
				"because a map, a slice, an interface or an error is an escape hatch",
				field.Name, field.Type.Kind())
		}
	}
}

// A terminal announcement with no reason is refused.
//
// A watcher told only that something ended learns nothing they did not already
// know, and the reason is the only part of the payload that distinguishes a
// captured output from a cancelled one from an unconfirmed seal.
func TestATerminalAnnouncementWithoutAReasonIsRefused(t *testing.T) {
	whole := Announcement{
		Kind:        AnnouncementDisposition,
		Disposition: output.DispositionCapture,
		Reason:      "captured",
	}
	if err := whole.Validate(); err != nil {
		t.Fatalf("a whole announcement was refused: %v", err)
	}

	reasonless := whole
	reasonless.Reason = ""
	if err := reasonless.Validate(); err == nil {
		t.Error("a terminal announcement with no reason was admitted")
	}

	unknown := whole
	unknown.Kind = "capture-something-else"
	if err := unknown.Validate(); err == nil {
		t.Error("an announcement of an unknown kind was admitted")
	}
}

// Nothing this component reports carries an identity.
//
// The debt report is a COUNT per bounded label and the log line is a class
// word, and both are deliberate: a metric keyed by handoff is one series per
// capture forever, and an opaque handoff id -- which a consumer may treat as
// sensitive -- in a metrics store is a leak into a system nobody thinks of as a
// log. The one place an id appears at all is the failure log line, where an
// operator needs it to find the capture.
func TestEveryReportedClassIsABoundedWord(t *testing.T) {
	seen := map[string]bool{}
	for _, err := range []error{
		output.ErrUnauthorized, output.ErrConflict, output.ErrNotFound, output.ErrSealed,
		output.ErrSealUnconfirmed, output.ErrUnresolved, output.ErrIncomplete,
		output.ErrInvalidIdentity, output.ErrCorrupt, output.ErrInfrastructure,
	} {
		class := classOf(err)
		if class == "other" {
			t.Errorf("%v reduced to `other`; every sentinel this plane raises has a name", err)
		}
		if seen[class] {
			t.Errorf("two sentinels reduce to %q, so the label cannot tell them apart", class)
		}
		seen[class] = true
	}

	// And an error the plane does not name is `other` rather than its own text,
	// which is what keeps the label bounded when a dependency invents one.
	if class := classOf(errors.New("a store returned gs://bucket/key: no such object")); class != "other" {
		t.Errorf("an unnamed error reduced to %q; a label built from an error's text is "+
			"unbounded, and this one carries an object key", class)
	}
}
