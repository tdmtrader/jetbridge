package steps

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// PodNameDefinitions migrates podname_test.go — pod-name generation,
// sanitization, truncation and fallback. GeneratePodName is a pure exported
// function, so this suite needs no cluster, no database and no doubles: the
// seam is the function itself.

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// PodNameDefinitions is the vocabulary for pod naming.
func PodNameDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, PodNameRequest](
			"a {string} container in pipeline {string} job {string} build {string} step {string} with handle {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (PodNameRequest, error) {
				ctype, _ := p.GetString(0)
				pipeline, _ := p.GetString(1)
				job, _ := p.GetString(2)
				build, _ := p.GetString(3)
				step, _ := p.GetString(4)
				handle, ok := p.GetString(5)
				if !ok {
					return PodNameRequest{}, fmt.Errorf("expected six parameters")
				}
				return PodNameRequest{
					Metadata: db.ContainerMetadata{
						Type:         db.ContainerType(ctype),
						PipelineName: pipeline,
						JobName:      job,
						BuildName:    build,
						StepName:     step,
					},
					Handle: handle,
				}, nil
			},
		),

		brine.DefineMap[PodNameRequest, GeneratedPodName](
			"the pod name is generated",
			func(in PodNameRequest, _ brine.Params, _ *brine.Recorder) (GeneratedPodName, error) {
				return GeneratedPodName{
					Name:   jetbridge.GeneratePodName(in.Metadata, in.Handle),
					Handle: in.Handle,
				}, nil
			},
		),

		// Keeps its own body: a regular-expression match. No combinator
		// compares against a pattern — CheckString would demand the name equal
		// the pattern and CheckContains would demand the pattern appear in it
		// literally, and both are different rules.
		brine.DefineCheck[GeneratedPodName](
			"the pod name matches {string}",
			func(in GeneratedPodName, p brine.Params, _ *brine.Recorder) error {
				pattern, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pattern parameter")
				}
				re, err := regexp.Compile(pattern)
				if err != nil {
					return fmt.Errorf("bad pattern %q: %w", pattern, err)
				}
				if !re.MatchString(in.Name) {
					return fmt.Errorf("expected %q to match %q", in.Name, pattern)
				}
				return nil
			},
		),

		CheckThat[GeneratedPodName]("the pod name is the handle unchanged",
			func(in GeneratedPodName) error {
				if in.Name != in.Handle {
					return fmt.Errorf("expected the handle %q unchanged, got %q", in.Handle, in.Name)
				}
				return nil
			}),

		// Keeps its own body: a bound, not an equality. CheckInt would assert
		// that the name is EXACTLY that long, which is a different rule.
		brine.DefineCheck[GeneratedPodName](
			"the pod name is at most {int} characters",
			func(in GeneratedPodName, p brine.Params, _ *brine.Recorder) error {
				max, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected a length parameter")
				}
				if len(in.Name) > max {
					return fmt.Errorf("expected at most %d characters, got %d (%q)", max, len(in.Name), in.Name)
				}
				return nil
			},
		),

		// A deletion probe found the sanitization outline asserted only
		// NEGATIVE properties — no underscore, no dot, valid label — so a
		// mutation that DELETED separators instead of replacing them with
		// hyphens passed: "my_pipe" became "mypipe", still a valid label and
		// still wrong. The positive form is what pins it.
		//
		// The failure carries that rule as its detail: whoever trips it is
		// looking at a name that is still a valid label, and needs telling why
		// that is not enough.
		CheckContains[GeneratedPodName]("the pod name reads {string}",
			"the sanitized name",
			func(in GeneratedPodName) (string, error) { return in.Name, nil },
			func(GeneratedPodName) string {
				return "a separator that is DELETED rather than hyphenated still yields a valid label, " +
					"so the positive form is the assertion that matters"
			}),

		// Keeps its own body: a negative check on a SUBSTRING of one string.
		// CheckNotMember is the negative form for a collection and compares
		// whole elements, so it cannot say that "_" appears nowhere inside a
		// single name.
		brine.DefineCheck[GeneratedPodName](
			"the pod name does not contain {string}",
			func(in GeneratedPodName, p brine.Params, _ *brine.Recorder) error {
				unwanted, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a substring parameter")
				}
				if strings.Contains(in.Name, unwanted) {
					return fmt.Errorf("expected %q not to contain %q", in.Name, unwanted)
				}
				return nil
			},
		),

		// Keeps its own body: a suffix is neither equality nor "mentions
		// anywhere". CheckContains would pass on a name carrying the suffix in
		// the middle, which is the wider rule.
		brine.DefineCheck[GeneratedPodName](
			"the pod name ends with {string}",
			func(in GeneratedPodName, p brine.Params, _ *brine.Recorder) error {
				suffix, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a suffix parameter")
				}
				if !strings.HasSuffix(in.Name, suffix) {
					return fmt.Errorf("expected %q to end with %q", in.Name, suffix)
				}
				return nil
			},
		),

		// The rule the whole sanitization block exists to keep. A Kubernetes
		// DNS label is lowercase alphanumerics and hyphens, at most 63
		// characters, starting and ending alphanumeric — which also rules out
		// the doubled and trailing hyphens the individual cases checked for.
		CheckThat[GeneratedPodName]("the pod name is a valid DNS label",
			func(in GeneratedPodName) error {
				if in.Name == "" {
					return fmt.Errorf("the pod name is empty")
				}
				if len(in.Name) > 63 {
					return fmt.Errorf("a DNS label is at most 63 characters, got %d (%q)", len(in.Name), in.Name)
				}
				if !dnsLabel.MatchString(in.Name) {
					return fmt.Errorf("%q is not a valid DNS label (lowercase alphanumerics and hyphens, "+
						"starting and ending alphanumeric)", in.Name)
				}
				if strings.Contains(in.Name, "--") {
					return fmt.Errorf("%q contains consecutive hyphens", in.Name)
				}
				return nil
			}),
	}
}

// PodNameSegmentDefinitions covers PN-06's per-segment cap.
func PodNameSegmentDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		// The 63-character total is not the whole rule. If one segment could
		// take the entire budget the name would still be a valid label and
		// still be useless: an operator scanning `kubectl get pods` needs to
		// see WHICH JOB the pod belongs to, not just which pipeline.
		CheckThat[GeneratedPodName]("the pod name still identifies its job",
			func(in GeneratedPodName) error {
				parts := strings.Split(in.Name, "-b1-")
				if len(parts) != 2 {
					return fmt.Errorf("expected the name to carry a build segment, got %q", in.Name)
				}
				prefix := parts[0]
				// Two segments capped at 20 each, plus the hyphen between them.
				if len(prefix) > 41 {
					return fmt.Errorf(
						"expected pipeline and job to be capped at 20 characters each (41 with the separator), "+
							"got a %d-character prefix %q", len(prefix), prefix)
				}
				if !strings.Contains(prefix, "-") {
					return fmt.Errorf(
						"expected both a pipeline and a job segment so the pod is findable by job; got only %q", prefix)
				}
				return nil
			}),
	}
}
