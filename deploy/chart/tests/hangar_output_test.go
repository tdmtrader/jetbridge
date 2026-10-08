package tests

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// The output plane's rendered surface.
//
// A Kubernetes service account is Pod-wide, so the plane's two cloud
// identities are two Pods with two KSAs: the artifact daemon publishes (create
// and get), and the web reclaims and sweeps (list, get and delete). The
// chart's job is to render that separation and to refuse the configurations
// that quietly collapse it.
//
// The chart does NOT create buckets, lifecycle rules or GCP IAM. It renders
// explicit identities and names what an operator must provision. A render test
// can say a KSA exists and that no other workload mounts a private key. It
// cannot say an IAM binding is what the annotation claims, and nothing here
// pretends otherwise.

const (
	outputDaemonComponent = "artifact-daemon"
	webComponent          = "web"
)

// webReclaimFlags are the flags the web's reclaim pass and orphan sweep are
// configured by. They render with the capture facet and with nothing less.
var webReclaimFlags = []string{
	"--kubernetes-hangar-output-store=",
	"--kubernetes-hangar-output-prefix=",
	"--kubernetes-hangar-output-publication-grace=",
	"--kubernetes-hangar-output-reclaim-interval=",
	"--kubernetes-hangar-output-reclaim-batch=",
	"--kubernetes-hangar-output-delete-timeout=",
	"--kubernetes-hangar-output-orphan-sweep-interval=",
}

// webDiskFlags render only on the disk store.
var webDiskFlags = []string{
	"--kubernetes-hangar-output-endpoint=",
	"--kubernetes-hangar-output-store-id=",
	"--kubernetes-hangar-output-store-ca-cert=",
	"--kubernetes-hangar-output-list-token-file=",
	"--kubernetes-hangar-output-delete-token-file=",
}

// baseControlSets turn on the BASE exact-execution-control facet and nothing
// else. It is a real deployment on its own: the sibling `exact_execution_control`
// track schedules onto exactly these nodes.
var baseControlSets = []string{
	"hangarOutput.executionControl.enabled=true",
	"hangarOutput.capabilityKeySecret=op-capability-key",
	// Required under the BASE switch, not the output one: the DaemonSet, its
	// scratch emptyDir and its --scratch-dir flag all render here.
	"artifactDaemon.outputScratch.sizeLimit=32Gi",
}

// outputSets add the OUTPUT capture facet on top of the base one.
var outputSets = append(append([]string{}, baseControlSets...),
	"hangarOutput.enabled=true",
	"hangarOutput.bucket=jb-output",
	"hangarOutput.prefix=cluster-a",
	"hangarOutput.tenant=tenant-a",
	"hangarOutput.cacheBucket=jb-cache",
	"hangarOutput.strictInputBucket=jb-strict-input",
	"hangarOutput.materializationKeySecret=op-output-materialize",
	// The two Workload Identity annotations: the publisher's on the artifact
	// daemon, and the reclaim principal's on the web. Not required (see
	// hangar_output_principals_test.go), but declared, so the rules over
	// distinct principals have something to compare.
	`artifactDaemon.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=publisher@p.iam.gserviceaccount.com`,
	`serviceAccount.annotations.iam\.gke\.io/gcp-service-account=web@p.iam.gserviceaccount.com`,
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

	// The artifact daemon always renders; it is the output plane's flags, and
	// the web's reclaim and orphan sweep, that are opt-in.
	for _, unexpected := range append([]string{
		"--output-bucket", "--materialization-key-file", "--capability-key",
		"concourse.dev/hangar-output-v1", "concourse.dev/hangar-execution-control-v1",
		"hangar-output-scratch", "hangar-output-list", "hangar-output-delete",
	}, webReclaimFlags...) {
		if strings.Contains(out, unexpected) {
			t.Errorf("the default render contains %q", unexpected)
		}
	}
}

// The two facets are distinct switches, and the base one is a deployment on its
// own. A single switch would make "serves exact control" and "has an
// output bucket" the same claim, which is the thing the two node labels exist
// to keep apart.
func TestBaseControlRendersWithoutTheOutputFacet(t *testing.T) {
	out := renderBaseControl(t)

	if !hasObject(t, out, "DaemonSet", "-"+outputDaemonComponent) {
		t.Fatal("the base execution-control facet rendered no artifact daemon output plane; it is the " +
			"process that owns the execution ledger")
	}
	daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)

	if strings.Contains(daemon.body, "--output-bucket") {
		t.Error("a base-control-only daemon is configured with an output bucket")
	}
	if strings.Contains(daemon.body, "--materialization-key-file") {
		t.Error("a base-control-only daemon mounts the read-warrant key; it serves no reads")
	}
	if !strings.Contains(daemon.body, "--capability-key=") {
		t.Error("a base-control-only daemon is given no capability key; it is what mounts " +
			"the output plane, and the base facet cannot verify a capability without it")
	}
	web := objectNamed(t, out, "Deployment", "-"+webComponent)
	for _, flag := range webReclaimFlags {
		if strings.Contains(web.body, flag) {
			t.Errorf("base control alone gave web %s; reclaim and the orphan sweep belong to "+
				"the output facet and have nothing to sweep", flag)
		}
	}
}

// The base facet gives the web node the capability key and nothing of the
// capture facet's. There is no epoch: the output plane has no control-key
// generation, and every capability is minted under the one key.
func TestTheWebNodeIsGivenOnlyTheBaseFacetUnderTheBaseFacet(t *testing.T) {
	web := objectNamed(t, renderBaseControl(t), "Deployment", "-web")

	if !strings.Contains(web.body, "--kubernetes-hangar-output-warrant-key=") {
		t.Error("a base-control-only web node is given no capability key, so it can mint " +
			"no control capability")
	}
	if strings.Contains(web.body, "--kubernetes-hangar-output-activation-epoch") {
		t.Error("the web node is given a Hangar activation epoch; there is no control-key generation")
	}
	for _, captureOnly := range append([]string{
		"--kubernetes-hangar-output-materialization-key=",
		"--kubernetes-hangar-output-bucket=",
	}, webReclaimFlags...) {
		if strings.Contains(web.body, captureOnly) {
			t.Errorf("a base-control-only web node is given %s, which belongs to capture",
				captureOnly)
		}
	}
}

