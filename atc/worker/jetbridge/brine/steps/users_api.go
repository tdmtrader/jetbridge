package steps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type UsersAPIObservation struct {
	Status      int
	ContentType string
	Names       []string
	Body        string
}

func UsersAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, UsersAPIObservation](
			"the real users API handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (UsersAPIObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return UsersAPIObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				return observeUsersAPI(database, profile, rec)
			},
		),
		CheckInt[UsersAPIObservation]("the users API returned status {int}", "users API status", func(in UsersAPIObservation) (int, error) { return in.Status, nil }),
		CheckString[UsersAPIObservation]("the users API content type is {string}", "users API content type", func(in UsersAPIObservation) (string, error) { return in.ContentType, nil }),
		brine.DefineCheck[UsersAPIObservation]("the users API returned users {string}", func(in UsersAPIObservation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if strings.Join(in.Names, ",") != want {
				return fmt.Errorf("expected users %q, got %q", want, strings.Join(in.Names, ","))
			}
			return nil
		}),
		brine.DefineCheck[UsersAPIObservation]("the users response contains {string}", func(in UsersAPIObservation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if !strings.Contains(in.Body, want) {
				return fmt.Errorf("users response %q does not contain %q", in.Body, want)
			}
			return nil
		}),
	}
}

func observeUsersAPI(database JetbridgeDB, profile string, rec *brine.Recorder) (UsersAPIObservation, error) {
	api, err := newPipelineAPI(database, rec)
	if err != nil {
		return UsersAPIObservation{}, err
	}
	path := "/api/v1/users"
	if profile == "current" {
		path = "/api/v1/user"
	} else {
		factory := db.NewUserFactory(database.Conn)
		if profile == "list-user" || profile == "since-past" || profile == "since-future" {
			if err := factory.CreateOrUpdateUser("bob", "github", "bob-sub"); err != nil {
				return UsersAPIObservation{}, err
			}
		}
		switch profile {
		case "since-past":
			path += "?since=1969-12-30"
		case "since-future":
			path += "?since=" + url.QueryEscape(time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02"))
		case "since-invalid":
			path += "?since=1969-14-30"
		case "since-empty":
			path += "?since="
		}
	}
	if err := api.request(http.MethodGet, path, nil); err != nil {
		return UsersAPIObservation{}, err
	}
	observation := UsersAPIObservation{Status: api.Status, ContentType: api.ContentType, Body: string(api.Body)}
	if profile != "current" && api.Status == http.StatusOK {
		var users []atc.User
		if err := json.Unmarshal(api.Body, &users); err != nil {
			return observation, err
		}
		for _, user := range users {
			observation.Names = append(observation.Names, user.Username)
		}
		sort.Strings(observation.Names)
	}
	return observation, nil
}
