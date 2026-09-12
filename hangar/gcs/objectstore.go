// Package gcs is the Hangar OUTPUT plane's Cloud Storage seam: the object
// adapter its four roles share, and the bucket-metadata source the attestor
// reads.
//
// It opens every client it hands out and hands out none. A constructor here
// takes an endpoint, never a *storage.Client, because the third round of one
// finding showed what an exported client constructor costs: three non-reclaimer
// command roots held the raw client in a local variable, and
// `client.Bucket(b).Object("any/key").Delete(ctx)` compiled in all three with no
// new import and every architecture guard green. The client is now opened by
// hangar/internal/gcsclient, which nothing outside hangar/ can import at all.
//
// The artifact daemon's strict-input store used to live in this package and is
// now hangar/gcsstore, so an output root that links this seam cannot name
// GCSStore.DeleteTree either.
package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	"github.com/concourse/concourse/hangar/internal/gcsclient"
	"github.com/concourse/concourse/hangar/objectstore"
)

// NormalizeStorageEndpoint puts an emulator endpoint on the JSON API base.
//
// It is re-exported from the internal client package for harnesses that must
// point a client of their own at the same emulator the production seam uses.
// It is a string in and a string out: it hands out no capability, which is the
// whole reason it may be exported while the client constructor may not.
func NormalizeStorageEndpoint(endpoint string) (string, error) {
	return gcsclient.NormalizeEndpoint(endpoint)
}

// The exported object adapter, for the output plane's four cloud roles.
//
// It is a second adapter beside the unexported one GCSStore uses, and that is
// deliberate. GCSStore is the strict-input foundation and its behaviour is
// frozen byte-for-byte; re-expressing it through a new interface to save sixty
// lines would put the one store this repository already depends on inside the
// blast radius of an output-plane change. The two adapters wrap the same
// *storage.ObjectHandle and are therefore the same behaviour, and the tier-2
// conformance suite is what keeps that claim honest.
//
// Nothing here interprets a key. The bucket and key arrive already derived from
// authenticated configuration (hangar/output.OutputNamespace), and this file's
// only judgement is turning a transport status into a typed sentinel.

// NewObjectClient opens a client for the shared object seam and owns it.
//
// The returned closer is the caller's to defer. It takes an endpoint rather
// than a client so that no caller ever holds a type from which delete is
// reachable: objectstore.Client has no delete on it, and a root that held the
// *storage.Client behind it would have one anyway.
func NewObjectClient(ctx context.Context, endpoint string) (objectstore.Client, func() error, error) {
	client, err := gcsclient.New(ctx, endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("hangar: opening the GCS object client: %w", err)
	}

	return outputObjectClient{client: client}, client.Close, nil
}

type outputObjectClient struct{ client *storage.Client }

func (client outputObjectClient) Object(bucket, key string) objectstore.Handle {
	return outputObjectHandle{handle: client.client.Bucket(bucket).Object(key)}
}

// List is bucket-wide with a caller-applied prefix.
//
// GCS grants storage.objects.list on the bucket and cannot scope it to a
// prefix, so the prefix here narrows *what is read*, not what may be read. The
// policy attestor is what says the bucket contains only this plane's objects;
// this method cannot and does not claim it.
func (client outputObjectClient) List(ctx context.Context, bucket string, request objectstore.ListRequest) (objectstore.Page, error) {
	if request.PageSize <= 0 {
		return objectstore.Page{}, fmt.Errorf("%w: a list page size must be positive",
			objectstore.ErrInfrastructure)
	}

	query := &storage.Query{Prefix: request.Prefix}
	if request.After != "" {
		// StartOffset is inclusive, so the resumed-from key comes back and is
		// dropped below. A page token would have been one call shorter and is
		// exactly what this seam refuses to carry: it expires, and the cursor
		// it would live in does not.
		query.StartOffset = request.After
	}
	// Only the fields the inventory classifies on. A projection that fetched
	// everything would make one page's metadata budget unpredictable.
	if err := query.SetAttrSelection([]string{"Name", "Generation", "Metageneration", "Size", "Created", "Metadata"}); err != nil {
		return objectstore.Page{}, translate(err)
	}

	iterated := client.client.Bucket(bucket).Objects(ctx, query)

	page := objectstore.Page{Objects: make([]objectstore.Attrs, 0, request.PageSize)}
	for len(page.Objects) < request.PageSize {
		attrs, err := iterated.Next()
		if errors.Is(err, iterator.Done) {
			page.Done = true

			break
		}
		if err != nil {
			return objectstore.Page{}, translate(err)
		}
		if attrs.Name == request.After &&
			(request.AfterGeneration == 0 || attrs.Generation <= request.AfterGeneration) {
			// The resumed-from key, already dispositioned. It is NOT dropped
			// when its generation is past the one the cursor named: that is an
			// object recreated at the same name since, and it is new.
			continue
		}
		page.Objects = append(page.Objects, outputAttrs(attrs))
	}
	if len(page.Objects) > 0 {
		page.LastKey = page.Objects[len(page.Objects)-1].Key
	}

	return page, nil
}