// Output can never be ready without base control. The chart refuses the
// configuration rather than rendering a daemon that would refuse itself at
// startup: a render is what an operator reviews.
func TestOutputEnablementRequiresBaseControl(t *testing.T) {
	message := renderHangarError(t,
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
}

// The prefix, the tenant and the bucket are server configuration handed to
// both halves of the plane, and they are the same values in both: an orphan
// sweep listing a namespace the daemon does not publish into would find every
// object orphaned, and one listing the wrong prefix would find nothing.
func TestEveryOutputWorkloadIsGivenTheSameDerivedNamespace(t *testing.T) {
	out := renderOutput(t)

	daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
	web := objectNamed(t, out, "Deployment", "-"+webComponent)
	for _, pair := range []struct{ daemon, web string }{
		{"--output-bucket=jb-output", "--kubernetes-hangar-output-bucket=jb-output"},
		{"--output-prefix=cluster-a", "--kubernetes-hangar-output-prefix=cluster-a"},
		{"--output-tenant=tenant-a", "--kubernetes-hangar-output-tenant=tenant-a"},
		{"--output-store=gcs", "--kubernetes-hangar-output-store=gcs"},
	} {
		if !strings.Contains(daemon.body, pair.daemon) {
			t.Errorf("the artifact daemon does not carry %s", pair.daemon)
		}
		if !strings.Contains(web.body, pair.web) {
			t.Errorf("the web does not carry %s", pair.web)
		}
	}
}

// Decision F3. The output plane's endpoint override is its own value: it
// reaches the daemon's --output-endpoint and the web's
// --kubernetes-hangar-output-endpoint, the two clients of the output store, and
// no other endpoint flag.
func TestTheOutputEndpointReachesOnlyItsOwnFlag(t *testing.T) {
	const endpoint = "http://fake-gcs.cicd.svc:4443"
	out := renderOutput(t, "hangarOutput.endpoint="+endpoint)
	for _, flag := range []string{"--output-endpoint=", "--kubernetes-hangar-output-endpoint="} {
		if !strings.Contains(out, flag+endpoint) {
			t.Errorf("hangarOutput.endpoint did not reach %s", flag)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "-endpoint="+endpoint) && !strings.Contains(line, "--output-endpoint=") &&
			!strings.Contains(line, "--kubernetes-hangar-output-endpoint=") {
			t.Errorf("hangarOutput.endpoint reached another endpoint flag: %s", strings.TrimSpace(line))
		}
	}
}

// ---------------------------------------------------------------------------
// Four principals, four service accounts
// ---------------------------------------------------------------------------

func TestTheOutputPrincipalsHaveDistinctServiceAccounts(t *testing.T) {
	out := renderOutput(t)

	// The Workload Identity annotations are two distinct cloud principals too.
	// The chart cannot verify the binding, but it can refuse to render one
	// principal into two roles.
	names := map[string]string{}
	principals := map[string]string{}
	for _, component := range []string{outputDaemonComponent, webComponent} {
		account := objectNamed(t, out, "ServiceAccount", "-"+component)
		if other, seen := names[account.name]; seen {
			t.Errorf("%s and %s share the ServiceAccount %s. A service account is Pod-wide: "+
				"two workloads sharing one are one cloud identity holding both sets of "+
				"permissions.", component, other, account.name)
		}
		names[account.name] = component

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
	if len(names) != 2 {
		t.Fatalf("expected two distinct output service accounts, got %d: %v", len(names), names)
	}
}

// Shared identities are an activation failure, and the chart is where an
// operator would try to express one.
func TestASharedCloudPrincipalIsRefused(t *testing.T) {
	message := renderOutputError(t,
		"artifactDaemon.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=one@p.iam.gserviceaccount.com",
		"serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=one@p.iam.gserviceaccount.com",
	)
	if !strings.Contains(message, "principal") {
		t.Errorf("two roles sharing one cloud principal were accepted:\n%s", message)
	}
}

func TestASharedKubernetesServiceAccountIsRefused(t *testing.T) {
	message := renderOutputError(t,
		"serviceAccount.name=shared",
		"kubernetes.serviceAccount=shared",
	)
	if !strings.Contains(message, "service account") {
		t.Errorf("two roles sharing one Kubernetes service account were accepted:\n%s", message)
	}
}

// ---------------------------------------------------------------------------
// Every service account the chart renders
// ---------------------------------------------------------------------------
//
// Phase 8 review R2-F1. The validation once covered only the output ROLES'
// accounts, and every other account the chart renders was reachable by a values
// override that collapsed it onto an output role. The plane now has two
// principals, the artifact daemon's and the web's, and the validation covers
// both, the task pods' account, and anything else that names one.

// The positive control: the ordinary render succeeds. Asserted FIRST, because a
// refusal assertion passes on a chart that refuses everything.
func TestTheOrdinaryRenderIsAccepted(t *testing.T) {
	out := renderOutput(t)

	for _, suffix := range []string{"-" + outputDaemonComponent, "-" + webComponent} {
		if !hasObject(t, out, "ServiceAccount", suffix) {
			t.Errorf("the ordinary render has no ServiceAccount ending %q", suffix)
		}
	}
}

// Req 54, the sharpest form: the web holds delete and the publisher holds
// create, and one account for both is a principal that can overwrite.
func TestTheWebIdentityMayNotBeNamedAfterAnOutputRole(t *testing.T) {
	message := renderOutputError(t,
		"serviceAccount.name=jb-concourse-jetbridge-artifact-daemon",
	)
	if !strings.Contains(message, "service account") {
		t.Errorf("the top-level service account was allowed to take the publisher's name, so "+
			"one identity holds create AND delete on the output bucket:\n%s", message)
	}
}

// And the name check is not create-gated: an externally provisioned account
// named after an output role is the same Pod running as the same identity, and
// the chart declining to render the object does not make that untrue.
func TestAPreProvisionedWebAccountNamedAfterAnOutputRoleIsAlsoRefused(t *testing.T) {
	message := renderOutputError(t,
		"serviceAccount.create=false",
		"serviceAccount.name=jb-concourse-jetbridge-artifact-daemon",
	)
	if !strings.Contains(message, "service account") {
		t.Errorf("serviceAccount.create=false let the web pod run as the publisher's identity:\n%s",
			message)
	}
}

// Phase 8 review R2-F3. `concourse.labels` appends component: web, so a policy
// built from it describes itself as governing the web pod. The output plane's
// own policy is the artifact daemon's, and it names its own component.
func TestEveryOutputNetworkPolicyNamesItsOwnComponent(t *testing.T) {
	out := renderOutput(t, "hangarOutput.networkPolicy.enabled=true")

	policy := objectNamed(t, out, "NetworkPolicy", "-"+outputDaemonComponent)
	var parsed struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(policy.body), &parsed); err != nil {
		t.Fatalf("parsing %s: %v", policy.name, err)
	}
	if component := parsed.Metadata.Labels["app.kubernetes.io/component"]; component != outputDaemonComponent {
		t.Errorf("NetworkPolicy %s is labelled component=%q, which is not the workload it "+
			"selects", policy.name, component)
	}
}

// On the disk store, the store's policy admits exactly the two output
// principals' pods: the artifact daemon, which publishes, and the web, which
// lists and deletes. The inventory and reclaimer pods it used to admit are
// gone, and a policy still naming only them would cut the web's reclaim off
// from the store with no error anywhere.
func TestTheDiskStoreAdmitsTheDaemonAndTheWeb(t *testing.T) {
	out := renderOutput(t, append(append([]string{}, diskSets...),
		"hangarOutput.networkPolicy.enabled=true")...)

	var policy networkingv1.NetworkPolicy
	decodeNamed(t, out, "NetworkPolicy", objectNamed(t, out, "NetworkPolicy", "-hangar-store").name, &policy)
	var admitted []string
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.PodSelector == nil {
				continue
			}
			for _, expression := range peer.PodSelector.MatchExpressions {
				if expression.Key == "app.kubernetes.io/component" {
					admitted = append(admitted, expression.Values...)
				}
			}
		}
	}
	sort.Strings(admitted)
	if !slices.Equal(admitted, []string{outputDaemonComponent, webComponent}) {
		t.Errorf("the disk store's NetworkPolicy admits components %v, want exactly %v",
			admitted, []string{outputDaemonComponent, webComponent})
	}
}

