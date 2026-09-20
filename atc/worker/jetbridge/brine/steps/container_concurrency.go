package steps

import (
	"fmt"
	"sync"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// concurrentCalls joins every caller before returning to Brine's disposer.
// Each caller is ready before the gate opens; no worker goroutine asserts.
func concurrentCalls(count int, call func(int)) {
	var ready, done sync.WaitGroup
	ready.Add(count)
	done.Add(count)
	start := make(chan struct{})
	for i := 0; i < count; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			call(i)
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
}

type ConcurrentContainers struct {
	ready      WorkerReady
	containers []runtime.Container
	processes  []runtime.Process
	createErrs []error
	runErrs    []error
}

type ConcurrentProperties struct {
	container runtime.Container
	writers   int
	errors    []error
}

func containerConcurrencyDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		Transform[WorkerReady, ConcurrentContainers](
			"{int} independent containers are created and submitted concurrently",
			func(in WorkerReady, a Args) (ConcurrentContainers, error) {
				n := a.Int(0)
				if n < 2 || n > 100 {
					return ConcurrentContainers{}, fmt.Errorf("expected 2..100 concurrent containers, got %d", n)
				}
				out := ConcurrentContainers{
					ready: in, containers: make([]runtime.Container, n),
					processes: make([]runtime.Process, n), createErrs: make([]error, n), runErrs: make([]error, n),
				}
				concurrentCalls(n, func(i int) {
					// Preserve independent workers sharing the real DB/API. No
					// executor is configured: this checks submission, not execution.
					worker := jetbridge.NewWorker(in.DBWorker, in.Clientset, in.Config)
					out.containers[i], _, out.createErrs[i] = worker.FindOrCreateContainer(
						in.Ctx, db.NewFixedHandleContainerOwner(fmt.Sprintf("concurrent-run-%d", i)),
						db.ContainerMetadata{Type: db.ContainerTypeTask},
						runtime.ContainerSpec{TeamID: in.TeamID, Dir: "/workdir",
							ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}}, nil)
					if out.createErrs[i] != nil || out.containers[i] == nil {
						return
					}
					out.processes[i], out.runErrs[i] = out.containers[i].Run(in.Ctx,
						runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", fmt.Sprintf("echo %d", i)}},
						runtime.ProcessIO{})
				})
				return out, nil
			}),
		Assert[ConcurrentContainers](
			"all {int} containers have independent records and submitted pods",
			func(in ConcurrentContainers, a Args) error {
				n := a.Int(0)
				if n < 2 || len(in.containers) != n {
					return fmt.Errorf("expected %d concurrent results, got %d", n, len(in.containers))
				}
				pods, err := in.ready.Clientset.CoreV1().Pods(in.ready.Namespace).List(in.ready.Ctx, metav1.ListOptions{})
				if err != nil {
					return fmt.Errorf("list submitted pods: %w", err)
				}
				for i, container := range in.containers {
					if in.createErrs[i] != nil {
						return fmt.Errorf("container %d creation failed: %w", i, in.createErrs[i])
					}
					if container == nil {
						return fmt.Errorf("container %d creation returned nil", i)
					}
					if in.runErrs[i] != nil {
						return fmt.Errorf("container %d submission failed: %w", i, in.runErrs[i])
					}
					if in.processes[i] == nil {
						return fmt.Errorf("container %d submission returned no process", i)
					}
					handle := fmt.Sprintf("concurrent-run-%d", i)
					record := container.DBContainer()
					if record == nil {
						return fmt.Errorf("container %d has no database record", i)
					}
					if record.Handle() != handle {
						return fmt.Errorf("container %d has handle %q, want %q", i, record.Handle(), handle)
					}
					var count int
					if err := in.ready.DB.Conn.QueryRow(
						"SELECT count(*) FROM containers WHERE handle = $1 AND state = 'created'", handle).Scan(&count); err != nil {
						return fmt.Errorf("read container %q: %w", handle, err)
					}
					if count != 1 {
						return fmt.Errorf("container %q has %d created records, want one", handle, count)
					}
					found := false
					for _, pod := range pods.Items {
						if pod.Name == handle && pod.UID != "" {
							found = true
						}
					}
					if !found {
						return fmt.Errorf("container %q has no independently identified API pod", handle)
					}
				}
				if len(pods.Items) != n {
					return fmt.Errorf("expected %d submitted pods, got %d", n, len(pods.Items))
				}
				return nil
			}),
		Transform[ContainerProperties, ConcurrentProperties](
			"{int} callers write distinct properties while as many callers read them",
			func(in ContainerProperties, a Args) (ConcurrentProperties, error) {
				n := a.Int(0)
				if n < 2 || n > 100 || in.Container == nil {
					return ConcurrentProperties{}, fmt.Errorf("expected a container and 2..100 property callers")
				}
				out := ConcurrentProperties{container: in.Container, writers: n, errors: make([]error, n*2)}
				concurrentCalls(n*2, func(i int) {
					if i < n {
						out.errors[i] = in.Container.SetProperty(fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i))
						return
					}
					props, err := in.Container.Properties()
					out.errors[i] = err
					for range props {
						// Iteration must be safe while other callers write.
					}
				})
				return out, nil
			}),
		Assert[ConcurrentProperties](
			"all {int} property writes survive and readback snapshots are independent",
			func(in ConcurrentProperties, a Args) error {
				n := a.Int(0)
				if n < 2 || in.writers != n || len(in.errors) != 2*n {
					return fmt.Errorf("expected %d completed readers and writers", n)
				}
				for i, err := range in.errors {
					if err != nil {
						return fmt.Errorf("property caller %d failed: %w", i, err)
					}
				}
				props, err := in.container.Properties()
				if err != nil {
					return fmt.Errorf("final property read: %w", err)
				}
				for i := 0; i < n; i++ {
					key, value := fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i)
					if props[key] != value {
						return fmt.Errorf("property %q: got %q, want %q", key, props[key], value)
					}
				}
				// Verify copy semantics deterministically, after the concurrent
				// callers join. This is not a race-detector claim.
				props["key-0"] = "changed-only-in-snapshot"
				again, err := in.container.Properties()
				if err != nil {
					return fmt.Errorf("independent property read: %w", err)
				}
				if again["key-0"] != "value-0" {
					return fmt.Errorf("editing a readback snapshot changed the container's property")
				}
				return nil
			}),
	}
}
