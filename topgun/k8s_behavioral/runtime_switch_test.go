package behavioral_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	authorizationv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The production deployment runs stepRuntime job and stepPrivilege any, so
// three 0.4.6 changes have no live home there: the reaper collecting step Jobs
// under exec, the reaper's quiet report of a Role without batch/jobs, and the
// granted privilege mode. These specs move this proc's deployment through
// them with helm upgrades and put it back as the suite set it.
//
// One Ordered container, two upgrades: to job + granted (BeforeAll), then back
// to the suite's own values, which is exec + any, in the middle of the switch
// spec. AfterAll repeats the second if a failure left the deployment elsewhere.

const (
	grantedJob    = "granted-job"
	ungrantedJob  = "ungranted-job"
	grantedType   = "priv-mock"
	ungrantedType = "priv-mock-ungranted"

	taskGrantName  = "beh-granted-task"
	checkGrantName = "beh-granted-check"

	// holdPolicyName is the ValidatingAdmissionPolicy that keeps the web's
	// ServiceAccount from deleting one pipeline's step Jobs. It holds the
	// Jobs a job-runtime web leaves through the helm upgrade: the upgrade
	// gives the Role list and delete before the old web is gone, and the old
	// web would collect them itself.
	holdPolicyName = "k8s-beh-hold-step-jobs"

	deployedBase       = "base"
	deployedJobGranted = "job-granted"
	deployedUnknown    = "unknown"
)

