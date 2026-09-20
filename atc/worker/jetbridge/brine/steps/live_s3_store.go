package steps

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const liveMinIOImage = "quay.io/minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"

type liveS3Store struct {
	client                *s3.Client
	endpoint, key, secret string
}

// A real, namespace-owned MinIO server. No host storage, host port, supplied
// response or user cloud credentials. The independent client uses a loopback
// Kubernetes port-forward; the resource reaches the server's actual PodIP.
func newLiveS3Store(w WorkerReady, rec *brine.Recorder) (*liveS3Store, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	store := &liveS3Store{key: "brine", secret: hex.EncodeToString(random[:])}
	_, err := w.Clientset.CoreV1().Secrets(w.Namespace).Create(w.Ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "object-store-login"},
		StringData: map[string]string{"key": store.key, "secret": store.secret},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	ref := func(key string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "object-store-login"}, Key: key,
		}}
	}
	one, no := int64(1), false
	q := resource.MustParse
	pod, err := w.Clientset.CoreV1().Pods(w.Namespace).Create(w.Ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "object-store"},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &one,
			AutomountServiceAccountToken: &no,
			Containers: []corev1.Container{{
				Name: "main", Image: liveMinIOImage, Command: []string{"minio", "server", "/data", "--console-address", ":9001"},
				Env: []corev1.EnvVar{
					{Name: "MINIO_ROOT_USER", ValueFrom: ref("key")}, {Name: "MINIO_ROOT_PASSWORD", ValueFrom: ref("secret")},
					{Name: "MINIO_BROWSER", Value: "off"}, {Name: "GOMEMLIMIT", Value: "192MiB"}, {Name: "GOMAXPROCS", Value: "2"},
				},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/minio/health/ready", Port: intstr.FromInt32(9000)}}, PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 90},
				VolumeMounts:   []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: q("50m"), corev1.ResourceMemory: q("64Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: q("250m"), corev1.ResourceMemory: q("256Mi")},
				},
			}},
			Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(64*1024*1024, resource.BinarySI)}}}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	ready, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, pod.Name)
	if err != nil {
		return nil, err
	}
	if ready.UID != pod.UID || net.ParseIP(ready.Status.PodIP) == nil {
		return nil, fmt.Errorf("object store lost its real pod identity/address")
	}
	// An early refused connection terminates client-go's forwarder. Let the
	// kubelet prove HTTP readiness before opening the first forwarded stream.
	for {
		current, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(w.Ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if current.UID != pod.UID {
			return nil, fmt.Errorf("object store pod was replaced")
		}
		if len(current.Status.ContainerStatuses) == 1 && current.Status.ContainerStatuses[0].Ready {
			ready = current
			break
		}
		if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
			logs, logErr := w.Clientset.CoreV1().Pods(w.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "main"}).DoRaw(w.Ctx)
			return nil, fmt.Errorf("object store terminated: phase=%s containers=%+v logs=%q log-error=%v", current.Status.Phase, current.Status.ContainerStatuses, logs, logErr)
		}
		select {
		case <-w.Ctx.Done():
			return nil, fmt.Errorf("object store readiness: %w", w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	store.endpoint = "http://" + net.JoinHostPort(ready.Status.PodIP, "9000")
	local, err := forwardLiveS3(w, rec, pod.Name)
	if err != nil {
		return nil, err
	}
	store.client = s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(local), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(store.key, store.secret, ""),
	})
	if _, err := store.client.CreateBucket(w.Ctx, &s3.CreateBucketInput{Bucket: aws.String("releases")}); err != nil {
		return nil, err
	}
	fmt.Printf("real object store: namespace %s pod UID %s image %s; empty releases bucket ready\n", w.Namespace, pod.UID, ready.Spec.Containers[0].Image)
	return store, nil
}

func forwardLiveS3(w WorkerReady, rec *brine.Recorder, pod string) (string, error) {
	config, err := liveKubernetesConfig()
	if err != nil {
		return "", err
	}
	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return "", err
	}
	url := w.Clientset.CoreV1().RESTClient().Post().Resource("pods").Namespace(w.Namespace).Name(pod).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, url)
	stop, ready, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var output liveLogBuffer
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{"0:9000"}, stop, ready, &output, &output)
	if err != nil {
		return "", err
	}
	var forwardErr error
	go func() { forwardErr = forwarder.ForwardPorts(); close(finished) }()
	TrackDisposer(rec, "the object-store port forward", func() error {
		close(stop)
		select {
		case <-finished:
			if forwardErr != nil {
				return fmt.Errorf("object-store forward: %w; %s", forwardErr, output.String())
			}
			return nil
		case <-time.After(20 * time.Second):
			return fmt.Errorf("object-store forward did not drain")
		}
	})
	select {
	case <-ready:
	case <-finished:
		return "", fmt.Errorf("object-store forward ended: %v; %s", forwardErr, output.String())
	case <-w.Ctx.Done():
		return "", w.Ctx.Err()
	}
	ports, err := forwarder.GetPorts()
	if err != nil {
		return "", err
	}
	if len(ports) != 1 || ports[0].Local == 0 || ports[0].Remote != 9000 {
		return "", fmt.Errorf("unexpected object-store forwarding ports: %v", ports)
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ports[0].Local))), nil
}
