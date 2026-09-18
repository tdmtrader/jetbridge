package steps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const liveS3ResourceImage = "index.docker.io/concourse/s3-resource@sha256:03ac6f425ba720ea2d4d2f379a740665eaa2fbf62ad6d14078f5eb241f67d70e"
const liveS3ObjectPath = "releases/v1.0.0/app.tar.gz"

type S3PutOutcome struct {
	worker         WorkerReady
	store          *liveS3Store
	pod            *corev1.Pod
	trace          *execObservation
	payload        []byte
	stdout, stderr string
	result         runtime.ProcessResult
	err            error
}

func S3PutDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[LiveTaskPlan, S3PutOutcome]("the S3 resource publishes its mounted binary input", func(in LiveTaskPlan, _ brine.Params, rec *brine.Recorder) (S3PutOutcome, error) {
			return runLiveS3Put(in, rec)
		}),
		CheckThat[S3PutOutcome]("the put preserves both input mounts and forwards the exact resource response", checkLiveS3Put),
	}
}

func runLiveS3Put(in LiveTaskPlan, rec *brine.Recorder) (S3PutOutcome, error) {
	w, err := newLiveRuntimeWorker(in.Database, rec, 2)
	if err != nil {
		return S3PutOutcome{}, err
	}
	team, err := in.Database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
	if err != nil {
		return S3PutOutcome{}, err
	}
	w.TeamID = team.ID()
	// Keep API credentials out of the resource pod, so admission does not add
	// a token mount to the two input mounts under test. This is a real owned SA.
	noToken := false
	_, err = w.Clientset.CoreV1().ServiceAccounts(w.Namespace).Create(w.Ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "resource-without-api-access"}, AutomountServiceAccountToken: &noToken}, metav1.CreateOptions{})
	if err != nil {
		return S3PutOutcome{}, err
	}
	w.Config.ServiceAccount = "resource-without-api-access"
	store, err := newLiveS3Store(w, rec)
	if err != nil {
		return S3PutOutcome{}, err
	}
	config, err := liveKubernetesConfig()
	if err != nil {
		return S3PutOutcome{}, err
	}
	trace := new(execObservation)
	client, err := kubernetes.NewForConfig(trace.config(config))
	if err != nil {
		return S3PutOutcome{}, err
	}
	raw := w.Executor
	w.Clientset = client
	w.Config.ResourceTypeImages = jetbridge.MergeResourceTypeImages(nil)
	w.Config.ResourceTypeImages["s3"] = liveS3ResourceImage
	w = w.rebuild()
	w.Worker.SetExecutor(jetbridge.NewSPDYExecutor(client, trace.config(config)))
	const handle = "put-multi-input"
	names := []string{"compiled-binary", "release-notes"}
	inputs := make([]runtime.Input, 0, len(names))
	volumes := make([]*jetbridge.Volume, 0, len(names))
	for _, name := range names {
		path := "/tmp/build/put/" + name
		volume := jetbridge.NewDeferredVolume(name, w.Worker.Name(), raw, w.Namespace, "main", path)
		volume.SetPodName(handle)
		inputs = append(inputs, runtime.Input{Artifact: volume, DestinationPath: path})
		volumes = append(volumes, volume)
	}
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx, db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: db.ContainerTypePut},
		runtime.ContainerSpec{TeamID: w.TeamID, Type: db.ContainerTypePut, ImageSpec: runtime.ImageSpec{ResourceType: "s3"}, Inputs: inputs}, nil)
	if err != nil {
		return S3PutOutcome{}, err
	}
	request, err := json.Marshal(map[string]any{
		"source": map[string]any{"bucket": "releases", "regexp": "releases/v1.0.0/(app\\.tar\\.gz)", "access_key_id": store.key, "secret_access_key": store.secret, "endpoint": store.endpoint, "disable_ssl": true, "use_path_style": true, "private": true, "region_name": "us-east-1", "disable_multipart": true},
		"params": map[string]any{"file": "compiled-binary/app.tar.gz"},
	})
	if err != nil {
		return S3PutOutcome{}, err
	}
	var stdout, stderr bytes.Buffer
	process, err := container.Run(w.Ctx, runtime.ProcessSpec{ID: "resource", Path: "/opt/resource/out", Args: []string{"/tmp/build/put"}},
		runtime.ProcessIO{Stdin: bytes.NewReader(request), Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return S3PutOutcome{}, err
	}
	pod, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: client, Namespace: w.Namespace}, handle)
	if err != nil {
		return S3PutOutcome{}, err
	}
	archive, err := tarOfOneFile("app", "binary-"+w.Namespace)
	if err != nil {
		return S3PutOutcome{}, err
	}
	payload, err := io.ReadAll(archive)
	if err != nil {
		return S3PutOutcome{}, err
	}
	// Explicit real-volume uploads establish the premise. This is not a claim
	// that streamInputs (a no-op) or daemon init containers performed staging.
	for i, file := range []struct{ name, body string }{{"app.tar.gz", string(payload)}, {"notes.txt", "notes-" + w.Namespace}} {
		tar, err := plainTarOfOneFile(file.name, file.body)
		if err != nil {
			return S3PutOutcome{}, err
		}
		if err := volumes[i].StreamIn(w.Ctx, ".", nil, 0, bytes.NewReader(tar)); err != nil {
			return S3PutOutcome{}, err
		}
	}
	var notes bytes.Buffer
	if err := raw.ExecInPod(w.Ctx, w.Namespace, handle, "main", []string{"cat", "/tmp/build/put/release-notes/notes.txt"}, nil, &notes, nil, false, jetbridge.ExecAttrs{Purpose: "verify-put-input"}); err != nil || notes.String() != "notes-"+w.Namespace {
		return S3PutOutcome{}, fmt.Errorf("release-notes input not readable: %v", err)
	}
	result, waitErr := process.Wait(w.Ctx)
	return S3PutOutcome{worker: w, store: store, pod: pod, trace: trace, payload: payload, stdout: stdout.String(), stderr: stderr.String(), result: result, err: waitErr}, nil
}