// Only the output principals gain an output role. The artifact daemon serves
// the output plane, so it is the publisher, and web mints read warrants; no
// other identity gains anything.
func TestOnlyTheOutputPrincipalsGainAnOutputRole(t *testing.T) {
	out := renderOutput(t)

	// Both keys legitimately reach the control plane -- web MINTS capabilities
	// and read warrants -- so the rule is per secret and not "anything with
	// the word output in it". What must not leave the two principals is either
	// key and the bucket itself.
	allowed := map[string][]string{
		"op-capability-key":     {outputDaemonComponent, "-web"},
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
		if strings.Contains(subject.name, "hangar-output") ||
			strings.HasSuffix(subject.name, "-"+outputDaemonComponent) {
			continue
		}
		if strings.Contains(subject.body, "--output-bucket") {
			t.Errorf("%s %s is configured with the output bucket", subject.kind, subject.name)
		}
	}
}

// ---------------------------------------------------------------------------
// Key material
// ---------------------------------------------------------------------------

// Nothing in the plane signs and nothing verifies. The daemon is inside the
// trusted computing base and is reached over mTLS, so its acknowledgement is
// its answer on that channel: the daemon is handed no control key and no key
// id, the web is handed no verification ring and no epoch, and no object in
// the render carries either.
func TestNoWorkloadSignsAndNoWorkloadVerifies(t *testing.T) {
	out := renderOutput(t)

	for _, doc := range documentsIn(t, out) {
		if strings.HasSuffix(doc.name, "-hangar-output-control-keys") || strings.Contains(doc.name, "-hangar-rings-") {
			t.Errorf("%s %s renders a verification ring", doc.kind, doc.name)
		}
		// The Run contract's own epoch stays; everything else named like it goes.
		body := strings.ReplaceAll(doc.body, "--pipeline-run-activation-epoch", "")
		for _, forbidden := range []string{"control-key", "control-keys", "activation-epoch", "control.key", "receipt"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s %s carries %q; nothing is signed and nothing is keyed by generation", doc.kind, doc.name, forbidden)
			}
		}
	}

	var web appsv1.Deployment
	decodeNamed(t, out, "Deployment", objectNamed(t, out, "Deployment", "-web").name, &web)
	for _, container := range web.Spec.Template.Spec.Containers {
		for _, arg := range append(append([]string{}, container.Command...), container.Args...) {
			if strings.HasPrefix(arg, "--pipeline-run-activation-epoch") {
				continue // the Run contract's own epoch, which stays
			}
			for _, forbidden := range []string{"control-key", "activation-epoch", "receipt"} {
				if strings.Contains(arg, forbidden) {
					t.Errorf("web container %s is passed %s; nothing is signed and nothing is keyed by generation", container.Name, arg)
				}
			}
		}
	}
}

