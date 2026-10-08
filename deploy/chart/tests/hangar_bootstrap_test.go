package tests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
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
// strict inputs and the output plane on the disk store.
var bootstrapSets = []string{
	"hangarBootstrap.enabled=true",
	"postgresql.existingSecret=op-db-password",
	"hangarOutput.executionControl.enabled=true",
	"hangarOutput.capabilityKeySecret=op-capability-key",
	"artifactDaemon.outputScratch.sizeLimit=32Gi",
	"hangarOutput.enabled=true",
	"hangarOutput.webEnabled=true",
	"hangarOutput.tenant=tenant-a",
	"hangarOutput.bucket=outputs",
	"hangarOutput.materializationKeySecret=op-output-materialize",
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
	if strings.Contains(out, "hangar-warrant-key") {
		t.Error("the default render mounts a bootstrap Secret")
	}
}

// The runbook's bootstrap step turns the bootstrap on and names its Secrets
// while every feature is still off.
func TestTheBootstrapRendersWithEveryFeatureOff(t *testing.T) {
	out := render(t,
		"hangarBootstrap.enabled=true",
		"hangarOutput.capabilityKeySecret=op-capability-key",
		"hangarOutput.materializationKeySecret=op-output-materialize",
		"hangarStorage.disk.tls.existingSecret=storage-tls",
		"hangarStorage.disk.credentials.existingSecret=storage-credentials",
		"artifactDaemon.hangar.keySecret=op-hangar-key",
	)
	inv := renderedInventory(t, out)
	if len(inv.Entries) < 5 {
		t.Errorf("the inventory has %d entries, want every named Secret", len(inv.Entries))
	}
}

// The rendered inventory is one the bootstrap accepts: it decodes strictly and
// a reconcile over an empty store creates every entry. There is no database
// credential entry any more: the activation database role went with the
// activation walk.
func TestTheRenderedInventoryReconciles(t *testing.T) {
	inv := renderedInventory(t, render(t, bootstrapSets...))
	store := &chartTestStore{secrets: map[string]bootstrap.Secret{}}
	if err := bootstrap.Reconcile(context.Background(), inv, store, nil); err != nil {
		t.Fatalf("the bootstrap refused the chart's own inventory: %v", err)
	}
	for _, entry := range inv.Entries {
		if _, created := store.secrets[entry.Name]; !created {
			t.Errorf("the reconcile did not create %s", entry.Name)
		}
		if entry.Kind == "dsn" {
			t.Errorf("the inventory still declares the activation database credential %s", entry.Name)
		}
	}
}

// The web reclaims and sweeps the disk store's output namespace, so it is a
// consumer of the store's CA and tokens -- and the controllers that used to be
// are not.
func TestTheWebConsumesTheDiskStoreSecrets(t *testing.T) {
	inv := renderedInventory(t, render(t, bootstrapSets...))
	found := 0
	for _, entry := range inv.Entries {
		if entry.Name != "storage-tls" && entry.Name != "storage-credentials" {
			continue
		}
		found++
		consumers := append([]string{}, entry.Consumers...)
		sort.Strings(consumers)
		if strings.Join(consumers, ",") != "artifact-daemon,hangar-store,web" {
			t.Errorf("%s lists consumers %v, want the artifact daemon, the store and web", entry.Name, consumers)
		}
	}
	if found != 2 {
		t.Fatalf("found %d disk store entries in the inventory, want the CA bundle and the tokens", found)
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

// The inventory holds CAs, leaves, bundles, symmetric keys and tokens, and
// nothing that signs: the daemon is trusted over mTLS, so there is no node
// control key to mint, no verification ring to compose, and no earlier
// control-key generation to keep. The bootstrap creates every entry.
func TestTheInventoryHoldsNoSigningKeyAndNoRing(t *testing.T) {
	out := render(t, bootstrapSets...)
	inv := renderedInventory(t, out)
	store := &chartTestStore{secrets: map[string]bootstrap.Secret{}}
	if err := bootstrap.Reconcile(context.Background(), inv, store, nil); err != nil {
		t.Fatalf("the bootstrap refused the chart's own inventory: %v", err)
	}
	for _, entry := range inv.Entries {
		if !slices.Contains([]string{"random32", "tls-bundle", "store-tokens"}, string(entry.Kind)) {
			t.Errorf("inventory entry %s is of kind %q; the inventory holds CAs, leaves, bundles, symmetric keys and tokens only", entry.Name, entry.Kind)
		}
		for file := range store.secrets[entry.Name].Data {
			if file == "control.key" || file == "control-keys.json" {
				t.Errorf("the bootstrap created %s in %s; nothing signs and nothing verifies", file, entry.Name)
			}
		}
	}
	for _, doc := range documentsIn(t, out) {
		if strings.HasSuffix(doc.name, "-hangar-output-control-keys") || strings.Contains(doc.name, "-hangar-rings-") {
			t.Errorf("%s %s renders a verification ring", doc.kind, doc.name)
		}
	}
	if web := webDeployment(t, out); strings.Contains(web, "control-keys") || strings.Contains(web, "hangar-rings-") {
		t.Error("web mounts or reads a verification ring")
	}
}

// Earlier control-key generations have nothing to be kept for: the value is
// refused naming its removal, not ignored.
func TestReferencedKeysAreRefused(t *testing.T) {
	msg := renderHangarError(t, append(append([]string{}, bootstrapSets...),
		"hangarBootstrap.referencedKeys[0].epoch=1",
		"hangarBootstrap.referencedKeys[0].controlSecret=op-control-key-e0")...)
	if !strings.Contains(msg, "hangarBootstrap.referencedKeys has been removed") {
		t.Errorf("a referencedKeys entry rendered or failed for another reason: %s", firstLines(msg, 3))
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

// The bootstrap renders one Job, its Sync hook, and nothing runs after it: the
// activation walk Job and the PostSync database step went with the activation
// command and its database role.
func TestTheBootstrapRendersOneJobAndNoActivationStep(t *testing.T) {
	out := render(t, bootstrapSets...)

	var jobs []string
	for _, doc := range documentsIn(t, out) {
		if doc.kind == "Job" {
			jobs = append(jobs, doc.name)
		}
		if strings.Contains(doc.body, "hangar-output-activate") || strings.Contains(doc.body, "PostSync") {
			t.Errorf("%s %s still renders an activation step", doc.kind, doc.name)
		}
	}
	if len(jobs) != 1 || jobs[0] != bootstrapName {
		t.Errorf("the render holds Jobs %v, want exactly %s", jobs, bootstrapName)
	}
	var job batchv1.Job
	decodeNamed(t, out, "Job", bootstrapName, &job)
	if args := job.Spec.Template.Spec.Containers[0].Args; slices.Contains(args, "--database") {
		t.Errorf("the bootstrap Job passes --database, which the command no longer has: %v", args)
	}
}

// The chart's own PostgreSQL reads the same Secret when it is set, so web and
// the database agree on a fresh install with it.
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
