package jetbridge

import (
	"fmt"
	"strconv"

	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
)

// recoverExecProcess interprets the persisted completion record. Pod phase is
// not evidence that an exec command completed: even a terminal pause pod may
// have died before the command ran.
func recoverExecProcess(podName, processID string, pod *corev1.Pod) (runtime.Process, error) {
	if statusStr, ok := pod.Annotations[exitStatusAnnotationKey]; ok {
		status, err := strconv.Atoi(statusStr)
		if err == nil {
			return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: status}}, nil
		}
	}
	return nil, fmt.Errorf("attach: exec-mode pod %q has no completion status", podName)
}
