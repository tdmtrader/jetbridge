package jetbridge

// Req 59's twins that are not about a capture pod's shape.
//
// The claim is byte-compatibility: with durable output capture disabled --
// and, for everything a capture does not select, with it ENABLED -- the
// existing resource-cache routes, the fail-open cache behaviour, ordinary task
// execution and the original strict-input capability are what they were.
//
// Most of that claim is already pinned, and this file cites rather than
// restates, because a second description of one behaviour is how two
// descriptions drift:
//
//   - the ordinary POD, plane off versus plane on, reused and fresh:
//     TestTheOutputPlaneChangesNoOrdinaryPodWhenNothingIsCaptured
//     (capture_control_test.go:131), which is a reflect.DeepEqual over the
//     whole spec with the single legitimate difference named.
//   - an unselected output still recording `<handle>/<name>`:
//     TestDaemonSetMode_RecordOutputsPointsTheCapturedOutputAtItsIncarnation
//     (daemonset_integration_test.go:2382), whose control is the unselected
//     output.
//   - post-completion HIJACK, which Req 18 takes away from a capture-held
//     source and leaves alone everywhere else: the refuseIfCaptureHeld specs
//     in exact_execution_test.go:605-780, including the looked-up container
//     with no spec, which is the one path that is hijack.
//   - the durable CACHE tier's miss semantics: brine's
//     `features/daemon-durable.feature` and `features/step-closing.feature`,
//     which the Phase 9 plan cites rather than duplicating.
//   - a capture pod's strict-input init being present and naming its own ref:
//     brine's `hangar-capture-pod.feature`, scenario "A capture-selected step's
//     strict input is untouched by the output plane".
//
// What was left is the one below, and it is the strongest form of the
// strict-input half: not "the init is still there" but "the Pod is the same
// Pod". A brine scenario can say the first; only a comparison of two renders
// can say the second, and the second is what Req 59 actually promises.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	corev1 "k8s.io/api/core/v1"
)

// strictInputRegressionRef is the exact immutable tree the consuming step takes.
var strictInputRegressionRef = hangar.TreeRef{
	Scope:      "regression-scope",
	Digest:     hangar.Digest("sha256:6738ec08b183496d6d90375bbca158e7460d4a8ff61b154c01bead9de12a2fac"),
	Generation: 1725830823000123,
}

// strictInputConfig is the ordinary strict-input deployment, with the output
// plane switched by the caller and NOTHING else different.
func strictInputConfig(t *testing.T, outputPlane bool) Config {
	t.Helper()

	signer, err := hangar.NewWarrantSigner(
		[]byte("0123456789abcdef0123456789abcdef"), hangar.MaxWarrantTTL, time.Now)
	if err != nil {
		t.Fatalf("build the strict-input warrant signer: %v", err)
	}

	cfg := capturePodConfig(outputPlane)
	cfg.HangarEnabled = true
	cfg.HangarWarrantSigner = signer

	return cfg
}

func strictInputContainer(t *testing.T, cfg Config) *Container {
	t.Helper()

	ref := strictInputRegressionRef

	return &Container{
		handle:   "strict-consumer",
		podName:  "strict-consumer-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:       "/tmp/build/task",
			Type:      db.ContainerTypeTask,
			ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
			Inputs:    []runtime.Input{{HangarTree: &ref, DestinationPath: "/tmp/build/exact"}},
		},
		config:         cfg,
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
		properties:     map[string]string{},
	}
}

