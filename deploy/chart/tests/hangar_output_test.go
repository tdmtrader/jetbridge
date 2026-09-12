package tests

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// The output plane's rendered surface.
//
// Every workload here exists because a Kubernetes service account is Pod-wide.
// The publisher may create and get objects; inventory may list and get
// bucket-wide; the reclaimer may get and delete; the attestor reads bucket
// lifecycle and IAM and holds no object permission at all. Those are four
// disjoint cloud identities, so they are four Pods with four KSAs, and the
// chart's job is to render that separation and to refuse the configurations
// that quietly collapse it.
//
// The chart does NOT create buckets, lifecycle rules or GCP IAM. It renders
// explicit identities and names what an operator must provision; activation is
// what proves the provisioning is real. A render test can say a KSA exists and
// that no other workload mounts a private key. It cannot say an IAM binding is
// what the annotation claims, and nothing here pretends otherwise.

const (
	outputDaemonComponent    = "hangar-output-daemon"
	outputInventoryComponent = "hangar-output-inventory"
	outputReclaimerComponent = "hangar-output-reclaimer"
	outputAttestorComponent  = "hangar-output-policy-attestor"
)

// baseControlSets turn on the BASE exact-execution-control facet and nothing
// else. It is a real deployment on its own: the sibling `exact_execution_control`
// track schedules onto exactly this cohort.
var baseControlSets = []string{
	"artifactDaemon.enabled=true",
	"hangarOutput.executionControl.enabled=true",
	"hangarOutput.executionControl.keySecret=op-control-key",
	"hangarOutput.executionControl.keyID=control-key-7",
	"hangarOutput.capabilityKeySecret=op-capability-key",
	"hangarOutput.daemon.tls.existingSecret=op-output-daemon-tls",
	"hangarOutput.daemon.tls.clientSecret=op-output-daemon-client-tls",
	"hangarOutput.activationEpoch=7",
	// Required under the BASE switch, not the output one: the DaemonSet, its
	// scratch emptyDir and its --scratch-dir flag all render here.
	"hangarOutput.daemon.scratch.sizeLimit=32Gi",
}

// outputSets add the OUTPUT capture facet on top of the base one.
var outputSets = append(append([]string{}, baseControlSets...),
	"hangarOutput.enabled=true",
	"hangarOutput.bucket=jb-output",
	"hangarOutput.prefix=cluster-a",
	"hangarOutput.tenant=tenant-a",
	"hangarOutput.cacheBucket=jb-cache",
	"hangarOutput.strictInputBucket=jb-strict-input",
	"hangarOutput.receipt.keyID=receipt-7",
	"hangarOutput.receipt.privateKeySecret=op-receipt-private",
	"hangarOutput.receipt.publicKeys[0].id=receipt-7",
	"hangarOutput.receipt.publicKeys[0].epoch=7",
	"hangarOutput.receipt.publicKeys[0].key=cHVibGljLWtleS1ieXRlcw==",
	"hangarOutput.materializationKeySecret=op-output-materialize",
	"hangarOutput.database.existingSecret=op-activation-db",
	// The four Workload Identity annotations. The output facet requires them:
	// the policy attestor compares the bucket's IAM policy against these four
	// members, so a plane that does not declare them can attest nothing. See
	// hangar_output_principals_test.go.
	`hangarOutput.daemon.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=publisher@p.iam.gserviceaccount.com`,
	`hangarOutput.inventory.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=inventory@p.iam.gserviceaccount.com`,
	`hangarOutput.reclaimer.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=reclaimer@p.iam.gserviceaccount.com`,
	`hangarOutput.policyAttestor.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=attestor@p.iam.gserviceaccount.com`,
)

func renderBaseControl(t *testing.T, extra ...string) string {
	t.Helper()

	return render(t, append(append([]string{}, baseControlSets...), extra...)...)
}

func renderOutput(t *testing.T, extra ...string) string {
	t.Helper()

	return render(t, append(append([]string{}, outputSets...), extra...)...)
}

// renderOutputError requires the render to FAIL and returns helm's message.
func renderOutputError(t *testing.T, extra ...string) string {
	t.Helper()

	return renderHangarError(t, append(append([]string{}, outputSets...), extra...)...)
}

// document is one rendered manifest with the template it came from.
type document struct {
	source string
	body   string
	kind   string
	name   string
}

func documentsIn(t *testing.T, out string) []document {
	t.Helper()

	var documents []document
	for _, chunk := range splitDocuments(out) {
		var object renderedObject
		if err := yaml.Unmarshal([]byte(chunk), &object); err != nil {
			continue
		}
		if object.Kind == "" {
			continue
		}
		documents = append(documents, document{
			source: sourceOf(chunk),
			body:   chunk,
			kind:   object.Kind,
			name:   object.Metadata.Name,
		})
	}
	if len(documents) < 5 {
		t.Fatalf("only %d documents parsed out of the render; the split failed and every "+
			"rule in this file would pass vacuously", len(documents))
	}

	return documents
}

// objectNamed finds exactly one rendered object by kind and name suffix.
func objectNamed(t *testing.T, out, kind, suffix string) document {
	t.Helper()

	var found []document
	for _, candidate := range documentsIn(t, out) {
		if candidate.kind == kind && strings.HasSuffix(candidate.name, suffix) {
			found = append(found, candidate)
		}
	}
	switch len(found) {
	case 0:
		t.Fatalf("no %s whose name ends in %q was rendered", kind, suffix)
	case 1:
		return found[0]
	default:
		t.Fatalf("%d %ss whose name ends in %q were rendered", len(found), kind, suffix)
	}

	return document{}
}

func hasObject(t *testing.T, out, kind, suffix string) bool {
	t.Helper()

	for _, candidate := range documentsIn(t, out) {
		if candidate.kind == kind && strings.HasSuffix(candidate.name, suffix) {
			return true
		}
	}

	return false
}

// ---------------------------------------------------------------------------
// Disabled defaults
// ---------------------------------------------------------------------------

// Req 59: with durable output capture disabled, the existing chart is what it
// was. Not "mostly" -- a rendered flag the old binary does not accept is a
// CrashLoopBackOff on the first sync, and a rendered workload is a bill.
func TestTheOutputPlaneRendersNothingByDefault(t *testing.T) {
	out := render(t)

	for _, component := range []string{
		outputDaemonComponent, outputInventoryComponent,
		outputReclaimerComponent, outputAttestorComponent,
	} {
		if strings.Contains(out, component) {
			t.Errorf("the default render mentions %q; the output plane is opt-in", component)
		}
	}
	for _, unexpected := range []string{
		"--output-bucket", "--receipt-key-file", "--control-key-file",
		"concourse.dev/hangar-output-v1", "concourse.dev/hangar-execution-control-v1",
		"hangar-output-scratch",
	} {
		if strings.Contains(out, unexpected) {
			t.Errorf("the default render contains %q", unexpected)
		}
	}
}

// The two facets are distinct switches, and the base one is a deployment on its
// own. A single switch would make "attested for exact control" and "has an
// output bucket" the same claim, which is the thing the two node labels exist
// to keep apart.
func TestBaseControlRendersWithoutTheOutputFacet(t *testing.T) {
	out := renderBaseControl(t)

	if !hasObject(t, out, "DaemonSet", "-"+outputDaemonComponent) {
		t.Fatal("the base execution-control facet rendered no output daemon; it is the " +
			"process that owns the execution ledger")
	}
	daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)

	if strings.Contains(daemon.body, "--output-bucket") {
		t.Error("a base-control-only daemon is configured with an output bucket")
	}
	if strings.Contains(daemon.body, "--receipt-key-file") {
		t.Error("a base-control-only daemon mounts the receipt private key; it signs no receipts")
	}
	if !strings.Contains(daemon.body, "--control-key-file") {
		t.Error("a base-control-only daemon has no control key; an unsigned acknowledgement " +
			"is not proof")
	}
	for _, controller := range []string{
		outputInventoryComponent, outputReclaimerComponent, outputAttestorComponent,
	} {
		if hasObject(t, out, "Deployment", "-"+controller) {
			t.Errorf("base control alone rendered %s; the controllers belong to the output "+
				"facet and have nothing to sweep", controller)
		}
	}
}

// Output can never be ready without base control. The chart refuses the
// configuration rather than rendering a daemon that would refuse itself at
// startup: a render is what an operator reviews.
func TestOutputEnablementRequiresBaseControl(t *testing.T) {
	message := renderHangarError(t,
		"artifactDaemon.enabled=true",
		"hangarOutput.enabled=true",
		"hangarOutput.bucket=jb-output",
	)
	if !strings.Contains(message, "hangarOutput.executionControl.enabled") {
		t.Errorf("the refusal does not name the base facet:\n%s", message)
	}
}

// ---------------------------------------------------------------------------
// The dedicated bucket and the server-derived namespace
// ---------------------------------------------------------------------------

