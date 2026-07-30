package jetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/outputbuilder"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestManagedAgentBrokerGetsOnlyBrokerPrivateWorkspaceAndCredentialMounts(t *testing.T) {
	spec := managedAgentBrokerPodSpec(t)
	c := newPrivateMountTestContainer(fake.NewSimpleClientset(), spec)
	pod, err := c.buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"/bin/sh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Labels["concourse.ci/agent-broker"] != "true" {
		t.Fatalf("broker label = %#v", pod.Labels)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("broker pod must not automount a service-account token")
	}
	if len(pod.Spec.Containers) != 2 {
		t.Fatalf("containers = %d, want main and broker", len(pod.Spec.Containers))
	}
	main, companion := pod.Spec.Containers[0], pod.Spec.Containers[1]
	if len(main.Env) != 2 || main.Env[0].Name != runtime.ManagedAgentBrokerMarkerEnv || main.Env[0].Value != "1" ||
		main.Env[1].Name != runtime.ManagedAgentBrokerTokenFileEnv {
		t.Fatalf("main marker = %#v", main.Env)
	}
	parentAccessFound := false
	for _, mount := range main.VolumeMounts {
		if mount.MountPath == runtime.ManagedAgentBrokerParentMountRoot {
			parentAccessFound = true
			continue
		}
		if strings.Contains(mount.MountPath, "agent-broker") || mount.MountPath == runtime.ManagedAgentBrokerScratchMountPath || mount.MountPath == runtime.ManagedAgentBrokerWorkspaceMountPath {
			t.Fatalf("broker-only mount leaked to main: %#v", mount)
		}
	}
	if !parentAccessFound {
		t.Fatalf("main mounts = %#v, missing broker parent access", main.VolumeMounts)
	}
	got := map[string]corev1.VolumeMount{}
	for _, mount := range companion.VolumeMounts {
		got[mount.MountPath] = mount
	}
	for _, path := range []string{
		runtime.ManagedAgentBrokerAuthorityMountRoot + "/" + runtime.ManagedAgentBrokerAuthorityFile,
		runtime.ManagedAgentBrokerAuthorityMountRoot + "/" + runtime.ManagedAgentBrokerBootstrapFile,
		runtime.ManagedAgentBrokerAuthorityMountRoot + "/" + runtime.ManagedAgentBrokerMCPAccessFile,
		runtime.ManagedAgentBrokerWorkspaceMountPath,
		runtime.ManagedAgentBrokerScratchMountPath,
		runtime.ManagedAgentBrokerCredentialMountRoot + "/shared",
	} {
		if _, found := got[path]; !found {
			t.Fatalf("broker mounts = %#v, missing %q", companion.VolumeMounts, path)
		}
	}
	if !got[runtime.ManagedAgentBrokerWorkspaceMountPath].ReadOnly ||
		!got[runtime.ManagedAgentBrokerCredentialMountRoot+"/shared"].ReadOnly ||
		got[runtime.ManagedAgentBrokerScratchMountPath].ReadOnly {
		t.Fatalf("broker mount modes = %#v", got)
	}
	if len(companion.VolumeMounts) != 6 {
		t.Fatalf("broker inherited generic mounts: %#v", companion.VolumeMounts)
	}
	if companion.SecurityContext == nil || companion.SecurityContext.RunAsNonRoot == nil || !*companion.SecurityContext.RunAsNonRoot ||
		companion.SecurityContext.ReadOnlyRootFilesystem == nil || !*companion.SecurityContext.ReadOnlyRootFilesystem ||
		companion.SecurityContext.AllowPrivilegeEscalation == nil || *companion.SecurityContext.AllowPrivilegeEscalation ||
		len(companion.SecurityContext.Capabilities.Drop) != 1 || companion.SecurityContext.Capabilities.Drop[0] != "ALL" ||
		companion.SecurityContext.SeccompProfile == nil || companion.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("broker security = %#v", companion.SecurityContext)
	}
	if companion.ReadinessProbe == nil || companion.ReadinessProbe.Exec == nil ||
		len(companion.ReadinessProbe.Exec.Command) != 2 ||
		companion.ReadinessProbe.Exec.Command[0] != "/usr/local/bin/agent-broker" ||
		companion.ReadinessProbe.Exec.Command[1] != "healthcheck" {
		t.Fatalf("broker readiness = %#v", companion.ReadinessProbe)
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "agent-broker-scratch" {
			if volume.EmptyDir == nil || volume.EmptyDir.SizeLimit == nil || volume.EmptyDir.SizeLimit.Value() != spec.ManagedAgentBroker.ScratchSizeBytes {
				t.Fatalf("broker scratch volume = %#v", volume)
			}
		}
		if volume.Name == "agent-broker-credential-0" {
			if volume.Secret == nil || volume.Secret.SecretName != "broker-provider" || len(volume.Secret.Items) != 1 || volume.Secret.Items[0].Key != "token" {
				t.Fatalf("broker credential volume = %#v", volume)
			}
		}
	}
}