var _ = Describe("0.4.6 step runtime and privilege mode, through the chart", Ordered, func() {
	var (
		// privPipeline is the pipeline the job-granted deployment's grants
		// name. A grant binds a pipeline's name, so it is chosen before the
		// upgrade and the privilege spec sets it as its pipelineName.
		privPipeline string
		// deployed is what this proc's chart release currently holds.
		deployed string
		// savedRoleRules is the pod-manager Role's rules while a spec has
		// taken batch/jobs out of it; nil otherwise.
		savedRoleRules []rbacv1.PolicyRule
		// holdPipeline is the pipeline whose step Jobs the hold policy
		// protects, for AfterAll to clear what a failure left.
		holdPipeline string
	)

	ensureDeployed := func(want string) {
		GinkgoHelper()
		if deployed == want {
			return
		}
		var sets []string
		if want == deployedJobGranted {
			sets = []string{
				"kubernetes.stepRuntime=job",
				"kubernetes.stepPrivilege=granted",
				"kubernetes.stepPodGrants[0].name=" + taskGrantName,
				"kubernetes.stepPodGrants[0].owner=main/" + privPipeline + "/" + grantedJob,
				"kubernetes.stepPodGrants[0].privileged=true",
				"kubernetes.stepPodGrants[1].name=" + checkGrantName,
				"kubernetes.stepPodGrants[1].owner=main/" + privPipeline + "/resource-type/" + grantedType,
				"kubernetes.stepPodGrants[1].privileged=true",
			}
		}
		deployed = deployedUnknown
		upgradeConcourse(sets...)
		deployed = want
	}

	BeforeAll(func() {
		privPipeline = randomPipelineName()
		deployed = deployedBase
		ensureDeployed(deployedJobGranted)
	})

	AfterAll(func() {
		removeHoldPolicy()
		if savedRoleRules != nil {
			restorePodManagerRules(savedRoleRules)
			savedRoleRules = nil
		}
		if holdPipeline != "" {
			deleteStepJobsOfPipeline(holdPipeline)
		}
		ensureDeployed(deployedBase)
	})

	It("admits privilege under granted only through a grant that declares it", func() {
		ensureDeployed(deployedJobGranted)
		pipelineName = privPipeline

		By("the web logs the mode and its privileged grants at startup")
		line := webLogLine("step-privilege-mode")
		Expect(line).To(ContainSubstring(`"mode":"granted"`))
		Expect(line).To(ContainSubstring(taskGrantName + "=main/" + privPipeline + "/" + grantedJob))
		Expect(line).To(ContainSubstring(checkGrantName + "=main/" + privPipeline + "/resource-type/" + grantedType))

		pipelineFile := writePipelineFile("step-privilege.yml", fmt.Sprintf(`
resource_types:
- name: %[1]s
  type: registry-image
  privileged: true
  source: {repository: concourse/mock-resource}
- name: %[2]s
  type: registry-image
  privileged: true
  source: {repository: concourse/mock-resource}

resources:
- name: granted-res
  type: %[1]s
  source: {}
- name: ungranted-res
  type: %[2]s
  source: {}

jobs:
- name: %[3]s
  plan:
  - task: mount-tmpfs
    privileged: true
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -ec
        - |
          mkdir -p /tmp/privilege-probe
          mount -t tmpfs tmpfs /tmp/privilege-probe
          echo privileged-mount-ok
- name: %[4]s
  plan:
  - task: refused
    privileged: true
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["ungranted-task-ran"]
`, grantedType, ungrantedType, grantedJob, ungrantedJob))
		setAndUnpausePipeline(pipelineFile)

		By("a privileged task of the granted job runs privileged: it mounts a tmpfs")
		granted := recordStepObjects(fmt.Sprintf("concourse.ci/pipeline=%s,concourse.ci/job=%s,concourse.ci/type=task", pipelineName, grantedJob))
		triggerJob(grantedJob)
		session := waitForBuildAndWatch(grantedJob)
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("privileged-mount-ok"))
		grantedPods, grantedJobs := granted()
		Expect(grantedJobs).ToNot(BeEmpty(), "the job step runtime runs the task as a Job")
		Expect(grantedPods).ToNot(BeEmpty())
		for _, pod := range grantedPods {
			main := containerByName(&pod, "main")
			Expect(main).ToNot(BeNil(), "pod %s has no main container", pod.Name)
			Expect(main.SecurityContext).ToNot(BeNil())
			Expect(main.SecurityContext.Privileged).To(HaveValue(BeTrue()), "pod %s main is not privileged", pod.Name)
		}

		By("a privileged task of a job no grant names fails naming its owner, and makes no pod or Job")
		ungranted := recordStepObjects(fmt.Sprintf("concourse.ci/pipeline=%s,concourse.ci/job=%s,concourse.ci/type=task", pipelineName, ungrantedJob))
		triggerJob(ungrantedJob)
		session = waitForBuildAndWatch(ungrantedJob)
		output := string(session.Out.Contents()) + string(session.Err.Contents())
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(output).To(ContainSubstring(`container "main" is privileged`))
		Expect(output).To(ContainSubstring("--kubernetes-step-privilege=granted"))
		Expect(output).To(ContainSubstring("Owner main/" + pipelineName + "/" + ungrantedJob + " has no step pod grant"))
		Expect(output).ToNot(ContainSubstring("ungranted-task-ran"))
		refusedPods, refusedJobs := ungranted()
		Expect(refusedPods).To(BeEmpty())
		Expect(refusedJobs).To(BeEmpty())

		By("a privileged one-off is refused: main/one-off holds no grant")
		oneOff := recordStepObjects("concourse.ci/type=task,!concourse.ci/pipeline")
		taskFile := writeTaskFile("privileged-one-off.yml", `
platform: linux
image_resource: {type: registry-image, source: {repository: busybox}}
run:
  path: echo
  args: ["privileged-one-off-ran"]
`)
		session = fly.Start("execute", "-p", "-c", taskFile)
		Eventually(session, 3*time.Minute).Should(gexec.Exit())
		output = string(session.Out.Contents()) + string(session.Err.Contents())
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(output).To(ContainSubstring("Owner main/one-off has no step pod grant"))
		Expect(output).ToNot(ContainSubstring("privileged-one-off-ran"))
		oneOffPods, oneOffJobs := oneOff()
		Expect(oneOffPods).To(BeEmpty())
		Expect(oneOffJobs).To(BeEmpty())

		By("a privileged resource type's check runs privileged under its resource-type grant")
		checks := recordStepObjects("concourse.ci/type=check")
		newMockVersion("granted-res", "granted")
		checkPods, _ := checks()
		var granteds []corev1.Pod
		for _, pod := range checkPods {
			if pod.Annotations["concourse.dev/step-pod-grant"] == checkGrantName {
				granteds = append(granteds, pod)
			}
		}
		Expect(granteds).ToNot(BeEmpty(), "no check pod was annotated with grant %q", checkGrantName)
		for _, pod := range granteds {
			Expect(privilegedContainers(&pod)).ToNot(BeEmpty(), "check pod %s holds no privilege", pod.Name)
		}

		By("a privileged resource type no grant names cannot run its check")
		session = fly.Start("check-resource", "-r", inPipeline("ungranted-res"))
		Eventually(session, 3*time.Minute).Should(gexec.Exit())
		output = string(session.Out.Contents()) + string(session.Err.Contents())
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(output).To(ContainSubstring("Owner main/" + pipelineName + "/resource-type/" + ungrantedType + " has no step pod grant"))
	})

	It("collects under exec the step Jobs a job deployment left, and reports a Role without batch/jobs once", func() {
		// A retried attempt starts where the first did.
		ensureDeployed(deployedJobGranted)
		removeHoldPolicy()
		if savedRoleRules != nil {
			restorePodManagerRules(savedRoleRules)
			savedRoleRules = nil
		}
		holdPipeline = pipelineName
		serviceAccount := webServiceAccount()

		By("holding every step Job of this pipeline from the web's deletes")
		applyHoldPolicy(serviceAccount, pipelineName, "")

		pipelineFile := writePipelineFile("runtime-switch.yml", `
jobs:
- name: leave
  plan:
  - task: first
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run: {path: echo, args: ["left-behind"]}
- name: held
  plan:
  - task: hold
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run: {path: sleep, args: ["1800"]}
- name: freed
  plan:
  - task: free
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run: {path: sleep, args: ["1800"]}
`)
		setAndUnpausePipeline(pipelineFile)

		By("under job, a finished step and two aborted ones leave their Jobs")
		triggerJob("leave")
		session := waitForBuildAndWatch("leave")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("left-behind"))

		for _, job := range []string{"held", "freed"} {
			triggerJob(job)
			waitForPodWithLabel(fmt.Sprintf("concourse.ci/pipeline=%s,concourse.ci/job=%s,concourse.ci/type=task", pipelineName, job), corev1.PodRunning)
			fly.Run("abort-build", "-j", inPipeline(job), "-b", "1")
			Eventually(func() string {
				rows := flyTable("builds", "-j", inPipeline(job))
				if len(rows) == 0 {
					return ""
				}
				return rows[0]["status"]
			}, 2*time.Minute, time.Second).Should(Equal("aborted"), "build %s/1", job)
		}

		left := map[string]types.UID{}
		for _, job := range stepJobsOfPipeline(pipelineName) {
			Expect(job.Labels["concourse.ci/runtime"]).To(Equal("job"), "Job %s", job.Name)
			if job.Labels["concourse.ci/type"] == "task" {
				left[job.Labels["concourse.ci/step"]] = job.UID
			}
		}
		Expect(left).To(HaveKey("first"))
		Expect(left).To(HaveKey("hold"))
		Expect(left).To(HaveKey("free"))
		leftUIDs := func() []types.UID {
			var uids []types.UID
			for _, job := range stepJobsOfPipeline(pipelineName) {
				uids = append(uids, job.UID)
			}
			return uids
		}
		Consistently(leftUIDs, 25*time.Second, time.Second).Should(ContainElements(left["first"], left["hold"], left["free"]),
			"the job runtime's web collected a held step Job")
		holdPods := getPods(fmt.Sprintf("concourse.ci/worker,concourse.ci/pipeline=%s,concourse.ci/job=held,concourse.ci/type=task", pipelineName))
		Expect(holdPods).To(HaveLen(1), "the held step's pod")
		holdPod := holdPods[0].Name

		By("switching the step runtime back to exec")
		ensureDeployed(deployedBase)
		Expect(leftUIDs()).To(ContainElements(left["first"], left["hold"], left["free"]))

		By("the chart's exec Role lets the web list and delete step Jobs, and nothing more")
		for _, verb := range []string{"list", "delete"} {
			Expect(serviceAccountMay(serviceAccount, verb, "batch", "jobs")).To(BeTrue(), "batch/jobs %s under exec", verb)
		}
		for _, verb := range []string{"get", "watch", "create", "update", "patch"} {
			Expect(serviceAccountMay(serviceAccount, verb, "batch", "jobs")).To(BeFalse(), "batch/jobs %s under exec", verb)
		}

		By("one step Job the web cannot delete does not stop the pass: the others are collected")
		applyHoldPolicy(serviceAccount, pipelineName, "hold")
		Eventually(leftUIDs, 3*time.Minute, time.Second).ShouldNot(ContainElement(left["first"]))
		Eventually(leftUIDs, 3*time.Minute, time.Second).ShouldNot(ContainElement(left["free"]))
		for _, job := range []string{"leave", "freed"} {
			Eventually(func() []corev1.Pod {
				return getPods(fmt.Sprintf("concourse.ci/worker,concourse.ci/pipeline=%s,concourse.ci/job=%s", pipelineName, job))
			}, 3*time.Minute, time.Second).Should(BeEmpty(), "pods of %s", job)
		}
		Consistently(leftUIDs, 25*time.Second, time.Second).Should(ContainElement(left["hold"]))
		Expect(currentWebPodLog()).To(MatchRegexp(`failed-to-delete-pod"[^\n]*"pod":"`+holdPod+`"`),
			"the refused delete is logged and left for the next pass")

		By("an older chart's Role, with no batch/jobs at all, is reported once at info")
		Expect(currentWebPodLog()).ToNot(ContainSubstring(`step-jobs-not-collected"`))
		savedRoleRules = stripStepJobRules()
		Eventually(func() bool {
			return serviceAccountMay(serviceAccount, "list", "batch", "jobs")
		}, time.Minute, time.Second).Should(BeFalse())
		Eventually(func() int {
			return strings.Count(currentWebPodLog(), `step-jobs-not-collected"`)
		}, time.Minute, time.Second).Should(Equal(1))
		// Three passes or more: the note is not repeated, and no pass errors on
		// the step Jobs it can no longer list or delete.
		Consistently(func() int {
			return strings.Count(currentWebPodLog(), `step-jobs-not-collected"`)
		}, 35*time.Second, time.Second).Should(Equal(1))
		webLog := currentWebPodLog()
		Expect(webLog).To(MatchRegexp(`"level":"info"[^\n]*step-jobs-not-collected"`))
		Expect(webLog).ToNot(ContainSubstring(`failed-to-list-step-jobs"`))
		afterNote := webLog[strings.Index(webLog, `step-jobs-not-collected"`):]
		afterNote = afterNote[strings.Index(afterNote, "\n")+1:]
		Expect(afterNote).ToNot(ContainSubstring(`failed-to-delete-pod"`))
		Expect(afterNote).ToNot(ContainSubstring(`failed-to-cleanup-completed-pod"`))
		Expect(leftUIDs()).To(ContainElement(left["hold"]))

		By("restoring the chart's Role and lifting the hold")
		restorePodManagerRules(savedRoleRules)
		savedRoleRules = nil
		Eventually(func() bool {
			return serviceAccountMay(serviceAccount, "delete", "batch", "jobs")
		}, time.Minute, time.Second).Should(BeTrue())
		removeHoldPolicy()

		By("the exec web collects the last step Job and its pod")
		Eventually(leftUIDs, 3*time.Minute, time.Second).Should(BeEmpty())
		waitForNoConcourseWorkloadPods()
		holdPipeline = ""
	})
})