func checkLiveS3Put(in S3PutOutcome) error {
	if in.pod == nil || len(in.pod.Spec.Containers) == 0 || in.pod.Spec.Containers[0].Name != "main" {
		return fmt.Errorf("missing real put main container")
	}
	main := in.pod.Spec.Containers[0]
	if len(main.VolumeMounts) != 2 {
		return fmt.Errorf("put has %d input mounts, want 2: %+v", len(main.VolumeMounts), main.VolumeMounts)
	}
	paths := map[string]bool{"/tmp/build/put/compiled-binary": false, "/tmp/build/put/release-notes": false}
	volumes := map[string]bool{}
	for _, v := range in.pod.Spec.Volumes {
		volumes[v.Name] = true
	}
	for _, m := range main.VolumeMounts {
		seen, ok := paths[m.MountPath]
		if !ok || seen || !volumes[m.Name] {
			return fmt.Errorf("unexpected, duplicate or unbacked put mount: %+v", m)
		}
		paths[m.MountPath] = true
	}
	if in.err != nil || in.result.ExitStatus != 0 {
		return fmt.Errorf("real S3 put failed: exit=%d error=%v stderr=%s", in.result.ExitStatus, in.err, in.stderr)
	}
	// Independent literal oracle for the pinned resource's JSON encoding.
	// Keep every byte, including metadata and the trailing newline.
	want := "{\"version\":{\"path\":\"releases/v1.0.0/app.tar.gz\"},\"metadata\":[{\"name\":\"filename\",\"value\":\"app.tar.gz\"}]}\n"
	if in.stdout != want {
		return fmt.Errorf("resource stdout changed: got %q want %q", in.stdout, want)
	}
	if err := in.trace.requireRuntimeAttempts(in.worker.Namespace, in.pod.Name, 1, 1); err != nil {
		return err
	}
	object, err := in.store.client.GetObject(in.worker.Ctx, &s3.GetObjectInput{Bucket: aws.String("releases"), Key: aws.String(liveS3ObjectPath)})
	if err != nil {
		return fmt.Errorf("uploaded object unavailable: %w", err)
	}
	actual, readErr := io.ReadAll(object.Body)
	closeErr := object.Body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(actual, in.payload) {
		return fmt.Errorf("uploaded object bytes differ: read=%v close=%v", readErr, closeErr)
	}
	pod, err := in.worker.Clientset.CoreV1().Pods(in.worker.Namespace).Get(in.worker.Ctx, in.pod.Name, metav1.GetOptions{})
	if err != nil || pod.UID != in.pod.UID {
		return fmt.Errorf("put pod identity changed: %v", err)
	}
	if main.Image != liveS3ResourceImage || in.pod.Spec.ServiceAccountName != "resource-without-api-access" {
		return fmt.Errorf("resource image or credential-free service account changed")
	}
	fmt.Printf("real S3 put: pod UID %s, two backed input mounts, exact resource stdout, independently downloaded %d matching object bytes\n", pod.UID, len(actual))
	return nil
}
