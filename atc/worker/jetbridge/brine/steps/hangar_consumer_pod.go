package steps

// What a consuming step's Pod says about a published output.
//
// This is where the learning's split row 2 lands: the consumer's Hangar init
// container's args and mounts are spec, so the pod-shape half of the two Go
// tests at atc/worker/jetbridge/storage_daemonset_test.go moves here. The one
// that DRIVES the generated shell with a fake wget on PATH stays where it is,
// for the reason the plan gives for its sibling: script text is not spec, and
// brine cannot run a pod's shell.
//
// The pod is built through the worker the way the ATC builds one -- the same
// FindOrCreateContainer, the same buildPod, the same BuildFetchInitContainers.
// Nothing here composes a container spec of its own, because a fixture that did
// would be asserting about a pod it built rather than about the one production
// asks the cluster for.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/brine-dev/brine-go/pkg/brine"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
)

// hangarInitName is the init container BuildFetchInitContainers emits for
// Hangar tree inputs. It is named here rather than searched for by substring:
// a check that matched any init container whose name contained "hangar" would
// also match the capture control init, which is a different container with a
// different job.
const hangarInitName = "materialize-hangar-inputs"

// newHangarConsumerWorker is the worker a consuming step runs on: the ordinary
// one, plus the two things a Hangar tree input is refused without.
//
// The signer is the FOUNDATION's strict-input materialization signer, because
// that is what BuildFetchInitContainers uses today and the consumer-pod
// contract is about the pod's shape rather than about which key signs. The
// output plane's own read warrant is asserted where it lives -- over the
// repository and the lease-control endpoints -- and wiring it into the pod
// builder is Phase 8's, beside the chart values that carry the key.
func newHangarConsumerWorker(res brine.Resources, rec *brine.Recorder) (WorkerReady, error) {
	signer, err := hangar.NewWarrantSigner(brineReadWarrantKey, hangar.MaxWarrantTTL, time.Now)
	if err != nil {
		return WorkerReady{}, err
	}

	return newWorkerReady(res, rec, "k8s-worker-consumer", "", func(cfg *jetbridge.Config) {
		cfg.ArtifactDaemonHostPath = "/var/concourse/artifacts"
		cfg.OutputPlaneEnabled = true
		cfg.HangarEnabled = true
		cfg.HangarWarrantSigner = signer
	})
}

// buildConsumerPod runs the consuming step through the worker.
func buildConsumerPod(in ConsumerDraft) (PodCreated, error) {
	if in.Cluster.Worker == nil {
		return PodCreated{}, fmt.Errorf("this chain has no worker to build a consuming pod on")
	}
	ref := in.Tree.Ref
	if err := ref.Validate(); err != nil {
		return PodCreated{}, fmt.Errorf("the published tree has no tree ref to consume: %w", err)
	}

	draft := ContainerDraft{
		Namespace:     in.Cluster.Namespace,
		Worker:        in.Cluster.Worker,
		Clientset:     in.Cluster.Clientset,
		MountExecutor: in.Cluster.ProducerExecutor,
		Ctx:           in.Cluster.Ctx,
		Handle:        "consumer-" + in.StepName,
		StepName:      in.StepName,
		ImageURL:      "busybox",
		Dir:           "/workdir",
		TeamID:        in.Cluster.TeamID,
		ContainerType: db.ContainerTypeTask,
	}

	spec := runtime.ContainerSpec{
		TeamID:    draft.TeamID,
		Dir:       draft.Dir,
		ImageSpec: runtime.ImageSpec{ImageURL: draft.ImageURL},
		// The one input, and it is a TREE rather than an artifact: the ref the
		// capture published, at the destination the consumer asked for.
		Inputs: []runtime.Input{{HangarTree: &ref, DestinationPath: in.Destination}},
	}

	return runDraft(draft, db.ContainerTypeTask, spec, false)
}

// hangarInit finds the verification init container, or says why there is none.
func hangarInit(in PodCreated) (corev1.Container, error) {
	if in.Pod == nil {
		return corev1.Container{}, fmt.Errorf("no pod was built")
	}
	for _, container := range in.Pod.Spec.InitContainers {
		if container.Name == hangarInitName {
			return container, nil
		}
	}

	var names []string
	for _, container := range in.Pod.Spec.InitContainers {
		names = append(names, container.Name)
	}

	return corev1.Container{}, fmt.Errorf("the pod has no %q init container; its inits are %v",
		hangarInitName, names)
}