// upgradeConcourse runs the suite's helm upgrade with extra values and waits
// until the new web is the only web and answers.
func upgradeConcourse(sets ...string) {
	GinkgoHelper()
	image := envOr("CONCOURSE_IMAGE", "concourse-local:latest")
	chartPath := filepath.Join(mustRepoRoot(), "deploy", "chart")
	helmDeployConcourse(config.Kubeconfig, config.Namespace, chartPath, image, sets...)
	waitForWebRollout()
	waitForAPI(config.ATCURL, 5*time.Minute)
}

// waitForWebRollout waits until the web Deployment's rollout is over and its
// old pod is gone, terminating included, so only the new web's process runs.
// helmDeployConcourse's own wait can be satisfied by the old pod.
func waitForWebRollout() {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		deployments, err := kubeClient.AppsV1().Deployments(config.Namespace).List(context.Background(), metav1.ListOptions{})
		g.Expect(err).ToNot(HaveOccurred())
		var found int
		for _, deployment := range deployments.Items {
			if deployment.Spec.Template.Labels["app.kubernetes.io/component"] != "web" {
				continue
			}
			found++
			want := int32(1)
			if deployment.Spec.Replicas != nil {
				want = *deployment.Spec.Replicas
			}
			g.Expect(deployment.Status.ObservedGeneration).To(BeNumerically(">=", deployment.Generation))
			g.Expect(deployment.Status.UpdatedReplicas).To(Equal(want))
			g.Expect(deployment.Status.AvailableReplicas).To(Equal(want))
			g.Expect(deployment.Status.Replicas).To(Equal(want))
			g.Expect(getPods("app.kubernetes.io/component=web")).To(HaveLen(int(want)))
		}
		g.Expect(found).To(Equal(1), "web Deployments")
	}, 6*time.Minute, 2*time.Second).Should(Succeed())
}