// Req 59, at its strongest: turning the output plane on does not change one
// byte of a strict-input consumer's Pod -- except the one byte-range that
// CANNOT be equal, which is named rather than papered over.
//
// The strict-input init's command carries a signed materialization warrant, and
// a warrant carries a fresh nonce and a fresh issue/expiry pair on every Sign.
// Two renders can therefore never be byte-identical, and a test that asserted
// they were would fail for a reason that says nothing. Measured at this head:
// the whole-spec diff between plane-off and plane-on is exactly one line, the
// `REQUEST_B64=` assignment, and inside it exactly the nonce and the two
// timestamps.
//
// So the comparison is in two halves, and the FIRST half is the one that
// matters. The request the two renders encode is compared field by field --
// the tree ref, the handle, the volume, and the warrant's own subject -- and
// only then is the encoded blob normalised away and the rest of the spec
// compared for equality. An output plane that re-derived the volume name,
// renamed the handle, reordered the inputs or signed a different ref would be
// caught by the first half; one that changed a mount, a volume, an affinity or
// a security context would be caught by the second.
func TestEnablingTheOutputPlaneChangesNoStrictInputPod(t *testing.T) {
	cfgOff := strictInputConfig(t, false)
	cfgOn := strictInputConfig(t, true)

	off, err := strictInputContainer(t, cfgOff).
		buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
	if err != nil {
		t.Fatalf("building the strict-input pod with the output plane off: %v", err)
	}
	on, err := strictInputContainer(t, cfgOn).
		buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
	if err != nil {
		t.Fatalf("building the strict-input pod with the output plane on: %v", err)
	}

	// Half one: what the two renders ASK FOR, and what the warrant in each one
	// AUTHORISES. Not the warrant's bytes -- those carry a nonce and two
	// timestamps and are unequal by construction -- but its subject, which is
	// the thing a re-derived volume name or a renamed handle would change.
	offRequest := strictInputSubject(t, off)
	onRequest := strictInputSubject(t, on)
	if !reflect.DeepEqual(offRequest, onRequest) {
		t.Errorf("enabling the output plane changed what the strict-input init asks for, or "+
			"what its warrant authorises:\n off: %+v\n  on: %+v", offRequest, onRequest)
	}
	// Non-vacuity: the comparison above would pass on two empty subjects.
	if offRequest.Ref != strictInputRegressionRef {
		t.Fatalf("the plane-off render's materialization request names %v, not the ref the "+
			"step declared (%v); this comparison is looking at the wrong thing",
			offRequest.Ref, strictInputRegressionRef)
	}
	if offRequest.Handle == "" || offRequest.Volume == "" {
		t.Fatalf("the plane-off request is incomplete (%+v); two incomplete requests compare "+
			"equal and say nothing", offRequest)
	}
	if offRequest.WarrantRef != offRequest.Ref || offRequest.WarrantVolume != offRequest.Volume {
		t.Fatalf("the plane-off warrant authorises %v into %q while the request asks for %v into "+
			"%q; the warrant is what the daemon checks, so a subject that does not match the "+
			"request is a materialization the daemon refuses at task startup",
			offRequest.WarrantRef, offRequest.WarrantVolume, offRequest.Ref, offRequest.Volume)
	}

	// Half two: everything else, with the one unequal-by-construction blob
	// normalised to a constant in BOTH renders.
	normaliseWarrant(off)
	normaliseWarrant(on)
	if !reflect.DeepEqual(off.Spec, on.Spec) {
		t.Errorf("enabling the output plane changed a strict-input consumer's pod somewhere "+
			"other than the signed warrant. Req 59 says the original strict-input capability is "+
			"byte-compatible, and this step selects no capture at all.\n off: %+v\n  on: %+v",
			off.Spec, on.Spec)
	}
}

// materializationItem is the init's request, as encoded.
type materializationItem struct {
	Ref     hangar.TreeRef `json:"ref"`
	Handle  string         `json:"handle"`
	Volume  string         `json:"volume"`
	Warrant string         `json:"warrant"`
}

// warrantSubject is the stable half of a signed materialization warrant: what it
// authorises, without the nonce and the two timestamps that make every token
// unique. Comparing this rather than the token is what lets Req 59's claim be
// asserted at all.
type warrantSubject struct {
	Domain  string         `json:"domain"`
	Version int            `json:"version"`
	Ref     hangar.TreeRef `json:"ref"`
	Handle  string         `json:"handle"`
	Volume  string         `json:"volume"`
}

// requestSubject is the whole comparable claim: the request, plus what its
// warrant authorises.
type requestSubject struct {
	Ref            hangar.TreeRef
	Handle         string
	Volume         string
	WarrantDomain  string
	WarrantVersion int
	WarrantRef     hangar.TreeRef
	WarrantHandle  string
	WarrantVolume  string
}