// Req 20. The bucket is explicit, and it is never one of the other two.
func TestTheOutputBucketIsExplicitAndIsNeitherOtherBucket(t *testing.T) {
	if message := renderHangarError(t, append(append([]string{}, outputSets...),
		"hangarOutput.bucket=")...); !strings.Contains(message, "hangarOutput.bucket") {
		t.Errorf("an empty output bucket was accepted:\n%s", message)
	}

	for _, collision := range []string{
		"hangarOutput.bucket=jb-cache",
		"hangarOutput.bucket=jb-strict-input",
	} {
		message := renderOutputError(t, collision)
		if !strings.Contains(strings.ToLower(message), "dedicated") {
			t.Errorf("%s did not name the dedicated-bucket rule:\n%s", collision, message)
		}
	}

	// And the durable cache bucket the artifact daemon is pointed at, which an
	// operator sets in a different block entirely.
	message := renderHangarError(t, append(append([]string{}, outputSets...),
		"artifactDaemon.durable.store=gcs",
		"artifactDaemon.durable.bucket=jb-output",
	)...)
	if !strings.Contains(strings.ToLower(message), "dedicated") {
		t.Errorf("the output bucket was accepted as the artifact daemon's durable bucket:\n%s",
			message)
	}
}

// Prefix-only isolation inside one shared bucket is not an activation-compatible
// substitute, and Req 20 says so. The chart has no value that expresses it.
func TestPrefixOnlyIsolationInAMixedBucketIsRefused(t *testing.T) {
	message := renderOutputError(t, "hangarOutput.sharedBucketPrefixOnlyIsolation=true")
	if !strings.Contains(message, "prefix") {
		t.Errorf("prefix-only isolation was accepted:\n%s", message)
	}
}

// The prefix and the tenant are server configuration handed to every output
// workload, and they are the same values everywhere: a controller sweeping a
// namespace the daemon does not publish into would find every object orphaned.
func TestEveryOutputWorkloadIsGivenTheSameDerivedNamespace(t *testing.T) {
	out := renderOutput(t)

	workloads := []document{
		objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent),
		objectNamed(t, out, "Deployment", "-"+outputInventoryComponent),
		objectNamed(t, out, "Deployment", "-"+outputReclaimerComponent),
		objectNamed(t, out, "Deployment", "-"+outputAttestorComponent),
	}
	for _, workload := range workloads {
		for _, flag := range []string{
			"--output-bucket=jb-output",
			"--activation-epoch=7",
		} {
			if !strings.Contains(workload.body, flag) {
				t.Errorf("%s does not carry %s", workload.name, flag)
			}
		}
	}
	// The attestor reads the bucket's policy and has no namespace inside it,
	// so prefix and tenant are asserted over the three that do.
	for _, workload := range workloads[:3] {
		for _, flag := range []string{"--output-prefix=cluster-a", "--output-tenant=tenant-a"} {
			if !strings.Contains(workload.body, flag) {
				t.Errorf("%s does not carry %s", workload.name, flag)
			}
		}
	}
}

// Decision F3. The output plane's endpoint override is its own value and its
// own flag; the artifact daemon's --durable-endpoint serves the durable CACHE
// bucket, which Req 20 forbids the output plane sharing.
func TestTheOutputEndpointAndTheDurableCacheEndpointAreIndependent(t *testing.T) {
	outputOnly := renderOutput(t, "hangarOutput.endpoint=http://fake-gcs.cicd.svc:4443")
	if !strings.Contains(outputOnly, "--output-endpoint=http://fake-gcs.cicd.svc:4443") {
		t.Error("hangarOutput.endpoint did not reach --output-endpoint")
	}
	if strings.Contains(outputOnly, "--durable-endpoint=http://fake-gcs.cicd.svc:4443") {
		t.Error("hangarOutput.endpoint populated the durable CACHE tier's endpoint too; the " +
			"two are different buckets under different identities")
	}

	cacheOnly := renderOutput(t,
		"artifactDaemon.durable.store=gcs",
		"artifactDaemon.durable.bucket=jb-cache-2",
		"artifactDaemon.durable.endpoint=http://minio.local:9000",
	)
	if !strings.Contains(cacheOnly, "--durable-endpoint=http://minio.local:9000") {
		t.Error("artifactDaemon.durable.endpoint did not reach --durable-endpoint")
	}
	if strings.Contains(cacheOnly, "--output-endpoint=http://minio.local:9000") {
		t.Error("artifactDaemon.durable.endpoint populated the OUTPUT plane's endpoint")
	}
}

// ---------------------------------------------------------------------------
// Four principals, four service accounts
// ---------------------------------------------------------------------------

func TestTheFourOutputPrincipalsHaveFourDistinctServiceAccounts(t *testing.T) {
	out := renderOutput(t,
		"hangarOutput.daemon.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=publisher@p.iam.gserviceaccount.com",
		"hangarOutput.inventory.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=inventory@p.iam.gserviceaccount.com",
		"hangarOutput.reclaimer.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=reclaimer@p.iam.gserviceaccount.com",
		"hangarOutput.policyAttestor.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=attestor@p.iam.gserviceaccount.com",
	)

	names := map[string]bool{}
	for _, component := range []string{
		outputDaemonComponent, outputInventoryComponent,
		outputReclaimerComponent, outputAttestorComponent,
	} {
		account := objectNamed(t, out, "ServiceAccount", "-"+component)
		if names[account.name] {
			t.Errorf("%s reuses the ServiceAccount name %s. A service account is Pod-wide: "+
				"two workloads sharing one are one cloud identity holding both sets of "+
				"permissions.", component, account.name)
		}
		names[account.name] = true
	}
	if len(names) != 4 {
		t.Fatalf("expected four distinct output service accounts, got %d: %v", len(names), names)
	}

	// The Workload Identity annotations are four distinct cloud principals too.
	// The chart cannot verify the binding -- activation does -- but it can
	// refuse to render one principal into two roles.
	principals := map[string]string{}
	for _, component := range []string{
		outputDaemonComponent, outputInventoryComponent,
		outputReclaimerComponent, outputAttestorComponent,
	} {
		account := objectNamed(t, out, "ServiceAccount", "-"+component)
		var parsed struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(account.body), &parsed); err != nil {
			t.Fatalf("parsing %s: %v", account.name, err)
		}
		principal := parsed.Metadata.Annotations["iam.gke.io/gcp-service-account"]
		if principal == "" {
			t.Errorf("%s has no Workload Identity annotation", component)

			continue
		}
		if other, seen := principals[principal]; seen {
			t.Errorf("%s and %s are annotated with the same cloud principal %s",
				component, other, principal)
		}
		principals[principal] = component
	}
}

// Shared identities are an activation failure, and the chart is where an
// operator would try to express one.
func TestASharedCloudPrincipalIsRefused(t *testing.T) {
	message := renderOutputError(t,
		"hangarOutput.daemon.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=one@p.iam.gserviceaccount.com",
		"hangarOutput.reclaimer.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=one@p.iam.gserviceaccount.com",
	)
	if !strings.Contains(message, "principal") {
		t.Errorf("two roles sharing one cloud principal were accepted:\n%s", message)
	}
}

func TestASharedKubernetesServiceAccountIsRefused(t *testing.T) {
	message := renderOutputError(t,
		"hangarOutput.inventory.serviceAccount.name=shared",
		"hangarOutput.reclaimer.serviceAccount.name=shared",
	)
	if !strings.Contains(message, "service account") {
		t.Errorf("two roles sharing one Kubernetes service account were accepted:\n%s", message)
	}
}

// ---------------------------------------------------------------------------
// Seven service accounts, not four
// ---------------------------------------------------------------------------
//
// Phase 8 review R2-F1. The validation above used to cover the four output
// ROLES, and the chart renders seven service accounts: those four, the
// activation Job's, the artifact daemon's, and the top-level (web) one. Each of
// the three it did not cover was reachable by a values override, and each of
// the three was ACCEPTED at cc1d77ad5e -- measured, not inferred:
//
//	helm template ... --set hangarOutput.activation.serviceAccount.name=<reclaimer>
//	  -> rendered TWO ServiceAccount objects named jb-...-hangar-output-reclaimer
//	helm template ... --set <activation annotation>=<the reclaimer's principal>
//	  -> rendered the delete-holding cloud identity on two Kubernetes accounts
//	helm template ... --set serviceAccount.name=<reclaimer>
//	  -> rendered the WEB Deployment with
//	     serviceAccountName: jb-...-hangar-output-reclaimer
//
// The third is the one that matters most and reads the most innocuous: Req 54
// says web/control-plane, task, cache and strict-input identities have no role
// on the output bucket, and that override gives web the delete grant. The first
// is the quieter outage -- two objects, one name, and whichever the apply leaves
// standing decides whether the reclaimer still carries its Workload Identity
// annotation. A reclaimer without it deletes nothing, so the plane keeps every
// published object forever while its status says it reclaims.
//
// No shipped configuration was ever in any of these states.
//
// These are three separate tests rather than a table because each one names a
// different consequence, and a table would report "a refusal happened".

// The positive control for all three: the ordinary render, with the activation
// Job present, still succeeds. Asserted FIRST, because a refusal assertion
// passes on a chart that refuses everything.
func TestTheOrdinaryRenderWithAnActivationJobIsAccepted(t *testing.T) {
	out := renderOutput(t,
		"hangarOutput.activation.job.mode=attest",
		"hangarOutput.activation.job.facet=output",
	)

	for _, suffix := range []string{
		"-" + outputDaemonComponent, "-" + outputInventoryComponent,
		"-" + outputReclaimerComponent, "-" + outputAttestorComponent,
		"-hangar-output-activation", "-artifact-daemon", "-web",
	} {
		if !hasObject(t, out, "ServiceAccount", suffix) {
			t.Errorf("the ordinary render has no ServiceAccount ending %q; this chart renders "+
				"seven and the validation below covers whatever it renders", suffix)
		}
	}
}

