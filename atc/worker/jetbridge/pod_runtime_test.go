package jetbridge_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

type podKey struct {
	Namespace string
	Pod       string
	Container string
}

type execEffect string

const (
	execCompletes   execEffect = "completes"
	execOOMKillsPod execEffect = "oom-kills-pod"
	execDeletesPod  execEffect = "deletes-pod"
)

type program struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Effect   execEffect
}

type modeledProcess struct {
	Command    []string
	Stdin      []byte
	TTY        bool
	Supervised bool
}

type supervisorRun struct {
	Command  []string
	Log      []byte
	ExitCode int
}

type containerState struct {
	Files       map[string][]byte
	Programs    map[string]program
	Processes   []modeledProcess
	Supervisors map[string]supervisorRun
}

type podRuntime struct {
	mu         sync.Mutex
	clientset  kubernetes.Interface
	containers map[podKey]*containerState
	podUIDs    map[podKey]types.UID
	nextUID    uint64
}

var (
	errPodNotFound                 = errors.New("pod not found")
	errPodNotRunning               = errors.New("pod not running")
	errPodTerminated               = errors.New("pod terminated")
	errContainerNotFound           = errors.New("container not found")
	errContainerTerminated         = errors.New("container terminated")
	errProgramNotFound             = errors.New("program not found")
	errExecOOMKilled               = errors.New("pod OOM killed during exec")
	errExecPodDeleted              = errors.New("pod deleted during exec")
	errMalformedSupervisorEnvelope = errors.New("malformed task supervisor envelope")
)

func newPodRuntime(clientset kubernetes.Interface) *podRuntime {
	return &podRuntime{
		clientset:  clientset,
		containers: map[podKey]*containerState{},
		podUIDs:    map[podKey]types.UID{},
	}
}

func (r *podRuntime) AddContainer(key podKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	pods := r.clientset.CoreV1().Pods(key.Namespace)
	pod, err := pods.Get(context.Background(), key.Pod, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		pod = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Pod},
		}
		r.assignPodUID(pod)
		ensureRunningContainer(pod, key.Container)
		if pod, err = pods.Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create pod: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get pod: %w", err)
	} else {
		pod = pod.DeepCopy()
		r.assignPodUID(pod)
		ensureRunningContainer(pod, key.Container)
		if pod, err = pods.Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update pod: %w", err)
		}
	}

	identityKey := podKey{Namespace: key.Namespace, Pod: key.Pod}
	if previousUID := r.podUIDs[identityKey]; previousUID != "" && previousUID != pod.UID {
		r.resetPodIncarnation(key.Namespace, key.Pod)
	}
	r.podUIDs[identityKey] = pod.UID

	if _, found := r.containers[key]; !found {
		r.containers[key] = &containerState{
			Files:       map[string][]byte{},
			Programs:    map[string]program{},
			Supervisors: map[string]supervisorRun{},
		}
	}
	return nil
}

func (r *podRuntime) assignPodUID(pod *corev1.Pod) {
	if pod.UID != "" {
		return
	}
	r.nextUID++
	pod.UID = types.UID(fmt.Sprintf("pod-runtime-%06d", r.nextUID))
}

func (r *podRuntime) resetPodIncarnation(namespace, podName string) {
	for key, previous := range r.containers {
		if key.Namespace != namespace || key.Pod != podName {
			continue
		}
		r.containers[key] = &containerState{
			Files:       map[string][]byte{},
			Programs:    previous.Programs,
			Supervisors: map[string]supervisorRun{},
		}
	}
}

func ensureRunningContainer(pod *corev1.Pod, containerName string) {
	hasContainer := false
	for _, container := range pod.Spec.Containers {
		if container.Name == containerName {
			hasContainer = true
			break
		}
	}
	if !hasContainer {
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: containerName})
	}

	runningStatus := corev1.ContainerStatus{
		Name:  containerName,
		Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}
	hasStatus := false
	for index := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[index].Name == containerName {
			pod.Status.ContainerStatuses[index].Ready = true
			pod.Status.ContainerStatuses[index].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
			hasStatus = true
			break
		}
	}
	if !hasStatus {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, runningStatus)
	}
	pod.Status.Phase = corev1.PodRunning
}

func (r *podRuntime) PutFile(key podKey, name string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	container, found := r.containers[key]
	if !found {
		return errContainerNotFound
	}
	container.Files[path.Clean(name)] = bytes.Clone(data)
	return nil
}

func (r *podRuntime) InstallProgram(key podKey, executable string, installed program) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	container, found := r.containers[key]
	if !found {
		return errContainerNotFound
	}
	installed.Stdout = bytes.Clone(installed.Stdout)
	installed.Stderr = bytes.Clone(installed.Stderr)
	container.Programs[executable] = installed
	return nil
}

func (r *podRuntime) Terminate(key podKey, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	pods := r.clientset.CoreV1().Pods(key.Namespace)
	pod, err := pods.Get(context.Background(), key.Pod, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return errPodNotFound
	}
	if err != nil {
		return fmt.Errorf("get pod: %w", err)
	}
	if _, found := r.containers[key]; !found {
		return errContainerNotFound
	}

	pod = pod.DeepCopy()
	markContainerTerminated(pod, key.Container, reason, 1)
	if _, err := pods.Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update terminated pod: %w", err)
	}
	return nil
}

