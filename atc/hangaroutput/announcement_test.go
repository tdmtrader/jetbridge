package hangaroutput

import (
	"reflect"
	"testing"

	"github.com/concourse/concourse/hangar/output"
)

// The announcement's field set is the assertion.
//
// Requirement 18 says a capture's outcome reaches existing diagnostics
// carrying the redacted disposition and reason and "no grant, key, path or
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
			"reason, and a fourth field is where a grant, key, path or consumer reference "+
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