func managedAgentBrokerPodSpec(t *testing.T) runtime.ContainerSpec {
	t.Helper()
	profile := broker.Profile{
		ID: "review-balanced", Revision: 1,
		Selector:    broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools:       []broker.Tool{broker.ToolRequestReview},
		WorkerImage: "registry.example/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:     broker.AdapterSpec{Name: broker.AdapterCodex, Version: "1.2.3"},
		Provider:    broker.ProviderSpec{Name: "provider", Model: "model"}, NativeEffort: "high",
		InstructionsDigest: "sha256:" + strings.Repeat("b", 64), CredentialSlot: "shared",
		Limits:   broker.Limits{Timeout: time.Minute, MaxInputBytes: 1024, MaxOutputBytes: 1024},
		Controls: broker.Controls{ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true, NativeOutputSchema: true, IgnoresUserConfig: true},
	}
	catalog, err := broker.NewCatalog([]broker.Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := catalog.Resolve(broker.ToolRequestReview, profile.Selector)
	configuredProfile := resolved
	configuredProfile.Digest = ""
	raw, _ := json.Marshal(map[string]any{
		"authority_endpoint":        "http://concourse-web/api/v1/internal",
		"bootstrap_capability_file": runtime.ManagedAgentBrokerAuthorityMountRoot + "/" + runtime.ManagedAgentBrokerBootstrapFile,
		"mcp_access_token_file":     runtime.ManagedAgentBrokerAuthorityMountRoot + "/" + runtime.ManagedAgentBrokerMCPAccessFile,
		"workspace_root":            runtime.ManagedAgentBrokerWorkspaceMountPath,
		"scratch_root":              runtime.ManagedAgentBrokerScratchMountPath,
		"adapter_binaries":          map[string]string{"codex": "/opt/bin/codex", "claude": "/opt/bin/claude", "cursor-agent": "/opt/bin/cursor-agent"},
		"output_schemas":            map[string]string{"request_review": "/opt/schemas/review.json", "consult_agent": "/opt/schemas/consultation.json"},
		"credential_slots":          map[string]string{"shared": runtime.ManagedAgentBrokerCredentialMountRoot + "/shared"},
		"instructions": map[string]any{
			"request_review": map[string]string{"path": "/opt/instructions/review.md", "digest": profile.InstructionsDigest},
			"consult_agent":  map[string]string{"path": "/opt/instructions/consult.md", "digest": profile.InstructionsDigest},
		},
		"attachments": map[string]any{}, "profiles": []broker.Profile{configuredProfile},
		"profile_digests": map[string]string{resolved.ID: resolved.Digest},
		"capture_limits":  map[string]any{"MaxPatchBytes": 1024, "MaxEntries": 100, "StabilityAttempts": 2},
	})
	return runtime.ContainerSpec{
		Type: db.ContainerTypeAgent, Hermetic: true, Dir: "/work",
		ImageSpec: runtime.ImageSpec{ImageURL: "registry.example/agent@sha256:" + strings.Repeat("c", 64)},
		Env: []string{
			runtime.ManagedAgentBrokerMarkerEnv + "=1",
			runtime.ManagedAgentBrokerTokenFileEnv + "=" + runtime.ManagedAgentBrokerParentMountRoot + "/" + runtime.ManagedAgentBrokerParentAccessFile,
		},
		Inputs:  []runtime.Input{{DestinationPath: "/work/workspace"}},
		Outputs: runtime.OutputPaths{"flight": "/work/flight"}, Caches: []string{"/work/cache"},
		PrivateFileMounts: []runtime.PrivateFileMount{{
			MountPath: runtime.ManagedAgentBrokerParentMountRoot,
			Files:     map[string][]byte{runtime.ManagedAgentBrokerParentAccessFile: []byte("parent-access")},
		}},
		Sidecars: []atc.SidecarConfig{{Name: runtime.ManagedAgentBrokerName, Image: resolved.WorkerImage, Command: []string{"/usr/local/bin/agent-broker"}, WorkingDir: "/", Ports: []atc.SidecarPort{{ContainerPort: runtime.ManagedAgentBrokerPort, Protocol: "TCP"}}}},
		ManagedAgentBroker: &runtime.ManagedAgentBroker{
			Authority: runtime.PrivateFileMount{MountPath: runtime.ManagedAgentBrokerAuthorityMountRoot, Files: map[string][]byte{
				runtime.ManagedAgentBrokerAuthorityFile: raw, runtime.ManagedAgentBrokerBootstrapFile: []byte("capability"),
				runtime.ManagedAgentBrokerMCPAccessFile: []byte("parent-access"),
			}},
			ParentAccess: runtime.PrivateFileMount{
				MountPath: runtime.ManagedAgentBrokerParentMountRoot,
				Files:     map[string][]byte{runtime.ManagedAgentBrokerParentAccessFile: []byte("parent-access")},
			},
			WorkspaceInputPath: "/work/workspace", ScratchSizeBytes: 1 << 30,
			Credentials: []runtime.SecretKeyMount{{Slot: "shared", SecretName: "broker-provider", Key: "token", MountPath: runtime.ManagedAgentBrokerCredentialMountRoot + "/shared"}},
			Resources:   atc.SidecarResources{Requests: atc.SidecarResourceList{CPU: "100m", Memory: "128Mi"}, Limits: atc.SidecarResourceList{CPU: "1", Memory: "1Gi"}},
		},
	}
}

func privateMountTestSpec(path string) runtime.ContainerSpec {
	return runtime.ContainerSpec{
		ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
		Dir:       "/work",
		PrivateFileMounts: []runtime.PrivateFileMount{{
			MountPath: path,
			Files:     map[string][]byte{"profile.yml": []byte("trusted")},
		}},
	}
}

func newPrivateMountTestContainer(clientset *fake.Clientset, spec runtime.ContainerSpec) *Container {
	clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		if pod.UID == "" {
			pod.UID = "pod-uid"
		}
		return false, nil, nil
	})
	return newContainer(
		"private-mount-test",
		taskMetadata(),
		spec,
		nil,
		clientset,
		permEmptyDirConfig(),
		"worker-1",
		nil,
		nil,
		nil,
		false,
	)
}