// strictInputSubject decodes the request AND its warrant into the comparable
// claim.
func strictInputSubject(t *testing.T, pod *corev1.Pod) requestSubject {
	t.Helper()

	item := strictInputRequest(t, pod)
	token := strings.TrimPrefix(item.Warrant, "Bearer ")
	if token == item.Warrant {
		t.Fatalf("the warrant is not a bearer token: %q", item.Warrant)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(token)
	}
	if err != nil {
		t.Fatalf("decoding the warrant %q: %v", token, err)
	}

	// A warrant is a JSON claim set with its signature appended, so the decoder
	// reads the first value and leaves the rest. Unmarshal would refuse the
	// trailing bytes.
	var subject warrantSubject
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&subject); err != nil {
		t.Fatalf("parsing the warrant's claims from %s: %v", raw, err)
	}

	return requestSubject{
		Ref: item.Ref, Handle: item.Handle, Volume: item.Volume,
		WarrantDomain: subject.Domain, WarrantVersion: subject.Version,
		WarrantRef: subject.Ref, WarrantHandle: subject.Handle, WarrantVolume: subject.Volume,
	}
}

var strictRequestPattern = regexp.MustCompile(`REQUEST_B64='([^']+)'`)

// strictInputRequest decodes the one materialization request the strict-input
// init carries, so the comparison is over the REQUEST the daemon will receive
// rather than over the characters that happen to encode it.
func strictInputRequest(t *testing.T, pod *corev1.Pod) materializationItem {
	t.Helper()

	for _, init := range pod.Spec.InitContainers {
		if init.Name != "materialize-hangar-inputs" {
			continue
		}
		match := strictRequestPattern.FindStringSubmatch(strings.Join(init.Command, " "))
		if match == nil {
			t.Fatalf("the strict-input init carries no encoded request:\n%v", init.Command)
		}
		raw, err := base64.StdEncoding.DecodeString(match[1])
		if err != nil {
			t.Fatalf("decoding the strict-input request: %v", err)
		}
		var body struct {
			Items []materializationItem `json:"items"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("parsing the strict-input request %s: %v", raw, err)
		}
		if len(body.Items) != 1 {
			t.Fatalf("the strict-input init asks for %d trees; this step declared one",
				len(body.Items))
		}

		return body.Items[0]
	}

	t.Fatal("the pod has no materialize-hangar-inputs init container")

	return materializationItem{}
}

// normaliseWarrant replaces the encoded request with a constant, because a signed
// warrant carries a fresh nonce and fresh timestamps and can never be equal
// across two calls. What it encodes is compared separately, above.
func normaliseWarrant(pod *corev1.Pod) {
	for i, init := range pod.Spec.InitContainers {
		for j, argument := range init.Command {
			pod.Spec.InitContainers[i].Command[j] = strictRequestPattern.
				ReplaceAllString(argument, "REQUEST_B64='<signed, compared separately>'")
		}
	}
}

// And the fail-closed half, which no comparison can reach: a strict-input ref
// the caller got wrong is refused at BUILD time, with the plane on, rather than
// becoming an empty directory the task discovers at runtime.
//
// It is here rather than in the strict-input foundation's own suite because
// what is under test is the output plane's effect on it: the foundation proves
// the refusal, and this proves the refusal survives the extension.
func TestAnInvalidStrictInputRefIsStillRefusedWithTheOutputPlaneOn(t *testing.T) {
	cfg := strictInputConfig(t, true)

	// The control, first: a well-formed ref builds.
	if _, err := strictInputContainer(t, cfg).
		buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil); err != nil {
		t.Fatalf("a well-formed strict input was refused with the plane on: %v", err)
	}

	for name, broken := range map[string]hangar.TreeRef{
		"no generation": {
			Scope:  strictInputRegressionRef.Scope,
			Digest: strictInputRegressionRef.Digest,
		},
		"no digest": {
			Scope:      strictInputRegressionRef.Scope,
			Generation: strictInputRegressionRef.Generation,
		},
		"no scope": {
			Digest:     strictInputRegressionRef.Digest,
			Generation: strictInputRegressionRef.Generation,
		},
	} {
		ref := broken
		container := strictInputContainer(t, cfg)
		container.containerSpec.Inputs = []runtime.Input{
			{HangarTree: &ref, DestinationPath: "/tmp/build/exact"},
		}
		if _, err := container.buildPod(
			runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil); err == nil {
			t.Errorf("a strict input with %s built a pod. A tree ref that does not validate "+
				"is a materialization the daemon will refuse, and a pod that carries one "+
				"turns a caller's mistake into an empty directory the task finds at runtime",
				name)
		}
	}
}