// The activation Job's account is a distinct identity (decision F4). Naming it
// after the reclaimer's renders two objects with one name.
func TestTheActivationAccountMayNotBeNamedAfterTheReclaimers(t *testing.T) {
	message := renderOutputError(t,
		"hangarOutput.activation.job.mode=attest",
		"hangarOutput.activation.job.facet=output",
		"hangarOutput.activation.serviceAccount.name=jb-concourse-jetbridge-hangar-output-reclaimer",
	)
	if !strings.Contains(message, "service account") {
		t.Errorf("the activation Job was allowed to render a second ServiceAccount object with "+
			"the reclaimer's name. Which object the apply leaves standing decides whether the "+
			"reclaimer keeps its Workload Identity annotation, and a reclaimer without one "+
			"keeps every published object forever:\n%s", message)
	}
}

// The same union-of-grants defect the four-role check refuses, one account over.
func TestTheActivationAccountMayNotCarryTheReclaimersPrincipal(t *testing.T) {
	message := renderOutputError(t,
		"hangarOutput.activation.job.mode=attest",
		"hangarOutput.activation.job.facet=output",
		"hangarOutput.reclaimer.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=reclaimer@p.iam.gserviceaccount.com",
		"hangarOutput.activation.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=reclaimer@p.iam.gserviceaccount.com",
	)
	if !strings.Contains(message, "principal") {
		t.Errorf("the activation identity was allowed to carry the reclaimer's cloud principal. "+
			"The activation command signs nothing and touches no object; giving it delete is "+
			"the union-of-grants failure Req 54 calls an activation failure:\n%s", message)
	}
}

// Req 54, the sharpest form: web has no role on the output bucket, and this is
// the one override that gives it every one of them.
func TestTheWebIdentityMayNotBeNamedAfterAnOutputRole(t *testing.T) {
	message := renderOutputError(t,
		"serviceAccount.name=jb-concourse-jetbridge-hangar-output-reclaimer",
	)
	if !strings.Contains(message, "service account") {
		t.Errorf("the top-level service account was allowed to take the reclaimer's name, so the "+
			"WEB Deployment runs as the delete-holding identity. Req 54: web/control-plane, "+
			"task, cache and strict-input identities have no role on the output bucket:\n%s",
			message)
	}
}

// And the name check is not create-gated: an externally provisioned account
// named after an output role is the same Pod running as the same identity, and
// the chart declining to render the object does not make that untrue.
func TestAPreProvisionedWebAccountNamedAfterAnOutputRoleIsAlsoRefused(t *testing.T) {
	message := renderOutputError(t,
		"serviceAccount.create=false",
		"serviceAccount.name=jb-concourse-jetbridge-hangar-output-daemon",
	)
	if !strings.Contains(message, "service account") {
		t.Errorf("serviceAccount.create=false let the web pod run as the publisher's identity:\n%s",
			message)
	}
}

// Phase 8 review R2-F3. `concourse.labels` appends component: web, so the three
// controller NetworkPolicies described themselves as governing the web pod. The
// label selects nothing -- no controller selects a NetworkPolicy -- so this is a
// label an operator reads, and the render is the thing an operator reads.
func TestEveryOutputNetworkPolicyNamesItsOwnComponent(t *testing.T) {
	out := renderOutput(t, "hangarOutput.networkPolicy.enabled=true")

	found := 0
	for _, subject := range documentsIn(t, out) {
		if subject.kind != "NetworkPolicy" || !strings.Contains(subject.name, "hangar-output") {
			continue
		}
		found++

		var parsed struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(subject.body), &parsed); err != nil {
			t.Fatalf("parsing %s: %v", subject.name, err)
		}

		component := parsed.Metadata.Labels["app.kubernetes.io/component"]
		if component == "web" {
			t.Errorf("NetworkPolicy %s is labelled component=web. It governs an output "+
				"workload; an operator filtering component=web to see what constrains the "+
				"web pod is shown a policy that constrains something else.", subject.name)
		}
		if !strings.HasSuffix(subject.name, component) {
			t.Errorf("NetworkPolicy %s is labelled component=%q, which is not the workload "+
				"it selects", subject.name, component)
		}
	}

	if found != 4 {
		t.Fatalf("the output plane renders %d NetworkPolicies, not four; this guard is looking "+
			"at the wrong render", found)
	}
}

// The existing identities gain nothing. This is the whole reason there is a
// second daemon binary at all.
func TestNoExistingIdentityGainsAnOutputRole(t *testing.T) {
	out := renderOutput(t)

	// The read-grant key legitimately reaches the control plane -- web MINTS
	// grants -- so the rule is per secret and not "anything with the word
	// output in it". What must not leave the daemon is the RECEIPT PRIVATE key
	// and the bucket itself.
	allowed := map[string][]string{
		"op-receipt-private":    {outputDaemonComponent},
		"op-output-materialize": {outputDaemonComponent, "-web"},
	}
	for secret, carriers := range allowed {
		found := 0
		for _, subject := range documentsIn(t, out) {
			if !strings.Contains(subject.body, secret) {
				continue
			}
			found++
			permitted := false
			for _, carrier := range carriers {
				if strings.Contains(subject.name, carrier) {
					permitted = true
				}
			}
			if !permitted {
				t.Errorf("%s %s references %q, and only %v may. Web, task, cache and "+
					"strict-input identities have no role on the output bucket.",
					subject.kind, subject.name, secret, carriers)
			}
		}
		if found == 0 {
			t.Errorf("nothing references %q; this rule would pass vacuously", secret)
		}
	}

	for _, subject := range documentsIn(t, out) {
		if strings.Contains(subject.name, "hangar-output") {
			continue
		}
		if strings.Contains(subject.body, "--output-bucket") {
			t.Errorf("%s %s is configured with the output bucket", subject.kind, subject.name)
		}
	}

	// And the artifact daemon's own KSA is untouched: it still exists, and it
	// is not one of the four.
	daemonAccount := objectNamed(t, out, "ServiceAccount", "-artifact-daemon")
	if strings.Contains(daemonAccount.name, "hangar-output") {
		t.Errorf("the artifact daemon's service account is an output one: %s", daemonAccount.name)
	}
}

// ---------------------------------------------------------------------------
// Key material
// ---------------------------------------------------------------------------

// Req 24. The receipt private key is mounted in exactly one Pod.
func TestTheReceiptPrivateKeyIsMountedOnlyInTheOutputDaemon(t *testing.T) {
	out := renderOutput(t)

	carriers := []string{}
	for _, subject := range documentsIn(t, out) {
		if !strings.Contains(subject.body, "op-receipt-private") {
			continue
		}
		carriers = append(carriers, subject.kind+"/"+subject.name)
	}
	if len(carriers) == 0 {
		t.Fatal("nothing references the receipt private key Secret; this rule would pass " +
			"vacuously")
	}
	for _, carrier := range carriers {
		if !strings.Contains(carrier, outputDaemonComponent) {
			t.Errorf("%s references the receipt private key. The control plane, the web node, "+
				"the existing artifact daemon, the controllers, the control init container, "+
				"the task and the sidecar hold the public key and the key id only.", carrier)
		}
	}
}

// The control plane gets the versioned public ring and nothing that can sign.
func TestTheControlPlaneGetsOnlyTheVersionedPublicRing(t *testing.T) {
	out := renderOutput(t)

	ring := objectNamed(t, out, "ConfigMap", "-hangar-output-receipt-keys")
	if !strings.Contains(ring.body, "receipt-7") {
		t.Error("the receipt public key ring does not carry the active key id")
	}
	if strings.Contains(ring.body, "PRIVATE KEY") {
		t.Error("the receipt public key ring contains private key material")
	}

	web := objectNamed(t, out, "Deployment", "-web")
	if !strings.Contains(web.body, "hangar-output-receipt-keys") {
		t.Error("the web pod does not mount the receipt public key ring; it verifies every " +
			"receipt before registration")
	}
	if strings.Contains(web.body, "op-receipt-private") {
		t.Error("the web pod mounts the receipt private key")
	}
}

// Rotation creates a new epoch. A key id whose ring entry names a different
// epoch is an in-place replacement, which the receipt-key rule forbids: an old
// private key is retained while its epoch still has an unsettled capture.
func TestAReceiptKeyIsNeverReplacedInPlace(t *testing.T) {
	message := renderHangarError(t, append(append([]string{}, outputSets...),
		"hangarOutput.receipt.publicKeys[0].epoch=6",
	)...)
	if !strings.Contains(message, "epoch") {
		t.Errorf("a key id bound to a different epoch than the active one was accepted:\n%s",
			message)
	}

	// The same id appearing twice with two different keys is the same defect
	// spelled the other way.
	message = renderOutputError(t,
		"hangarOutput.receipt.publicKeys[1].id=receipt-7",
		"hangarOutput.receipt.publicKeys[1].epoch=8",
		"hangarOutput.receipt.publicKeys[1].key=YW5vdGhlci1wdWJsaWMta2V5",
	)
	if !strings.Contains(message, "receipt-7") {
		t.Errorf("one key id with two different public keys was accepted:\n%s", message)
	}
}

