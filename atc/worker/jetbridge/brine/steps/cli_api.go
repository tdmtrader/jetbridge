package steps

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
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

func observeCLIAPI(database JetbridgeDB, profile string, rec *brine.Recorder) (CLIAPIObservation, error) {
	api, err := newPipelineAPI(database, rec)
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
	if err := api.request(http.MethodGet, "/api/v1/cli?"+query, nil); err != nil {
		return CLIAPIObservation{}, err
	}
	req, _ := http.NewRequest(http.MethodGet, api.Server.URL+"/api/v1/cli?"+query, nil)
	resp, err := api.Client.Do(req)
	if err != nil {
		return CLIAPIObservation{}, err
	}
	resp.Body.Close()
	return CLIAPIObservation{
		Status: api.Status, ContentType: resp.Header.Get("Content-Type"), Length: resp.Header.Get("Content-Length"),
		Disposition: resp.Header.Get("Content-Disposition"), Modified: resp.Header.Get("Last-Modified"), Body: string(api.Body),
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