func TestCreatePodWithoutPrivateMountsDoesNotRequirePodUIDOrCreateSecrets(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	c := newContainer(
		"zero-private-mounts", taskMetadata(),
		runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}},
		nil, clientset, permEmptyDirConfig(), "worker-1", nil, nil, nil, false,
	)

	pod, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"})
	if err != nil {
		t.Fatalf("createPod without private mounts: %v", err)
	}
	if pod.UID != "" {
		t.Fatalf("fake client unexpectedly assigned UID %q", pod.UID)
	}
	secrets, err := clientset.CoreV1().Secrets("test-ns").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("zero private mounts created Secrets: %#v", secrets.Items)
	}
}

func TestManagedOutputBuilderMountsOnePrivateAuthorityFileOnlyInBuilder(t *testing.T) {
	pinnedImage := "registry.example.test/agent@sha256:" + strings.Repeat("a", 64)
	digest := snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	authorityBytes, err := json.Marshal(outputbuilder.NodeAuthority{
		WorkRoot: "/work",
		Inputs: map[string]outputbuilder.InputAuthority{"change": {
			Ref: snapshot.SnapshotRef{ID: 1, Type: "repository-change/v1", Digest: digest}, MountRoot: "/work/change", Exposure: snapshot.FullTreeExposure("/work/change", digest),
		}},
		Outputs: map[string]outputbuilder.OutputAuthority{"review": {Port: snapshot.Port{Name: "review", Type: "review/v1"}, MountRoot: "/work/review"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := runtime.ContainerSpec{
		Type: db.ContainerTypeAgent, ImageSpec: runtime.ImageSpec{ImageURL: pinnedImage}, Dir: "/work",
		Inputs:       []runtime.Input{{DestinationPath: "/work/change"}},
		Outputs:      runtime.OutputPaths{"review": "/work/review", "flight": "/work/flight"},
		Caches:       []string{"/work/cache"},
		ScratchPaths: []string{"/work/scratch"},
		SecretMounts: []runtime.SecretMount{{SecretName: "ordinary", MountPath: "/work/ordinary-secret"}},
		Sidecars: []atc.SidecarConfig{
			{Name: "observer", Image: "busybox:latest"},
			{Name: runtime.ManagedOutputBuilderName, Image: pinnedImage, Command: []string{"/usr/local/bin/agent-output", "serve"}, Ports: []atc.SidecarPort{{ContainerPort: 7783, Protocol: "TCP"}}, WorkingDir: "/"},
		},
		ManagedOutputBuilder: &runtime.ManagedOutputBuilder{
			Authority:       runtime.PrivateFileMount{MountPath: runtime.ManagedOutputBuilderAuthorityMountRoot, Files: map[string][]byte{runtime.ManagedOutputBuilderAuthorityFile: authorityBytes}},
			InputMountPaths: []string{"/work/change"}, OutputMountPaths: []string{"/work/review"},
		},
	}
	c := newPrivateMountTestContainer(fake.NewSimpleClientset(), spec)
	pod, err := c.buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"/bin/sh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("managed builder pod must disable service-account token")
	}
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		if mount.MountPath == runtime.ManagedOutputBuilderAuthorityMountRoot || strings.Contains(mount.MountPath, "output-builder/authority.json") {
			t.Fatal("managed authority leaked to main")
		}
	}
	observer := pod.Spec.Containers[1]
	for _, mount := range observer.VolumeMounts {
		if strings.Contains(mount.MountPath, "output-builder") {
			t.Fatalf("ordinary sidecar received managed authority %q", mount.MountPath)
		}
	}
	builder := pod.Spec.Containers[2]
	authority := findMountByPath(t, builder.VolumeMounts, runtime.ManagedOutputBuilderAuthorityMountRoot+"/"+runtime.ManagedOutputBuilderAuthorityFile)
	if authority.SubPath != runtime.ManagedOutputBuilderAuthorityFile || !authority.ReadOnly {
		t.Fatalf("authority mount = %#v, want readonly authority.json subPath", authority)
	}
	got := map[string]corev1.VolumeMount{}
	for _, mount := range builder.VolumeMounts {
		got[mount.MountPath] = mount
	}
	if len(got) != 3 || !got["/work/change"].ReadOnly || got["/work/review"].ReadOnly || got["/work/flight"].Name != "" || got["/work/cache"].Name != "" || got["/work/scratch"].Name != "" || got["/work/ordinary-secret"].Name != "" || got["/work"].Name != "" {
		t.Fatalf("managed builder mounts = %#v; want only typed input, typed output, and authority", builder.VolumeMounts)
	}
}

func TestManagedOutputBuilderMountsRejectAliasedTypedVolume(t *testing.T) {
	_, err := managedOutputBuilderMounts(runtime.ManagedOutputBuilder{
		InputMountPaths: []string{"/work/change"},
	}, []corev1.VolumeMount{
		{Name: "typed-input", MountPath: "/work/change"},
		{Name: "typed-input", MountPath: "/work/ordinary-secret"},
	}, []corev1.Volume{{Name: "typed-input", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}})
	if err == nil || !strings.Contains(err.Error(), "shares runtime volume") {
		t.Fatalf("aliased typed volume error = %v, want fail-closed alias rejection", err)
	}
}

func TestManagedOutputBuilderMountsRejectDaemonSetVolumeSourceAliases(t *testing.T) {
	for _, outputName := range []string{"dir", "input-1"} {
		t.Run(outputName, func(t *testing.T) {
			spec := managedOutputBuilderAliasSpec(t, outputName)
			c := newPrivateMountTestContainer(fake.NewSimpleClientset(), spec)
			c.config = testDaemonConfig()
			c.storageBackend = NewDaemonSetBackend(c.config, nil, nil)

			_, err := c.buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"/bin/sh"}, nil)
			if err == nil || !strings.Contains(err.Error(), "aliases pod volume") {
				t.Fatalf("DaemonSet output %q alias error = %v, want fail-closed pod-volume rejection", outputName, err)
			}
		})
	}
}

