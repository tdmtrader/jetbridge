package tests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/hangar/bootstrap"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// bootstrapSets turn on the bootstrap and every consumer of its inventory:
// strict inputs and the output plane on the disk store, rings from the
// bootstrap rather than from values.
var bootstrapSets = []string{
	"hangarBootstrap.enabled=true",
	"hangarBootstrap.database.enabled=true",
	"postgresql.existingSecret=op-db-password",
	"hangarOutput.executionControl.enabled=true",
	"hangarOutput.executionControl.keySecret=op-control-key",
	"hangarOutput.executionControl.keyID=control-key-1",
	"hangarOutput.capabilityKeySecret=op-capability-key",
	"hangarOutput.activationEpoch=1",
	"artifactDaemon.outputScratch.sizeLimit=32Gi",
	"hangarOutput.enabled=true",
	"hangarOutput.webEnabled=true",
	"hangarOutput.tenant=tenant-a",
	"hangarOutput.bucket=outputs",
	"hangarOutput.receipt.keyID=receipt-1",
	"hangarOutput.receipt.privateKeySecret=op-receipt-private",
	"hangarOutput.materializationKeySecret=op-output-materialize",
	"hangarOutput.database.existingSecret=op-activation-db",
	"hangarOutput.activation.target=off",
	"hangarStorage.disk.enabled=true",
	"hangarStorage.disk.storeID=store-1",
	"hangarStorage.disk.tls.existingSecret=storage-tls",
	"hangarStorage.disk.credentials.existingSecret=storage-credentials",
	"hangarOutput.store=disk",
	"artifactDaemon.hangar.enabled=true",
	"artifactDaemon.hangar.webEnabled=true",
	"artifactDaemon.hangar.store=disk",
	"artifactDaemon.hangar.bucket=inputs",
	"artifactDaemon.hangar.keySecret=op-hangar-key",
	"web.runInputSigningKeySecret=jb-concourse-jetbridge-run-input-signing-key",
}

const bootstrapName = "jb-concourse-jetbridge-hangar-bootstrap"

// Off renders nothing of the bootstrap and changes no consumer's mounts, so
// landing it on a deployment that syncs every chart change is inert.
func TestTheHangarBootstrapIsOffByDefault(t *testing.T) {
	out := render(t)
	if strings.Contains(out, "hangar-bootstrap") || strings.Contains(out, "concourse-hangar-bootstrap") {
		t.Error("the default render carries a bootstrap object")
	}
	if strings.Contains(out, "argocd.argoproj.io/sync-wave") {
		t.Error("the default render carries a sync-wave annotation")
	}
	if strings.Contains(out, "hangar-warrant-key") || strings.Contains(out, "hangar-rings-") {
		t.Error("the default render mounts a bootstrap Secret")
	}
}

// The runbook's bootstrap step turns the bootstrap on and names its Secrets
// while every feature is still off.
func TestTheBootstrapRendersWithEveryFeatureOff(t *testing.T) {
	out := render(t,
		"hangarBootstrap.enabled=true",
		"hangarOutput.activationEpoch=1",
		"hangarOutput.executionControl.keySecret=op-control-key",
		"hangarOutput.receipt.privateKeySecret=op-receipt-private",
		"hangarOutput.receipt.keyID=receipt-1",
		"hangarStorage.disk.tls.existingSecret=storage-tls",
		"hangarStorage.disk.credentials.existingSecret=storage-credentials",
		"artifactDaemon.hangar.keySecret=op-hangar-key",
	)
	inv := renderedInventory(t, out)
	if len(inv.Entries) < 6 {
		t.Errorf("the inventory has %d entries, want every named Secret", len(inv.Entries))
	}
}

// The rendered inventory is one the bootstrap accepts: it decodes strictly and
// a reconcile over an empty store creates every entry but the database
// credential, which is the database step's.
func TestTheRenderedInventoryReconciles(t *testing.T) {
	inv := renderedInventory(t, render(t, bootstrapSets...))
	store := &chartTestStore{secrets: map[string]bootstrap.Secret{}}
	if err := bootstrap.Reconcile(context.Background(), inv, store, nil); err != nil {
		t.Fatalf("the bootstrap refused the chart's own inventory: %v", err)
	}
	for _, entry := range inv.Entries {
		_, created := store.secrets[entry.Name]
		if entry.Kind == bootstrap.KindDatabaseCredential && created {
			t.Errorf("the sync-start reconcile created %s", entry.Name)
		}
		if entry.Kind != bootstrap.KindDatabaseCredential && !created {
			t.Errorf("the reconcile did not create %s", entry.Name)
		}
	}
}

