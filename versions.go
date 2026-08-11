package concourse

// Version is the version of Concourse. Overridden at build time via:
//
//	-ldflags "-X github.com/concourse/concourse.Version=<version>"
//
// The source of truth is the VERSION file at the repo root.
// Local dev builds default to "0.0.0-dev".
var Version = "0.0.0-dev"

// JetBridgeVersion is the version of the JetBridge edition.
//
// This must equal the VERSION file and deploy/chart/Chart.yaml's appVersion.
// TestVersionDeclarationsAgree enforces it; nothing keeps them in sync
// automatically. The comment here used to claim "kept in sync with the VERSION
// file by the CI bump step" -- no such step has ever existed, and all three
// values sat at 0.2.80 across every release the fork ever cut.
var JetBridgeVersion = "0.3.0"

// ConcourseVersion is the upstream Concourse version this fork is based on.
var ConcourseVersion = "8.0.1"

// WorkerVersion identifies compatibility between Concourse and a worker.
//
// Backwards-incompatible changes to the worker API should result in a major
// version bump.
//
// New features that are otherwise backwards-compatible should result in a
// minor version bump.
var WorkerVersion = "2.5"