// currentWebPod is the one web pod. Specs here run after waitForWebRollout.
func currentWebPod() corev1.Pod {
	GinkgoHelper()
	pods := getPods("app.kubernetes.io/component=web")
	Expect(pods).To(HaveLen(1), "web pods")
	return pods[0]
}

// currentWebPodLog is the web container's whole log since its process started.
func currentWebPodLog() string {
	GinkgoHelper()
	pod := currentWebPod()
	raw, err := kubeClient.CoreV1().Pods(config.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: "concourse-web",
	}).DoRaw(context.Background())
	Expect(err).ToNot(HaveOccurred())
	return string(raw)
}

// webLogLine is the web's first log line holding message.
func webLogLine(message string) string {
	GinkgoHelper()
	var line string
	Eventually(func() string {
		for _, l := range strings.Split(currentWebPodLog(), "\n") {
			if strings.Contains(l, message+`"`) {
				line = l
				return line
			}
		}
		return ""
	}, time.Minute, time.Second).ShouldNot(BeEmpty(), "the web never logged %s", message)
	return line
}

// webServiceAccount is the ServiceAccount the web pod runs as.
func webServiceAccount() string {
	GinkgoHelper()
	pod := currentWebPod()
	Expect(pod.Spec.ServiceAccountName).ToNot(BeEmpty())
	return pod.Spec.ServiceAccountName
}

