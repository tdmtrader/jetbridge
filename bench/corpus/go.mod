// The corpus is fixture data, not code. Cases hold withheld grading tests
// harvested verbatim from other trees and other repos: they reference symbols
// as they existed at the case's pre_state SHA, and some import modules this
// one never depends on (github.com/concourse/ci-agent,
// github.com/tdmtrader/lightingdesign). They compile only once a harness
// materializes them into the matching snapshot — never in place, and never as
// part of the parent module's build.
//
// Declaring a module boundary is what keeps `go list ./...` in the repo root
// from walking into them. Without it the root module claims every *_test.go
// under bench/, which broke the unit-tests job (build failures for the cases
// that cannot compile here) and, more quietly, executed the handful that
// happen to compile — grading tests scored against the wrong tree state.
// Same mechanism ci-agent/ and agent/schema/ already use.
//
// Nothing builds or imports this module. It declares no dependencies on
// purpose: adding one would mean something in here was being compiled, which
// is the situation this file exists to prevent.
module github.com/concourse/concourse/bench/corpus

go 1.25.6
