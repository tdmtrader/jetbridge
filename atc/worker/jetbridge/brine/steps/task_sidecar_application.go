package steps

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
)

type taskApplication struct {
	handle, directory string
	postgres          bool
}

func (in TaskCluster) applicationVolume(pod string) *jetbridge.Volume {
	volume := jetbridge.NewDeferredVolume(in.application.handle, "k8s-worker-1", in.Executor,
		in.Namespace, "main", in.application.directory)
	volume.SetPodName(pod)
	return volume
}

func configureMountedApplication(in TaskCluster, handle string, spec *runtime.ContainerSpec) {
	spec.Dir = "/tmp/build/workdir"
	spec.Inputs = []runtime.Input{{Artifact: in.applicationVolume(in.podName(handle)), DestinationPath: in.application.directory}}
	if !in.application.postgres {
		return
	}
	memory := uint64(256 * 1024 * 1024)
	spec.Limits.Memory = &memory
	spec.Sidecars = []atc.SidecarConfig{{
		Name: "postgres", Image: "postgres:15",
		Env: []atc.SidecarEnvVar{
			{Name: "POSTGRES_PASSWORD", Value: "test"},
			{Name: "POSTGRES_DB", Value: "testdb"},
			// Use the existing shared workspace, not an additional hostPath or PVC.
			{Name: "PGDATA", Value: "/tmp/build/workdir/pgdata"},
		},
		Ports: []atc.SidecarPort{{ContainerPort: 5432}},
		Resources: &atc.SidecarResources{
			Requests: atc.SidecarResourceList{CPU: "50m", Memory: "32Mi"},
			Limits:   atc.SidecarResourceList{CPU: "250m", Memory: "256Mi"},
		},
	}}
}

// Preserve the original pod assertions before waiting or executing setup.
// Later checks observe actual container identity, database readiness and files.
func requireApplicationPod(in TaskCluster, pod *corev1.Pod) error {
	if pod == nil || len(pod.Spec.Containers) == 0 || pod.Spec.Containers[0].Name != "main" {
		return fmt.Errorf("npm task requires its main container first")
	}
	main := pod.Spec.Containers[0]
	if in.application.postgres {
		var postgres *corev1.Container
		for i := range pod.Spec.Containers {
			if pod.Spec.Containers[i].Name == "postgres" {
				postgres = &pod.Spec.Containers[i]
			}
		}
		if postgres == nil {
			return fmt.Errorf("npm task is missing its postgres sidecar")
		}
		if postgres.Image != "postgres:15" {
			return fmt.Errorf("postgres image %q, want postgres:15", postgres.Image)
		}
		for _, want := range []corev1.EnvVar{{Name: "POSTGRES_PASSWORD", Value: "test"}, {Name: "POSTGRES_DB", Value: "testdb"}} {
			found := false
			for _, got := range postgres.Env {
				found = found || reflect.DeepEqual(got, want)
			}
			if !found {
				return fmt.Errorf("postgres is missing environment %s=%s", want.Name, want.Value)
			}
		}
		port := corev1.ContainerPort{ContainerPort: 5432, Protocol: corev1.ProtocolTCP}
		foundPort := false
		for _, got := range postgres.Ports {
			foundPort = foundPort || reflect.DeepEqual(got, port)
		}
		if !foundPort {
			return fmt.Errorf("postgres is missing TCP port 5432")
		}
		if !reflect.DeepEqual(postgres.VolumeMounts, main.VolumeMounts) {
			return fmt.Errorf("postgres mounts %v differ from main mounts %v", postgres.VolumeMounts, main.VolumeMounts)
		}
	}
	volumes := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		volumes[v.Name] = true
	}
	mounted := false
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if !volumes[m.Name] {
				return fmt.Errorf("container %s mount %s has no Volume", c.Name, m.Name)
			}
			if c.Name == "main" && m.MountPath == in.application.directory {
				mounted = true
			}
		}
	}
	if !mounted {
		return fmt.Errorf("npm application input is not mounted at %s", in.application.directory)
	}
	return nil
}

// This is an explicit application premise, not automatic artifact staging:
// the exec path stages no inputs. Real Volume.StreamIn writes to the
// kubelet-mounted input before the real npm command runs. Setup and readiness
// use the original SPDY executor so they cannot satisfy the task-exec oracle.
func prepareMountedApplication(in TaskCluster, pod *corev1.Pod) error {
	if err := requireApplicationPod(in, pod); err != nil {
		return err
	}
	ready, err := awaitLivePod(in.Ctx, liveKubernetes{Clientset: in.Clientset, Namespace: in.Namespace}, pod.Name)
	if err != nil {
		return err
	}
	if ready.UID != pod.UID {
		return fmt.Errorf("npm task pod was replaced during setup")
	}
	marker := "application-" + in.Namespace
	script := "const fs=require('fs'),net=require('net');\n" +
		"if(fs.readFileSync('input.txt','utf8')!==" + fmt.Sprintf("%q", marker) + ")throw Error('wrong mounted application input');\n" +
		"const socket=net.createConnection(5432,'127.0.0.1');\n" +
		"socket.setTimeout(5000,()=>{console.error('postgres timeout');process.exit(1)});\n" +
		"socket.on('error',e=>{console.error(e);process.exit(1)});\n" +
		"socket.on('connect',()=>{socket.end();console.log('application-ok')});\n"
	files := []struct{ name, body string }{
		{"package.json", "{\"name\":\"brine-sidecar-application\",\"version\":\"1.0.0\",\"scripts\":{\"test\":\"node verify.js\"}}"},
		{"verify.js", script},
		{"input.txt", marker},
	}
	if !in.application.postgres {
		files = files[2:]
	}
	volume := in.applicationVolume(pod.Name)
	for _, file := range files {
		raw, err := plainTarOfOneFile(file.name, file.body)
		if err != nil {
			return err
		}
		if err := volume.StreamIn(in.Ctx, ".", nil, 0, bytes.NewReader(raw)); err != nil {
			return fmt.Errorf("prepare mounted npm application: %w", err)
		}
	}
	if in.application.postgres {
		for {
			var output bytes.Buffer
			err := in.Executor.ExecInPod(in.Ctx, in.Namespace, pod.Name, "postgres",
				[]string{"psql", "-U", "postgres", "-d", "testdb", "-Atc", "select 1"}, nil, &output, nil, false, jetbridge.ExecAttrs{Purpose: "sidecar-readiness"})
			if err == nil && strings.TrimSpace(output.String()) == "1" {
				break
			}
			select {
			case <-in.Ctx.Done():
				return fmt.Errorf("real PostgreSQL did not become queryable: %w", in.Ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	fmt.Printf("mounted application ready in %s/%s UID %s node %s; PostgreSQL=%t\n", in.Namespace, pod.Name, pod.UID, ready.Spec.NodeName, in.application.postgres)
	return nil
}
