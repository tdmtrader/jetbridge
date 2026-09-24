// Package conformance exercises the output plane's publisher, inventory and
// reclaimer roles against shared immutable-object semantics.
//
// The in-memory substrate supports deterministic fault injection. The GCS API
// substrate runs the real adapter against fake-gcs-server, and the disk
// substrate uses a real persistent store. Failures identify their substrate;
// an emulator result is not evidence about production GCS permissions.
//
// When HANGAR_CI is set, the GCS API tier requires a configured reachable
// endpoint. Outside CI it can start fake-gcs-server in-process. It never
// substitutes the in-memory client for a failed API tier.
//
// Capability probes expose emulator gaps. In particular, fake-gcs-server
// v1.52.3 ignores delete generation preconditions and does not provide normal
// continuation tokens. Tests that need those capabilities must name the gap;
// memory and disk results do not establish real-GCS conformance.
//
// Role tests verify which operations the code exposes and issues. Operators
// configure GCS IAM, lifecycle, versioning, soft delete and retention. Hangar
// does not attest those settings or make admission depend on reading them.
// Production validation should exercise effective permissions with each role's
// credentials, exact-generation deletion, missing-object versus missing-bucket
// errors, and body reads through the production transport. Deletion completion
// refers to the managed generation's accessibility, not physical erasure of
// retained provider copies.
//
// GCS listing also needs live continuation-token verification before changing
// the SDK page-size hint: the pinned emulator truncates maxResults without a
// nextPageToken. The adapter currently bounds processed entries rather than
// every entry the SDK decodes; see hangar/gcs/objectstore.go.
package conformance
