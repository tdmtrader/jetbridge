package steps

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"k8s.io/client-go/kubernetes"
)

func RunResultDownloadDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[RunResultPublication, RunResultPublication]("a fresh {string} client downloads the named Run result", []string{"auth-server", "real-cluster"}, func(in RunResultPublication, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunResultPublication, error) {
			caller, _ := p.GetString(0)
			auth, err := authServer(res)
			if err != nil {
				return in, err
			}
			if err := configureRunDownload(in, auth, rec, res); err != nil {
				return in, err
			}
			team, found, err := in.Start.DB.TeamFactory.FindTeam("output-start")
			if err != nil || !found {
				return in, fmt.Errorf("load result team (found %t): %w", found, err)
			}
			if err = team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:owner"}}, "viewer": {"users": {"local:viewer"}}}); err != nil {
				return in, err
			}
			template, found, err := team.Pipeline(atc.PipelineRef{Name: "review"})
			if err != nil || !found {
				return in, fmt.Errorf("load template (found %t): %w", found, err)
			}
			if err = template.Expose(); err != nil {
				return in, err
			}
			client := RunCancellationAPI{Start: in.Start, Auth: auth}
			if caller != "anonymous" {
				user := caller
				if caller == "cross-team" || caller == "unknown" {
					user = "owner"
				}
				if caller == "cross-team" {
					if err = team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:someone-else"}}}); err != nil {
						return in, err
					}
				}
				if _, err = auth.fly("login", "-c", auth.URL, "-n", "auth-team", "-u", user, "-p", authPassword); err != nil {
					return in, err
				}
				client.Token, err = auth.savedFlyToken()
				if err != nil {
					return in, err
				}
			}
			factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
			terminal, complete, err := factory.TerminalResult(context.Background(), in.Start.Creation.Run.ID())
			if err != nil {
				return in, err
			}
			name := "review"
			for key := range terminal.Results {
				name = key
				break
			}
			if caller == "unknown" {
				name = "not-a-result"
			}
			client, err = client.request(http.MethodGet, fmt.Sprintf("/api/v1/teams/output-start/pipelines/review/runs/%d/results/%s", in.Start.Creation.Run.Number(), name), nil)
			if err != nil {
				return in, err
			}
			want := http.StatusOK
			switch {
			case caller == "anonymous":
				want = http.StatusUnauthorized
			case caller == "cross-team":
				want = http.StatusForbidden
			case !complete:
				want = http.StatusConflict
			case caller == "unknown":
				want = http.StatusNotFound
			}
			if client.Status != want {
				return in, fmt.Errorf("%s result download returned HTTP %d, want %d", caller, client.Status, want)
			}
			if want != http.StatusOK {
				return in, nil
			}
			scratch, err := AttributedTempDir("brine-run-download-")
			if err != nil {
				return in, err
			}
			defer os.RemoveAll(scratch)
			tree, err := (hangar.Canonicalizer{TempDir: scratch}).Capture(context.Background(), bytes.NewReader(client.Body))
			if err != nil {
				return in, err
			}
			defer tree.Close()
			if tree.Digest != terminal.Results[name].Ref.Digest {
				return in, fmt.Errorf("download differs from the committed named result")
			}
			return in, nil
		}),
	}
}

// Compose the production read admission, node transport and signed lease
// callback with the same PostgreSQL database used by the authenticated API.
func configureRunDownload(in RunResultPublication, auth *AuthFixture, rec *brine.Recorder, res brine.Resources) error {
	if auth.ResultReader != nil {
		return nil
	}
	source, signer, _, err := configureRunReadPlane(in, rec, res)
	if err != nil {
		return err
	}

	reader := &runs.ResultReader{Conn: in.Start.DB.Conn, Minter: signer, Source: func(ctx context.Context, epoch executioncontrol.ActivationEpoch) (runs.ResultSource, error) {
		return source.ForResultRead(ctx, epoch)
	}}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	auth.ResultReader = reader
	auth.API, err = auth.apiHandler(auth.Verifier)
	return err
}

// The same real control plane and node transport serve downloads and task inputs.
func configureRunReadPlane(in RunResultPublication, rec *brine.Recorder, res brine.Resources) (*jetbridge.OutputSource, *output.ReadWarrantSigner, jetbridge.Config, error) {
	cluster, err := getRealCluster(res)
	if err != nil {
		return nil, nil, jetbridge.Config{}, err
	}
	return configureRunReadPlaneForClient(in, rec, cluster.Clientset, jetbridge.NewConfig("default", ""))
}

func configureRunReadPlaneForClient(in RunResultPublication, rec *brine.Recorder, client kubernetes.Interface, config jetbridge.Config) (*jetbridge.OutputSource, *output.ReadWarrantSigner, jetbridge.Config, error) {
	daemon := in.Start.Daemon
	clock := output.ClockFunc(func() time.Time { return time.Now().UTC() })
	signer, err := output.NewReadWarrantSigner(brineReadWarrantKey)
	if err != nil {
		return nil, nil, jetbridge.Config{}, err
	}
	verifier, err := output.NewReadWarrantVerifier(brineReadWarrantKey, clock)
	if err != nil {
		return nil, nil, jetbridge.Config{}, err
	}
	control := &hangaroutput.LeaseControl{Transactor: brineTransactor{conn: in.Start.DB.Conn}, Leases: db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()), Warrants: verifier, Minter: signer, Clock: clock}
	server := httptest.NewUnstartedServer(control.Handler())
	cert, err := tls.LoadX509KeyPair(filepath.Join(daemon.CertDir, "server.crt"), filepath.Join(daemon.CertDir, "server.key"))
	if err != nil {
		return nil, nil, jetbridge.Config{}, err
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	TrackDisposer(rec, "the result-download TLS server", func() error { server.Close(); return nil })
	if err = daemon.Output.crash(); err != nil {
		return nil, nil, jetbridge.Config{}, err
	}
	daemon.Output.cmd.Args = append(daemon.Output.cmd.Args, "--read-control-url", server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = daemon.Output.restart(ctx, daemon.HTTP); err != nil {
		return nil, nil, jetbridge.Config{}, err
	}
	config.OutputDaemonPort, err = hangarDaemonPort(daemon.Output.URL)
	if err != nil {
		return nil, nil, jetbridge.Config{}, err
	}
	config.OutputDaemonTLSCert = filepath.Join(daemon.CertDir, "client.crt")
	config.OutputDaemonTLSKey = filepath.Join(daemon.CertDir, "client.key")
	config.OutputDaemonTLSCACert = filepath.Join(daemon.CertDir, "ca.crt")
	config.OutputDaemonTLSServerName = "artifact-daemon"
	source := jetbridge.NewOutputSource(client, config, daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	control.Keys = &hangaroutput.ReadNodeKeys{Nodes: source, Membership: db.OutputNodeKeys{Conn: in.Start.DB.Conn}, Ring: hangaroutput.ControlKeyRing{
		ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(daemon.ControlPublic)}}}}

	return source, signer, config, nil
}
