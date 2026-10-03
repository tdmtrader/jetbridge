package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
// deferred: rendered by the chart's bootstrap Jobs (hangar_secret_bootstrap
// T5; the database step, hangar_activation_db_role T11) and exercised on a
// real API server by the K3s contract (hangar_secret_bootstrap T11).
type HangarBootstrapCommand struct {
	Inventory  string `long:"inventory"  required:"true" description:"Path to the bootstrap inventory JSON the chart renders."`
	Namespace  string `long:"namespace"  required:"true" description:"Namespace the inventory's Secrets live in."`
	Kubeconfig string `long:"kubeconfig" description:"Path to a kubeconfig. Empty means in-cluster configuration."`

	// The database step: create the activation database role and the Secret
	// holding its connection string. Its password comes from PGPASSWORD.
	Database         bool   `long:"database"          description:"Run the database step instead: give the activation database role its login and grants, and write its connection-string Secret."`
	PostgresHost     string `long:"postgres-host"     default:"127.0.0.1" description:"PostgreSQL host."`
	PostgresPort     int    `long:"postgres-port"     default:"5432" description:"PostgreSQL port."`
	PostgresUser     string `long:"postgres-user"     description:"A PostgreSQL user that may create roles: web's own. Its password is read from PGPASSWORD."`
	PostgresDatabase string `long:"postgres-database" default:"atc" description:"The database."`
	PostgresSSLMode  string `long:"postgres-sslmode"  default:"disable" description:"sslmode for both connections."`
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

	store := kubeSecretStore{client: clientset, namespace: cmd.Namespace}
	if cmd.Database {
		err = cmd.databaseStep(inventory, store)
	} else {
		err = bootstrap.Reconcile(context.Background(), inventory, store, logLine)
	}
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

// databaseStep runs EnsureActivationRole for the inventory's one database
// credential entry. Every connection string is keyword-form; the admin one has
// no password, which pgx reads from PGPASSWORD.
func (cmd *HangarBootstrapCommand) databaseStep(inventory bootstrap.Inventory, store bootstrap.SecretStore) error {
	var secret string
	for _, entry := range inventory.Entries {
		if entry.Kind != bootstrap.KindDatabaseCredential {
			continue
		}
		if secret != "" {
			return errors.New("the inventory declares more than one database credential")
		}
		secret = entry.Name
	}
	if secret == "" {
		return errors.New("the inventory declares no database credential; is hangarBootstrap.database.enabled set?")
	}
	if cmd.PostgresUser == "" {
		return errors.New("--postgres-user is required for the database step")
	}
	base := fmt.Sprintf("host=%s port=%d dbname=%s sslmode=%s", cmd.PostgresHost, cmd.PostgresPort, cmd.PostgresDatabase, cmd.PostgresSSLMode)
	admin, err := sql.Open("pgx", base+" user="+cmd.PostgresUser)
	if err != nil {
		return err
	}
	defer admin.Close()
	return bootstrap.EnsureActivationRole(context.Background(), bootstrap.DatabaseRole{
		Admin:  admin,
		Store:  store,
		Secret: secret,
		Labels: inventory.Labels,
		DSN: func(role, password string) string {
			return base + " user=" + role + " password=" + password
		},
		Log: logLine,
	})
}
