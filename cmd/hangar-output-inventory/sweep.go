package main

import (
	"flag"
	"os"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// controllerConfig is what an output-plane controller is told.
//
// The bucket, prefix and tenant are configuration and never a request field:
// the namespace is derived from authenticated server configuration plus the
// active epoch, and no caller anywhere in this plane can choose one.
type controllerConfig struct {
	DSN             string
	Endpoint        string
	Store           string
	StoreID         string
	TokenFile       string
	CACert          string
	Bucket          string
	Prefix          string
	Tenant          string
	ActivationEpoch int64
	Interval        time.Duration
	Grace           time.Duration
}

func (config *controllerConfig) bind(flags *flag.FlagSet) {
	flags.StringVar(&config.DSN, "database", "",
		"PostgreSQL connection string. The controller reads and writes the output plane's tables; it never migrates them, because the activation epoch is what attests compatible migrations.")
	flags.StringVar(&config.Endpoint, "output-endpoint", "",
		"Object-store endpoint. Empty means real GCS with ambient credentials; a value is the emulator profile used by CI and the conformance tier.")
	flags.StringVar(&config.Store, "output-store", output.StoreGCS,
		"Object-store profile. Supported profiles: gcs and disk.")
	flags.StringVar(&config.StoreID, "output-store-id", "", "Expected persistent disk storage identity.")
	flags.StringVar(&config.TokenFile, "output-token-file", "", "Disk storage role credential file.")
	flags.StringVar(&config.CACert, "output-ca-cert", "", "Disk storage CA certificate.")
	flags.StringVar(&config.Bucket, "output-bucket", "",
		"The dedicated output bucket. It is never the durable cache bucket or the caller-published strict-input bucket.")
	flags.StringVar(&config.Prefix, "output-prefix", "",
		"Deployment prefix. Derived namespaces hang off it; no caller may choose one.")
	flags.StringVar(&config.Tenant, "output-tenant", "",
		"Opaque tenant identity the scope is derived from.")
	flags.Int64Var(&config.ActivationEpoch, "activation-epoch", 0,
		"The activation epoch this controller speaks for. A stale epoch authorizes nothing.")
	flags.DurationVar(&config.Interval, "interval", output.WorkerFallbackInterval,
		"Periodic wake. Bounded at one minute: a worker that woke only on NOTIFY would be silenced by one missed notification until it restarted.")
	flags.DurationVar(&config.Grace, "publication-grace", output.DefaultPublicationGrace,
		"How long a marked, unregistered object is left alone before it may be treated as an orphan. Startup refuses a value that does not exceed the maximum capture deadline by an hour.")
}

func (config controllerConfig) namespace() (output.OutputNamespace, error) {
	return output.DeriveNamespace(output.NamespaceConfig{
		Store:            config.Store,
		StoreID:          config.StoreID,
		Bucket:           config.Bucket,
		DeploymentPrefix: config.Prefix,
		TenantID:         config.Tenant,
		ActivationEpoch:  executioncontrol.ActivationEpoch(config.ActivationEpoch),
	})
}

// dsnEnvironmentVariable is where this command reads its PostgreSQL connection
// string when --database is not given, and is how the chart supplies it.
//
// Not `--database=$(HANGAR_OUTPUT_DSN)`: the kubelet expands $(VAR) in args, so
// the credential lands in /proc/<pid>/cmdline, which is world-readable inside
// the container. /proc/<pid>/environ is readable only by the process's own uid.
// The flag stays, because a developer running this by hand has no environment
// set up for it and a flag is the honest way to say so.
const dsnEnvironmentVariable = "HANGAR_OUTPUT_DSN"

// resolveDSN fills the connection string from the environment when the flag
// left it empty.
func resolveDSN(dsn string) string {
	if dsn != "" {
		return dsn
	}

	return os.Getenv(dsnEnvironmentVariable)
}