func markContainerTerminated(pod *corev1.Pod, containerName, reason string, exitCode int32) {
	terminatedStatus := corev1.ContainerStatus{
		Name:  containerName,
		Ready: false,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason:   reason,
			ExitCode: exitCode,
		}},
	}
	found := false
	for index := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[index].Name == containerName {
			pod.Status.ContainerStatuses[index] = terminatedStatus
			found = true
			break
		}
	}
	if !found {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, terminatedStatus)
	}
	pod.Status.Phase = corev1.PodFailed
}

func (r *podRuntime) File(key podKey, name string) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	container, found := r.containers[key]
	if !found {
		return nil, false
	}
	data, found := container.Files[path.Clean(name)]
	return bytes.Clone(data), found
}

func (r *podRuntime) Processes(key podKey) []modeledProcess {
	r.mu.Lock()
	defer r.mu.Unlock()

	container, found := r.containers[key]
	if !found {
		return nil
	}
	return cloneProcesses(container.Processes)
}

func (r *podRuntime) TerminalSessions(key podKey) []modeledProcess {
	r.mu.Lock()
	defer r.mu.Unlock()

	container, found := r.containers[key]
	if !found {
		return nil
	}
	sessions := make([]modeledProcess, 0, len(container.Processes))
	for _, process := range container.Processes {
		if process.TTY {
			sessions = append(sessions, process)
		}
	}
	return cloneProcesses(sessions)
}

func cloneProcesses(processes []modeledProcess) []modeledProcess {
	clones := make([]modeledProcess, len(processes))
	for index, process := range processes {
		clones[index] = modeledProcess{
			Command:    append([]string(nil), process.Command...),
			Stdin:      bytes.Clone(process.Stdin),
			TTY:        process.TTY,
			Supervised: process.Supervised,
		}
	}
	return clones
}

