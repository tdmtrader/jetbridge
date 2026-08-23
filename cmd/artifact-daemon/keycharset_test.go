package main

import "testing"

// The charset regression guard lives in-package: these are all LEGAL Concourse
// identifiers (atc/configwarning.go admits any Unicode letter, and it is only a
// warning). An earlier commit required durable.ValidateKey's ASCII charset and
// 400'd them — a broken build, surfacing nowhere in the suite.
func TestValidateRequestKey_AcceptsLegalConcourseIdentifiers(t *testing.T) {
	for _, key := range []string{
		"café", "_out", "-leading-dash", ".git", "out put",
		"steps/build-42/result", "caches/job-42/build-abc.tar", "rc-42",
	} {
		if err := validateRequestKey(key); err != nil {
			t.Errorf("legal key %q refused: %v — this breaks artifact delivery", key, err)
		}
	}
	for _, key := range []string{"..", ".", "", "a/../..", "steps", "STEPS", "aliases.json"} {
		if err := validateRequestKey(key); err == nil {
			if _, err2 := locateArtifact(t.TempDir(), key); err2 == nil {
				t.Errorf("unsafe key %q accepted", key)
			}
		}
	}
}
