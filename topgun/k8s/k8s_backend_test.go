package k8s_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

// These tests validate the K8s execution backend end-to-end. They require
// a real Kubernetes cluster and deploy Concourse with --kubernetes-namespace
// enabled so that task and resource steps run as K8s Pods instead of Garden
// containers.
//
// To run: go test -tags topgun ./topgun/k8s/ -run "K8s Backend"
var _ = Describe("K8s Backend", func() {
	JustBeforeEach(func() {
		setReleaseNameAndNamespace("k8sbe")

		deployConcourseChart(releaseName,
			"--set=worker.replicas=0",
			"--set=web.additionalArgs[0]=--kubernetes-namespace="+namespace,
		)

		_ = waitAndLogin(namespace, releaseName+"-web")
	})

	AfterEach(func() {
		helmDestroy(releaseName, namespace)
	})

	Describe("task execution", func() {
		It("runs a simple task as a K8s Pod", func() {
			fly.Run("set-pipeline", "-n",
				"-c", "pipelines/simple-task.yml",
				"-p", "k8s-task-pipeline",
			)
			fly.Run("unpause-pipeline", "-p", "k8s-task-pipeline")

			session := fly.Start("trigger-job", "-j", "k8s-task-pipeline/simple-job", "-w")
			Eventually(session, 5*time.Minute).Should(gbytes.Say("hello from k8s"))
			Eventually(session, 1*time.Minute).Should(gexec.Exit(0))
		})
	})

	Describe("resource steps", func() {
		It("runs get and put steps as K8s Pods", func() {
			fly.Run("set-pipeline", "-n",
				"-c", "pipelines/get-put-resource.yml",
				"-p", "k8s-resource-pipeline",
			)
			fly.Run("unpause-pipeline", "-p", "k8s-resource-pipeline")

			session := fly.Start("trigger-job", "-j", "k8s-resource-pipeline/resource-job", "-w")
			Eventually(session, 5*time.Minute).Should(gexec.Exit(0))
		})
	})

	Describe("build cancellation", func() {
		It("cleans up K8s Pods when a build is cancelled", func() {
			fly.Run("set-pipeline", "-n",
				"-c", "pipelines/task-waiting.yml",
				"-p", "k8s-cancel-pipeline",
			)
			fly.Run("unpause-pipeline", "-p", "k8s-cancel-pipeline")

			session := fly.Start("trigger-job", "-j", "k8s-cancel-pipeline/simple-job")

			By("waiting for the task to start running")
			Eventually(func() bool {
				containers := fly.GetContainers()
				for _, c := range containers {
					if c.Type == "task" && c.State == "created" {
						return true
					}
				}
				return false
			}, 2*time.Minute, 10*time.Second).Should(BeTrue())

			By("aborting the build")
			fly.Run("abort-build", "-j", "k8s-cancel-pipeline/simple-job", "-b", "1")

			By("verifying the build finished and Pod was cleaned up")
			Eventually(session, 1*time.Minute).Should(gexec.Exit(3))
		})
	})
})
