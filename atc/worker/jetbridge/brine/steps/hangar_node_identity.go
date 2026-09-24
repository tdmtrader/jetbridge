package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func HangarNodeIdentityDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[RunOutputRuntime, RunOutputRuntime]("its output daemon restarts with {string} Kubernetes identity", []string{"real-cluster"}, func(in RunOutputRuntime, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunOutputRuntime, error) {
			mode, _ := p.GetString(0)
			cluster, err := getRealCluster(res)
			if err != nil {
				return in, err
			}
			config := cluster.env.Config
			home, err := AttributedTempDir("brine-node-kubeconfig-")
			if err != nil {
				return in, err
			}
			TrackDisposer(rec, "the node identity kubeconfig directory", func() error { return os.RemoveAll(home) })
			path := filepath.Join(home, "config")
			kube := clientcmdapi.Config{Clusters: map[string]*clientcmdapi.Cluster{"test": {Server: config.Host, CertificateAuthorityData: config.CAData}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {ClientCertificateData: config.CertData, ClientKeyData: config.KeyData}}, Contexts: map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}}, CurrentContext: "test"}
			if err = clientcmd.WriteToFile(kube, path); err != nil {
				return in, err
			}
			daemon := in.Start.Daemon.Output
			if err = daemon.crash(); err != nil {
				return in, err
			}
			var args []string
			for i := 0; i < len(daemon.cmd.Args); i++ {
				if daemon.cmd.Args[i] == "--node-uid" {
					i++
					continue
				}
				args = append(args, daemon.cmd.Args[i])
			}
			daemon.cmd.Args = append(args, "--node-name", in.Node.Name, "--kubeconfig", path)
			if mode == "mismatched" {
				daemon.cmd.Args = append(daemon.cmd.Args, "--node-uid", freshUUID())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			in.Err = daemon.restart(ctx, in.Start.Daemon.HTTP)
			if mode == "derived" {
				return in, in.Err
			}
			return in, nil
		}),
		CheckThat[RunOutputRuntime]("its output daemon refuses the wrong Kubernetes identity", func(in RunOutputRuntime) error {
			if in.Err == nil {
				return fmt.Errorf("daemon accepted an identity different from the real Node")
			}
			return nil
		}),
	}
}