func (r *podRuntime) ExecInPod(
	ctx context.Context,
	namespace, podName, containerName string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	tty bool,
	attrs jetbridge.ExecAttrs,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	var stdinData []byte
	var err error
	if stdin != nil {
		stdinData, err = io.ReadAll(stdin)
		if err != nil {
			return fmt.Errorf("read exec stdin: %w", err)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := podKey{namespace, podName, containerName}
	container, err := r.runningContainer(key)
	if err != nil {
		return err
	}

	process := modeledProcess{
		Command: append([]string(nil), command...),
		Stdin:   bytes.Clone(stdinData),
		TTY:     tty,
	}
	stateDir, childCommand, supervised, supervisorErr := decodeSupervisorEnvelope(command, attrs)
	if supervised {
		if supervisorErr != nil {
			return supervisorErr
		}
		return r.runSupervisor(key, container, stateDir, childCommand, stdinData, stdout, tty)
	}
	switch {
	case isTarCreate(command):
		container.Processes = append(container.Processes, process)
		return writeTar(container.Files, command[4], command[5], stdout)
	case isTarExtract(command):
		container.Processes = append(container.Processes, process)
		return extractTar(container.Files, command[4], stdinData)
	default:
		if len(command) == 0 {
			return errProgramNotFound
		}
		installed, found := container.Programs[command[0]]
		if !found {
			return errProgramNotFound
		}
		container.Processes = append(container.Processes, process)
		if tty {
			combined := make([]byte, 0, len(installed.Stdout)+len(installed.Stderr))
			combined = append(combined, installed.Stdout...)
			combined = append(combined, installed.Stderr...)
			if err := writeProgramOutput(stdout, combined); err != nil {
				return err
			}
		} else {
			if err := writeProgramOutput(stdout, installed.Stdout); err != nil {
				return err
			}
			if err := writeProgramOutput(stderr, installed.Stderr); err != nil {
				return err
			}
		}
		if err := r.applyEffect(key, installed.Effect); err != nil {
			return err
		}
		if installed.ExitCode != 0 {
			return &jetbridge.ExecExitError{ExitCode: installed.ExitCode}
		}
		return nil
	}
}

func (r *podRuntime) runSupervisor(
	key podKey,
	container *containerState,
	stateDir string,
	command []string,
	stdin []byte,
	stdout io.Writer,
	tty bool,
) error {
	if previous, found := container.Supervisors[stateDir]; found {
		if err := writeProgramOutput(stdout, previous.Log); err != nil {
			return err
		}
		if previous.ExitCode != 0 {
			return &jetbridge.ExecExitError{ExitCode: previous.ExitCode}
		}
		return nil
	}

	installed, found := container.Programs[command[0]]
	if !found {
		return errProgramNotFound
	}
	log := make([]byte, 0, len(installed.Stdout)+len(installed.Stderr))
	log = append(log, installed.Stdout...)
	log = append(log, installed.Stderr...)
	container.Processes = append(container.Processes, modeledProcess{
		Command:    append([]string(nil), command...),
		Stdin:      bytes.Clone(stdin),
		TTY:        tty,
		Supervised: true,
	})
	container.Supervisors[stateDir] = supervisorRun{
		Command:  append([]string(nil), command...),
		Log:      bytes.Clone(log),
		ExitCode: installed.ExitCode,
	}
	if err := writeProgramOutput(stdout, log); err != nil {
		return err
	}
	if err := r.applyEffect(key, installed.Effect); err != nil {
		return err
	}
	if installed.ExitCode != 0 {
		return &jetbridge.ExecExitError{ExitCode: installed.ExitCode}
	}
	return nil
}

const supervisorEnvelopeBodyPrefix = `
alive() {
  pid="$(cat "$S/pid" 2>/dev/null)"
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}
mkdir -p "$S"
: >>"$S/log"
if [ ! -f "$S/exit" ] && ! alive; then
  ( trap '' HUP; `

const supervisorLaunchSuffix = ` >>"$S/log" 2>&1; echo $? >"$S/exit.tmp" && mv "$S/exit.tmp" "$S/exit" ) &`

const supervisorEnvelopeAfterLaunch = `
  echo $! >"$S/pid"
fi
tail -n +1 -f "$S/log" 2>/dev/null &
T=$!
while [ ! -f "$S/exit" ] && alive; do sleep 1; done
sleep 2
kill "$T" 2>/dev/null
wait "$T" 2>/dev/null
if [ -f "$S/exit" ]; then exit "$(cat "$S/exit")"; fi
exit 255`

func decodeSupervisorEnvelope(command []string, attrs jetbridge.ExecAttrs) (string, []string, bool, error) {
	if attrs.Purpose != "step-command" || len(command) != 3 || command[0] != "sh" || command[1] != "-c" {
		return "", nil, false, nil
	}
	script := command[2]
	if !strings.HasPrefix(script, "S='/tmp/concourse-task-") {
		return "", nil, false, nil
	}

	lineEnd := strings.IndexByte(script, '\n')
	if lineEnd < 0 {
		return "", nil, true, errMalformedSupervisorEnvelope
	}
	stateWords, err := decodeShellQuotedWords(strings.TrimPrefix(script[:lineEnd], "S="))
	if err != nil || len(stateWords) != 1 || !strings.HasPrefix(stateWords[0], "/tmp/concourse-task-") {
		return "", nil, true, errMalformedSupervisorEnvelope
	}

	body := script[lineEnd:]
	if !strings.HasPrefix(body, supervisorEnvelopeBodyPrefix) {
		return "", nil, true, errMalformedSupervisorEnvelope
	}
	commandStart := len(supervisorEnvelopeBodyPrefix)
	commandEnd := strings.LastIndex(body, supervisorLaunchSuffix)
	if commandEnd < commandStart {
		return "", nil, true, errMalformedSupervisorEnvelope
	}
	expectedTail := supervisorLaunchSuffix + supervisorEnvelopeAfterLaunch
	if body[commandEnd:] != expectedTail {
		return "", nil, true, errMalformedSupervisorEnvelope
	}
	child, err := decodeShellQuotedWords(body[commandStart:commandEnd])
	if err != nil || len(child) == 0 {
		return "", nil, true, errMalformedSupervisorEnvelope
	}
	return stateWords[0], child, true, nil
}

func decodeShellQuotedWords(encoded string) ([]string, error) {
	if encoded == "" {
		return nil, errMalformedSupervisorEnvelope
	}

	words := []string{}
	for offset := 0; offset < len(encoded); {
		if encoded[offset] != '\'' {
			return nil, errMalformedSupervisorEnvelope
		}
		offset++
		var word strings.Builder
		for {
			segmentStart := offset
			for offset < len(encoded) && encoded[offset] != '\'' {
				offset++
			}
			if offset == len(encoded) {
				return nil, errMalformedSupervisorEnvelope
			}
			word.WriteString(encoded[segmentStart:offset])
			offset++
			if offset+2 < len(encoded) && encoded[offset] == '\\' && encoded[offset+1] == '\'' && encoded[offset+2] == '\'' {
				word.WriteByte('\'')
				offset += 3
				continue
			}
			break
		}
		words = append(words, word.String())
		if offset == len(encoded) {
			break
		}
		if encoded[offset] != ' ' || offset+1 == len(encoded) {
			return nil, errMalformedSupervisorEnvelope
		}
		offset++
	}
	return words, nil
}

func (r *podRuntime) applyEffect(key podKey, effect execEffect) error {
	pods := r.clientset.CoreV1().Pods(key.Namespace)
	switch effect {
	case "", execCompletes:
		return nil
	case execOOMKillsPod:
		pod, err := pods.Get(context.Background(), key.Pod, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get pod for OOM effect: %w", err)
		}
		pod = pod.DeepCopy()
		markContainerTerminated(pod, key.Container, "OOMKilled", 137)
		if _, err := pods.Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update OOM-killed pod: %w", err)
		}
		return errExecOOMKilled
	case execDeletesPod:
		if err := pods.Delete(context.Background(), key.Pod, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("delete pod during exec: %w", err)
		}
		for containerKey, container := range r.containers {
			if containerKey.Namespace == key.Namespace && containerKey.Pod == key.Pod {
				container.Supervisors = map[string]supervisorRun{}
			}
		}
		return errExecPodDeleted
	default:
		return fmt.Errorf("unsupported exec effect: %s", effect)
	}
}

func writeProgramOutput(destination io.Writer, data []byte) error {
	if destination == nil || len(data) == 0 {
		return nil
	}
	if _, err := destination.Write(data); err != nil {
		return fmt.Errorf("write exec output: %w", err)
	}
	return nil
}

func (r *podRuntime) runningContainer(key podKey) (*containerState, error) {
	pod, err := r.clientset.CoreV1().Pods(key.Namespace).Get(context.Background(), key.Pod, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, errPodNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get pod: %w", err)
	}
	containerExists := false
	for _, container := range pod.Spec.Containers {
		if container.Name == key.Container {
			containerExists = true
			break
		}
	}
	if !containerExists {
		return nil, errContainerNotFound
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == key.Container && status.State.Terminated != nil {
			return nil, errContainerTerminated
		}
	}
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		return nil, errPodTerminated
	}
	if pod.Status.Phase != corev1.PodRunning {
		return nil, errPodNotRunning
	}
	container, found := r.containers[key]
	if !found {
		return nil, errContainerNotFound
	}
	return container, nil
}