func serviceAccountUser(serviceAccount string) string {
	return "system:serviceaccount:" + config.Namespace + ":" + serviceAccount
}

// serviceAccountMay asks the API server whether serviceAccount may verb the
// resource in the suite's namespace.
func serviceAccountMay(serviceAccount, verb, group, resource string) bool {
	GinkgoHelper()
	review, err := kubeClient.AuthorizationV1().SubjectAccessReviews().Create(context.Background(), &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   serviceAccountUser(serviceAccount),
			Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + config.Namespace, "system:authenticated"},
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: config.Namespace,
				Verb:      verb,
				Group:     group,
				Resource:  resource,
			},
		},
	}, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	return review.Status.Allowed
}

// podManagerRole is the chart's pod-manager Role, the web's namespaced grant.
func podManagerRole() *rbacv1.Role {
	GinkgoHelper()
	roles, err := kubeClient.RbacV1().Roles(config.Namespace).List(context.Background(), metav1.ListOptions{})
	Expect(err).ToNot(HaveOccurred())
	var found []rbacv1.Role
	for _, role := range roles.Items {
		if strings.HasSuffix(role.Name, "-pod-manager") {
			found = append(found, role)
		}
	}
	Expect(found).To(HaveLen(1), "pod-manager Roles")
	return &found[0]
}

func isStepJobRule(rule rbacv1.PolicyRule) bool {
	return slices.Contains(rule.APIGroups, "batch") && slices.Contains(rule.Resources, "jobs")
}

// stripStepJobRules takes the batch/jobs rule out of the pod-manager Role, as
// a chart older than the web renders it, and returns the rules it had.
func stripStepJobRules() []rbacv1.PolicyRule {
	GinkgoHelper()
	role := podManagerRole()
	saved := slices.Clone(role.Rules)
	before := len(role.Rules)
	role.Rules = slices.DeleteFunc(role.Rules, isStepJobRule)
	Expect(before-len(role.Rules)).To(Equal(1), "batch/jobs rules in the pod-manager Role")
	_, err := kubeClient.RbacV1().Roles(config.Namespace).Update(context.Background(), role, metav1.UpdateOptions{})
	Expect(err).ToNot(HaveOccurred())
	return saved
}

func restorePodManagerRules(rules []rbacv1.PolicyRule) {
	GinkgoHelper()
	Eventually(func() error {
		role := podManagerRole()
		role.Rules = rules
		_, err := kubeClient.RbacV1().Roles(config.Namespace).Update(context.Background(), role, metav1.UpdateOptions{})
		return err
	}, time.Minute, time.Second).Should(Succeed())
}

// stepJobsOfPipeline lists the step Jobs (the worker's label: the reaper's own
// selector) labelled with pipeline's name.
func stepJobsOfPipeline(pipeline string) []batchv1.Job {
	GinkgoHelper()
	jobs, err := kubeClient.BatchV1().Jobs(config.Namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: "concourse.ci/worker,concourse.ci/pipeline=" + pipeline,
	})
	Expect(err).ToNot(HaveOccurred())
	return jobs.Items
}