// Every consumer the inventory lists mounts that Secret, and every workload
// mounting an inventory Secret is listed as its consumer.
func TestEveryInventoryConsumerMountsItsSecretAndNoOtherDoes(t *testing.T) {
	out := render(t, bootstrapSets...)
	inv := renderedInventory(t, out)

	mounted := map[string]map[string]bool{} // component -> Secret names its pod mounts
	for _, doc := range documentsIn(t, out) {
		component, pod := podOf(t, doc)
		if component == "" || component == "hangar-bootstrap" {
			continue
		}
		if mounted[component] == nil {
			mounted[component] = map[string]bool{}
		}
		for _, volume := range pod.Volumes {
			if volume.Secret != nil {
				mounted[component][volume.Secret.SecretName] = true
			}
			if volume.Projected != nil {
				for _, source := range volume.Projected.Sources {
					if source.Secret != nil {
						mounted[component][source.Secret.Name] = true
					}
				}
			}
		}
		for _, container := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			for _, env := range container.Env {
				if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
					mounted[component][env.ValueFrom.SecretKeyRef.Name] = true
				}
			}
		}
	}

	listed := map[string]map[string]bool{}
	for _, entry := range inv.Entries {
		for _, consumer := range entry.Consumers {
			if listed[consumer] == nil {
				listed[consumer] = map[string]bool{}
			}
			listed[consumer][entry.Name] = true
			if !mounted[consumer][entry.Name] {
				t.Errorf("%s lists %s as a consumer, and %s does not mount it", entry.Name, consumer, consumer)
			}
		}
	}
	for component, secrets := range mounted {
		for secret := range secrets {
			for _, entry := range inv.Entries {
				if entry.Name == secret && !listed[component][secret] {
					t.Errorf("%s mounts inventory Secret %s without being listed as its consumer", component, secret)
				}
			}
		}
	}
}

// The Job's identity may get the inventory's Secrets by name and create
// Secrets, nothing else; the policy limits its creates; and the ordering keeps
// the policy in force whenever the Role grants create.
func TestTheBootstrapIdentityAndItsOrdering(t *testing.T) {
	out := render(t, bootstrapSets...)
	inv := renderedInventory(t, out)
	var names []string
	for _, entry := range inv.Entries {
		names = append(names, entry.Name)
	}
	sort.Strings(names)

	var role rbacv1.Role
	decodeNamed(t, out, "Role", bootstrapName, &role)
	if len(role.Rules) != 2 {
		t.Fatalf("the bootstrap Role has %d rules, want get-by-names and create", len(role.Rules))
	}
	get, create := role.Rules[0], role.Rules[1]
	gotNames := append([]string{}, get.ResourceNames...)
	sort.Strings(gotNames)
	if strings.Join(get.Verbs, ",") != "get" || strings.Join(get.Resources, ",") != "secrets" || strings.Join(gotNames, ",") != strings.Join(names, ",") {
		t.Errorf("the get rule is %+v, want get on exactly the inventory's Secrets", get)
	}
	if strings.Join(create.Verbs, ",") != "create" || strings.Join(create.Resources, ",") != "secrets" || len(create.ResourceNames) != 0 {
		t.Errorf("the create rule is %+v, want create on secrets", create)
	}

	var policy admissionv1.ValidatingAdmissionPolicy
	decodeNamed(t, out, "ValidatingAdmissionPolicy", bootstrapName, &policy)
	expressions := ""
	for _, v := range policy.Spec.Validations {
		expressions += v.Expression + "\n"
	}
	for _, want := range []string{`object.metadata.name in`, `"Opaque", "kubernetes.io/tls"`, `app.kubernetes.io/managed-by`} {
		if !strings.Contains(expressions, want) {
			t.Errorf("the policy does not check %s", want)
		}
	}
	if policy.Spec.FailurePolicy == nil || *policy.Spec.FailurePolicy != admissionv1.Fail {
		t.Error("the policy does not fail closed")
	}

	waves := map[string]string{}
	hooks := map[string]string{}
	for _, doc := range documentsIn(t, out) {
		if doc.name != bootstrapName && doc.name != bootstrapName+"-inventory" {
			continue
		}
		var meta struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc.body), &meta); err != nil {
			t.Fatal(err)
		}
		waves[doc.kind] = meta.Metadata.Annotations["argocd.argoproj.io/sync-wave"]
		hooks[doc.kind] = meta.Metadata.Annotations["argocd.argoproj.io/hook"]
	}
	for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
		if waves[kind] != "-3" || hooks[kind] != "" {
			t.Errorf("%s is wave %q hook %q, want a plain resource at wave -3", kind, waves[kind], hooks[kind])
		}
	}
	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding", "ConfigMap"} {
		if waves[kind] != "-2" || hooks[kind] != "" {
			t.Errorf("%s is wave %q hook %q, want a plain resource at wave -2", kind, waves[kind], hooks[kind])
		}
	}
	if waves["Job"] != "-1" || hooks["Job"] != "Sync" {
		t.Errorf("the Job is wave %q hook %q, want a Sync hook at wave -1", waves["Job"], hooks["Job"])
	}
}

