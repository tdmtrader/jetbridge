// Package policy is the output-plane role that reads bucket lifetime policy and
// IAM, and touches no object at all.
//
// That absence is the whole design. The attestor is the workload whose word the
// plane trusts about whether the bucket is safe to publish into, and a
// workload that could also read, create or delete an object would be a
// workload whose compromise costs the data rather than the assessment. So this
// package derives its findings from a reading someone else made, and there is
// no object-store type in its signature anywhere.
//
// What it can and cannot prove is stated plainly because Req 41 and AC 16
// require it: this package reports what a bucket's policy and IAM bindings
// *say*. It does not prove that IAM enforces anything -- no fake enforces a
// binding, and a real assertion about enforcement is a real-GCS observation
// recorded with its date and project. AC 17's lifecycle-rule behaviour is the
// same: this reads the rules, and Phase 7 and Phase 9 own what happens when one
// takes effect.
package policy

import "time"

// StaleAfter is the detection bound the schema enforces, restated here so the
// attestor knows how often it must run rather than learning it from a refusal.
const StaleAfter = 15 * time.Minute