func managedOutputBuilderAliasSpec(t *testing.T, outputName string) runtime.ContainerSpec {
	t.Helper()
	pinnedImage := "registry.example.test/agent@sha256:" + strings.Repeat("a", 64)
	digest := snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	authorityBytes, err := json.Marshal(outputbuilder.NodeAuthority{
		WorkRoot: "/work",
		Inputs: map[string]outputbuilder.InputAuthority{"change": {
			Ref: snapshot.SnapshotRef{ID: 1, Type: "repository-change/v1", Digest: digest}, MountRoot: "/work/change", Exposure: snapshot.FullTreeExposure("/work/change", digest),
		}},
		Outputs: map[string]outputbuilder.OutputAuthority{outputName: {Port: snapshot.Port{Name: outputName, Type: "review/v1"}, MountRoot: "/work/" + outputName}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime.ContainerSpec{
		Type: db.ContainerTypeAgent, ImageSpec: runtime.ImageSpec{ImageURL: pinnedImage}, Dir: "/work",
		Inputs: []runtime.Input{{DestinationPath: "/work/change"}}, Outputs: runtime.OutputPaths{outputName: "/work/" + outputName},
		Sidecars: []atc.SidecarConfig{{Name: runtime.ManagedOutputBuilderName, Image: pinnedImage, Command: []string{"/usr/local/bin/agent-output", "serve"}, Ports: []atc.SidecarPort{{ContainerPort: 7783, Protocol: "TCP"}}, WorkingDir: "/"}},
		ManagedOutputBuilder: &runtime.ManagedOutputBuilder{
			Authority:       runtime.PrivateFileMount{MountPath: runtime.ManagedOutputBuilderAuthorityMountRoot, Files: map[string][]byte{runtime.ManagedOutputBuilderAuthorityFile: authorityBytes}},
			InputMountPaths: []string{"/work/change"}, OutputMountPaths: []string{"/work/" + outputName},
		},
	}
}

func TestAmbiguousPodCreateErrorRetainsTrustedSecretUntilReaperConfirmsAbsence(t *testing.T) {
	for _, mode := range []struct {
		name   string
		create func(*Container, context.Context, runtime.ProcessSpec) (*corev1.Pod, error)
	}{
		{name: "ordinary", create: (*Container).createPod},
		{name: "pause", create: (*Container).createPausePod},
	} {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			clientset := fake.NewSimpleClientset()
			c := newPrivateMountTestContainer(clientset, privateMountTestSpec("/run/concourse/dev-validation"))
			c.privateMountNameGenerator = func() (string, error) { return "concourse-private-ambiguous-" + mode.name, nil }
			createErr := errors.New("ambiguous pod create transport error")
			clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
				requested := action.(ktesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
				requested.UID = types.UID("persisted-" + mode.name)
				if err := clientset.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), requested, "test-ns"); err != nil {
					t.Fatalf("persist attempted pod: %v", err)
				}
				return true, nil, createErr
			})

			_, err := mode.create(c, ctx, runtime.ProcessSpec{Path: "/bin/sh"})
			if !errors.Is(err, createErr) {
				t.Fatalf("create error = %v, want original %v", err, createErr)
			}
			if _, err := clientset.CoreV1().Pods("test-ns").Get(ctx, c.podName, metav1.GetOptions{}); err != nil {
				t.Fatalf("ambiguous create did not retain attempted Pod: %v", err)
			}
			secret, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, c.privateMountSecretName(0), metav1.GetOptions{})
			if err != nil {
				t.Fatalf("ambiguous create deleted trusted Secret: %v", err)
			}
			if secret.Immutable == nil || !*secret.Immutable || string(secret.Data["profile.yml"]) != "trusted" {
				t.Fatalf("ambiguous create retained wrong Secret: %#v", secret)
			}
		})
	}
}

func TestPrivateMountCollisionIsFailClosedAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	c := newPrivateMountTestContainer(clientset, privateMountTestSpec("/run/concourse/dev-validation"))
	c.privateMountNameGenerator = func() (string, error) { return "concourse-private-collision", nil }

	// The pre-existing Secret simulates a concurrent or hostile substitution.
	// A private mount must never read, update, or adopt it.
	name := "concourse-private-collision"
	_, err := clientset.CoreV1().Secrets("test-ns").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-ns"},
		Data:       map[string][]byte{"profile.yml": []byte("attacker")},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
		t.Fatal("expected private Secret collision to fail closed")
	}
	secret, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(secret.Data["profile.yml"]); got != "attacker" {
		t.Fatalf("collision Secret was overwritten: got %q", got)
	}
}

func TestPrivateMountTrustedSecretAlreadyExistsWhenPodBecomesVisible(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	c := newPrivateMountTestContainer(clientset, privateMountTestSpec("/run/concourse/dev-validation"))
	c.privateMountNameGenerator = func() (string, error) { return "concourse-private-race", nil }
	var exposedSecretName string
	clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		for _, volume := range pod.Spec.Volumes {
			if volume.Secret == nil || volume.Secret.SecretName != "concourse-private-race" {
				continue
			}
			exposedSecretName = volume.Secret.SecretName
		}
		return false, nil, nil
	})
	pod, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	if exposedSecretName == "" {
		t.Fatal("Pod did not expose the expected private Secret name")
	}
	// The fake reactor above is the adversarial observer that sees the Pod's
	// volume reference. Once that Pod is visible, its competing Create cannot
	// win because this implementation already created the immutable Secret.
	_, attackerErr := clientset.CoreV1().Secrets("test-ns").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: exposedSecretName, Namespace: "test-ns"},
		Data:       map[string][]byte{"profile.yml": []byte("attacker")},
	}, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(attackerErr) {
		t.Fatalf("attacker Create after Pod visibility = %v, want AlreadyExists", attackerErr)
	}
	secret, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, privateSecretName(t, pod), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["profile.yml"]) != "trusted" || secret.Immutable == nil || !*secret.Immutable {
		t.Fatalf("Pod did not retain trusted immutable Secret: %#v", secret)
	}
}