// No private value is rendered: no key, no connection string, no 32-byte
// base64 value, and no password in any bootstrap object. (Web's bundled
// PostgreSQL password is a chart default outside this inventory.)
func TestTheBootstrapRendersNoPrivateValue(t *testing.T) {
	out := render(t, bootstrapSets...)
	for _, forbidden := range []string{"PRIVATE KEY", "postgres://"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the render contains %q", forbidden)
		}
	}
	for _, doc := range documentsIn(t, out) {
		if !strings.HasPrefix(doc.name, bootstrapName) {
			continue
		}
		if strings.Contains(doc.body, "password=") {
			t.Errorf("bootstrap object %s %s renders a password into a connection string", doc.kind, doc.name)
		}
		if doc.kind != "Job" {
			continue
		}
		_, pod := podOf(t, doc)
		for _, container := range pod.Containers {
			for _, env := range container.Env {
				if strings.Contains(strings.ToUpper(env.Name), "PASSWORD") && env.ValueFrom == nil {
					t.Errorf("bootstrap Job %s renders %s as a literal value", doc.name, env.Name)
				}
			}
		}
	}
	base64Value := regexp.MustCompile(`[A-Za-z0-9+/]{43}=`)
	for _, match := range base64Value.FindAllString(out, -1) {
		if raw, err := base64.StdEncoding.DecodeString(match); err == nil && len(raw) == 32 {
			t.Errorf("the render contains a 32-byte base64 value %s…", match[:8])
		}
	}
}

// The value-built rings are off in bootstrap mode, and a ring declared in
// values beside the bootstrap's is refused.
func TestTheBootstrapOwnsTheRings(t *testing.T) {
	out := render(t, bootstrapSets...)
	if strings.Contains(out, "jb-concourse-jetbridge-hangar-output-receipt-keys") {
		t.Error("the value-built receipt key ConfigMap is rendered in bootstrap mode")
	}
	if !strings.Contains(webDeployment(t, out), "secretName: "+ringName(t, renderedInventory(t, out))) {
		t.Error("web does not mount the bootstrap's ring Secret")
	}
	msg := renderHangarError(t, append(append([]string{}, bootstrapSets...),
		"hangarOutput.receipt.publicKeys[0].id=receipt-1",
		"hangarOutput.receipt.publicKeys[0].epoch=1",
		"hangarOutput.receipt.publicKeys[0].key=cHVibGljLWtleS1ieXRlcw==")...)
	if !strings.Contains(msg, "hangarBootstrap composes the verification rings") {
		t.Errorf("a values ring beside the bootstrap's rendered: %s", firstLines(msg, 3))
	}
}

