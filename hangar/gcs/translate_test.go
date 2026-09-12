package gcs

// The 404/412/403 split, and the one absence it must not fold.

import (
	"errors"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"

	"github.com/concourse/concourse/hangar/objectstore"
)

// A deleted bucket, or a controller started against a bucket name nobody
// publishes into, used to answer "not found" for every operation -- and the
// reclaim path reads object absence as evidence that the object is gone. Folded
// together, a wrong bucket finalizes an entire registered set as this plane's
// own successful deletions while every object is still there.
func TestBucketAbsenceIsNeverObjectAbsence(t *testing.T) {
	translated := TranslateObjectError(storage.ErrBucketNotExist)

	if !errors.Is(translated, objectstore.ErrBucketNotFound) {
		t.Errorf("a missing BUCKET translated to %v, which is not the bucket-absence sentinel",
			translated)
	}
	if errors.Is(translated, objectstore.ErrNotFound) {
		t.Error("a missing BUCKET still reads as object absence.\n\nThis is the amplifier: the " +
			"reclaim path reads objectstore.ErrNotFound as 'the generation is gone', so a " +
			"deleted bucket or a wrong --output-prefix finalizes every admitted job in the " +
			"registered set as reclaimed, with no violation and no at-risk.")
	}
}

func TestTheObjectStatusSplitIsPinned(t *testing.T) {
	for _, row := range []struct {
		name string
		err  error
		want error
	}{
		{"object 404", storage.ErrObjectNotExist, objectstore.ErrNotFound},
		{"api 404", &googleapi.Error{Code: 404}, objectstore.ErrNotFound},
		{"api 403", &googleapi.Error{Code: 403}, objectstore.ErrUnauthorized},
		{"api 401", &googleapi.Error{Code: 401}, objectstore.ErrUnauthorized},
		{"api 412", &googleapi.Error{Code: 412}, objectstore.ErrPreconditionFailed},
		{"api 500", &googleapi.Error{Code: 500}, objectstore.ErrInfrastructure},
	} {
		if translated := TranslateObjectError(row.err); !errors.Is(translated, row.want) {
			t.Errorf("%s translated to %v, expected %v", row.name, translated, row.want)
		}
	}
	if TranslateObjectError(nil) != nil {
		t.Error("a nil error translated to something")
	}
}