func TestPrivateMountSecretIsImmutableBeforePodAndCASBoundAfterCreate(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.UID = "pod-uid"
		return false, nil, nil
	})
	var created *corev1.Secret
	clientset.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, kruntime.Object, error) {
		created = action.(ktesting.CreateAction).GetObject().(*corev1.Secret).DeepCopy()
		return false, nil, nil
	})
	c := newPrivateMountTestContainer(clientset, privateMountTestSpec("/run/concourse/dev-validation"))

	pod, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	if created == nil {
		t.Fatal("expected private Secret Create")
	}
	if created.Immutable == nil || !*created.Immutable {
		t.Fatal("private Secret must be immutable at Create")
	}
	if len(created.OwnerReferences) != 0 || created.Labels[privateMountPodNameLabelKey] != c.podName {
		t.Fatalf("private Secret must be an ownerless pre-Pod object: %#v", created.ObjectMeta)
	}
	secretName := privateSecretName(t, pod)
	bound, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.OwnerReferences) != 1 || bound.OwnerReferences[0].UID != "pod-uid" || bound.Labels[privateMountPodUIDLabelKey] != "pod-uid" {
		t.Fatalf("private Secret was not CAS-bound to exact pod: %#v", bound.ObjectMeta)
	}
}

func TestPrivateMountCreatesSecretsBeforePodAndCleansPartialPrePodFailure(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	var order []string
	clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
		order = append(order, "pod")
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.UID = "pod-uid"
		return false, nil, nil
	})
	clientset.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, kruntime.Object, error) {
		order = append(order, "secret")
		if len(order) == 2 {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("second Secret failed"))
		}
		return false, nil, nil
	})
	c := newPrivateMountTestContainer(clientset, runtime.ContainerSpec{
		ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
		PrivateFileMounts: []runtime.PrivateFileMount{
			{MountPath: "/run/concourse/dev-validation", Files: map[string][]byte{"profile.yml": []byte("trusted")}},
			{MountPath: "/run/concourse/dev-validation-extra", Files: map[string][]byte{"config.yml": []byte("trusted")}},
		},
	})

	if _, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
		t.Fatal("expected partial Secret creation failure")
	}
	if len(order) != 2 || order[0] != "secret" || order[1] != "secret" {
		t.Fatalf("trusted Secrets must be created before Pod visibility: %v", order)
	}
	// A fake client deletes a Pod immediately. Real clusters normally retain it
	// during termination; the implementation must not independently delete the
	// created Secret before the Pod is confirmed absent.
	secrets, err := clientset.CoreV1().Secrets("test-ns").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("orphan cleanup after confirmed pod absence should remove Secret: %#v", secrets.Items)
	}
}