func renderedInventory(t *testing.T, out string) bootstrap.Inventory {
	t.Helper()
	var configMap corev1.ConfigMap
	decodeNamed(t, out, "ConfigMap", bootstrapName+"-inventory", &configMap)
	var inv bootstrap.Inventory
	decoder := json.NewDecoder(strings.NewReader(configMap.Data["inventory.json"]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inv); err != nil {
		t.Fatalf("the inventory does not decode strictly: %v", err)
	}
	return inv
}

func ringName(t *testing.T, inv bootstrap.Inventory) string {
	t.Helper()
	for _, entry := range inv.Entries {
		if entry.Kind == bootstrap.KindRing {
			return entry.Name
		}
	}
	t.Fatal("the inventory has no ring")
	return ""
}

func decodeNamed(t *testing.T, out, kind, name string, into any) {
	t.Helper()
	for _, doc := range documentsIn(t, out) {
		if doc.kind == kind && doc.name == name {
			if err := yaml.UnmarshalStrict([]byte(doc.body), into); err != nil {
				t.Fatalf("decode %s %s: %v", kind, name, err)
			}
			return
		}
	}
	t.Fatalf("the render has no %s %s", kind, name)
}

// podOf returns a workload's component label and pod spec.
func podOf(t *testing.T, doc document) (string, corev1.PodSpec) {
	t.Helper()
	var template corev1.PodTemplateSpec
	switch doc.kind {
	case "Deployment":
		var d appsv1.Deployment
		if err := yaml.Unmarshal([]byte(doc.body), &d); err != nil {
			t.Fatal(err)
		}
		template = d.Spec.Template
	case "DaemonSet":
		var d appsv1.DaemonSet
		if err := yaml.Unmarshal([]byte(doc.body), &d); err != nil {
			t.Fatal(err)
		}
		template = d.Spec.Template
	case "StatefulSet":
		var s appsv1.StatefulSet
		if err := yaml.Unmarshal([]byte(doc.body), &s); err != nil {
			t.Fatal(err)
		}
		template = s.Spec.Template
	case "Job":
		var j batchv1.Job
		if err := yaml.Unmarshal([]byte(doc.body), &j); err != nil {
			t.Fatal(err)
		}
		template = j.Spec.Template
	default:
		return "", corev1.PodSpec{}
	}
	return template.Labels["app.kubernetes.io/component"], template.Spec
}

type chartTestStore struct{ secrets map[string]bootstrap.Secret }

func (store *chartTestStore) Get(_ context.Context, name string) (bootstrap.Secret, bool, error) {
	secret, found := store.secrets[name]
	return secret, found, nil
}

func (store *chartTestStore) Create(_ context.Context, secret bootstrap.Secret) error {
	store.secrets[secret.Name] = secret
	return nil
}

// The database step is a PostSync hook, reading web's password from its
// Secret and never from a rendered value; off, it is not rendered at all.
func TestTheDatabaseStepIsAPostSyncHookReadingWebsSecret(t *testing.T) {
	if strings.Contains(render(t, "hangarBootstrap.enabled=true"), bootstrapName+"-database") {
		t.Error("the database Job renders with hangarBootstrap.database off")
	}

	out := render(t, bootstrapSets...)
	var job batchv1.Job
	decodeNamed(t, out, "Job", bootstrapName+"-database", &job)
	if job.Annotations["argocd.argoproj.io/hook"] != "PostSync" {
		t.Errorf("the database Job is hook %q, want PostSync", job.Annotations["argocd.argoproj.io/hook"])
	}
	var password *corev1.EnvVar
	for i, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "PGPASSWORD" {
			password = &job.Spec.Template.Spec.Containers[0].Env[i]
		}
	}
	if password == nil || password.ValueFrom == nil || password.ValueFrom.SecretKeyRef == nil ||
		password.ValueFrom.SecretKeyRef.Name != "op-db-password" {
		t.Errorf("PGPASSWORD is %+v, want it from postgresql.existingSecret", password)
	}

	msg := renderHangarError(t, "hangarBootstrap.enabled=true", "hangarBootstrap.database.enabled=true",
		"hangarOutput.database.existingSecret=op-activation-db")
	if !strings.Contains(msg, "requires postgresql.existingSecret") {
		t.Errorf("the database step rendered without postgresql.existingSecret: %s", firstLines(msg, 3))
	}
}

