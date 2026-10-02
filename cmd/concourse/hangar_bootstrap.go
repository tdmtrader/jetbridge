package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/concourse/concourse/hangar/bootstrap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// HangarBootstrapCommand creates the absent Secrets of the chart's bootstrap
// inventory and never changes an existing one. The chart runs it as a Job at
// the start of every Argo sync.
//
// deferred: rendered by the chart's bootstrap Job (hangar_secret_bootstrap
// T5) and exercised on a real API server by its K3s contract (T11).
type HangarBootstrapCommand struct {
	Inventory  string `long:"inventory"  required:"true" description:"Path to the bootstrap inventory JSON the chart renders."`
	Namespace  string `long:"namespace"  required:"true" description:"Namespace the inventory's Secrets live in."`
	Kubeconfig string `long:"kubeconfig" description:"Path to a kubeconfig. Empty means in-cluster configuration."`
}

func (cmd *HangarBootstrapCommand) Execute(args []string) error {
	body, err := os.ReadFile(cmd.Inventory)
	if err != nil {
		return fmt.Errorf("read the bootstrap inventory: %w", err)
	}
	var inventory bootstrap.Inventory
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return fmt.Errorf("decode the bootstrap inventory: %w", err)
	}

	config, err := kubeConfig(cmd.Kubeconfig)
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("build a Kubernetes client: %w", err)
	}

	err = bootstrap.Reconcile(context.Background(), inventory, kubeSecretStore{client: clientset, namespace: cmd.Namespace}, logLine)
	if err != nil {
		// The Job's termination message falls back to its last log lines, so
		// the named cause is what Argo shows.
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}

func kubeConfig(path string) (*rest.Config, error) {
	if path == "" {
		config, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster configuration: %w", err)
		}
		return config, nil
	}
	config, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", path, err)
	}
	return config, nil
}

// logLine writes one event per line. The bootstrap passes it names, kinds and
// public fingerprints only.
func logLine(event string, fields map[string]string) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var line strings.Builder
	line.WriteString("hangar-bootstrap: " + event)
	for _, key := range keys {
		fmt.Fprintf(&line, " %s=%s", key, fields[key])
	}
	fmt.Println(line.String())
}

// kubeSecretStore is the bootstrap's whole reach into the API server: get one
// Secret by name, create one. Nothing here lists, updates or deletes.
type kubeSecretStore struct {
	client    kubernetes.Interface
	namespace string
}

func (store kubeSecretStore) Get(ctx context.Context, name string) (bootstrap.Secret, bool, error) {
	secret, err := store.client.CoreV1().Secrets(store.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return bootstrap.Secret{}, false, nil
	}
	if err != nil {
		return bootstrap.Secret{}, false, err
	}
	return bootstrap.Secret{Name: secret.Name, Type: string(secret.Type), Labels: secret.Labels, Data: secret.Data}, true, nil
}

func (store kubeSecretStore) Create(ctx context.Context, secret bootstrap.Secret) error {
	_, err := store.client.CoreV1().Secrets(store.namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secret.Name, Namespace: store.namespace, Labels: secret.Labels},
		Type:       corev1.SecretType(secret.Type),
		Data:       secret.Data,
		Immutable:  boolPointer(true),
	}, metav1.CreateOptions{})
	return err
}

func boolPointer(value bool) *bool { return &value }
