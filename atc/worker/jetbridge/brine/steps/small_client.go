package steps

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
)

type SmallClientObservation struct{ Value string }

type systemAuthTransport struct {
	base http.RoundTripper
}

func (t systemAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer brine-system")
	return t.base.RoundTrip(request)
}

func SmallClientDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, SmallClientObservation](
			"the production Go client handles small surface {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (SmallClientObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return SmallClientObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeSmallClient(database, profile, rec)
				return SmallClientObservation{Value: value}, err
			},
		),
		CheckString[SmallClientObservation]("the small client result is {string}", "small client result", func(in SmallClientObservation) (string, error) { return in.Value, nil }),
	}
}

func observeSmallClient(database JetbridgeDB, profile string, rec *brine.Recorder) (string, error) {
	api, err := newPipelineAPI(database, rec)
	if err != nil {
		return "", err
	}
	client := clientapi.NewClient(api.Server.URL, api.Client, false)
	switch profile {
	case "info":
		info, err := client.GetInfo()
		return fmt.Sprintf("version=%s;worker=%s", info.Version, info.WorkerVersion), err
	case "user":
		user, err := client.UserInfo()
		return fmt.Sprintf("user=%s;admin=%t", user.DisplayUserId, user.IsAdmin), err
	case "users":
		if err := db.NewUserFactory(database.Conn).CreateOrUpdateUser("bob", "github", "bob-sub"); err != nil {
			return "", err
		}
		users, err := client.ListActiveUsersSince(time.Date(1969, 12, 30, 0, 0, 0, 0, time.UTC))
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(users))
		for _, user := range users {
			names = append(names, user.Username)
		}
		return strings.Join(names, ","), nil
	case "worker-save", "worker-list":
		transport := api.Client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		systemHTTPClient := *api.Client
		systemHTTPClient.Transport = systemAuthTransport{base: transport}
		client = clientapi.NewClient(api.Server.URL, &systemHTTPClient, false)
		worker := atc.Worker{Name: "client-worker", Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning)}
		saved, err := client.SaveWorker(worker, nil)
		if err != nil {
			return "", err
		}
		if profile == "worker-save" {
			return fmt.Sprintf("name=%s;state=%s", saved.Name, saved.State), nil
		}
		workers, err := client.ListWorkers()
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(workers))
		for _, worker := range workers {
			names = append(names, worker.Name)
		}
		return strings.Join(names, ","), nil
	case "cli":
		if err := writeCLIArchives(api.CLIDir); err != nil {
			return "", err
		}
		reader, headers, err := client.GetCLIReader("amd64", "darwin")
		if err != nil {
			return "", err
		}
		defer reader.Close()
		body, err := io.ReadAll(reader)
		return fmt.Sprintf("body=%s;length=%s", body, headers.Get("Content-Length")), err
	default:
		return "", fmt.Errorf("unknown small client profile %q", profile)
	}
}