// An unknown or retired active key is refused: a receipt names the key that can
// check it, and a verifier with no entry for it cannot.
func TestTheActiveReceiptKeyMustBeInTheRingAndNotRetired(t *testing.T) {
	message := renderOutputError(t, "hangarOutput.receipt.keyID=receipt-9")
	if !strings.Contains(message, "receipt-9") {
		t.Errorf("an active key id absent from the ring was accepted:\n%s", message)
	}

	message = renderOutputError(t, "hangarOutput.receipt.publicKeys[0].retired=true")
	if !strings.Contains(message, "retired") {
		t.Errorf("a retired key was accepted as the active one:\n%s", message)
	}
}

// Public verification material is retained while any durable state references
// its epoch. The chart cannot read the database, so the operator declares the
// referenced epochs and the chart refuses to drop one.
func TestAPublicKeyIsNotRemovedWhileAnEpochStillReferencesIt(t *testing.T) {
	message := renderOutputError(t, "hangarOutput.receipt.referencedEpochs[0]=5")
	if !strings.Contains(message, "5") {
		t.Errorf("a referenced epoch with no verification key was accepted:\n%s", message)
	}

	// With the entry present it renders, and the retired key stays in the ring.
	out := renderOutput(t,
		"hangarOutput.receipt.referencedEpochs[0]=5",
		"hangarOutput.receipt.publicKeys[1].id=receipt-5",
		"hangarOutput.receipt.publicKeys[1].epoch=5",
		"hangarOutput.receipt.publicKeys[1].retired=true",
		"hangarOutput.receipt.publicKeys[1].key=b2xkLXB1YmxpYy1rZXk=",
	)
	ring := objectNamed(t, out, "ConfigMap", "-hangar-output-receipt-keys")
	if !strings.Contains(ring.body, "receipt-5") {
		t.Error("the retired key was dropped from the ring while its epoch is still referenced")
	}
}

// The three key roles say different things and are pinned separately. One
// Secret serving two of them means rotating either rotates both.
func TestTheKeyRolesAreDistinctSecrets(t *testing.T) {
	for _, collapse := range [][]string{
		{"hangarOutput.executionControl.keySecret=op-receipt-private"},
		{"hangarOutput.materializationKeySecret=op-control-key"},
		{"hangarOutput.capabilityKeySecret=op-output-materialize"},
	} {
		message := renderOutputError(t, collapse...)
		if !strings.Contains(message, "same") {
			t.Errorf("%v collapsed two key roles into one Secret and was accepted:\n%s",
				collapse, message)
		}
	}
}

// ---------------------------------------------------------------------------
// Deadlines, grace and leases
// ---------------------------------------------------------------------------

// Req 39. Grace must exceed the configured maximum capture deadline by at least
// an hour, and must not exceed 30 days. The frozen defaults are 24h and 8 days.
func TestTheDeadlineGraceAndLeaseRelationshipsAreEnforced(t *testing.T) {
	for _, invalid := range []struct {
		sets   []string
		expect string
	}{
		{[]string{"hangarOutput.publicationGrace=24h"}, "publicationGrace"},
		{[]string{"hangarOutput.publicationGrace=744h"}, "publicationGrace"},
		{[]string{"hangarOutput.captureDeadline=30m"}, "captureDeadline"},
		{[]string{"hangarOutput.captureDeadline=200h"}, "captureDeadline"},
		{[]string{"hangarOutput.sealDeadline=10s"}, "sealDeadline"},
		{[]string{"hangarOutput.sealDeadline=45m"}, "sealDeadline"},
		{[]string{"hangarOutput.leaseTerm=5m"}, "leaseTerm"},
		{[]string{"hangarOutput.leaseRenewInterval=5m"}, "leaseRenewInterval"},
	} {
		message := renderOutputError(t, invalid.sets...)
		if !strings.Contains(message, invalid.expect) {
			t.Errorf("%v was accepted, or refused without naming %s:\n%s",
				invalid.sets, invalid.expect, message)
		}
	}

	// grace == maxCaptureDeadline + 1h is admissible; "greater than" was the
	// plan's own outlier wording and Req 39 says "by at least 1 hour".
	renderOutput(t, "hangarOutput.captureDeadline=24h", "hangarOutput.publicationGrace=25h")
}

// ---------------------------------------------------------------------------
// Scratch, concurrency and the node-eviction bound
// ---------------------------------------------------------------------------

// Branch-review follow-up F9. The output daemon canonicalizes and spools whole
// trees to an emptyDir; an emptyDir with no sizeLimit is bounded by the node's
// disk, and filling it evicts every pod on the node, not only this one.
func TestTheOutputScratchVolumeIsBounded(t *testing.T) {
	message := renderHangarError(t, append(append([]string{}, baseControlSets...),
		"hangarOutput.enabled=true",
		"hangarOutput.bucket=jb-output",
		"hangarOutput.tenant=tenant-a",
		"hangarOutput.activationEpoch=7",
		"hangarOutput.receipt.keyID=receipt-7",
		"hangarOutput.receipt.privateKeySecret=op-receipt-private",
		"hangarOutput.receipt.publicKeys[0].id=receipt-7",
		"hangarOutput.receipt.publicKeys[0].epoch=7",
		"hangarOutput.receipt.publicKeys[0].key=cHVibGljLWtleS1ieXRlcw==",
		"hangarOutput.materializationKeySecret=op-output-materialize",
		"hangarOutput.database.existingSecret=op-activation-db",
		"hangarOutput.daemon.scratch.sizeLimit=",
	)...)
	if !strings.Contains(message, "sizeLimit") {
		t.Errorf("an unbounded output scratch emptyDir was accepted:\n%s", message)
	}

	out := renderOutput(t)
	daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
	if !strings.Contains(daemon.body, "sizeLimit: 32Gi") {
		t.Errorf("the output scratch volume has no sizeLimit:\n%s", daemon.body)
	}

	// The bound only holds if concurrency times the content limit stays under
	// it. Two concurrent 10 GiB trees do not fit in 16 GiB.
	message = renderOutputError(t,
		"hangarOutput.daemon.scratch.sizeLimit=16Gi",
		"hangarOutput.daemon.scratch.concurrency=2",
		"hangarOutput.daemon.scratch.maxContentBytes=10737418240",
	)
	if !strings.Contains(message, "concurrency") {
		t.Errorf("a concurrency whose product exceeds the sizeLimit was accepted:\n%s", message)
	}

	if !strings.Contains(out, "--publish-concurrency=") {
		t.Error("the concurrency bound is a chart value with no flag behind it; the render " +
			"would be a promise the process does not keep")
	}
}

// And in BASE-CONTROL-ONLY mode, where the volume also renders.
//
// The DaemonSet, its hangar-output-scratch emptyDir and its --scratch-dir flag
// are all under the BASE switch, and the validation was under the OUTPUT one.
// So a base-control-only deployment rendered
//
//   - name: hangar-output-scratch
//     emptyDir:
//     # REQUIRED, and validated against the content limit ...
//     sizeLimit:
//
// -- an explicit null, which is to say unbounded, under a comment asserting a
// bound that is not there. Nothing in that mode writes the volume today
// (PrepareScratch and the canonicalizer are inside the output-facet branch of
// cmd/hangar-output-daemon), so the live exposure was nil; what was wrong was
// the claim, and a render that documents a limit it does not set is the kind of
// thing an operator reads once.
func TestTheOutputScratchVolumeIsBoundedInBaseControlOnlyModeToo(t *testing.T) {
	message := renderHangarError(t, append(append([]string{}, baseControlSets...),
		"hangarOutput.daemon.scratch.sizeLimit=",
	)...)
	if !strings.Contains(message, "sizeLimit") {
		t.Errorf("an unbounded scratch emptyDir was accepted in base-control-only mode:\n%s",
			message)
	}

	out := renderBaseControl(t)
	daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
	if !strings.Contains(daemon.body, "sizeLimit: 32Gi") {
		t.Errorf("the scratch volume renders no sizeLimit in base-control-only mode:\n%s",
			daemon.body)
	}
	if strings.Contains(daemon.body, "sizeLimit:\n") {
		t.Errorf("the scratch volume renders an explicit null sizeLimit, which is "+
			"unbounded:\n%s", daemon.body)
	}
}

// The same exposure was already carried by the existing artifact daemon's
// hangar-scratch emptyDir, and leaving one daemon guarded and one not is not a
// decision anybody made.
func TestTheArtifactDaemonScratchVolumeIsBoundedToo(t *testing.T) {
	out := render(t, append(append([]string{}, daemonHangarSets...),
		"artifactDaemon.hangar.scratchSizeLimit=24Gi")...)

	daemon := objectNamed(t, out, "DaemonSet", "-artifact-daemon")
	if !strings.Contains(daemon.body, "sizeLimit: 24Gi") {
		t.Errorf("the artifact daemon's hangar-scratch volume has no sizeLimit:\n%s", daemon.body)
	}
}

// ---------------------------------------------------------------------------
// Controllers
// ---------------------------------------------------------------------------

