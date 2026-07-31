// Bench harness tooling: out-of-band graders and fixture materialization for
// bench/corpus. A separate module so `go list ./...` in the repository root
// never walks into it and `make test-unit` never compiles it, the same
// mechanism bench/corpus/go.mod uses for fixture data.
//
// Unlike bench/corpus this module DOES build and DOES have tests; run them
// with `make test-bench-harness`.
module github.com/concourse/concourse/bench/harness

go 1.25.6

require gopkg.in/yaml.v3 v3.0.1