type outputObjectHandle struct{ handle *storage.ObjectHandle }

func (handle outputObjectHandle) If(conditions objectstore.Conditions) objectstore.Handle {
	return outputObjectHandle{handle: handle.handle.If(storage.Conditions{
		DoesNotExist:        conditions.DoesNotExist,
		GenerationMatch:     conditions.GenerationMatch,
		MetagenerationMatch: conditions.MetagenerationMatch,
	})}
}

func (handle outputObjectHandle) Generation(generation int64) objectstore.Handle {
	return outputObjectHandle{handle: handle.handle.Generation(generation)}
}

func (handle outputObjectHandle) NewWriter(ctx context.Context) objectstore.Writer {
	return &outputObjectWriter{writer: handle.handle.NewWriter(ctx)}
}

func (handle outputObjectHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	reader, err := handle.handle.NewReader(ctx)
	if err != nil {
		return nil, translate(err)
	}

	return reader, nil
}

func (handle outputObjectHandle) Attrs(ctx context.Context) (objectstore.Attrs, error) {
	attrs, err := handle.handle.Attrs(ctx)
	if err != nil {
		return objectstore.Attrs{}, translate(err)
	}

	return outputAttrs(attrs), nil
}

type outputObjectWriter struct{ writer *storage.Writer }

func (writer *outputObjectWriter) Write(content []byte) (int, error) {
	count, err := writer.writer.Write(content)

	return count, translate(err)
}

func (writer *outputObjectWriter) Close() error { return translate(writer.writer.Close()) }

func (writer *outputObjectWriter) Abort(cause error) error {
	return writer.writer.CloseWithError(cause)
}

func (writer *outputObjectWriter) SetMetadata(metadata map[string]string) {
	writer.writer.Metadata = metadata
}

func (writer *outputObjectWriter) Attrs() objectstore.Attrs {
	return outputAttrs(writer.writer.Attrs())
}

// OutputAttrs and TranslateObjectError are exported for hangar/gcsdelete, which
// is the delete capability's own package and therefore cannot be this one.
//
// They are the projection and the status-code split, and both are shared on
// purpose: a second reading of 404/412/403 is where a delete would eventually
// be told that 412 means "already gone".
func OutputAttrs(attrs *storage.ObjectAttrs) objectstore.Attrs { return outputAttrs(attrs) }

// TranslateObjectError is the 404/412/403 split, shared with the delete
// capability's package.
func TranslateObjectError(err error) error { return translate(err) }

func outputAttrs(attrs *storage.ObjectAttrs) objectstore.Attrs {
	if attrs == nil {
		return objectstore.Attrs{}
	}

	return objectstore.Attrs{
		Key:            attrs.Name,
		Generation:     attrs.Generation,
		Metageneration: attrs.Metageneration,
		Size:           attrs.Size,
		Created:        attrs.Created,
		Metadata:       attrs.Metadata,
	}
}

// translate is the 404/412/403 split.
//
// It is the whole reason this adapter exists as code rather than as a type
// assertion: a caller that saw a *googleapi.Error would have to know that 412
// on a create means "something is already there" and 412 on a delete means "not
// this generation", and every caller would decide that separately.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// The bucket and the object are two absences, and folding them together was
	// a finding: object absence is what the reclaim path reads as evidence a
	// generation is gone, so a deleted bucket or a misconfigured bucket name
	// answered "already absent" for every object in a registered set that was
	// entirely intact.
	if errors.Is(err, storage.ErrBucketNotExist) {
		return fmt.Errorf("%w: %v", objectstore.ErrBucketNotFound, err)
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("%w: %v", objectstore.ErrNotFound, err)
	}

	var api *googleapi.Error
	if errors.As(err, &api) {
		switch api.Code {
		case 404:
			return fmt.Errorf("%w: %v", objectstore.ErrNotFound, err)
		case 403, 401:
			return fmt.Errorf("%w: %v", objectstore.ErrUnauthorized, err)
		case 412:
			return fmt.Errorf("%w: %v", objectstore.ErrPreconditionFailed, err)
		}
	}

	return fmt.Errorf("%w: %v", objectstore.ErrInfrastructure, err)
}