func isTarCreate(command []string) bool {
	return len(command) == 6 && command[0] == "tar" && command[1] == "cf" && command[2] == "-" && command[3] == "-C"
}

func isTarExtract(command []string) bool {
	return len(command) == 5 && command[0] == "tar" && command[1] == "xf" && command[2] == "-" && command[3] == "-C"
}

func writeTar(files map[string][]byte, root, relative string, destination io.Writer) error {
	if destination == nil {
		destination = io.Discard
	}

	root = path.Clean(root)
	requested := path.Clean(path.Join(root, relative))
	prefix := strings.TrimSuffix(requested, "/") + "/"
	names := make([]string, 0, len(files))
	for name := range files {
		name = path.Clean(name)
		if name == requested || strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("tar source not found: %s", requested)
	}
	sort.Strings(names)

	tarWriter := tar.NewWriter(destination)
	for _, name := range names {
		archiveName := path.Clean(relative)
		if name != requested {
			archiveName = path.Join(archiveName, strings.TrimPrefix(name, prefix))
		}
		data := files[name]
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     archiveName,
			Mode:     0o600,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return fmt.Errorf("write tar header: %w", err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			return fmt.Errorf("write tar file: %w", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	return nil
}

func extractTar(files map[string][]byte, root string, archive []byte) error {
	root = path.Clean(root)
	updates := map[string][]byte{}
	tarReader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar header: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		archiveName := path.Clean(header.Name)
		if path.IsAbs(header.Name) || archiveName == ".." || strings.HasPrefix(archiveName, "../") {
			return fmt.Errorf("invalid tar path: %s", header.Name)
		}
		data, err := io.ReadAll(tarReader)
		if err != nil {
			return fmt.Errorf("read tar file: %w", err)
		}
		updates[path.Join(root, archiveName)] = data
	}
	for name, data := range updates {
		files[name] = bytes.Clone(data)
	}
	return nil
}

func taskSupervisorEnvelope(stateDir, encodedChild string) []string {
	script := fmt.Sprintf(`S='%s'
alive() {
  pid="$(cat "$S/pid" 2>/dev/null)"
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}
mkdir -p "$S"
: >>"$S/log"
if [ ! -f "$S/exit" ] && ! alive; then
  ( trap '' HUP; %s >>"$S/log" 2>&1; echo $? >"$S/exit.tmp" && mv "$S/exit.tmp" "$S/exit" ) &
  echo $! >"$S/pid"
fi
tail -n +1 -f "$S/log" 2>/dev/null &
T=$!
while [ ! -f "$S/exit" ] && alive; do sleep 1; done
sleep 2
kill "$T" 2>/dev/null
wait "$T" 2>/dev/null
if [ -f "$S/exit" ]; then exit "$(cat "$S/exit")"; fi
exit 255`, stateDir, encodedChild)
	return []string{"sh", "-c", script}
}

var _ = Describe("Pod runtime model", func() {
	It("round trips files through pod exec tar streams", func() {
		podRuntime := newPodRuntime(fake.NewSimpleClientset())
		source := podKey{"test-namespace", "source-pod", "main"}
		destination := podKey{"test-namespace", "destination-pod", "main"}

		Expect(podRuntime.AddContainer(source)).To(Succeed())
		Expect(podRuntime.AddContainer(destination)).To(Succeed())
		Expect(podRuntime.PutFile(source, "/tmp/source/data.txt", []byte("artifact payload"))).To(Succeed())

		var archive bytes.Buffer
		Expect(podRuntime.ExecInPod(
			context.Background(),
			source.Namespace,
			source.Pod,
			source.Container,
			[]string{"tar", "cf", "-", "-C", "/tmp/source", "data.txt"},
			nil,
			&archive,
			nil,
			false,
			jetbridge.ExecAttrs{Purpose: "stream-out"},
		)).To(Succeed())
		Expect(podRuntime.ExecInPod(
			context.Background(),
			destination.Namespace,
			destination.Pod,
			destination.Container,
			[]string{"tar", "xf", "-", "-C", "/tmp/destination"},
			bytes.NewReader(archive.Bytes()),
			nil,
			nil,
			false,
			jetbridge.ExecAttrs{Purpose: "stream-in"},
		)).To(Succeed())

		data, found := podRuntime.File(destination, "/tmp/destination/data.txt")
		Expect(found).To(BeTrue())
		Expect(data).To(Equal([]byte("artifact payload")))
	})

	It("executes installed programs and records semantic process state", func() {
		podRuntime := newPodRuntime(fake.NewSimpleClientset())
		key := podKey{"test-namespace", "resource-pod", "main"}
		Expect(podRuntime.AddContainer(key)).To(Succeed())
		Expect(podRuntime.InstallProgram(key, "/opt/resource/check", program{
			Stdout:   []byte(`[{"version":{"ref":"abc123"}}]`),
			Stderr:   []byte("resource warning\n"),
			ExitCode: 17,
		})).To(Succeed())

		var stdout, stderr bytes.Buffer
		err := podRuntime.ExecInPod(
			context.Background(),
			key.Namespace,
			key.Pod,
			key.Container,
			[]string{"/opt/resource/check", "--debug"},
			strings.NewReader(`{"source":{"repository":"example/repo"}}`),
			&stdout,
			&stderr,
			true,
			jetbridge.ExecAttrs{Purpose: "resource-check"},
		)

		var exitErr *jetbridge.ExecExitError
		Expect(errors.As(err, &exitErr)).To(BeTrue())
		Expect(exitErr.ExitCode).To(Equal(17))
		Expect(stdout.String()).To(Equal("[{\"version\":{\"ref\":\"abc123\"}}]resource warning\n"))
		Expect(stderr.String()).To(BeEmpty())
		Expect(podRuntime.Processes(key)).To(Equal([]modeledProcess{{
			Command: []string{"/opt/resource/check", "--debug"},
			Stdin:   []byte(`{"source":{"repository":"example/repo"}}`),
			TTY:     true,
		}}))
		Expect(podRuntime.TerminalSessions(key)).To(Equal(podRuntime.Processes(key)))
	})

	It("returns immutable copies of files, processes, and terminal sessions", func() {
		podRuntime := newPodRuntime(fake.NewSimpleClientset())
		key := podKey{"test-namespace", "copy-pod", "main"}
		Expect(podRuntime.AddContainer(key)).To(Succeed())

		seedFile := []byte("immutable file")
		Expect(podRuntime.PutFile(key, "/tmp/data", seedFile)).To(Succeed())
		seedFile[0] = 'X'
		data, found := podRuntime.File(key, "/tmp/data")
		Expect(found).To(BeTrue())
		Expect(data).To(Equal([]byte("immutable file")))
		data[0] = 'Y'
		data, _ = podRuntime.File(key, "/tmp/data")
		Expect(data).To(Equal([]byte("immutable file")))

		stdout := []byte("immutable output")
		Expect(podRuntime.InstallProgram(key, "/bin/program", program{Stdout: stdout})).To(Succeed())
		stdout[0] = 'X'
		var installedStdout bytes.Buffer
		Expect(podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			[]string{"/bin/program", "argument"}, strings.NewReader("immutable input"), &installedStdout, io.Discard, true,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)).To(Succeed())
		Expect(installedStdout.String()).To(Equal("immutable output"))

		processes := podRuntime.Processes(key)
		processes[0].Command[0] = "/mutated"
		processes[0].Stdin[0] = 'X'
		sessions := podRuntime.TerminalSessions(key)
		sessions[0].Command[0] = "/also-mutated"
		Expect(podRuntime.Processes(key)).To(Equal([]modeledProcess{{
			Command: []string{"/bin/program", "argument"},
			Stdin:   []byte("immutable input"),
			TTY:     true,
		}}))
		Expect(podRuntime.TerminalSessions(key)).To(Equal(podRuntime.Processes(key)))
	})

	It("adds a running container while preserving the complete existing Pod", func() {
		started := true
		existingMainStatus := corev1.ContainerStatus{
			Name:  "main",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "Starting"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason:   "PreviousExit",
				ExitCode: 7,
			}},
			Ready:        false,
			RestartCount: 4,
			Image:        "main-image",
			ImageID:      "sha256:main-image",
			ContainerID:  "containerd://main-container",
			Started:      &started,
		}
		existing := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "test-namespace",
				Name:      "merged-pod",
				UID:       "existing-pod-uid",
				Labels:    map[string]string{"retained": "label"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sidecar", Image: "sidecar-image"},
					{Name: "main"},
				},
				Volumes: []corev1.Volume{{Name: "retained-volume"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				PodIP: "10.0.0.8",
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "sidecar",
					Ready: false,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "Starting"}},
				}, existingMainStatus},
			},
		}
		clientset := fake.NewSimpleClientset(existing)
		podRuntime := newPodRuntime(clientset)
		key := podKey{"test-namespace", "merged-pod", "main"}

		Expect(podRuntime.AddContainer(key)).To(Succeed())

		stored, err := clientset.CoreV1().Pods(key.Namespace).Get(context.Background(), key.Pod, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.UID).To(Equal(existing.UID))
		Expect(stored.Labels).To(Equal(map[string]string{"retained": "label"}))
		Expect(stored.Spec.Volumes).To(Equal([]corev1.Volume{{Name: "retained-volume"}}))
		Expect(stored.Spec.Containers).To(ConsistOf(
			corev1.Container{Name: "sidecar", Image: "sidecar-image"},
			corev1.Container{Name: "main"},
		))
		Expect(stored.Status.Phase).To(Equal(corev1.PodRunning))
		Expect(stored.Status.PodIP).To(Equal("10.0.0.8"))
		Expect(stored.Status.ContainerStatuses).To(ContainElement(existing.Status.ContainerStatuses[0]))
		expectedMainStatus := existingMainStatus
		expectedMainStatus.Ready = true
		expectedMainStatus.State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		Expect(stored.Status.ContainerStatuses).To(ContainElement(expectedMainStatus))
	})

	It("returns stable errors for missing programs, pods, containers, and terminated containers", func() {
		missingRuntime := newPodRuntime(fake.NewSimpleClientset())
		err := missingRuntime.ExecInPod(
			context.Background(), "test-namespace", "missing-pod", "main",
			[]string{"/bin/program"}, nil, io.Discard, io.Discard, false, jetbridge.ExecAttrs{},
		)
		Expect(err).To(MatchError(errPodNotFound))

		unmodeledPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "test-namespace", Name: "unmodeled-pod"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "main",
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				}},
			},
		}
		unmodeledRuntime := newPodRuntime(fake.NewSimpleClientset(unmodeledPod))
		err = unmodeledRuntime.ExecInPod(
			context.Background(), "test-namespace", "unmodeled-pod", "main",
			[]string{"/bin/program"}, nil, io.Discard, io.Discard, false, jetbridge.ExecAttrs{},
		)
		Expect(err).To(MatchError(errContainerNotFound))

		clientset := fake.NewSimpleClientset()
		podRuntime := newPodRuntime(clientset)
		key := podKey{"test-namespace", "terminal-pod", "main"}
		Expect(podRuntime.AddContainer(key)).To(Succeed())
		err = podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			[]string{"/bin/missing"}, nil, io.Discard, io.Discard, false, jetbridge.ExecAttrs{},
		)
		Expect(err).To(MatchError(errProgramNotFound))

		Expect(podRuntime.Terminate(key, "Completed")).To(Succeed())
		stored, err := clientset.CoreV1().Pods(key.Namespace).Get(context.Background(), key.Pod, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Status.Phase).To(Equal(corev1.PodFailed))
		Expect(stored.Status.ContainerStatuses).To(ContainElement(corev1.ContainerStatus{
			Name:  "main",
			Ready: false,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason:   "Completed",
				ExitCode: 1,
			}},
		}))
		err = podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			[]string{"/bin/missing"}, nil, io.Discard, io.Discard, false, jetbridge.ExecAttrs{},
		)
		Expect(err).To(MatchError(errContainerTerminated))
	})

	It("honors a pre-canceled context before consuming input or changing runtime state", func() {
		clientset := fake.NewSimpleClientset()
		podRuntime := newPodRuntime(clientset)
		key := podKey{"test-namespace", "canceled-pod", "main"}
		Expect(podRuntime.AddContainer(key)).To(Succeed())
		Expect(podRuntime.InstallProgram(key, "/bin/delete-pod", program{Effect: execDeletesPod})).To(Succeed())

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stdin := bytes.NewBufferString("must remain unread")
		err := podRuntime.ExecInPod(
			ctx, key.Namespace, key.Pod, key.Container,
			[]string{"/bin/delete-pod"}, stdin, io.Discard, io.Discard, false,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)

		Expect(err).To(MatchError(context.Canceled))
		Expect(stdin.String()).To(Equal("must remain unread"))
		Expect(podRuntime.Processes(key)).To(BeEmpty())
		stored, getErr := clientset.CoreV1().Pods(key.Namespace).Get(context.Background(), key.Pod, metav1.GetOptions{})
		Expect(getErr).NotTo(HaveOccurred())
		Expect(stored.Status.Phase).To(Equal(corev1.PodRunning))
	})

	It("turns an exec-time OOM into Pod state and a transport error", func() {
		clientset := fake.NewSimpleClientset()
		podRuntime := newPodRuntime(clientset)
		key := podKey{"test-namespace", "oom-pod", "main"}
		Expect(podRuntime.AddContainer(key)).To(Succeed())

		pod, err := clientset.CoreV1().Pods(key.Namespace).Get(context.Background(), key.Pod, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		pod.Labels = map[string]string{"retained": "label"}
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "sidecar", Image: "sidecar-image"})
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name:  "sidecar",
			Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		})
		_, err = clientset.CoreV1().Pods(key.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(podRuntime.InstallProgram(key, "/bin/consume-memory", program{
			Stdout:   []byte("allocating\n"),
			ExitCode: 42,
			Effect:   execOOMKillsPod,
		})).To(Succeed())

		var stdout bytes.Buffer
		err = podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			[]string{"/bin/consume-memory"}, nil, &stdout, io.Discard, false,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)
		Expect(err).To(MatchError(errExecOOMKilled))
		var exitErr *jetbridge.ExecExitError
		Expect(errors.As(err, &exitErr)).To(BeFalse())
		Expect(stdout.String()).To(Equal("allocating\n"))

		stored, err := clientset.CoreV1().Pods(key.Namespace).Get(context.Background(), key.Pod, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Labels).To(Equal(map[string]string{"retained": "label"}))
		Expect(stored.Spec.Containers).To(ContainElement(corev1.Container{Name: "sidecar", Image: "sidecar-image"}))
		Expect(stored.Status.Phase).To(Equal(corev1.PodFailed))
		Expect(stored.Status.ContainerStatuses).To(ContainElement(corev1.ContainerStatus{
			Name:  "main",
			Ready: false,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason:   "OOMKilled",
				ExitCode: 137,
			}},
		}))
	})

	It("turns exec-time deletion into Pod absence and a non-retryable transport error", func() {
		clientset := fake.NewSimpleClientset()
		podRuntime := newPodRuntime(clientset)
		key := podKey{"test-namespace", "deleted-pod", "main"}
		Expect(podRuntime.AddContainer(key)).To(Succeed())
		Expect(podRuntime.InstallProgram(key, "/bin/delete-pod", program{
			Stderr:   []byte("pod vanished\n"),
			ExitCode: 9,
			Effect:   execDeletesPod,
		})).To(Succeed())

		var stderr bytes.Buffer
		err := podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			[]string{"/bin/delete-pod"}, nil, io.Discard, &stderr, false,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)
		Expect(err).To(MatchError(errExecPodDeleted))
		Expect(err.Error()).NotTo(ContainSubstring("container not found"))
		var exitErr *jetbridge.ExecExitError
		Expect(errors.As(err, &exitErr)).To(BeFalse())
		Expect(stderr.String()).To(Equal("pod vanished\n"))
		_, err = clientset.CoreV1().Pods(key.Namespace).Get(context.Background(), key.Pod, metav1.GetOptions{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("binds state to a deterministic Pod UID and resets ephemeral state for a new incarnation", func() {
		clientset := fake.NewSimpleClientset()
		podRuntime := newPodRuntime(clientset)
		key := podKey{"test-namespace", "incarnation-pod", "main"}
		Expect(podRuntime.AddContainer(key)).To(Succeed())
		original, err := clientset.CoreV1().Pods(key.Namespace).Get(context.Background(), key.Pod, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(original.UID).NotTo(BeEmpty())

		Expect(podRuntime.PutFile(key, "/tmp/ephemeral", []byte("old pod"))).To(Succeed())
		Expect(podRuntime.InstallProgram(key, "/bin/preserved", program{Stdout: []byte("still installed")})).To(Succeed())
		incarnationEnvelope := taskSupervisorEnvelope(
			"/tmp/concourse-task-incarnation-a1b2c3d4",
			`'/bin/preserved'`,
		)
		Expect(podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			incarnationEnvelope, nil, io.Discard, io.Discard, false,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)).To(Succeed())
		Expect(podRuntime.Processes(key)).To(HaveLen(1))
		Expect(podRuntime.Processes(key)[0].Supervised).To(BeTrue())

		Expect(clientset.CoreV1().Pods(key.Namespace).Delete(context.Background(), key.Pod, metav1.DeleteOptions{})).To(Succeed())
		_, err = clientset.CoreV1().Pods(key.Namespace).Create(context.Background(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Pod},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(podRuntime.AddContainer(key)).To(Succeed())

		recreated, err := clientset.CoreV1().Pods(key.Namespace).Get(context.Background(), key.Pod, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(recreated.UID).NotTo(BeEmpty())
		Expect(recreated.UID).NotTo(Equal(original.UID))
		_, found := podRuntime.File(key, "/tmp/ephemeral")
		Expect(found).To(BeFalse())
		Expect(podRuntime.Processes(key)).To(BeEmpty())

		var stdout bytes.Buffer
		Expect(podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			incarnationEnvelope, nil, &stdout, io.Discard, false,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)).To(Succeed())
		Expect(stdout.String()).To(Equal("still installed"))
		Expect(podRuntime.Processes(key)).To(HaveLen(1))
		Expect(podRuntime.Processes(key)[0].Supervised).To(BeTrue())
	})

	It("decodes a supervised child, merges its streams, and replays completed state", func() {
		podRuntime := newPodRuntime(fake.NewSimpleClientset())
		key := podKey{"test-namespace", "supervised-pod", "main"}
		Expect(podRuntime.AddContainer(key)).To(Succeed())
		Expect(podRuntime.InstallProgram(key, "/bin/sh", program{
			Stdout:   []byte("child stdout\n"),
			Stderr:   []byte("child stderr\n"),
			ExitCode: 23,
		})).To(Succeed())

		markerArgument := `literal >>"$S/log" 2>&1; echo $? >"$S/exit.tmp" && mv "$S/exit.tmp" "$S/exit" ) & remains child data`
		encodedChild := `'/bin/sh' '-c' 'printf "%s" "$HOME"; echo it'\''s' 'two words' '$HOME "x" ;&|' 'literal >>"$S/log" 2>&1; echo $? >"$S/exit.tmp" && mv "$S/exit.tmp" "$S/exit" ) & remains child data'`
		child := []string{
			"/bin/sh",
			"-c",
			`printf "%s" "$HOME"; echo it's`,
			"two words",
			`$HOME "x" ;&|`,
			markerArgument,
		}
		firstEnvelope := taskSupervisorEnvelope("/tmp/concourse-task-first-a1b2c3d4", encodedChild)
		var firstStdout, firstStderr bytes.Buffer
		firstErr := podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			firstEnvelope, strings.NewReader("supervisor input"), &firstStdout, &firstStderr, true,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)
		var exitErr *jetbridge.ExecExitError
		Expect(errors.As(firstErr, &exitErr)).To(BeTrue())
		Expect(exitErr.ExitCode).To(Equal(23))
		Expect(firstStdout.String()).To(Equal("child stdout\nchild stderr\n"))
		Expect(firstStderr.String()).To(BeEmpty())
		Expect(podRuntime.Processes(key)).To(Equal([]modeledProcess{{
			Command:    child,
			Stdin:      []byte("supervisor input"),
			TTY:        true,
			Supervised: true,
		}}))

		var replayStdout bytes.Buffer
		replayErr := podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			firstEnvelope, nil, &replayStdout, io.Discard, true,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)
		exitErr = nil
		Expect(errors.As(replayErr, &exitErr)).To(BeTrue())
		Expect(exitErr.ExitCode).To(Equal(23))
		Expect(replayStdout.String()).To(Equal("child stdout\nchild stderr\n"))
		Expect(podRuntime.Processes(key)).To(HaveLen(1))

		secondEnvelope := taskSupervisorEnvelope("/tmp/concourse-task-second-e5f6a7b8", encodedChild)
		secondErr := podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			secondEnvelope, nil, io.Discard, io.Discard, true,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)
		exitErr = nil
		Expect(errors.As(secondErr, &exitErr)).To(BeTrue())
		Expect(exitErr.ExitCode).To(Equal(23))
		Expect(podRuntime.Processes(key)).To(HaveLen(2))
		Expect(podRuntime.Processes(key)[1]).To(Equal(modeledProcess{
			Command:    child,
			TTY:        true,
			Supervised: true,
		}))
	})

	It("keeps ordinary shell exec unsupervised and rejects malformed supervisor envelopes", func() {
		podRuntime := newPodRuntime(fake.NewSimpleClientset())
		key := podKey{"test-namespace", "raw-shell-pod", "main"}
		Expect(podRuntime.AddContainer(key)).To(Succeed())
		Expect(podRuntime.InstallProgram(key, "sh", program{Stdout: []byte("raw shell\n")})).To(Succeed())

		var stdout bytes.Buffer
		Expect(podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			[]string{"sh", "-c", "echo raw"}, nil, &stdout, io.Discard, false,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)).To(Succeed())
		Expect(stdout.String()).To(Equal("raw shell\n"))
		Expect(podRuntime.Processes(key)).To(Equal([]modeledProcess{{
			Command: []string{"sh", "-c", "echo raw"},
		}}))
		Expect(podRuntime.TerminalSessions(key)).To(BeEmpty())

		nonStepEnvelope := taskSupervisorEnvelope("/tmp/concourse-task-hijack-a1b2c3d4", `'/bin/ignored'`)
		Expect(podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			nonStepEnvelope, nil, io.Discard, io.Discard, false,
			jetbridge.ExecAttrs{Purpose: "hijack"},
		)).To(Succeed())
		Expect(podRuntime.Processes(key)[1]).To(Equal(modeledProcess{Command: nonStepEnvelope}))

		malformed := []string{
			"sh",
			"-c",
			"S='/tmp/concourse-task-malformed-a1b2c3d4'\n  ( trap '' HUP; /bin/sh -c broken >>\"$S/log\" 2>&1; echo $? >\"$S/exit.tmp\" && mv \"$S/exit.tmp\" \"$S/exit\" ) &",
		}
		err := podRuntime.ExecInPod(
			context.Background(), key.Namespace, key.Pod, key.Container,
			malformed, nil, io.Discard, io.Discard, false,
			jetbridge.ExecAttrs{Purpose: "step-command"},
		)
		Expect(err).To(MatchError(errMalformedSupervisorEnvelope))
		Expect(podRuntime.Processes(key)).To(HaveLen(2))
	})
})
