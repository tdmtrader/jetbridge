// Package gcs implements Hangar immutable object operations over Cloud Storage.
// Clients expose no raw SDK handle or delete capability; deletion is isolated
// in hangar/gcsdelete.
package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

func (client outputObjectClient) CreateAbsent(ctx context.Context, bucket, key string, metadata map[string]string, body io.Reader) (objectstore.Attrs, error) {
	writer := client.client.Bucket(bucket).Object(key).If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	writer.Metadata = metadata
	if _, err := io.Copy(writer, body); err != nil {
		_ = writer.CloseWithError(err)
		return objectstore.Attrs{}, translate(err)
	}
	if err := writer.Close(); err != nil {
		return objectstore.Attrs{}, translate(err)
	}
	return outputAttrs(writer.Attrs()), nil
}

func (client outputObjectClient) StatCurrent(ctx context.Context, bucket, key string) (objectstore.Attrs, error) {
	attrs, err := client.client.Bucket(bucket).Object(key).Attrs(ctx)
	if err != nil {
		return objectstore.Attrs{}, translate(err)
	}
	return outputAttrs(attrs), nil
}

func (client outputObjectClient) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	if err := objectstore.ValidateGeneration(generation); err != nil {
		return objectstore.Attrs{}, err
	}
	attrs, err := client.client.Bucket(bucket).Object(key).Generation(generation).Attrs(ctx)
	if err != nil {
		return objectstore.Attrs{}, translate(err)
	}
	return outputAttrs(attrs), nil
}

func (client outputObjectClient) OpenExact(ctx context.Context, bucket, key string, generation int64) (io.ReadCloser, error) {
	if err := objectstore.ValidateGeneration(generation); err != nil {
		return nil, err
	}
	reader, err := client.client.Bucket(bucket).Object(key).Generation(generation).NewReader(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return translatedReader{ReadCloser: reader}, nil
}

// Translate stream errors as well as open errors: authorization can fail while
// reading a resumed download, after OpenExact has already returned successfully.
type translatedReader struct{ io.ReadCloser }

func (reader translatedReader) Read(body []byte) (int, error) {
	n, err := reader.ReadCloser.Read(body)
	if errors.Is(err, io.EOF) {
		return n, err
	}
	return n, translate(err)
}
func (reader translatedReader) Close() error { return translate(reader.ReadCloser.Close()) }

// List is bucket-wide with a caller-applied prefix.
//
// GCS grants storage.objects.list on the bucket and cannot scope it to a
// prefix, so the prefix here narrows *what is read*, not what may be read. The
// operator provisions a dedicated bucket; this method does not enforce that.
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

	// Req 44's budget bounds what this pass PROCESSES, not what the SDK
	// decodes, and the difference is recorded here rather than closed.
	//
	// The loop below stops at request.PageSize, but the SDK still fetches its
	// own default of 1000 items per list RPC and decodes every one of them
	// first, so a pass bounded to 100 objects and 8 MiB of decoded metadata can
	// have the client decode ten times that in one call. The one-line fix is
	// `iterated.PageInfo().MaxSize = request.PageSize`, and it is WITHHELD on
	// measured evidence rather than overlooked: fsouza/fake-gcs-server v1.52.3
	// honours maxResults by truncating the answer and returns NO
	// nextPageToken, so with the bound set the SDK's iterator reaches atEnd
	// after one RPC and every sweep on tier 2 silently stops at the first page
	// -- the exact class of defect the suite exists to catch, traded for a
	// memory bound. Real GCS returns the token (the adapter pages correctly
	// against a server that does, measured against an httptest double of one),
	// so this is a real-GCS observation before it is a change: set maxResults
	// against the real API, confirm the continuation token, then set MaxSize
	// here. It is in the Phase 9 evidence list in this package's doc.go.
	//
	// What IS bounded already is the per-object decode: SetAttrSelection above
	// asks for six fields rather than the whole object resource.
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
		return fmt.Errorf("%w: %w", objectstore.ErrBucketNotFound, err)
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("%w: %w", objectstore.ErrNotFound, err)
	}

	switch status.Code(err) {
	case codes.NotFound:
		return fmt.Errorf("%w: %w", objectstore.ErrNotFound, err)
	case codes.Unauthenticated, codes.PermissionDenied:
		return fmt.Errorf("%w: %w", objectstore.ErrUnauthorized, err)
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: %w", objectstore.ErrPreconditionFailed, err)
	}
	var api *googleapi.Error
	if errors.As(err, &api) {
		switch api.Code {
		case 404:
			return fmt.Errorf("%w: %w", objectstore.ErrNotFound, err)
		case 403, 401:
			return fmt.Errorf("%w: %w", objectstore.ErrUnauthorized, err)
		case 412:
			return fmt.Errorf("%w: %w", objectstore.ErrPreconditionFailed, err)
		}
	}

	return fmt.Errorf("%w: %w", objectstore.ErrInfrastructure, err)
}