// Req 57. Output enablement without every controller is refused: an activation
// epoch attests recovery, inventory and reclaim workers, and a plane with no
// reclaimer keeps every published object forever while claiming it does not.
func TestOutputEnablementRequiresEveryController(t *testing.T) {
	for _, missing := range []string{
		"hangarOutput.inventory.enabled=false",
		"hangarOutput.reclaimer.enabled=false",
		"hangarOutput.policyAttestor.enabled=false",
	} {
		message := renderOutputError(t, missing)
		if !strings.Contains(message, "controller") {
			t.Errorf("%s was accepted:\n%s", missing, message)
		}
	}
}

func TestEachControllerRendersItsOwnDeploymentImageAndTimeouts(t *testing.T) {
	out := renderOutput(t,
		"hangarOutput.inventory.interval=90s",
		"hangarOutput.reclaimer.deleteTimeout=3m",
		"hangarOutput.policyAttestor.interval=4m",
	)

	for _, expected := range []struct {
		component string
		command   string
		flag      string
	}{
		{outputInventoryComponent, "/usr/local/concourse/bin/hangar-output-inventory", "--interval=90s"},
		{outputReclaimerComponent, "/usr/local/concourse/bin/hangar-output-reclaimer", "--delete-timeout=3m"},
		{outputAttestorComponent, "/usr/local/concourse/bin/hangar-output-policy-attestor", "--interval=4m"},
	} {
		deployment := objectNamed(t, out, "Deployment", "-"+expected.component)
		if !strings.Contains(deployment.body, expected.command) {
			t.Errorf("%s does not run %s", expected.component, expected.command)
		}
		if !strings.Contains(deployment.body, expected.flag) {
			t.Errorf("%s does not carry %s", expected.component, expected.flag)
		}
	}
}