// deleteStepJobsOfPipeline deletes, as the suite's admin, the Jobs a failed
// spec left, so the next specs on this cluster start without them.
func deleteStepJobsOfPipeline(pipeline string) {
	propagation := metav1.DeletePropagationBackground
	_ = kubeClient.BatchV1().Jobs(config.Namespace).DeleteCollection(context.Background(),
		metav1.DeleteOptions{PropagationPolicy: &propagation},
		metav1.ListOptions{LabelSelector: "concourse.ci/pipeline=" + pipeline})
}

// applyHoldPolicy refuses every delete, by the web's ServiceAccount, of a Job
// labelled with pipeline's name (and, when onlyStep is set, with that step
// name), and waits until the API server enforces exactly that. The refusal is
// 422 Invalid, not 403: a refusal the reaper must log as an error and step
// past, distinct from the Forbidden of a Role without batch/jobs, which it
// reports once and keeps quiet about. Applying it again replaces it.
func applyHoldPolicy(serviceAccount, pipeline, onlyStep string) {
	GinkgoHelper()
	held := fmt.Sprintf("has(oldObject.metadata.labels) && "+
		"'concourse.ci/pipeline' in oldObject.metadata.labels && oldObject.metadata.labels['concourse.ci/pipeline'] == '%s'", pipeline)
	if onlyStep != "" {
		held += fmt.Sprintf(" && 'concourse.ci/step' in oldObject.metadata.labels && oldObject.metadata.labels['concourse.ci/step'] == '%s'", onlyStep)
	}
	manifest := fmt.Sprintf(`
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: %[1]s
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
    - apiGroups: ["batch"]
      apiVersions: ["v1"]
      operations: ["DELETE"]
      resources: ["jobs"]
  matchConditions:
  - name: web-service-account
    expression: "request.userInfo.username == '%[2]s'"
  validations:
  - expression: "!(%[3]s)"
    reason: Invalid
    message: "held by the k8s-behavioral runtime switch spec"
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: %[1]s
spec:
  policyName: %[1]s
  validationActions: [Deny]
`, holdPolicyName, serviceAccountUser(serviceAccount), held)
	apply := exec.Command("kubectl", "--kubeconfig", config.Kubeconfig, "apply", "-f", "-")
	apply.Stdin = strings.NewReader(manifest)
	apply.Stdout = os.Stderr
	apply.Stderr = os.Stderr
	Expect(apply.Run()).To(Succeed())

	// Canary Jobs of the pipeline, unknown to the reaper (no worker label),
	// show when the policy in force is this one: a dry-run delete as the web's
	// ServiceAccount is refused for a held step, and admitted for another.
	impersonated := rest.CopyConfig(restConfig)
	impersonated.Impersonate = rest.ImpersonationConfig{UserName: serviceAccountUser(serviceAccount)}
	asWeb, err := kubernetes.NewForConfig(impersonated)
	Expect(err).ToNot(HaveOccurred())
	dryRunDelete := func(name string) func() error {
		return func() error {
			return asWeb.BatchV1().Jobs(config.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{
				DryRun: []string{metav1.DryRunAll},
			})
		}
	}

	heldStep := onlyStep
	if heldStep == "" {
		heldStep = "canary"
	}
	heldCanary := createCanaryJob(pipeline, heldStep)
	defer deleteJobAsAdmin(heldCanary)
	Eventually(dryRunDelete(heldCanary), time.Minute, time.Second).Should(SatisfyAll(
		WithTransform(apierrors.IsInvalid, BeTrue()),
		MatchError(ContainSubstring(holdPolicyName)),
	))
	if onlyStep != "" {
		freeCanary := createCanaryJob(pipeline, onlyStep+"-not")
		defer deleteJobAsAdmin(freeCanary)
		Eventually(dryRunDelete(freeCanary), time.Minute, time.Second).Should(Succeed())
	}
}

