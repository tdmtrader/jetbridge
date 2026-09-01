package steps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
)

type WallAPIObservation struct {
	Status      int
	ContentType string
	Message     string
	TTL         time.Duration
	Err         error
}

type wallFixedClock struct{ now time.Time }

func (c *wallFixedClock) Now() time.Time                  { return c.now }
func (c *wallFixedClock) Until(t time.Time) time.Duration { return t.Sub(c.now) }

func WallAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, WallAPIObservation](
			"the real wall API handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (WallAPIObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return WallAPIObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				return observeWallAPI(database, profile, rec)
			},
		),
		CheckInt[WallAPIObservation]("the wall API returned status {int}", "wall API status", func(in WallAPIObservation) (int, error) { return in.Status, nil }),
		CheckString[WallAPIObservation]("the wall API content type is {string}", "wall API content type", func(in WallAPIObservation) (string, error) { return in.ContentType, nil }),
		CheckString[WallAPIObservation]("the stored wall message is {string}", "wall message", func(in WallAPIObservation) (string, error) { return in.Message, nil }),
		brine.DefineCheck[WallAPIObservation]("the wall TTL is close to one minute", func(in WallAPIObservation, _ brine.Params, _ *brine.Recorder) error {
			if in.TTL < 59*time.Second || in.TTL > time.Minute {
				return fmt.Errorf("expected TTL close to one minute, got %s", in.TTL)
			}
			return nil
		}),
		brine.DefineCheck[WallAPIObservation]("the wall client returned no error", func(in WallAPIObservation, _ brine.Params, _ *brine.Recorder) error { return in.Err }),
	}
}

func observeWallAPI(database JetbridgeDB, profile string, rec *brine.Recorder) (WallAPIObservation, error) {
	api, err := newPipelineAPI(database, rec)
	if err != nil {
		return WallAPIObservation{}, err
	}
	client := clientapi.NewClient(api.Server.URL, api.Client, false)
	switch profile {
	case "get-message", "get-expiring":
		wall := atc.Wall{Message: "test message"}
		if profile == "get-expiring" {
			wall.TTL = time.Minute
		}
		if err := client.SetWall(wall); err != nil {
			return WallAPIObservation{}, err
		}
		if err := api.request(http.MethodGet, "/api/v1/wall", nil); err != nil {
			return WallAPIObservation{}, err
		}
		var returned atc.Wall
		if err := json.Unmarshal(api.Body, &returned); err != nil {
			return WallAPIObservation{}, err
		}
		return WallAPIObservation{Status: api.Status, ContentType: api.ContentType, Message: returned.Message, TTL: returned.TTL}, nil
	case "client-get":
		if err := client.SetWall(atc.Wall{Message: "test message", TTL: time.Minute}); err != nil {
			return WallAPIObservation{}, err
		}
		wall, err := client.GetWall()
		return WallAPIObservation{Message: wall.Message, TTL: wall.TTL, Err: err}, nil
	case "client-set":
		err := client.SetWall(atc.Wall{Message: "set message", TTL: time.Minute})
		if err != nil {
			return WallAPIObservation{Err: err}, nil
		}
		wall, getErr := client.GetWall()
		return WallAPIObservation{Message: wall.Message, TTL: wall.TTL, Err: getErr}, nil
	case "invalid-empty":
		payload, _ := json.Marshal(atc.Wall{TTL: time.Minute})
		if err := api.request(http.MethodPut, "/api/v1/wall", payload); err != nil {
			return WallAPIObservation{}, err
		}
		wall, getErr := client.GetWall()
		return WallAPIObservation{Status: api.Status, Message: wall.Message, Err: getErr}, nil
	case "client-clear":
		if err := client.SetWall(atc.Wall{Message: "to be cleared"}); err != nil {
			return WallAPIObservation{}, err
		}
		if err := client.ClearWall(); err != nil {
			return WallAPIObservation{Err: err}, nil
		}
		wall, getErr := client.GetWall()
		return WallAPIObservation{Message: wall.Message, Err: getErr}, nil
	case "db-expired":
		clock := &wallFixedClock{now: time.Now().Truncate(time.Microsecond)}
		wallDB := db.NewWall(database.Conn, clock)
		if err := wallDB.SetWall(atc.Wall{Message: "expires", TTL: time.Minute}); err != nil {
			return WallAPIObservation{}, err
		}
		clock.now = clock.now.Add(time.Hour)
		wall, getErr := wallDB.GetWall()
		return WallAPIObservation{Message: wall.Message, Err: getErr}, nil
	case "db-last":
		clock := &wallFixedClock{now: time.Now().Truncate(time.Microsecond)}
		wallDB := db.NewWall(database.Conn, clock)
		for _, message := range []string{"first", "second", "third"} {
			if err := wallDB.SetWall(atc.Wall{Message: message}); err != nil {
				return WallAPIObservation{}, err
			}
		}
		wall, getErr := wallDB.GetWall()
		return WallAPIObservation{Message: wall.Message, Err: getErr}, nil
	default:
		return WallAPIObservation{}, fmt.Errorf("unknown wall API profile %q", profile)
	}
}