func TestPrivateMountPrePodPartialFailureCleansOnlyCreatedSecrets(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, kruntime.Object, error) {
		if action.(ktesting.CreateAction).GetObject().(*corev1.Secret).Name == "concourse-private-second" {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("second Secret failed"))
		}
		return false, nil, nil
	})
	c := newPrivateMountTestContainer(clientset, runtime.ContainerSpec{
		ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
		PrivateFileMounts: []runtime.PrivateFileMount{
			{MountPath: "/run/concourse/dev-validation", Files: map[string][]byte{"profile.yml": []byte("trusted")}},
			{MountPath: "/run/concourse/dev-validation-extra", Files: map[string][]byte{"config.yml": []byte("trusted")}},
		},
	})
	names := []string{"concourse-private-first", "concourse-private-second"}
	index := 0
	c.privateMountNameGenerator = func() (string, error) {
		name := names[index]
		index++
		return name, nil
	}

	if _, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
		t.Fatal("expected partial Secret creation failure")
	}
	if _, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, "concourse-private-first", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pre-Pod partial failure leaked or replaced Secret: %v", err)
	}
}

func TestPrivateMountBindFailureNeverDeletesSecretWhilePodMayBeLive(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	c := newPrivateMountTestContainer(clientset, privateMountTestSpec("/run/concourse/dev-validation"))
	c.privateMountNameGenerator = func() (string, error) { return "concourse-private-bind-failure", nil }
	clientset.PrependReactor("update", "secrets", func(ktesting.Action) (bool, kruntime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("bind failed"))
	})
	clientset.PrependReactor("delete", "pods", func(ktesting.Action) (bool, kruntime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("pod delete failed"))
	})
	if _, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
		t.Fatal("expected bind failure")
	}
	secret, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, "concourse-private-bind-failure", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret deleted although pod cleanup failed: %v", err)
	}
	if secret.Immutable == nil || !*secret.Immutable || string(secret.Data["profile.yml"]) != "trusted" || len(secret.OwnerReferences) != 0 {
		t.Fatalf("bind failure Secret is not the original ownerless trusted object: %#v", secret)
	}
}