// verifiesExactlyTheReceipt asserts the init command carries the EXACT expected
// receipt for the tree, and nothing that is not that receipt.
//
// The receipt is compared whole, byte for byte in its encoded form, because
// every field of a TreeRef is part of the identity: a check that looked for the
// digest would pass for a command carrying the right digest at the wrong
// generation, which is the exact confusion a tree ref exists to prevent.
func verifiesExactlyTheReceipt(in PodCreated) error {
	container, err := hangarInit(in)
	if err != nil {
		return err
	}

	command := strings.Join(container.Command, " ")
	ref, err := consumedRef(in)
	if err != nil {
		return err
	}

	receipt, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(receipt)
	if !strings.Contains(command, encoded) {
		return fmt.Errorf("the verification command does not carry the exact receipt for "+
			"%s/%s/%d", ref.Scope, ref.Digest, ref.Generation)
	}

	// And it carries no OTHER generation of the same tree, which is the half a
	// substring check on the digest alone would miss.
	for _, other := range []int64{ref.Generation - 1, ref.Generation + 1, 0} {
		wrong := ref
		wrong.Generation = other
		encodedWrong, err := json.Marshal(wrong)
		if err != nil {
			return err
		}
		if strings.Contains(command,
			base64.StdEncoding.EncodeToString(encodedWrong)) {
			return fmt.Errorf("the verification command also carries a receipt for generation %d",
				other)
		}
	}

	return nil
}

// declaresOneReadOnlyVerificationMountPerTree asserts a COUNT plus ReadOnly,
// not membership: a mount list that contained the right mount and three others
// would pass a membership check and would be a pod with three more things
// mounted into a verification container than the contract allows.
func declaresOneReadOnlyVerificationMountPerTree(in PodCreated, p brine.Params) error {
	want, ok := p.GetInt(0)
	if !ok {
		return fmt.Errorf("expected a mount count parameter")
	}

	container, err := hangarInit(in)
	if err != nil {
		return err
	}

	trees, err := consumedTreeCount(in)
	if err != nil {
		return err
	}
	if trees == 0 {
		return fmt.Errorf("this pod consumes no tree, so a per-tree mount count says nothing")
	}
	if len(container.VolumeMounts) != want*trees {
		return fmt.Errorf("the verification container declares %d mount(s) for %d tree(s), and "+
			"the contract is %d per tree: %+v", len(container.VolumeMounts), trees, want,
			container.VolumeMounts)
	}

	for index, mount := range container.VolumeMounts {
		if !mount.ReadOnly {
			return fmt.Errorf("verification mount %d (%s at %s) is writable; a verification mount "+
				"is the tree the init is checking and it may never be written through",
				index, mount.Name, mount.MountPath)
		}
		fixed := fmt.Sprintf("/hangar-inputs/input-%d", index)
		if mount.MountPath != fixed {
			return fmt.Errorf("verification mount %d is at %s, and the fixed path is %s; a "+
				"verification path derived from anything a caller said is a caller-chosen path",
				index, mount.MountPath, fixed)
		}
	}

	return nil
}

// noUserDestinationInTheVerificationCommand is the absence, and its control is
// the receipt assertion in the same scenario, asserted first.
func noUserDestinationInTheVerificationCommand(in PodCreated) error {
	container, err := hangarInit(in)
	if err != nil {
		return err
	}

	destination, err := consumedDestination(in)
	if err != nil {
		return err
	}
	if destination == "" {
		return fmt.Errorf("this pod consumes nothing, so the absence says nothing")
	}

	command := strings.Join(container.Command, " ")
	if strings.Contains(command, destination) {
		return fmt.Errorf("the user-controlled destination %q entered the verification command; "+
			"every path the init touches is derived by the daemon from a handle and a volume",
			destination)
	}

	return nil
}