// The activation walk is one Job, a PostSync hook a wave after the database
// step that creates the role and the Secret it runs as, and a Helm post-install
// and post-upgrade hook for a plain `helm upgrade`. BeforeHookCreation is what
// lets one fixed name survive an image bump or a new epoch: the previous run is
// deleted before the next is created.
func TestTheActivationWalkIsAPostSyncHookAfterTheDatabaseStep(t *testing.T) {
	out := render(t, append(append([]string{}, bootstrapSets...),
		"hangarOutput.activation.target=output")...)

	var walks []string
	for _, doc := range documentsIn(t, out) {
		if doc.kind != "Job" {
			continue
		}
		component, _ := podOf(t, doc)
		if component == "hangar-output-activation" {
			walks = append(walks, doc.name)
		}
	}
	const walkName = "jb-concourse-jetbridge-hangar-output-walk"
	if len(walks) != 1 || walks[0] != walkName {
		t.Fatalf("the render holds activation Jobs %v, want exactly %s", walks, walkName)
	}

	var walk, database batchv1.Job
	decodeNamed(t, out, "Job", walkName, &walk)
	decodeNamed(t, out, "Job", bootstrapName+"-database", &database)
	for annotation, want := range map[string]string{
		"argocd.argoproj.io/hook":               "PostSync",
		"argocd.argoproj.io/hook-delete-policy": "BeforeHookCreation",
		"helm.sh/hook":                          "post-install,post-upgrade",
		"helm.sh/hook-delete-policy":            "before-hook-creation",
	} {
		if got := walk.Annotations[annotation]; got != want {
			t.Errorf("the walk Job's %s is %q, want %q", annotation, got, want)
		}
	}
	// Argo ignores Helm's hook annotations while its own is present, so the
	// database Job's Argo hook is what keeps its Argo behaviour unchanged.
	if database.Annotations["argocd.argoproj.io/hook"] != "PostSync" {
		t.Fatalf("the database Job is hook %q; the walk's ordering is stated against a "+
			"PostSync database step", database.Annotations["argocd.argoproj.io/hook"])
	}
	// Under a plain Helm upgrade the database Job is a hook as well, weighted
	// before the walk, so Helm runs and waits for it first.
	for _, job := range []batchv1.Job{walk, database} {
		if got := job.Annotations["helm.sh/hook"]; got != "post-install,post-upgrade" {
			t.Errorf("%s's helm.sh/hook is %q, want post-install,post-upgrade", job.Name, got)
		}
	}
	weight := func(job batchv1.Job) int {
		parsed, err := strconv.Atoi(job.Annotations["helm.sh/hook-weight"])
		if err != nil {
			t.Fatalf("%s has helm.sh/hook-weight %q", job.Name, job.Annotations["helm.sh/hook-weight"])
		}
		return parsed
	}
	if weight(walk) <= weight(database) {
		t.Errorf("the walk Job's Helm hook weight is %d and the database Job's %d; Helm "+
			"would not run the database step first", weight(walk), weight(database))
	}
	wave := func(job batchv1.Job) int {
		value := job.Annotations["argocd.argoproj.io/sync-wave"]
		if value == "" {
			return 0
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("%s has sync-wave %q", job.Name, value)
		}
		return parsed
	}
	if wave(walk) <= wave(database) {
		t.Errorf("the walk Job is at wave %d and the database Job at wave %d; the walk runs as "+
			"the role the database step creates, so it needs a later wave", wave(walk), wave(database))
	}

	// The artifact daemon serves the output plane, so the walk attests its
	// cohort and waits on its DaemonSet.
	daemon := "jb-concourse-jetbridge-artifact-daemon"
	args := strings.Join(walk.Spec.Template.Spec.Containers[0].Command, " ")
	for _, want := range []string{"--mode=walk", "--target=output", "--daemonset-name=" + daemon,
		"--tls-cert=", "--tls-key=", "--tls-ca-cert=", "--tls-server-name=", "--receipt-key-lifetime="} {
		if !strings.Contains(args, want) {
			t.Errorf("the walk Job's command lacks %s: %s", want, args)
		}
	}
	if strings.Contains(args, "--facet") {
		t.Errorf("the walk Job names a facet: %s", args)
	}
	if strings.Contains(args, "--finalize") {
		t.Errorf("the walk Job finalizes with hangarOutput.activation.job.finalize unset: %s", args)
	}
	finalizing := render(t, append(append([]string{}, bootstrapSets...),
		"hangarOutput.activation.target=base", "hangarOutput.activation.job.finalize=true")...)
	var finalized batchv1.Job
	decodeNamed(t, finalizing, "Job", walkName, &finalized)
	if command := finalized.Spec.Template.Spec.Containers[0].Command; !slices.Contains(command, "--finalize") {
		t.Errorf("the walk Job does not pass --finalize with hangarOutput.activation.job.finalize=true: %v", command)
	}

	var role rbacv1.Role
	decodeNamed(t, out, "Role", "jb-concourse-jetbridge-hangar-output-activation", &role)
	var daemonsets []rbacv1.PolicyRule
	for _, rule := range role.Rules {
		if slices.Contains(rule.Resources, "daemonsets") {
			daemonsets = append(daemonsets, rule)
		}
	}
	if len(daemonsets) != 1 || strings.Join(daemonsets[0].APIGroups, ",") != "apps" ||
		strings.Join(daemonsets[0].Verbs, ",") != "get" ||
		strings.Join(daemonsets[0].ResourceNames, ",") != daemon {
		t.Errorf("the activation Role's DaemonSet rules are %+v, want get on %s alone", daemonsets, daemon)
	}
}

// The chart's own PostgreSQL reads the same Secret when it is set, so the
// database step works on a fresh install with it.
func TestTheBundledPostgreSQLReadsTheExistingSecret(t *testing.T) {
	out := render(t, "postgresql.existingSecret=op-db-password")
	var database appsv1.Deployment
	decodeNamed(t, out, "Deployment", "jb-concourse-jetbridge-db", &database)
	for _, env := range database.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "POSTGRES_PASSWORD" && (env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil ||
			env.ValueFrom.SecretKeyRef.Name != "op-db-password" || env.Value != "") {
			t.Errorf("the bundled PostgreSQL's POSTGRES_PASSWORD is %+v, want it from postgresql.existingSecret", env)
		}
	}
}
