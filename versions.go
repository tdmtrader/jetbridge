package concourse

// Version is the version of Concourse. This variable is overridden at build
// time in the pipeline using ldflags.
var Version = "0.0.0-dev"

// JetBridgeVersion is the version of the JetBridge edition.
var JetBridgeVersion = "0.1.0"

// ConcourseVersion is the upstream Concourse version this fork is based on.
var ConcourseVersion = "8.0.1"

func init() { Version = JetBridgeVersion }

// WorkerVersion identifies compatibility between Concourse and a worker.
//
// Backwards-incompatible changes to the worker API should result in a major
// version bump.
//
// New features that are otherwise backwards-compatible should result in a
// minor version bump.
var WorkerVersion = "2.5"