// asksForExactlyTheReceiptsTree asserts the request the init issues names the
// ref the capture published, and names no other.
//
// IT IS A CHECK ABOUT THE REQUEST, and the phrase says so. The earlier name --
// "materializes exactly the receipt's tree" -- promised something this tier
// cannot see: the pod is never run here, no bytes move, and the only way to
// stage in this fixture would be a stand-in for the daemon's own read path,
// which the doctrine forbids and which would be asserting the stand-in. The
// staging proof is elsewhere and is real in both places:
// TestAManagedMaterializationStagesTheSameBytesAsAStrictOne walks both
// destinations byte for byte, and AC 19's K3s tier runs the pod.
//
// WHAT "EXACTLY" CAN MEAN IN THIS TIER, AND WHAT IT CANNOT. Two arms, and both
// of them fire: the command carries a materialization request at all, and one
// of those requests names this ref rather than a digest that happens to appear
// nearby.
//
// A third arm refused a command whose request batches did not ALL name the ref,
// and nothing could ever make it fire -- not because no scenario happens to
// build that shape, but because the ENCODING has no such shape. A consumer
// taking N trees produces ONE materialization request carrying N items
// (storage_daemonset.go:311), and the expected receipts beside it are marshalled
// TreeRefs, which carry no "ref" key at all. So the batch count this walked is 1
// whenever it is not 0, and `naming != batches` could only restate the arm above
// it. An arm nothing can redden is not a check, it is a sentence about one --
// the standard the read-lease fence was decided on in round 1 -- so it is gone.
//
// Saying "exactly" in the other direction means reading the request's ITEM LIST
// and comparing it against the set of receipts, over a consumer that takes more
// than one published output. Neither exists in this tier; both arrive with
// Phase 8's composition.
func asksForExactlyTheReceiptsTree(in PodCreated) error {
	container, err := hangarInit(in)
	if err != nil {
		return err
	}
	ref, err := consumedRef(in)
	if err != nil {
		return err
	}

	command := strings.Join(container.Command, " ")
	payload, err := json.Marshal(struct {
		Ref hangar.TreeRef `json:"ref"`
	}{Ref: ref})
	if err != nil {
		return err
	}
	// The request body is base64-encoded into the command. What is asserted is
	// that the encoded batch names this tree ref: the body's own JSON, not a
	// digest that happens to appear.
	fragment := strings.TrimSuffix(strings.TrimPrefix(string(payload), "{"), "}")
	decoded, err := decodedInitPayloads(command)
	if err != nil {
		return err
	}
	naming, batches := 0, 0
	for _, body := range decoded {
		if !strings.Contains(body, `"ref"`) {
			continue
		}
		batches++
		if strings.Contains(body, fragment) {
			naming++
		}
	}
	if batches == 0 {
		return fmt.Errorf("the verification command carries no materialization request at all")
	}
	if naming == 0 {
		return fmt.Errorf("no materialization request in the verification command names %s/%s/%d",
			ref.Scope, ref.Digest, ref.Generation)
	}

	return nil
}

// decodedInitPayloads returns every base64 blob in the command, decoded.
//
// It decodes rather than string-matching because what is under test is the
// REQUEST the daemon will receive, and a command that contained the right
// characters in the wrong structure would pass a textual check.
func decodedInitPayloads(command string) ([]string, error) {
	var payloads []string
	// Every encoded value in the generated script is single-quoted: the request
	// batch as REQUEST_B64='...', each expected receipt as an argument to
	// verify_receipt. Splitting on whitespace would leave the assignment's name
	// attached to its value, so the quotes are what is read.
	for _, quoted := range singleQuotedTokens.FindAllStringSubmatch(command, -1) {
		raw, err := base64.StdEncoding.DecodeString(quoted[1])
		if err != nil {
			continue
		}
		payloads = append(payloads, string(raw))
	}
	if len(payloads) == 0 {
		return nil, fmt.Errorf("the verification command carries no encoded payload at all")
	}

	return payloads, nil
}

var singleQuotedTokens = regexp.MustCompile(`'([^']*)'`)

// consumedRef, consumedTreeCount and consumedDestination read what the pod was
// asked to consume back off the POD rather than out of a field the fixture
// remembered.
//
// That is what makes these assertions about production: the destination is the
// mount the main container got, and the ref is the one the init was told to
// verify, both read from the spec the cluster was handed.
func consumedRef(in PodCreated) (hangar.TreeRef, error) {
	container, err := hangarInit(in)
	if err != nil {
		return hangar.TreeRef{}, err
	}

	payloads, err := decodedInitPayloads(strings.Join(container.Command, " "))
	if err != nil {
		return hangar.TreeRef{}, err
	}
	for _, body := range payloads {
		var batch struct {
			Items []struct {
				Ref hangar.TreeRef `json:"ref"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(body), &batch); err != nil || len(batch.Items) == 0 {
			continue
		}

		return batch.Items[0].Ref, nil
	}

	return hangar.TreeRef{}, fmt.Errorf("the verification command carries no materialization batch")
}

func consumedTreeCount(in PodCreated) (int, error) {
	container, err := hangarInit(in)
	if err != nil {
		return 0, err
	}
	payloads, err := decodedInitPayloads(strings.Join(container.Command, " "))
	if err != nil {
		return 0, err
	}
	for _, body := range payloads {
		var batch struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal([]byte(body), &batch); err != nil || len(batch.Items) == 0 {
			continue
		}

		return len(batch.Items), nil
	}

	return 0, fmt.Errorf("the verification command carries no materialization batch")
}

func consumedDestination(in PodCreated) (string, error) {
	if in.Pod == nil {
		return "", fmt.Errorf("no pod was built")
	}
	for _, container := range in.Pod.Spec.Containers {
		for _, mount := range container.VolumeMounts {
			if strings.HasPrefix(mount.MountPath, "/hangar-inputs/") {
				continue
			}
			if strings.HasPrefix(mount.MountPath, "/tmp/build/") {
				return mount.MountPath, nil
			}
		}
	}

	return "", fmt.Errorf("the main container mounts no consumed destination")
}
