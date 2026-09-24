package main

import (
	"flag"
	"os"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

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
	DeleteTimeout   time.Duration
	Grace           time.Duration
	Batch           int
}

func (config *controllerConfig) bind(flags *flag.FlagSet) {
	flags.StringVar(&config.DSN, "database", "",
		"PostgreSQL connection string. The controller reads and writes the output plane's tables; it never migrates them.")
	flags.StringVar(&config.Endpoint, "output-endpoint", "",
		"Object-store endpoint. Empty means real GCS with ambient credentials.")
	flags.StringVar(&config.Store, "output-store", output.StoreGCS,
		"Object-store profile. Supported profiles: gcs and disk.")
	flags.StringVar(&config.StoreID, "output-store-id", "", "Expected persistent disk storage identity.")
	flags.StringVar(&config.TokenFile, "output-token-file", "", "Disk storage role credential file.")
	flags.StringVar(&config.CACert, "output-ca-cert", "", "Disk storage CA certificate.")
	flags.StringVar(&config.Bucket, "output-bucket", "",
		"The dedicated output bucket. It is never the durable cache bucket or the strict-input bucket.")
	flags.StringVar(&config.Prefix, "output-prefix", "",
		"Deployment prefix. Derived namespaces hang off it; no caller may choose one.")
	flags.StringVar(&config.Tenant, "output-tenant", "",
		"Opaque tenant identity the scope is derived from.")
	flags.Int64Var(&config.ActivationEpoch, "activation-epoch", 0,
		"The activation epoch this controller speaks for.")
	flags.DurationVar(&config.Interval, "interval", output.WorkerFallbackInterval,
		"Periodic wake, bounded at one minute.")
	flags.DurationVar(&config.DeleteTimeout, "delete-timeout", 2*time.Minute,
		"How long one conditional delete may take. The lease term is derived from it, and work begins only with the timeout plus two minutes of lease remaining.")
	flags.DurationVar(&config.Grace, "publication-grace", output.DefaultPublicationGrace,
		"Elapsed publication grace is one of reclaim admission's preconditions. Startup refuses a value that does not exceed the maximum capture deadline by an hour.")
	flags.IntVar(&config.Batch, "batch", 10,
		"How many jobs one pass may admit or advance. A pass that drained its whole backlog would hold every other operation behind its slowest item.")
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