func TestPrivateMountRejectsUnsafeAndOverlappingPaths(t *testing.T) {
	cases := []string{
		"", "/", "relative", "/run/concourse/../dev-validation", "/work", "/work/private", "/tmp/elsewhere",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			c := newPrivateMountTestContainer(fake.NewSimpleClientset(), privateMountTestSpec(path))
			if _, err := c.createPod(context.Background(), runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
				t.Fatalf("expected unsafe private mount path %q to be rejected", path)
			}
		})
	}
}

func TestPrivateMountRejectsUnsafeFileNamesAndOversizeData(t *testing.T) {
	badFiles := []map[string][]byte{
		{"../profile.yml": []byte("trusted")},
		{"profile\\.yml": []byte("trusted")},
		{".": []byte("trusted")},
		{"profile.yml": nil},
		{"profile.yml": make([]byte, 1024*1024+1)},
	}
	for _, files := range badFiles {
		c := newPrivateMountTestContainer(fake.NewSimpleClientset(), runtime.ContainerSpec{
			ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
			PrivateFileMounts: []runtime.PrivateFileMount{{
				MountPath: "/run/concourse/dev-validation",
				Files:     files,
			}},
		})
		if _, err := c.createPod(context.Background(), runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
			t.Fatalf("expected unsafe private mount files %q to be rejected", files)
		}
	}
}

func TestPrivateMountRejectsEveryRuntimeMountOverlap(t *testing.T) {
	private := runtime.PrivateFileMount{MountPath: "/run/concourse/authority", Files: map[string][]byte{"profile.yml": []byte("trusted")}}
	cases := []struct {
		name string
		spec runtime.ContainerSpec
	}{
		{"working directory parent", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, Dir: "/run/concourse", PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"candidate input child", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, Inputs: []runtime.Input{{DestinationPath: "/run/concourse/authority/candidate"}}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"output parent", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, Outputs: map[string]string{"out": "/run/concourse"}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"cache child", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, Caches: []string{"/run/concourse/authority/cache"}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"scratch child", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, ScratchPaths: []string{"/run/concourse/authority/scratch"}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"ordinary Secret parent", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, SecretMounts: []runtime.SecretMount{{SecretName: "ordinary", MountPath: "/run/concourse"}}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"other private child", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, PrivateFileMounts: []runtime.PrivateFileMount{private, {MountPath: "/run/concourse/authority/child", Files: map[string][]byte{"config.yml": []byte("trusted")}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newPrivateMountTestContainer(fake.NewSimpleClientset(), tc.spec)
			if _, err := c.createPod(context.Background(), runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
				t.Fatal("expected runtime mount overlap to be rejected")
			}
		})
	}
}

func TestPrivateMountNewPodsUseDifferentSecretNamesAndAttachDoesNotAllocate(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	spec := privateMountTestSpec("/run/concourse/dev-validation")
	c := newPrivateMountTestContainer(clientset, spec)
	first, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	firstSecret := privateSecretName(t, first)
	if err := clientset.CoreV1().Pods("test-ns").Delete(ctx, first.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	// Simulate owner-reference GC before another execution attempt.
	if err := clientset.CoreV1().Secrets("test-ns").Delete(ctx, firstSecret, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	second, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	secondSecret := privateSecretName(t, second)
	if firstSecret == secondSecret {
		t.Fatal("fresh pod reused a predictable private Secret name")
	}
	reloaded := newContainer(c.handle, taskMetadata(), spec, nil, clientset, c.config, c.workerName, nil, nil, nil, true)
	if _, err := reloaded.Attach(ctx, "task", runtime.ProcessIO{}); err != nil {
		t.Fatal(err)
	}
	secrets, err := clientset.CoreV1().Secrets("test-ns").List(ctx, metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 1 || secrets.Items[0].Name != secondSecret {
		t.Fatalf("attach allocated or mutated private authority: %#v, %v", secrets.Items, err)
	}
}

func privateSecretName(t *testing.T, pod *corev1.Pod) string {
	t.Helper()
	for _, volume := range pod.Spec.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName != "" {
			return volume.Secret.SecretName
		}
	}
	t.Fatal("pod has no private Secret volume")
	return ""
}