// Req 42: exactly one renewable lease owner per bucket and epoch. Two inventory
// replicas are two cursor owners racing for one lease, which the plan forbids
// by construction rather than by luck.
func TestTheSingletonControllersRenderExactlyOneReplica(t *testing.T) {
	out := renderOutput(t)

	for _, component := range []string{
		outputInventoryComponent, outputReclaimerComponent, outputAttestorComponent,
	} {
		deployment := objectNamed(t, out, "Deployment", "-"+component)
		var parsed struct {
			Spec struct {
				Replicas *int32 `json:"replicas"`
				Strategy struct {
					Type string `json:"type"`
				} `json:"strategy"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(deployment.body), &parsed); err != nil {
			t.Fatalf("parsing %s: %v", component, err)
		}
		if parsed.Spec.Replicas == nil || *parsed.Spec.Replicas != 1 {
			t.Errorf("%s renders %v replicas; there is one cursor and one lease owner per "+
				"bucket and epoch", component, parsed.Spec.Replicas)
		}
		if parsed.Spec.Strategy.Type != "Recreate" {
			t.Errorf("%s uses the %q strategy; a rolling update runs two owners at once",
				component, parsed.Spec.Strategy.Type)
		}
	}

	message := renderOutputError(t, "hangarOutput.inventory.replicas=2")
	if !strings.Contains(message, "replica") {
		t.Errorf("a second inventory replica was accepted:\n%s", message)
	}
}

// ---------------------------------------------------------------------------
// Probes, policies and disruption
// ---------------------------------------------------------------------------

func TestEveryOutputWorkloadHasProbesAndANetworkPolicy(t *testing.T) {
	out := renderOutput(t, "hangarOutput.networkPolicy.enabled=true")

	daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
	for _, probe := range []string{"livenessProbe", "readinessProbe"} {
		if !strings.Contains(daemon.body, probe) {
			t.Errorf("the output daemon has no %s", probe)
		}
	}

	for _, component := range []string{
		outputDaemonComponent, outputInventoryComponent,
		outputReclaimerComponent, outputAttestorComponent,
	} {
		if !hasObject(t, out, "NetworkPolicy", "-"+component) {
			t.Errorf("%s has no NetworkPolicy", component)
		}
	}

	if !hasObject(t, out, "PodDisruptionBudget", "-"+outputDaemonComponent) {
		t.Error("the output daemon has no PodDisruptionBudget")
	}
}

// The daemon owns node-local state; the controllers own none, and a controller
// that mounted the managed hostPath would be a second writer to a directory one
// daemon is the authority for.
func TestOnlyTheOutputDaemonMountsTheNodeLocalPaths(t *testing.T) {
	out := renderOutput(t)

	daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
	if !strings.Contains(daemon.body, "hostPath") {
		t.Fatal("the output daemon mounts no hostPath; it owns the source ledger and the " +
			"step incarnations")
	}
	for _, component := range []string{
		outputInventoryComponent, outputReclaimerComponent, outputAttestorComponent,
	} {
		deployment := objectNamed(t, out, "Deployment", "-"+component)
		if strings.Contains(deployment.body, "hostPath") {
			t.Errorf("%s mounts a hostPath; the node-local ledger has one writer", component)
		}
	}
}

// ---------------------------------------------------------------------------
// Both node labels
// ---------------------------------------------------------------------------

// Req 56/58. The two ready labels are scheduling HINTS, and they are two
// because a base-only cohort is a real deployment.
//
// What the CHART decides is not the label STRINGS -- those are protocol
// constants in hangar/executioncontrol and hangar/output, and rendering them
// here would be a second spelling of a value the daemon already holds. What it
// decides is whether the daemon can advertise at all, and which facets it has
// to advertise. So that is what this asserts, and
// cmd/hangar-output-daemon/labels_test.go owns the order the two go on and come
// off in.
func TestTheDaemonCanAdvertiseAndAdvertisesOnlyTheFacetsItHas(t *testing.T) {
	for name, out := range map[string]string{
		"base control only": renderBaseControl(t),
		"base plus output":  renderOutput(t),
	} {
		daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)

		if !strings.Contains(daemon.body, "--node-name=$(NODE_NAME)") {
			t.Errorf("%s: the daemon is given no node to label, so it advertises nothing and "+
				"the scheduler never places a controlled pod on it", name)
		}
		if !strings.Contains(daemon.body, "fieldPath: spec.nodeName") {
			t.Errorf("%s: NODE_NAME does not come from the Downward API", name)
		}

		// Parsed, not grepped: the rules are what grant permission, and this
		// file is full of prose that names the resources it is saying the role
		// must NOT have.
		role := objectNamed(t, out, "ClusterRole", "-"+outputDaemonComponent)
		var parsed struct {
			Rules []struct {
				Resources []string `json:"resources"`
				Verbs     []string `json:"verbs"`
			} `json:"rules"`
		}
		if err := yaml.Unmarshal([]byte(role.body), &parsed); err != nil {
			t.Fatalf("%s: parsing the daemon ClusterRole: %v", name, err)
		}
		if len(parsed.Rules) != 1 {
			t.Errorf("%s: the daemon's ClusterRole has %d rules; its only API business is its "+
				"own node's labels", name, len(parsed.Rules))

			continue
		}
		if got := strings.Join(parsed.Rules[0].Resources, ","); got != "nodes" {
			t.Errorf("%s: the daemon's ClusterRole covers %q and not just nodes", name, got)
		}
		verbs := strings.Join(parsed.Rules[0].Verbs, ",")
		if verbs != "get,patch" {
			t.Errorf("%s: the daemon's ClusterRole grants %q; get and patch are what a label "+
				"needs, and a node-local daemon with list or watch over every node is a "+
				"cluster-wide reach it has no use for", name, verbs)
		}

		if strings.Contains(daemon.body, "concourse.dev/hangar-v1") {
			t.Errorf("%s: the output daemon claims the strict-input capability, which attests "+
				"inputs and belongs to the existing artifact daemon", name)
		}
	}

	// The output facet is what the output label attests, and a base-only daemon
	// has none of it: no bucket, no receipt key, no publisher. It therefore
	// cannot advertise the output label however the code is written, which is a
	// stronger statement than a render asserting the string is absent.
	base := objectNamed(t, renderBaseControl(t), "DaemonSet", "-"+outputDaemonComponent)
	for _, absent := range []string{"--output-bucket", "--receipt-key-file", "--materialization-key-file"} {
		if strings.Contains(base.body, absent) {
			t.Errorf("a base-control-only daemon carries %s", absent)
		}
	}
	full := objectNamed(t, renderOutput(t), "DaemonSet", "-"+outputDaemonComponent)
	for _, present := range []string{"--output-bucket", "--receipt-key-file", "--materialization-key-file"} {
		if !strings.Contains(full.body, present) {
			t.Errorf("the output daemon does not carry %s", present)
		}
	}

	// The operator has to be able to find both label keys without reading Go.
	values := readChartFile(t, "values.yaml")
	for _, label := range []string{
		"concourse.dev/hangar-execution-control-v1", "concourse.dev/hangar-output-v1",
	} {
		if !strings.Contains(values, label) {
			t.Errorf("deploy/chart/values.yaml does not name %s", label)
		}
	}
}

// ---------------------------------------------------------------------------
// Honest documentation
// ---------------------------------------------------------------------------

// Req 41 and 54. The chart's own documentation must state the two GCS facts
// that make the permission matrix weaker than it reads: object get authorizes
// the body as well as the metadata, and list authority covers the bucket rather
// than a prefix. A permission matrix that omits them is a matrix an operator
// will believe.
func TestTheChartDocumentsTheHonestGCSPermissions(t *testing.T) {
	body := readChartFile(t, "values.yaml")

	for _, phrase := range []string{
		"storage.objects.get",
		"storage.objects.list",
	} {
		if !strings.Contains(body, phrase) {
			t.Errorf("deploy/chart/values.yaml does not name %s in the output plane's "+
				"permission documentation", phrase)
		}
	}
	if !strings.Contains(strings.ToLower(body), "body") {
		t.Error("the documentation does not say that object get is body-capable")
	}
	if !strings.Contains(strings.ToLower(body), "bucket-wide") {
		t.Error("the documentation does not say that list authority is bucket-wide")
	}
	if !strings.Contains(strings.ToLower(body), "does not create") &&
		!strings.Contains(strings.ToLower(body), "never creates") {
		t.Error("the documentation does not say the chart creates no bucket, lifecycle rule " +
			"or IAM binding")
	}
}

// readChartFile reads a file from deploy/chart.
func readChartFile(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "chart", name))
	if err != nil {
		t.Fatalf("reading deploy/chart/%s: %v", name, err)
	}

	return string(body)
}

// ---------------------------------------------------------------------------
// The documented IAM matrix, checked against the roles it describes
// ---------------------------------------------------------------------------

// roleOperations maps each output role's own Store/Handle interface method to
// the GCS permission a principal needs to make that call.
//
// This is the translation an operator makes from the chart's documentation to a
// Terraform file, and it is the one place it can go wrong quietly: a role that
// gains a method gains a permission, and a permission matrix written in prose
// stays right until the first time it does not. The chart's documentation is
// what an operator GRANTS; the interface is what the process can actually do.
// If those two drift, the grant is either too small (a broken plane) or too
// large (a principal that can do something nobody wrote down).
var roleOperations = map[string]map[string]string{
	"publisher": {
		"NewWriter": "storage.objects.create",
		"NewReader": "storage.objects.get",
		"Attrs":     "storage.objects.get",
		// Pure refinements: they narrow a handle and issue no request.
		"If":         "",
		"Generation": "",
		"Object":     "",
	},
	"inventory": {
		"List":       "storage.objects.list",
		"Attrs":      "storage.objects.get",
		"Generation": "",
		"Object":     "",
	},
	"reclaimer": {
		"Delete":     "storage.objects.delete",
		"Attrs":      "storage.objects.get",
		"If":         "",
		"Generation": "",
		"Object":     "",
	},
}

// documentedRolePermissions is what deploy/chart/values.yaml tells an operator
// to grant, per workload.
var documentedRolePermissions = map[string][]string{
	"publisher": {"storage.objects.create", "storage.objects.get"},
	"inventory": {"storage.objects.list", "storage.objects.get"},
	"reclaimer": {"storage.objects.get", "storage.objects.delete"},
}

func TestTheDocumentedIAMMatrixMatchesWhatEachRoleCanActuallyDo(t *testing.T) {
	values := readChartFile(t, "values.yaml")
	root := repoRoot(t)

	for role, permissions := range documentedRolePermissions {
		// Every documented permission appears in values.yaml, verbatim. A
		// matrix an operator cannot copy is a matrix they will approximate.
		for _, permission := range permissions {
			if !strings.Contains(values, permission) {
				t.Errorf("deploy/chart/values.yaml does not name %s, which the %s role needs",
					permission, role)
			}
		}

		// And the reverse: every method the role's own interfaces declare maps
		// to a permission the documentation grants. A method with no mapping is
		// a capability nobody wrote down.
		methods := declaredRoleMethods(t, filepath.Join(root, "hangar", "output", role, role+".go"))
		if len(methods) < 3 {
			t.Fatalf("parsed only %d interface methods out of the %s role; the declaration "+
				"moved and this rule would pass vacuously", len(methods), role)
		}

		granted := map[string]bool{}
		for _, permission := range permissions {
			granted[permission] = true
		}
		for method := range methods {
			permission, known := roleOperations[role][method]
			if !known {
				t.Errorf("the %s role declares %s and roleOperations has no entry for it.\n\n"+
					"Every method on a role's Store or Handle is a request some principal has "+
					"to be permitted to make. A method with no entry is a capability the "+
					"documented IAM matrix does not account for -- and the matrix is what an "+
					"operator copies into Terraform.", role, method)

				continue
			}
			if permission == "" {
				continue
			}
			if !granted[permission] {
				t.Errorf("the %s role declares %s, which needs %s, and the documented matrix "+
					"grants only %v. The role silently broadened.",
					role, method, permission, permissions)
			}
		}
	}
}

// declaredRoleMethods reads the method names off every interface a role package
// declares.
func declaredRoleMethods(t *testing.T, path string) map[string]bool {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	methods := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		iface, ok := node.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, field := range iface.Methods.List {
			for _, name := range field.Names {
				methods[name.Name] = true
			}
		}

		return true
	})

	return methods
}

// The attestor holds NO object permission at all, which is why its compromise
// costs the assessment rather than the data. Its own interface is the statement.
func TestThePolicyAttestorHoldsNoObjectPermission(t *testing.T) {
	// The attestor's capability is a CONCRETE type rather than a role
	// interface: hangar/gcs.BucketPolicySource is what the process holds, and
	// what it can do is the set of methods on it. An interface would state what
	// the role may do; this states what the process actually has, which is the
	// generalisation TestEachOutputPrincipalHoldsOnlyItsOwnOperations made when
	// the conformance guard was re-pointed at production.
	methods := declaredMethodsOnType(t,
		filepath.Join(repoRoot(t), "hangar", "gcs", "lifetime.go"), "BucketPolicySource")
	if len(methods) < 2 {
		t.Fatalf("parsed %d methods off BucketPolicySource; the declaration moved and this "+
			"rule would pass vacuously", len(methods))
	}

	for method := range methods {
		for _, objectMethod := range []string{
			"Object", "NewReader", "NewWriter", "Delete", "List", "Attrs",
			"ObjectToDelete",
		} {
			if method == objectMethod {
				t.Errorf("the policy attestor's role declares %s. It reads bucket lifecycle "+
					"and IAM and holds no object permission at all: that is the whole reason "+
					"it is a fourth identity, and it is what makes its compromise cost the "+
					"assessment rather than the data.", method)
			}
		}
	}

	values := readChartFile(t, "values.yaml")
	for _, permission := range []string{"storage.buckets.get", "storage.buckets.getIamPolicy"} {
		if !strings.Contains(values, permission) {
			t.Errorf("deploy/chart/values.yaml does not name %s for the policy attestor",
				permission)
		}
	}
}

// declaredMethodsOnType reads the exported methods declared on one concrete
// type in one file.
func declaredMethodsOnType(t *testing.T, path, receiver string) map[string]bool {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	methods := map[string]bool{}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || len(function.Recv.List) == 0 {
			continue
		}
		name := function.Recv.List[0].Type
		if star, isStar := name.(*ast.StarExpr); isStar {
			name = star.X
		}
		ident, isIdent := name.(*ast.Ident)
		if !isIdent || ident.Name != receiver {
			continue
		}
		if !function.Name.IsExported() {
			continue
		}
		methods[function.Name.Name] = true
	}

	return methods
}

// ---------------------------------------------------------------------------
// The IAM half, made load-bearing
// ---------------------------------------------------------------------------
//
// Source guards cannot bound a credential holder: a Go program holding
// application default credentials can delete an object with a raw HTTPS request,
// a vendor CLI, or any SDK nobody thought to name, and no rule over imports
// changes that. IAM is the control -- so the claim "only the reclaimer may
// reach delete" is only as true as the grant, and the grant has to be asserted
// somewhere rather than described.
//
// This is that assertion, over the two artefacts an operator actually acts on:
// the permission matrix generated into deploy/chart/values.yaml, which is what
// they copy into Terraform, and the rendered Workload Identity annotations,
// which are what bind a Pod to the cloud principal that holds it. Neither proves
// a binding exists -- the chart creates no IAM and says so, and activation is
// what attests the real policy (Req 54). What they prove is that the
// documentation grants storage.objects.delete to exactly one principal and that
// exactly one workload runs as it.

// documentedPermissionMatrix reads the matrix out of values.yaml rather than
// restating it, because a matrix restated in a test is a second copy that drifts
// with the first.
//
// The block is
//
//	#   <workload>
//	#     storage.x, storage.y
//
// under "THE HONEST PERMISSION MATRIX", and it ends at the first bulleted
// caveat.
func documentedPermissionMatrix(t *testing.T) map[string][]string {
	t.Helper()

	const marker = "THE HONEST PERMISSION MATRIX"

	lines := strings.Split(readChartFile(t, "values.yaml"), "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, marker) {
			start = i

			break
		}
	}
	if start < 0 {
		t.Fatalf("deploy/chart/values.yaml no longer contains %q; the matrix moved and this "+
			"rule would pass over nothing", marker)
	}

	matrix := map[string][]string{}
	workload := ""
	for _, line := range lines[start+1:] {
		if !strings.HasPrefix(line, "#") {
			break
		}
		body := strings.TrimPrefix(line, "#")
		trimmed := strings.TrimSpace(body)
		if trimmed == "" {
			continue
		}
		indent := len(body) - len(strings.TrimLeft(body, " "))
		switch {
		case indent == 3 && strings.HasPrefix(trimmed, "*"):
			// The caveats below the matrix. Everything after is prose.
			return matrix
		case indent == 3:
			workload = trimmed
		case indent == 5 && strings.HasPrefix(trimmed, "storage."):
			if workload == "" {
				t.Fatalf("permission line %q has no workload above it", trimmed)
			}
			for _, permission := range strings.Split(trimmed, ",") {
				permission = strings.TrimSpace(permission)
				if permission == "" {
					continue
				}
				if !strings.HasPrefix(permission, "storage.") {
					t.Errorf("%q under %q is not a GCS permission", permission, workload)

					continue
				}
				matrix[workload] = append(matrix[workload], permission)
			}
		}
	}

	return matrix
}

func TestOnlyTheReclaimerPrincipalIsGrantedObjectDelete(t *testing.T) {
	matrix := documentedPermissionMatrix(t)
	if len(matrix) != 4 {
		t.Fatalf("parsed %d workloads out of the documented permission matrix, not four: %v. "+
			"The block's shape changed and every assertion below would pass over the wrong "+
			"text.", len(matrix), matrix)
	}

	var granted []string
	total := 0
	for workload, permissions := range matrix {
		total += len(permissions)
		for _, permission := range permissions {
			if permission == "storage.objects.delete" {
				granted = append(granted, workload)
			}
		}
	}
	if total < 8 {
		t.Fatalf("the documented matrix grants %d permissions across four workloads; it "+
			"collapsed", total)
	}
	sort.Strings(granted)

	if len(granted) != 1 {
		t.Fatalf("deploy/chart/values.yaml grants storage.objects.delete to %d workloads (%v).\n\n"+
			"IAM is the control here -- source guards cannot bound a process that holds "+
			"credentials -- so exactly one principal may hold delete on the output bucket, and "+
			"the whole isolation is that the reclaimer is a separate Pod because of it.",
			len(granted), granted)
	}
	if !strings.Contains(granted[0], "reclaimer") {
		t.Fatalf("the documented matrix grants storage.objects.delete to %q, not to the "+
			"reclaimer", granted[0])
	}

	// And the rendered side: the annotation that binds a Pod to that principal
	// is on one ServiceAccount, and one workload runs as it. Rendered with the
	// activation Job on, because it is the fifth identity in this namespace and
	// the easiest one to point at the wrong account by copy-paste.
	out := renderOutput(t,
		"hangarOutput.daemon.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=publisher@p.iam.gserviceaccount.com",
		"hangarOutput.inventory.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=inventory@p.iam.gserviceaccount.com",
		"hangarOutput.reclaimer.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=reclaimer@p.iam.gserviceaccount.com",
		"hangarOutput.policyAttestor.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=attestor@p.iam.gserviceaccount.com",
		"hangarOutput.activation.job.mode=enable",
		"hangarOutput.activation.job.facet=output",
	)

	const deletePrincipal = "reclaimer@p.iam.gserviceaccount.com"

	reclaimerAccount := objectNamed(t, out, "ServiceAccount", "-"+outputReclaimerComponent).name

	accountsWithDelete := map[string]string{}
	annotated := 0
	for _, document := range documentsIn(t, out) {
		if document.kind != "ServiceAccount" {
			continue
		}
		var account corev1.ServiceAccount
		if err := yaml.UnmarshalStrict([]byte(document.body), &account); err != nil {
			t.Fatalf("%s: %v", document.source, err)
		}
		principal := account.Annotations["iam.gke.io/gcp-service-account"]
		if principal == "" {
			continue
		}
		annotated++
		if principal == deletePrincipal {
			accountsWithDelete[account.Name] = document.source
		}
	}
	if annotated < 4 {
		t.Fatalf("only %d rendered ServiceAccounts carry a Workload Identity annotation; the "+
			"render changed shape and this rule is looking at nothing", annotated)
	}
	if len(accountsWithDelete) != 1 {
		t.Fatalf("%d ServiceAccounts are annotated with the delete-holding principal %s: %v.\n\n"+
			"A Kubernetes service account is Pod-wide. Two accounts bound to one cloud identity "+
			"is two Pods holding storage.objects.delete, whatever their code does.",
			len(accountsWithDelete), deletePrincipal, accountsWithDelete)
	}
	if _, ok := accountsWithDelete[reclaimerAccount]; !ok {
		t.Fatalf("the delete-holding principal %s is bound to %v, not to the reclaimer's "+
			"ServiceAccount %s", deletePrincipal, accountsWithDelete, reclaimerAccount)
	}

	// Finally: which workloads run as that account. Every Pod template in the
	// render, not just the output ones -- web, the artifact daemon, postgres and
	// the activation Job are all in this namespace.
	var runAs []string
	templates := 0
	for _, document := range documentsIn(t, out) {
		var spec corev1.PodSpec
		switch document.kind {
		case "Deployment":
			var object appsv1.Deployment
			if err := yaml.UnmarshalStrict([]byte(document.body), &object); err != nil {
				t.Fatalf("%s: %v", document.source, err)
			}
			spec = object.Spec.Template.Spec
		case "DaemonSet":
			var object appsv1.DaemonSet
			if err := yaml.UnmarshalStrict([]byte(document.body), &object); err != nil {
				t.Fatalf("%s: %v", document.source, err)
			}
			spec = object.Spec.Template.Spec
		case "Job":
			var object batchv1.Job
			if err := yaml.UnmarshalStrict([]byte(document.body), &object); err != nil {
				t.Fatalf("%s: %v", document.source, err)
			}
			spec = object.Spec.Template.Spec
		default:
			continue
		}
		templates++
		if spec.ServiceAccountName == reclaimerAccount {
			runAs = append(runAs, document.source)
		}
	}
	if templates < 7 {
		t.Fatalf("only %d Pod templates were decoded out of the render; the walk failed and "+
			"this rule would pass vacuously", templates)
	}
	if len(runAs) != 1 {
		t.Fatalf("%d Pod templates run as %s, the only account bound to a principal holding "+
			"storage.objects.delete: %v", len(runAs), reclaimerAccount, runAs)
	}
	if !strings.Contains(runAs[0], "reclaimer") {
		t.Fatalf("the account holding storage.objects.delete is used by %s, which is not the "+
			"reclaimer", runAs[0])
	}
}

// ---------------------------------------------------------------------------
// The control key's identity
// ---------------------------------------------------------------------------

// A key id names KEY MATERIAL, and the chart used to give the control key the
// name of its Secret.
//
// `--control-key-id={{ .executionControl.keySecret }}` means two nodes holding
// different private keys under one Secret name report one id. Base attestation
// collects `control_key_id` into the evidence bundle and the digest, so a
// cohort half-way through a control-key rollout attests as homogeneous on the
// base facet's ONLY key material -- and the in-place replacement that the
// receipt key's ring rules refuse was unguarded for the control key precisely
// because the id could not move.
//
// `receipt.keyID` has been a value of its own since Phase 8, with the ring rule
// that a key id is never reused for different material and rotation creates a
// new epoch. The control key now gets the same shape.
func TestTheControlKeyIdNamesKeyMaterialAndNotItsSecret(t *testing.T) {
	daemon := objectNamed(t, renderBaseControl(t), "DaemonSet", "-"+outputDaemonComponent)

	id := ""
	for _, line := range strings.Split(daemon.body, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "- --control-key-id=") {
			id = strings.TrimPrefix(trimmed, "- --control-key-id=")
		}
	}
	if id == "" {
		t.Fatal("the output daemon renders no --control-key-id; this rule would pass vacuously")
	}
	if id == "op-control-key" {
		t.Error("--control-key-id carries hangarOutput.executionControl.keySecret, the SECRET " +
			"NAME. Two nodes holding different key material under one Secret name then report " +
			"one id, and base attestation compares ids: a cohort half-way through a " +
			"control-key rollout attests as homogeneous.")
	}

	// The id moves independently of the Secret. Same Secret name, different
	// key: a different id, which is the whole property.
	rotated := objectNamed(t,
		renderBaseControl(t, "hangarOutput.executionControl.keyID=control-key-8"),
		"DaemonSet", "-"+outputDaemonComponent)
	if !strings.Contains(rotated.body, "- --control-key-id=control-key-8") {
		t.Error("hangarOutput.executionControl.keyID does not reach --control-key-id, so the " +
			"id cannot be moved without renaming the Secret")
	}
	if !strings.Contains(rotated.body, "secretName: op-control-key") {
		t.Error("the rotated render no longer mounts the same Secret; the two are supposed to " +
			"be independent")
	}
}

// An id shared across key ROLES is the same ambiguity one level up: a control
// statement and a receipt say different things, and "which key checks this" has
// to have one answer per id.
func TestAKeyIdIsNotSharedBetweenTheControlReceiptAndReadGrantKeys(t *testing.T) {
	for _, collision := range []string{
		"hangarOutput.executionControl.keyID=receipt-7",
		"hangarOutput.materializationKeyID=receipt-7",
	} {
		message := renderOutputError(t, collision)
		if !strings.Contains(message, "key id") {
			t.Errorf("%s was refused, but not by the key-id rule:\n%s", collision, message)
		}
	}
}

// The base facet cannot render without one. A daemon started with no id
// refuses at startup (cmd/hangar-output-daemon/config.go), and the refusal an
// operator most needs is the one at render time.
func TestTheControlKeyIdIsRequiredWithTheBaseFacet(t *testing.T) {
	message := renderHangarError(t, append(append([]string{}, baseControlSets...),
		"hangarOutput.executionControl.keyID=")...)
	if !strings.Contains(message, "executionControl.keyID") {
		t.Errorf("an empty control key id rendered, or was refused by something else:\n%s",
			message)
	}
}

// forbiddenPermissionMatrix reads the second half of values.yaml's matrix: the
// permissions no runtime principal holds, each with its reason.
func forbiddenPermissionMatrix(t *testing.T) map[string]string {
	t.Helper()

	const marker = "PERMISSIONS NO RUNTIME PRINCIPAL HOLDS"

	lines := strings.Split(readChartFile(t, "values.yaml"), "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, marker) {
			start = i

			break
		}
	}
	if start < 0 {
		t.Fatalf("deploy/chart/values.yaml no longer contains %q; the block moved and this "+
			"rule would pass over nothing", marker)
	}

	forbidden := map[string]string{}
	permission := ""
	for _, line := range lines[start+1:] {
		if !strings.HasPrefix(line, "#") {
			break
		}
		body := strings.TrimPrefix(line, "#")
		trimmed := strings.TrimSpace(body)
		if trimmed == "" {
			continue
		}
		indent := len(body) - len(strings.TrimLeft(body, " "))
		switch {
		case indent == 3 && strings.HasPrefix(trimmed, "storage."):
			for _, name := range strings.Split(trimmed, ",") {
				if name = strings.TrimSpace(name); name != "" {
					forbidden[name] = ""
					permission = name
				}
			}
		case indent == 3:
			// Prose resumed; the block is over.
			return forbidden
		case indent == 5 && permission != "":
			forbidden[permission] += " " + trimmed
		}
	}

	return forbidden
}

// Req 22: the publisher cannot update the marker.
//
// That sentence is what the ownership-evidence story rests on -- a marker that
// can be rewritten is not evidence of who created an object -- and it was
// enforced by nothing but the Go `Handle` type having no update method.
// `storage.objects.update` was absent from this matrix, so nothing said the
// absence was deliberate, and it is absent from `permissionsOf`'s role
// expansion too, so even `roles/storage.objectAdmin` -- which really does grant
// it -- is invisible to the attestation on that axis. This is the matrix half;
// the expansion half is in hangar/gcs and hangar/output/policy.
func TestTheMatrixForbidsRewritingTheOwnershipMarker(t *testing.T) {
	const update = "storage.objects.update"

	granted := documentedPermissionMatrix(t)
	if len(granted) != 4 {
		t.Fatalf("parsed %d workloads out of the granted matrix, not four", len(granted))
	}
	for workload, permissions := range granted {
		for _, permission := range permissions {
			if permission == update {
				t.Errorf("the documented matrix grants %s to %s. It rewrites object "+
					"metadata, which is the ownership marker: Req 22's \"the publisher "+
					"cannot update the marker\" is the sentence the whole immutable-marker "+
					"argument rests on.", update, workload)
			}
		}
	}

	forbidden := forbiddenPermissionMatrix(t)
	if len(forbidden) < 3 {
		t.Fatalf("parsed %d forbidden permissions out of values.yaml (%v); the block's shape "+
			"changed and this rule would pass vacuously", len(forbidden), forbidden)
	}
	reason, named := forbidden[update]
	if !named {
		t.Errorf("values.yaml does not name %s among the permissions no runtime principal "+
			"holds (it names %v).\n\n"+
			"An absent grant is a promise, and this one is load-bearing: "+
			"roles/storage.objectAdmin and roles/storage.objectUser both GRANT it, so an "+
			"operator binding either one satisfies every other line of this matrix and "+
			"breaks Req 22.", update, sortedForbidden(forbidden))

		return
	}
	if !strings.Contains(reason, "marker") {
		t.Errorf("%s is named but its reason does not mention the marker: %q", update, reason)
	}
}

func sortedForbidden(forbidden map[string]string) []string {
	var names []string
	for name := range forbidden {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// ---------------------------------------------------------------------------
// Two refusals the plane was missing
// ---------------------------------------------------------------------------

// The output plane cannot schedule a single pod without the artifact daemon.
//
// `BuildAffinity` seeds EVERY pod's required node affinity with
// `concourse.dev/artifact-cache=ready` before it appends the two output labels,
// and the only thing in the tree that sets that label is the artifact daemon.
// With the daemon disabled no node ever carries it, so every capture pod -- and
// every ordinary pod -- sits Pending until its deadline expires, and the failure
// names a timeout rather than a label. The render succeeded.
func TestTheOutputPlaneRefusesToRenderWithoutTheArtifactDaemon(t *testing.T) {
	message := renderHangarError(t, append(append([]string{}, baseControlSets...),
		"artifactDaemon.enabled=false")...)
	if !strings.Contains(message, "artifactDaemon.enabled") {
		t.Errorf("the output plane rendered with the artifact daemon disabled, or was "+
			"refused by something else:\n%s", message)
	}
	if !strings.Contains(message, "concourse.dev/artifact-cache") {
		t.Errorf("the refusal does not name the label that is the reason:\n%s", message)
	}
}

// Every other duration in this plane is checked at render time. The attestor's
// interval was not, and it is the one whose bound has a consequence written
// into the schema: policy evidence older than fifteen minutes is stale, and a
// stale snapshot puts the plane at-risk, which blocks five kinds of admission.
func TestTheControllerIntervalsAreValidated(t *testing.T) {
	for _, bad := range []struct {
		set, names string
	}{
		{"hangarOutput.policyAttestor.interval=20m", "policyAttestor.interval"},
		{"hangarOutput.policyAttestor.interval=0s", "policyAttestor.interval"},
		{"hangarOutput.policyAttestor.interval=every-so-often", "policyAttestor.interval"},
		{"hangarOutput.inventory.interval=nope", "inventory.interval"},
		{"hangarOutput.reclaimer.interval=nope", "reclaimer.interval"},
	} {
		message := renderOutputError(t, bad.set)
		if !strings.Contains(message, bad.names) {
			t.Errorf("%s rendered, or was refused by something else:\n%s", bad.set, message)
		}
	}

	// And the default is accepted, so the rule is not simply "refuse".
	renderOutput(t, "hangarOutput.policyAttestor.interval=15m")
}

// ---------------------------------------------------------------------------
// The low set
// ---------------------------------------------------------------------------

// The database credential does not reach argv.
//
// `--database=$(HANGAR_OUTPUT_DSN)` is expanded by the KUBELET, so the
// connection string -- user, password and all -- ends up in
// /proc/<pid>/cmdline, which is world-readable inside the container, while
// /proc/<pid>/environ is readable only by the process's own uid. The commands
// read the variable themselves instead.
func TestTheDatabaseCredentialNeverReachesArgv(t *testing.T) {
	out := renderOutput(t,
		"hangarOutput.activation.job.mode=attest",
		"hangarOutput.activation.job.facet=base")

	carriers := 0
	for _, subject := range documentsIn(t, out) {
		if !strings.Contains(subject.body, "HANGAR_OUTPUT_DSN") {
			continue
		}
		carriers++
		if strings.Contains(subject.body, "--database=$(HANGAR_OUTPUT_DSN)") {
			t.Errorf("%s %s expands the DSN into its argv. The kubelet substitutes $(VAR) "+
				"in args, so the credential lands in /proc/<pid>/cmdline, which is readable "+
				"by anything in the container; the environment is not.",
				subject.kind, subject.name)
		}
	}
	if carriers < 4 {
		t.Fatalf("only %d workloads take a DSN from the environment; this rule is looking at "+
			"the wrong render", carriers)
	}
}

// readOnlyRootFilesystem with nowhere to write is a runtime error waiting for
// the first operation that wants a temp file -- on a Pod that passed every
// render check. The GCS client library spools resumable uploads.
func TestTheControllersHaveSomewhereToWrite(t *testing.T) {
	out := renderOutput(t)

	for _, component := range []string{
		outputInventoryComponent, outputReclaimerComponent, outputAttestorComponent,
	} {
		controller := objectNamed(t, out, "Deployment", "-"+component)

		var object appsv1.Deployment
		if err := yaml.UnmarshalStrict([]byte(controller.body), &object); err != nil {
			t.Fatalf("%s: %v", controller.source, err)
		}
		spec := object.Spec.Template.Spec
		if len(spec.Containers) == 0 || spec.Containers[0].SecurityContext == nil {
			t.Fatalf("%s has no container security context; this rule is looking at nothing",
				component)
		}
		readOnly := spec.Containers[0].SecurityContext.ReadOnlyRootFilesystem
		if readOnly == nil || !*readOnly {
			continue // Nothing to guarantee.
		}

		writable := false
		for _, mount := range spec.Containers[0].VolumeMounts {
			if mount.MountPath == "/tmp" {
				writable = true
			}
		}
		if !writable {
			t.Errorf("%s runs with readOnlyRootFilesystem and mounts nothing at /tmp. The "+
				"GCS client spools resumable uploads to a temp file, and the failure would "+
				"be a runtime error on a Pod that passed every render check.", component)
		}
	}
}