// createCanaryJob creates a suspended Job labelled with pipeline and step and
// no worker label, so the reaper never sees it, and returns its name.
func createCanaryJob(pipeline, step string) string {
	GinkgoHelper()
	suspend := true
	canary, err := kubeClient.BatchV1().Jobs(config.Namespace).Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "k8s-beh-hold-canary-",
			Labels:       map[string]string{"concourse.ci/pipeline": pipeline, "concourse.ci/step": step},
		},
		Spec: batchv1.JobSpec{
			Suspend: &suspend,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "main", Image: "busybox:latest", Command: []string{"true"}}},
				},
			},
		},
	}, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	return canary.Name
}

func deleteJobAsAdmin(name string) {
	propagation := metav1.DeletePropagationBackground
	_ = kubeClient.BatchV1().Jobs(config.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{PropagationPolicy: &propagation})
}

// removeHoldPolicy deletes the hold policy and its binding, if present.
func removeHoldPolicy() {
	GinkgoHelper()
	for _, kind := range []string{"validatingadmissionpolicybinding", "validatingadmissionpolicy"} {
		cmd := exec.Command("kubectl", "--kubeconfig", config.Kubeconfig, "delete", kind, holdPolicyName, "--ignore-not-found")
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		Expect(cmd.Run()).To(Succeed())
	}
}

// recordStepObjects watches the pods and Jobs matching selector from now on
// and returns a stop function that answers each one created meanwhile, as
// first seen. A watch that ends before stop fails the spec: it could have
// missed a creation.
func recordStepObjects(selector string) func() ([]corev1.Pod, []batchv1.Job) {
	GinkgoHelper()
	ctx, cancel := context.WithCancel(context.Background())
	listOpts := metav1.ListOptions{LabelSelector: selector}

	podList, err := kubeClient.CoreV1().Pods(config.Namespace).List(ctx, listOpts)
	Expect(err).ToNot(HaveOccurred())
	podWatch, err := kubeClient.CoreV1().Pods(config.Namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector: selector, ResourceVersion: podList.ResourceVersion,
	})
	Expect(err).ToNot(HaveOccurred())
	jobList, err := kubeClient.BatchV1().Jobs(config.Namespace).List(ctx, listOpts)
	Expect(err).ToNot(HaveOccurred())
	jobWatch, err := kubeClient.BatchV1().Jobs(config.Namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector: selector, ResourceVersion: jobList.ResourceVersion,
	})
	Expect(err).ToNot(HaveOccurred())

	var (
		mu          sync.Mutex
		pods        []corev1.Pod
		jobs        []batchv1.Job
		seen        = map[string]bool{}
		endedEarly  []string
		stopping    bool
		wg          sync.WaitGroup
		recordEvent = func(kind string, ev watch.Event) {
			mu.Lock()
			defer mu.Unlock()
			if ev.Type != watch.Added && ev.Type != watch.Modified {
				return
			}
			switch obj := ev.Object.(type) {
			case *corev1.Pod:
				if !seen["pod/"+obj.Name] {
					seen["pod/"+obj.Name] = true
					pods = append(pods, *obj)
				}
			case *batchv1.Job:
				if !seen["job/"+obj.Name] {
					seen["job/"+obj.Name] = true
					jobs = append(jobs, *obj)
				}
			}
		}
	)
	follow := func(kind string, w watch.Interface) {
		defer wg.Done()
		for ev := range w.ResultChan() {
			if ev.Type == watch.Error {
				mu.Lock()
				endedEarly = append(endedEarly, fmt.Sprintf("%s watch error: %v", kind, apierrors.FromObject(ev.Object)))
				mu.Unlock()
				continue
			}
			recordEvent(kind, ev)
		}
		mu.Lock()
		if !stopping {
			endedEarly = append(endedEarly, kind+" watch ended early")
		}
		mu.Unlock()
	}
	wg.Add(2)
	go follow("pod", podWatch)
	go follow("job", jobWatch)

	return func() ([]corev1.Pod, []batchv1.Job) {
		GinkgoHelper()
		mu.Lock()
		stopping = true
		mu.Unlock()
		podWatch.Stop()
		jobWatch.Stop()
		cancel()
		wg.Wait()
		Expect(endedEarly).To(BeEmpty(), "watching %q", selector)
		return pods, jobs
	}
}

// privilegedContainers names the containers and init containers of pod that
// run privileged.
func privilegedContainers(pod *corev1.Pod) []string {
	var names []string
	for _, container := range append(slices.Clone(pod.Spec.InitContainers), pod.Spec.Containers...) {
		if container.SecurityContext != nil && container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged {
			names = append(names, container.Name)
		}
	}
	return names
}