// Publication receipts were removed in T3: their values are refused with a
// message that says so, rather than ignored. (The receipt key's lifetime lived
// under hangarOutput.activation, which went with the activation walk, and is
// refused as part of it: TestTheRemovedControllerAndActivationValuesAreRefused.)
func TestTheRemovedReceiptValuesAreRefused(t *testing.T) {
	for _, set := range []string{
		"hangarOutput.receipt.keyID=receipt-7",
		"hangarOutput.receipt.privateKeySecret=op-receipt-private",
	} {
		key := set[:strings.Index(set, "=")]
		if strings.HasPrefix(key, "hangarOutput.receipt.") {
			key = "hangarOutput.receipt"
		}
		message := renderOutputError(t, set)
		if !strings.Contains(message, key+" has been removed") ||
			!strings.Contains(message, "publication receipts were removed") {
			t.Errorf("%s rendered, or was refused without naming the removal:\n%s", set, message)
		}
	}
}

// The two key roles say different things and are pinned separately. One
// Secret serving both means rotating either rotates both.
func TestTheKeyRolesAreDistinctSecrets(t *testing.T) {
	for _, collapse := range [][]string{
		{"hangarOutput.materializationKeySecret=op-capability-key"},
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

// Branch-review follow-up F9. The output plane canonicalizes and spools whole
// trees to an emptyDir; an emptyDir with no sizeLimit is bounded by the node's
// disk, and filling it evicts every pod on the node, not only this one.
func TestTheOutputScratchVolumeIsBounded(t *testing.T) {
	message := renderHangarError(t, append(append([]string{}, baseControlSets...),
		"hangarOutput.enabled=true",
		"hangarOutput.bucket=jb-output",
		"hangarOutput.tenant=tenant-a",
		"hangarOutput.materializationKeySecret=op-output-materialize",
		"artifactDaemon.outputScratch.sizeLimit=",
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
		"artifactDaemon.outputScratch.sizeLimit=16Gi",
		"artifactDaemon.outputScratch.concurrency=2",
		"artifactDaemon.outputScratch.maxContentBytes=10737418240",
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
// cmd/artifact-daemon/outputplane), so the live exposure was nil; what was wrong was
// the claim, and a render that documents a limit it does not set is the kind of
// thing an operator reads once.
func TestTheOutputScratchVolumeIsBoundedInBaseControlOnlyModeToo(t *testing.T) {
	message := renderHangarError(t, append(append([]string{}, baseControlSets...),
		"artifactDaemon.outputScratch.sizeLimit=",
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
// Reclaim and the orphan sweep, in web
// ---------------------------------------------------------------------------

// The inventory and reclaimer controllers are gone; the web runs reclaim and
// the orphan sweep, and each of their values reaches the web's own flag.
func TestTheWebRunsReclaimAndTheOrphanSweep(t *testing.T) {
	defaults := objectNamed(t, renderOutput(t), "Deployment", "-"+webComponent)
	for _, flag := range []string{
		"--kubernetes-hangar-output-store=gcs",
		"--kubernetes-hangar-output-prefix=cluster-a",
		"--kubernetes-hangar-output-publication-grace=192h",
		"--kubernetes-hangar-output-reclaim-interval=1m",
		"--kubernetes-hangar-output-reclaim-batch=10",
		"--kubernetes-hangar-output-delete-timeout=2m",
		"--kubernetes-hangar-output-orphan-sweep-interval=1h",
	} {
		if !strings.Contains(defaults.body, flag) {
			t.Errorf("the default output render does not give web %s", flag)
		}
	}
	// GCS: the web reaches the bucket with its ambient credential. No disk
	// flag, and neither disk token volume.
	for _, absent := range append(append([]string{}, webDiskFlags...), "name: hangar-output-list", "name: hangar-output-delete") {
		if strings.Contains(defaults.body, absent) {
			t.Errorf("a GCS output render gives web %s, which is the disk store's", absent)
		}
	}

	tuned := objectNamed(t, renderOutput(t,
		"hangarOutput.reclaim.interval=90s",
		"hangarOutput.reclaim.deleteTimeout=3m",
		"hangarOutput.reclaim.batch=25",
		"hangarOutput.orphanSweep.interval=2h",
		"hangarOutput.publicationGrace=200h",
	), "Deployment", "-"+webComponent)
	for _, flag := range []string{
		"--kubernetes-hangar-output-reclaim-interval=90s",
		"--kubernetes-hangar-output-delete-timeout=3m",
		"--kubernetes-hangar-output-reclaim-batch=25",
		"--kubernetes-hangar-output-orphan-sweep-interval=2h",
		"--kubernetes-hangar-output-publication-grace=200h",
	} {
		if !strings.Contains(tuned.body, flag) {
			t.Errorf("the tuned render does not give web %s", flag)
		}
	}

	// Capture off is not drain: the plane stays configured, and web keeps
	// reclaiming what the plane still holds until residue reaches zero.
	draining := objectNamed(t, renderOutput(t, "hangarOutput.webEnabled=false"), "Deployment", "-"+webComponent)
	for _, flag := range webReclaimFlags {
		if !strings.Contains(draining.body, flag) {
			t.Errorf("with hangarOutput.webEnabled=false web loses %s; a drain needs reclaim "+
				"to keep running until the residue is zero", flag)
		}
	}
}

// On the disk store the web holds two of the store's role tokens, each in a
// volume of its own and read at the path its flag names: the inventory token
// the orphan sweep lists with, and the reclaimer token reclaim and the sweep
// delete with. Neither is the publisher's, which only the artifact daemon holds.
func TestOnTheDiskStoreTheWebHoldsTheListAndDeleteTokens(t *testing.T) {
	out := renderOutput(t, diskSets...)

	var web appsv1.Deployment
	decodeNamed(t, out, "Deployment", objectNamed(t, out, "Deployment", "-"+webComponent).name, &web)
	pod := web.Spec.Template.Spec
	args := strings.Join(pod.Containers[0].Args, "\n")
	for _, flag := range webDiskFlags {
		if !strings.Contains(args, flag) {
			t.Errorf("a disk output render does not give web %s", flag)
		}
	}
	if !strings.Contains(args, "--kubernetes-hangar-output-endpoint=https://jb-concourse-jetbridge-hangar-store.") {
		t.Errorf("web's output endpoint is not the disk store's Service:\n%s", args)
	}
	if !strings.Contains(args, "--kubernetes-hangar-output-store-id=store-1") {
		t.Error("web is not given the disk store's id")
	}

	mounts := map[string]string{}
	for _, mount := range pod.Containers[0].VolumeMounts {
		mounts[mount.Name] = mount.MountPath
	}
	for volume, want := range map[string]struct{ role, flag string }{
		"hangar-output-list":   {"inventory", "--kubernetes-hangar-output-list-token-file="},
		"hangar-output-delete": {"reclaimer", "--kubernetes-hangar-output-delete-token-file="},
	} {
		path, mounted := mounts[volume]
		if !mounted {
			t.Errorf("web does not mount %s", volume)

			continue
		}
		if !slices.Contains(pod.Containers[0].Args, want.flag+path+"/token") {
			t.Errorf("web's %s does not read %s/token", want.flag, path)
		}
		roles := []string{}
		for _, candidate := range pod.Volumes {
			if candidate.Name != volume || candidate.Projected == nil {
				continue
			}
			for _, source := range candidate.Projected.Sources {
				if source.Secret != nil && source.Secret.Name == "storage-credentials" {
					for _, item := range source.Secret.Items {
						roles = append(roles, item.Key)
					}
				}
			}
		}
		if !slices.Equal(roles, []string{want.role}) {
			t.Errorf("web's %s projects the store tokens %v, want only %s", volume, roles, want.role)
		}
	}
	if !slices.Contains(pod.Containers[0].Args, "--kubernetes-hangar-output-store-ca-cert="+mounts["hangar-output-list"]+"/ca.crt") {
		t.Error("web's store CA flag does not name the CA projected beside its list token")
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
			t.Errorf("the artifact daemon has no %s", probe)
		}
	}

	if !hasObject(t, out, "NetworkPolicy", "-"+outputDaemonComponent) {
		t.Errorf("%s has no NetworkPolicy", outputDaemonComponent)
	}

	if !hasObject(t, out, "PodDisruptionBudget", "-"+outputDaemonComponent) {
		t.Error("the artifact daemon has no PodDisruptionBudget")
	}
}

// The daemon owns node-local state; the web owns none, and a web that mounted
// the managed hostPath would be a second writer to a directory one daemon is
// the authority for.
func TestOnlyTheOutputDaemonMountsTheNodeLocalPaths(t *testing.T) {
	out := renderOutput(t)

	daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
	if !strings.Contains(daemon.body, "hostPath") {
		t.Fatal("the artifact daemon mounts no hostPath; it owns the source ledger and the " +
			"step incarnations")
	}
	if web := objectNamed(t, out, "Deployment", "-"+webComponent); strings.Contains(web.body, "hostPath") {
		t.Error("web mounts a hostPath; the node-local ledger has one writer")
	}
}

// ---------------------------------------------------------------------------
// The capture seal's Pod observation
// ---------------------------------------------------------------------------

// A capture seal waits for every container of the producing Pod to terminate,
// and in-cluster the daemon learns that by listing its own node's Pods
// (cmd/artifact-daemon/outputplane/pod_terminations.go). Without get/list on
// Pods the list is forbidden and no capture ever seals. The grant is read-only
// and belongs to the output facet alone: a base-only daemon seals nothing.
func TestTheCaptureSealCanObserveItsNodesPods(t *testing.T) {
	clusterPodVerbs := func(out string) []string {
		var role rbacv1.ClusterRole
		decodeNamed(t, out, "ClusterRole", objectNamed(t, out, "ClusterRole", "-"+outputDaemonComponent).name, &role)
		var verbs []string
		for _, rule := range role.Rules {
			if slices.Contains(rule.Resources, "pods") {
				verbs = append(verbs, rule.Verbs...)
			}
		}
		return verbs
	}
	podRoles := func(out string) []rbacv1.Role {
		var roles []rbacv1.Role
		for _, document := range strings.Split(out, "\n---") {
			if !strings.Contains("\n"+document+"\n", "\nkind: Role\n") || !strings.Contains(document, "-artifact-daemon-pods") {
				continue
			}
			var role rbacv1.Role
			if err := yaml.Unmarshal([]byte(document), &role); err != nil {
				t.Fatalf("decoding the daemon's pod Role: %v", err)
			}
			roles = append(roles, role)
		}
		return roles
	}

	out := renderOutput(t)
	if verbs := clusterPodVerbs(out); len(verbs) != 0 {
		t.Errorf("the daemon's ClusterRole grants %v on pods; Pod reads are namespaced", verbs)
	}
	roles := podRoles(out)
	if len(roles) != 1 {
		t.Fatalf("with the output facet the daemon has %d pod Roles, want 1", len(roles))
	}
	role := roles[0]
	if role.Namespace == "" {
		t.Errorf("the daemon's pod Role names no namespace")
	}
	var verbs []string
	for _, rule := range role.Rules {
		if slices.Contains(rule.Resources, "pods") && slices.Contains(rule.APIGroups, "") {
			verbs = append(verbs, rule.Verbs...)
		}
	}
	sort.Strings(verbs)
	if !slices.Equal(verbs, []string{"get", "list"}) {
		t.Errorf("the daemon's pod Role grants %v, want exactly get and list: the seal lists its "+
			"node's Pods and changes none", verbs)
	}
	daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
	if !strings.Contains(daemon.body, "--pod-terminations-namespace="+role.Namespace) {
		t.Errorf("the daemon does not read Pods in the namespace its Role covers (%s)", role.Namespace)
	}

	for name, out := range map[string]string{"base control only": renderBaseControl(t), "default": render(t)} {
		if roles := podRoles(out); len(roles) != 0 || len(clusterPodVerbs(out)) != 0 {
			t.Errorf("%s: the artifact daemon is granted pod reads; it seals no capture", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Both node labels
// ---------------------------------------------------------------------------

// Req 56/58. The two ready labels are scheduling HINTS, and they are two
// because a base-only node is a real deployment.
//
// What the CHART decides is not the label STRINGS -- those are protocol
// constants in hangar/executioncontrol and hangar/output, and rendering them
// here would be a second spelling of a value the daemon already holds. What it
// decides is whether the daemon can advertise at all, and which facets it has
// to advertise. So that is what this asserts, and
// cmd/artifact-daemon/outputplane/labels_test.go owns the order the two go on
// and come off in.
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
		// The artifact daemon's role: its own node's labels (get, patch), and
		// read access to its peers' EndpointSlices. The output facet adds read
		// access to Pods (TestTheCaptureSealCanObserveItsNodesPods); nothing
		// adds to nodes.
		nodes := 0
		for _, rule := range parsed.Rules {
			if strings.Join(rule.Resources, ",") != "nodes" {
				continue
			}
			nodes++
			if verbs := strings.Join(rule.Verbs, ","); verbs != "get,patch" {
				t.Errorf("%s: the daemon's ClusterRole grants %q on nodes; get and patch are "+
					"what a label needs, and a node-local daemon with list or watch over every "+
					"node is a cluster-wide reach it has no use for", name, verbs)
			}
		}
		if nodes != 1 {
			t.Errorf("%s: the daemon's ClusterRole has %d rules over nodes, want one", name, nodes)
		}

	}

	// The output facet is what the output label attests, and a base-only daemon
	// has none of it: no bucket, no read-warrant key, no publisher. It therefore
	// cannot advertise the output label however the code is written, which is a
	// stronger statement than a render asserting the string is absent.
	base := objectNamed(t, renderBaseControl(t), "DaemonSet", "-"+outputDaemonComponent)
	for _, absent := range []string{"--output-bucket", "--materialization-key-file"} {
		if strings.Contains(base.body, absent) {
			t.Errorf("a base-control-only daemon carries %s", absent)
		}
	}
	full := objectNamed(t, renderOutput(t), "DaemonSet", "-"+outputDaemonComponent)
	for _, present := range []string{"--output-bucket", "--materialization-key-file"} {
		if !strings.Contains(full.body, present) {
			t.Errorf("the artifact daemon does not carry %s", present)
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
	// The third weaker-than-it-reads grant, and the one an operator is most
	// likely to approve without noticing: storage.objects.create IS the
	// overwrite permission. GCS has no create-only object permission, so the
	// publisher can destroy a published object without holding delete, and
	// what makes that safe is the code's create-if-absent precondition and the
	// dedicated bucket -- not IAM. The matrix said two such limits and there
	// are three.
	if !strings.Contains(strings.ToLower(body), "overwrite") {
		t.Error("the documentation does not say that storage.objects.create is the OVERWRITE " +
			"permission, so an operator reads the publisher's grant as create-only and the " +
			"one thing that actually stops it destroying a published object -- the " +
			"create-if-absent precondition in the code -- is written down nowhere they look")
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
	"publisher": {"CreateAbsent": "storage.objects.create", "OpenExact": "storage.objects.get", "StatCurrent": "storage.objects.get", "StatExact": "storage.objects.get"},
	"web":       {"List": "storage.objects.list", "DeleteExact": "storage.objects.delete", "StatExact": "storage.objects.get"},
}

// roleCapabilitySource is where each principal's capability is DECLARED: every
// interface in each file is a view of the store that principal holds.
//
// The publisher's is its own package under hangar/output. The web holds two
// views: the reclaimer's store (stat and conditional delete), which reclaim and
// the orphan sweep delete through, and the sweep's lister. Both are read, so a
// new method on either is a permission somebody has to have written down.
var roleCapabilitySource = map[string][]string{
	"publisher": {"hangar/output/publisher/publisher.go"},
	"web":       {"hangar/output/reclaimer/reclaimer.go", "atc/hangaroutput/reclaim/sweep.go"},
}

// documentedRolePermissions is what an operator must grant each workload. The
// test below checks it both ways: values.yaml names every permission, and
// every method the role's declared capability has maps to one of them.
func documentedRolePermissions(t *testing.T) map[string][]string {
	t.Helper()
	return map[string][]string{
		"publisher": {"storage.objects.create", "storage.objects.get"},
		"web":       {"storage.objects.list", "storage.objects.get", "storage.objects.delete"},
	}
}

func TestTheDocumentedIAMMatrixMatchesWhatEachRoleCanActuallyDo(t *testing.T) {
	values := readChartFile(t, "values.yaml")
	root := repoRoot(t)

	for role, permissions := range documentedRolePermissions(t) {
		// Every documented permission appears in values.yaml, verbatim. A
		// matrix an operator cannot copy is a matrix they will approximate.
		for _, permission := range permissions {
			if !namesPermission(values, permission) {
				t.Errorf("deploy/chart/values.yaml does not name %s, which the %s role needs",
					permission, role)
			}
		}

		// And the reverse: every method the role's own interfaces declare maps
		// to a permission the documentation grants. A method with no mapping is
		// a capability nobody wrote down.
		sources, declared := roleCapabilitySource[role]
		if !declared {
			t.Errorf("%s is a documented role and roleCapabilitySource does not say where its "+
				"capability is declared, so nothing checks what it can actually do against "+
				"what the chart tells an operator to grant", role)

			continue
		}
		methods := map[string]bool{}
		for _, source := range sources {
			for method := range declaredRoleMethods(t, filepath.Join(root, filepath.FromSlash(source))) {
				methods[method] = true
			}
		}
		if len(methods) < 2 {
			t.Fatalf("parsed only %d methods out of the %s role's capability at %v; the "+
				"declaration moved and this rule would pass vacuously",
				len(methods), role, sources)
		}

		granted := map[string]bool{}
		for _, permission := range permissions {
			granted[permission] = true
		}
		// And no row for a method that is no longer declared. A mapping that
		// outlives its method is how a guard keeps reporting on a capability
		// nobody has any more, while the one that replaced it goes unmapped.
		for method := range roleOperations[role] {
			if !methods[method] {
				t.Errorf("roleOperations maps %s.%s to a permission and %v declares no such "+
					"method; the mapping outlived what it described",
					role, method, sources)
			}
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

// namesPermission reports whether the documentation names this permission as
// itself, rather than as the prefix of a longer one.
//
// SUBSTRING MATCHING MADE ONE ROW UNFALSIFIABLE. storage.buckets.get is a
// prefix of storage.buckets.getIamPolicy, so deleting the attestor's read
// permission from the matrix left this guard green -- measured, by deleting it.
// Any permission that is a prefix of another has the same hole, and the set is
// not fixed: it grows whenever Google adds a longer name beside a shorter one.
func namesPermission(documentation, permission string) bool {
	for offset := 0; ; {
		index := strings.Index(documentation[offset:], permission)
		if index < 0 {
			return false
		}
		after := offset + index + len(permission)
		if after >= len(documentation) {
			return true
		}
		next := documentation[after]
		if !(next >= 'a' && next <= 'z') && !(next >= 'A' && next <= 'Z') &&
			!(next >= '0' && next <= '9') && next != '.' {
			return true
		}
		offset = after
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

// ---------------------------------------------------------------------------
// The IAM half, made load-bearing
// ---------------------------------------------------------------------------
//
// Source guards cannot bound a credential holder: a Go program holding
// application default credentials can delete an object with a raw HTTPS request,
// a vendor CLI, or any SDK nobody thought to name, and no rule over imports
// changes that. IAM is the control -- so the claim "only the web may reach
// delete" is only as true as the grant, and the grant has to be asserted
// somewhere rather than described.
//
// This is that assertion, over the two artefacts an operator actually acts on:
// the permission matrix generated into deploy/chart/values.yaml, which is what
// they copy into Terraform, and the rendered Workload Identity annotations,
// which are what bind a Pod to the cloud principal that holds it. Neither proves
// a binding exists -- the chart creates no IAM and says so. What they prove is
// that the documentation grants storage.objects.delete to exactly one principal
// and that exactly one workload runs as it.

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

func TestOnlyTheWebPrincipalIsGrantedObjectDelete(t *testing.T) {
	matrix := documentedPermissionMatrix(t)
	if len(matrix) == 0 {
		t.Fatalf("parsed %d workloads out of the documented permission matrix, not two: %v. "+
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
	if total == 0 {
		t.Fatalf("the documented matrix grants %d permissions across two workloads; it "+
			"collapsed", total)
	}
	sort.Strings(granted)

	if len(granted) != 1 {
		t.Fatalf("deploy/chart/values.yaml grants storage.objects.delete to %d workloads (%v).\n\n"+
			"IAM is the control here -- source guards cannot bound a process that holds "+
			"credentials -- so exactly one principal may hold delete on the output bucket.",
			len(granted), granted)
	}
	if !strings.HasPrefix(granted[0], "web") {
		t.Fatalf("the documented matrix grants storage.objects.delete to %q, not to the web",
			granted[0])
	}

	// And the rendered side: the annotation that binds a Pod to that principal
	// is on one ServiceAccount, and one workload runs as it.
	out := renderOutput(t)

	const deletePrincipal = "web@p.iam.gserviceaccount.com"

	webAccount := objectNamed(t, out, "ServiceAccount", "-"+webComponent).name

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
	if annotated < 2 {
		t.Fatalf("only %d rendered ServiceAccounts carry a Workload Identity annotation; the "+
			"render changed shape and this rule is looking at nothing", annotated)
	}
	if len(accountsWithDelete) != 1 {
		t.Fatalf("%d ServiceAccounts are annotated with the delete-holding principal %s: %v.\n\n"+
			"A Kubernetes service account is Pod-wide. Two accounts bound to one cloud identity "+
			"is two Pods holding storage.objects.delete, whatever their code does.",
			len(accountsWithDelete), deletePrincipal, accountsWithDelete)
	}
	if _, ok := accountsWithDelete[webAccount]; !ok {
		t.Fatalf("the delete-holding principal %s is bound to %v, not to the web's "+
			"ServiceAccount %s", deletePrincipal, accountsWithDelete, webAccount)
	}

	// Finally: which workloads run as that account. Every Pod template in the
	// render, not just the output ones -- web, the artifact daemon and postgres
	// are all in this namespace.
	var runAs []string
	templates := 0
	for _, document := range documentsIn(t, out) {
		_, spec := podOf(t, document)
		if document.kind != "Deployment" && document.kind != "DaemonSet" && document.kind != "Job" &&
			document.kind != "StatefulSet" {
			continue
		}
		templates++
		if spec.ServiceAccountName == webAccount {
			runAs = append(runAs, document.source)
		}
	}
	if templates < 2 {
		t.Fatalf("only %d Pod templates were decoded out of the render; the walk failed and "+
			"this rule would pass vacuously", templates)
	}
	if len(runAs) != 1 || !strings.HasSuffix(runAs[0], "web-deployment.yaml") {
		t.Fatalf("the Pod templates running as %s, the only account bound to a principal "+
			"holding storage.objects.delete, are %v; want the web Deployment alone",
			webAccount, runAs)
	}
}

// ---------------------------------------------------------------------------
// The control key is gone
// ---------------------------------------------------------------------------

// The daemon is trusted over mTLS and signs nothing, so there is no node
// control key, no key id and no verification ring to configure. Each value
// is refused naming its removal, not ignored: a values file still setting one
// would otherwise deploy a node believed to be signing.
func TestTheControlKeyValuesAreRefused(t *testing.T) {
	for _, set := range []string{
		"hangarOutput.executionControl.keySecret=op-control-key",
		"hangarOutput.executionControl.keyID=control-key-7",
		"hangarOutput.executionControl.publicKeys[0].epoch=7",
		"hangarOutput.activationEpoch=7",
	} {
		key := set[:strings.Index(set, "=")]
		if i := strings.Index(key, "["); i >= 0 {
			key = key[:i]
		}
		message := renderOutputError(t, set)
		if !strings.Contains(message, key+" has been removed") {
			t.Errorf("%s rendered, or was refused without naming the removal:\n%s", set, message)
		}
	}
}

// The base facet cannot render without the capability key: it is what mounts
// the daemon's output plane, and the base facet verifies every capability
// with it. The refusal an operator most needs is the one at render time.
func TestTheCapabilityKeyIsRequiredWithTheBaseFacet(t *testing.T) {
	message := renderHangarError(t, append(append([]string{}, baseControlSets...),
		"hangarOutput.capabilityKeySecret=")...)
	if !strings.Contains(message, "hangarOutput.capabilityKeySecret") {
		t.Errorf("an empty capability key Secret rendered, or was refused by something else:\n%s",
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
	if len(granted) == 0 {
		t.Fatalf("parsed %d workloads out of the granted matrix, not two", len(granted))
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
	if !strings.Contains(message, "has been removed") {
		t.Errorf("the refusal does not identify the obsolete toggle:\n%s", message)
	}
}

// Every duration in this plane is checked at render time, and so is the reclaim
// batch: zero admits nothing, and a plane that reclaims nothing keeps every
// published object forever while its status says it reclaims.
func TestTheReclaimAndSweepValuesAreValidated(t *testing.T) {
	for _, bad := range []struct {
		set, names string
	}{
		{"hangarOutput.reclaim.interval=nope", "reclaim.interval"},
		{"hangarOutput.reclaim.deleteTimeout=nope", "reclaim.deleteTimeout"},
		{"hangarOutput.reclaim.batch=0", "reclaim.batch"},
		{"hangarOutput.orphanSweep.interval=nope", "orphanSweep.interval"},
	} {
		message := renderOutputError(t, bad.set)
		if !strings.Contains(message, bad.names) {
			t.Errorf("%s rendered, or was refused by something else:\n%s", bad.set, message)
		}
	}
}

// The values of the deleted controllers, the activation walk and the
// activation database role are refused by name, each with where its job went,
// rather than silently ignored -- with the output plane off as well as on, so
// an operator meets it before anything else.
func TestTheRemovedControllerAndActivationValuesAreRefused(t *testing.T) {
	for _, probe := range []struct {
		set, key, says string
	}{
		{"hangarOutput.inventory.interval=1m", "hangarOutput.inventory", "hangarOutput.orphanSweep.interval"},
		{"hangarOutput.inventory.serviceAccount.annotations.a=b", "hangarOutput.inventory", "serviceAccount.annotations"},
		{"hangarOutput.reclaimer.deleteTimeout=2m", "hangarOutput.reclaimer", "hangarOutput.reclaim.interval"},
		{"hangarOutput.reclaimer.serviceAccount.name=x", "hangarOutput.reclaimer", "serviceAccount.annotations"},
		{"hangarOutput.database.existingSecret=op-activation-db", "hangarOutput.database", "web's own database user"},
		{"hangarOutput.activation.target=output", "hangarOutput.activation", "hangarOutput.webEnabled"},
		{"hangarOutput.activation.job.finalize=true", "hangarOutput.activation", "hangar_enabled"},
		{"hangarOutput.activation.receiptKeyLifetime=43800h", "hangarOutput.activation", "hangar_enabled"},
		{"hangarBootstrap.database.enabled=true", "hangarBootstrap.database", "hangar_enabled"},
	} {
		for name, message := range map[string]string{
			"plane off": renderHangarError(t, probe.set),
			"plane on":  renderOutputError(t, probe.set),
		} {
			if !strings.Contains(message, probe.key+" has been removed") || !strings.Contains(message, probe.says) {
				t.Errorf("%s (%s) rendered, or was refused without naming %s's removal and %s:\n%s",
					probe.set, name, probe.key, probe.says, firstLines(message, 3))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The low set
// ---------------------------------------------------------------------------

// readOnlyRootFilesystem with nowhere to write is a runtime error waiting for
// the first operation that wants a temp file -- on a Pod that passed every
// render check. The web now holds the output store's clients, and the GCS
// client library spools to a temp file.
func TestTheWebHasSomewhereToWrite(t *testing.T) {
	var web appsv1.Deployment
	out := renderOutput(t)
	decodeNamed(t, out, "Deployment", objectNamed(t, out, "Deployment", "-"+webComponent).name, &web)
	for _, mount := range web.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.MountPath == "/tmp" {
			return
		}
	}
	t.Error("web mounts nothing at /tmp; the output store clients it holds have nowhere to spool")
}

// The output scratch is the plane's own directory: never the storage root the
// artifact daemon's sweeper and registry own, nor the strict-input scratch.
func TestTheOutputScratchIsDisjointFromTheStorageRootAndTheStrictInputScratch(t *testing.T) {
	for name, sets := range map[string][]string{
		"inside the storage root":  {"artifactDaemon.outputScratch.path=/var/concourse/artifacts/scratch"},
		"holding the storage root": {"artifactDaemon.outputScratch.path=/var/concourse"},
		"the strict-input scratch": {"artifactDaemon.hangar.enabled=true", "artifactDaemon.hangar.store=gcs",
			"artifactDaemon.hangar.bucket=jb-strict-input", "artifactDaemon.hangar.allowGeneratedKey=true",
			"artifactDaemon.outputScratch.path=/var/concourse/hangar-scratch"},
	} {
		message := renderHangarError(t, append(append([]string{}, baseControlSets...), sets...)...)
		if !strings.Contains(message, "must be disjoint") {
			t.Errorf("%s: an overlapping output scratch was accepted:\n%s", name, message)
		}
	}
	renderBaseControl(t)
}
