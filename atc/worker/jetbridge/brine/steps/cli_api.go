package steps

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/cliserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
)

type CLIAPIObservation struct {
	Status      int
	ContentType string
	Length      string
	Disposition string
	Modified    string
	Body        string
}

func CLIAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, CLIAPIObservation](
			"the real CLI download API handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (CLIAPIObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return CLIAPIObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				return observeCLIAPI(database, profile, rec)
			},
		),
		CheckInt[CLIAPIObservation]("the CLI API returned status {int}", "CLI API status", func(in CLIAPIObservation) (int, error) { return in.Status, nil }),
		CheckString[CLIAPIObservation]("the CLI API returned binary {string}", "CLI API body", func(in CLIAPIObservation) (string, error) { return in.Body, nil }),
		brine.DefineCheck[CLIAPIObservation]("the CLI API returned Unix download headers", func(in CLIAPIObservation, _ brine.Params, _ *brine.Recorder) error {
			want := CLIAPIObservation{ContentType: "application/octet-stream", Length: "11", Disposition: "attachment; filename=fly", Modified: "Mon, 03 Jun 1991 05:30:45 GMT"}
			if in.ContentType != want.ContentType || in.Length != want.Length || in.Disposition != want.Disposition || in.Modified != want.Modified {
				return fmt.Errorf("unexpected Unix headers: %#v", in)
			}
			return nil
		}),
		brine.DefineCheck[CLIAPIObservation]("the CLI API returned Windows download headers", func(in CLIAPIObservation, _ brine.Params, _ *brine.Recorder) error {
			want := CLIAPIObservation{ContentType: "application/octet-stream", Length: "25", Disposition: "attachment; filename=fly.exe", Modified: "Thu, 29 Jun 1989 05:30:44 GMT"}
			if in.ContentType != want.ContentType || in.Length != want.Length || in.Disposition != want.Disposition || in.Modified != want.Modified {
				return fmt.Errorf("unexpected Windows headers: %#v", in)
			}
			return nil
		}),
	}
}

type strictCLIAPI struct {
	URL    string
	Client *http.Client
	CLIDir string
}

func newStrictCLIAPI(database JetbridgeDB, rec *brine.Recorder) (*strictCLIAPI, error) {
	logger := lager.NewLogger("brine-cli-api")
	if _, err := database.TeamFactory.CreateTeam(atc.Team{
		Name: "cli-api-team",
		Auth: atc.TeamAuth{accessor.ViewerRole: {}},
	}); err != nil {
		return nil, fmt.Errorf("create CLI API team: %w", err)
	}

	displayUserID, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return nil, fmt.Errorf("create display user ID generator: %w", err)
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{"brine-cli-api"}),
		database.TeamFactory,
		"sub",
		[]string{"brine-system"},
		displayUserID,
	)
	buildFactory := db.NewBuildFactory(database.Conn, database.LockFactory, time.Minute, time.Minute)
	apiWrapper := wrappa.MultiWrappa{
		wrappa.NewAPIAuthWrappa(
			auth.NewCheckPipelineAccessHandlerFactory(database.TeamFactory),
			auth.NewCheckBuildReadAccessHandlerFactory(buildFactory),
			auth.NewCheckBuildWriteAccessHandlerFactory(buildFactory),
			auth.NewCheckWorkerTeamAccessHandlerFactory(database.WorkerFactory),
		),
		wrappa.NewAccessorWrappa(
			logger,
			accessFactory,
			auditor.NewAuditor(false, false, false, false, false, true, false, false, false, logger),
			map[string]string{},
		),
	}

	cliDir, err := os.MkdirTemp("", "brine-cli-api-*")
	if err != nil {
		return nil, fmt.Errorf("create CLI directory: %w", err)
	}
	rec.RegisterDisposer(func() { _ = os.RemoveAll(cliDir) })

	downloadRoute, found := atc.Routes.FindRouteByName(atc.DownloadCLI)
	if !found {
		return nil, fmt.Errorf("production CLI download route is missing")
	}
	cliServer := cliserver.NewServer(logger, cliDir)
	router, err := rata.NewRouter(
		rata.Routes{downloadRoute},
		apiWrapper.Wrap(rata.Handlers{atc.DownloadCLI: http.HandlerFunc(cliServer.Download)}),
	)
	if err != nil {
		return nil, fmt.Errorf("build production CLI router: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for CLI API: %w", err)
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		_ = server.Serve(listener)
	}()
	transport := &http.Transport{}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	rec.RegisterDisposer(func() {
		transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	})

	return &strictCLIAPI{
		URL: "http://" + listener.Addr().String(), Client: client, CLIDir: cliDir,
	}, nil
}

func observeCLIAPI(database JetbridgeDB, profile string, rec *brine.Recorder) (CLIAPIObservation, error) {
	api, err := newStrictCLIAPI(database, rec)
	if err != nil {
		return CLIAPIObservation{}, err
	}
	if err := writeCLIArchives(api.CLIDir); err != nil {
		return CLIAPIObservation{}, err
	}
	query := map[string]string{
		"darwin": "platform=darwin&arch=amd64", "windows": "platform=windows&arch=amd64",
		"Darwin": "platform=Darwin&arch=amd64", "Windows": "platform=Windows&arch=amd64",
		"path-arch": "platform=darwin&arch=../darwin/amd64", "path-platform": "platform=../etc/passwd&arch=amd64",
	}[profile]
	if query == "" {
		return CLIAPIObservation{}, fmt.Errorf("unknown CLI profile %q", profile)
	}
	req, err := http.NewRequest(http.MethodGet, api.URL+"/api/v1/cli?"+query, nil)
	if err != nil {
		return CLIAPIObservation{}, err
	}
	resp, err := api.Client.Do(req)
	if err != nil {
		return CLIAPIObservation{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CLIAPIObservation{}, err
	}
	return CLIAPIObservation{
		Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Length: resp.Header.Get("Content-Length"),
		Disposition: resp.Header.Get("Content-Disposition"), Modified: resp.Header.Get("Last-Modified"), Body: string(body),
	}, nil
}

func writeCLIArchives(dir string) error {
	unixPath := filepath.Join(dir, "fly-darwin-amd64.tgz")
	unixFile, err := os.Create(unixPath)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(unixFile)
	tw := tar.NewWriter(gz)
	skipped := []byte("skipped!")
	if err := tw.WriteHeader(&tar.Header{Name: "some-file", Mode: 0o644, Size: int64(len(skipped))}); err != nil {
		return err
	}
	if _, err := tw.Write(skipped); err != nil {
		return err
	}
	body := []byte("soi soi soi")
	if err := tw.WriteHeader(&tar.Header{Name: "fly", Mode: 0o755, Size: int64(len(body)), ModTime: time.Date(1991, time.June, 3, 5, 30, 45, 0, time.UTC)}); err != nil {
		return err
	}
	if _, err := tw.Write(body); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := unixFile.Close(); err != nil {
		return err
	}

	windowsFile, err := os.Create(filepath.Join(dir, "fly-windows-amd64.zip"))
	if err != nil {
		return err
	}
	zw := zip.NewWriter(windowsFile)
	skippedEntry, err := zw.Create("some-file")
	if err != nil {
		return err
	}
	if _, err := skippedEntry.Write(skipped); err != nil {
		return err
	}
	header := &zip.FileHeader{Name: "fly.exe", Method: zip.Deflate}
	header.SetModTime(time.Date(1989, time.June, 29, 5, 30, 44, 0, time.UTC))
	entry, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	if _, err := entry.Write([]byte("soi soi soi.notavirus.bat")); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return windowsFile.Close()
}
