// Package gcsdelete is the object-delete capability, and nothing else.
//
// It exists as a package rather than a method because of what Req 55 can and
// cannot be enforced by. GCS IAM cannot require a caller to send a generation
// precondition once storage.objects.delete exists, so the requirement lives in
// the code and in the workload boundary together -- and a code boundary that a
// reviewer has to read is not a boundary. While Delete was a method on the
// shared objectstore.Handle, the adapter handed to the daemon, the inventory
// controller and the reclaimer alike carried it, and the architecture guard
// that was supposed to stop a second deleter asked only whether a binary linked
// the reclaimer ROLE. A daemon could issue objects.delete with every guard
// green; that was demonstrated, and it is why this package exists.
//
// Now the question "which binaries can delete an output object" is answered by
// `go list -deps`, and the answer is checked for every main under cmd/.
package gcsdelete

import (
	"context"
	"fmt"

	"cloud.google.com/go/storage"

	"github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/objectstore"
)

// NewDeleteClient adapts a storage client to the delete seam.
//
// It is the only constructor of objectstore.DeleteClient over a real cloud
// client in this repository.
func NewDeleteClient(client *storage.Client) (objectstore.DeleteClient, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: the delete client needs a GCS client",
			objectstore.ErrInfrastructure)
	}

	return deleteClient{client: client}, nil
}

type deleteClient struct{ client *storage.Client }

func (client deleteClient) ObjectToDelete(bucket, key string) objectstore.DeleteHandle {
	return deleteHandle{handle: client.client.Bucket(bucket).Object(key)}
}

type deleteHandle struct{ handle *storage.ObjectHandle }

func (handle deleteHandle) If(conditions objectstore.Conditions) objectstore.DeleteHandle {
	return deleteHandle{handle: handle.handle.If(storage.Conditions{
		DoesNotExist:        conditions.DoesNotExist,
		GenerationMatch:     conditions.GenerationMatch,
		MetagenerationMatch: conditions.MetagenerationMatch,
	})}
}

func (handle deleteHandle) Generation(generation int64) objectstore.DeleteHandle {
	return deleteHandle{handle: handle.handle.Generation(generation)}
}

// Attrs and Delete both go through the ONE 404/412/403 split, which lives with
// the other adapter. Re-deriving it here would be a second reading of the same
// three status codes, and the case that matters -- 412 on a delete means "not
// this generation" and never "already gone" -- is exactly the one a second
// reading would get subtly wrong.
func (handle deleteHandle) Attrs(ctx context.Context) (objectstore.Attrs, error) {
	attrs, err := handle.handle.Attrs(ctx)
	if err != nil {
		return objectstore.Attrs{}, gcs.TranslateObjectError(err)
	}

	return gcs.OutputAttrs(attrs), nil
}

func (handle deleteHandle) Delete(ctx context.Context) error {
	return gcs.TranslateObjectError(handle.handle.Delete(ctx))
}
