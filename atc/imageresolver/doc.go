// Package imageresolver is the one home for turning what a pipeline says
// about an image into what a registry and a step pod understand. It has
// three concerns:
//
//   - a registry-image source → the digest its repository and tag currently
//     name (Reference, ReferenceFromSource, Reference.Resolve), used by lidar,
//     the check step's native path and the delegate's on-demand resolve;
//   - a registry reference → the same reference pinned to its digest
//     (ParseReference, Pin), used by the task step for sidecar images;
//   - a resource type → the image its step pod runs (Image,
//     DefaultResourceTypeImages, MergeResourceTypeImages), used by the
//     JetBridge runtime and worker registration.
//
// A "digest-pinned reference" here is only a string of the form
// repository@sha256:…. It is not the glossary's Pinned image (atc/CONTEXT.md):
// that is the resource cache a resource type declared with a direct image:
// is created under (db.PinnedImage), an identity in the store, which this
// package never touches.
package imageresolver
