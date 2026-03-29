package concourse

// Version is the version of Concourse. Overridden at build time via:
//
//	-ldflags "-X github.com/concourse/concourse.Version=<version>"
//
// The source of truth is the VERSION file at the repo root.
// Local dev builds default to "0.0.0-dev".
var Version = "0.0.0-dev"

// JetBridgeVersion is the version of the JetBridge edition.
// Kept in sync with the VERSION file by the CI bump step.
var JetBridgeVersion = "0.2.21"

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
