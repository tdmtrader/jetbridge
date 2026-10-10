// Package artifactwire is the wire seam between the ATC and the artifact
// daemon: every route, every request and response shape, the port, the TLS
// triple, and the one client that knows how to reach a daemon.
//
// It is a shared kernel in the same sense as artifactcap: no transport policy
// beyond HTTP itself, no filesystem, no Kubernetes, and no hangar. Both ends
// name these types, so a field renamed on one side fails to compile on the
// other instead of drifting into a body the handler silently ignores.
//
// Words used the way atc/worker/jetbridge/CONTEXT.md uses them: an artifact
// key names bytes a daemon holds; stream in and stream out move a tar over a
// key; mirror copies a key to peers; a refusal is the daemon's considered no.
package artifactwire
