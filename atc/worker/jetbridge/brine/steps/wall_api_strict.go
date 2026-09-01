package steps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/wallserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
)

const (
	wallStrictAudience  = "brine-wall-audience"
	wallStrictConnector = "brine-wall-connector"
)

type WallStrictObservation struct {
	Status      int
	ContentType string
	Body        string
	Message     string
	TTL         time.Duration
}

func WallAPIStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, WallStrictObservation](
			"the production wall API handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (WallStrictObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return WallStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, err := paramAt("the production wall API handles profile {string}", p, 0)
				if err != nil {
					return WallStrictObservation{}, err
				}
				return observeStrictWallAPI(database, profile)
			},
		),
		CheckInt[WallStrictObservation]("the wall API returned status {int}", "wall API status",
			func(in WallStrictObservation) (int, error) { return in.Status, nil }),
		CheckString[WallStrictObservation]("the wall API content type is {string}", "wall API content type",
			func(in WallStrictObservation) (string, error) { return in.ContentType, nil }),
		CheckString[WallStrictObservation]("the wall API returned the exact body {string}", "wall API body",
			func(in WallStrictObservation) (string, error) { return in.Body, nil }),
		CheckString[WallStrictObservation]("the persisted wall message is {string}", "persisted wall message",
			func(in WallStrictObservation) (string, error) { return in.Message, nil }),
		brine.DefineCheck[WallStrictObservation]("the returned wall document contains the permanent message only", func(in WallStrictObservation, _ brine.Params, _ *brine.Recorder) error {
			if in.Body != "{\"message\":\"test message\"}\n" || in.Message != "test message" || in.TTL != 0 {
				return fmt.Errorf("permanent wall: body=%q message=%q ttl=%s", in.Body, in.Message, in.TTL)
			}
			return nil
		}),
		brine.DefineCheck[WallStrictObservation]("the returned wall document contains the expiring message and a bounded TTL", func(in WallStrictObservation, _ brine.Params, _ *brine.Recorder) error {
			if in.Message != "test message" || in.TTL < 59*time.Second || in.TTL > time.Minute {
				return fmt.Errorf("expiring wall: message=%q ttl=%s body=%q", in.Message, in.TTL, in.Body)
			}
			return nil
		}),
		brine.DefineCheck[WallStrictObservation]("the persisted wall has a bounded one minute TTL", func(in WallStrictObservation, _ brine.Params, _ *brine.Recorder) error {
			if in.Message != "test message" || in.TTL < 59*time.Second || in.TTL > time.Minute {
				return fmt.Errorf("persisted wall: message=%q ttl=%s", in.Message, in.TTL)
			}
			return nil
		}),
	}
}

func observeStrictWallAPI(database JetbridgeDB, profile string) (WallStrictObservation, error) {
	method, action, err := strictWallAPIRequest(profile)
	if err != nil {
		return WallStrictObservation{}, err
	}

	logger := lager.NewLogger("brine-wall-api")
	clock := db.NewClock()
	wallDB := db.NewWall(database.Conn, &clock)
	wallHandler := wallserver.NewServer(wallDB, logger)
	var endpoint http.Handler
	switch action {
	case atc.GetWall:
		endpoint = auth.CheckAuthenticationIfProvidedHandler(http.HandlerFunc(wallHandler.GetWall), auth.UnauthorizedRejector{})
	case atc.SetWall:
		endpoint = auth.CheckAdminHandler(http.HandlerFunc(wallHandler.SetWall), auth.UnauthorizedRejector{})
	case atc.ClearWall:
		endpoint = auth.CheckAdminHandler(http.HandlerFunc(wallHandler.ClearWall), auth.UnauthorizedRejector{})
	}

	authorization, err := prepareStrictWallIdentity(database, strictWallIdentity(profile))
	if err != nil {
		return WallStrictObservation{}, err
	}
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return WallStrictObservation{}, err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{wallStrictAudience}),
		database.TeamFactory, "sub", []string{"brine-system"}, display,
	)
	aud := auditor.NewAuditor(true, true, true, true, true, true, true, true, true, logger)
	handler := accessor.NewHandler(logger, action, endpoint, accessFactory, aud, map[string]string{})
	router, err := strictWallAPIRouter(action, handler)
	if err != nil {
		return WallStrictObservation{}, err
	}

	if method == http.MethodGet {
		wall := atc.Wall{Message: "test message"}
		if profile == "get-expiring-document" {
			wall.TTL = time.Minute
		}
		if err := wallDB.SetWall(wall); err != nil {
			return WallStrictObservation{}, fmt.Errorf("persist wall before GET: %w", err)
		}
	}
	if method == http.MethodDelete {
		if err := wallDB.SetWall(atc.Wall{Message: "to be cleared"}); err != nil {
			return WallStrictObservation{}, fmt.Errorf("persist wall before DELETE: %w", err)
		}
	}

	var payload []byte
	if method == http.MethodPut {
		wall := atc.Wall{Message: "test message", TTL: time.Minute}
		if profile == "set-invalid-response" || profile == "set-invalid-state" {
			wall.Message = ""
		}
		payload, err = json.Marshal(wall)
		if err != nil {
			return WallStrictObservation{}, err
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return WallStrictObservation{}, fmt.Errorf("listen for production wall HTTP server: %w", err)
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()

	req, err := http.NewRequest(method, "http://"+listener.Addr().String()+"/api/v1/wall", bytes.NewReader(payload))
	if err != nil {
		return WallStrictObservation{}, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return WallStrictObservation{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return WallStrictObservation{}, err
	}

	observation := WallStrictObservation{
		Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: string(body),
	}
	if method == http.MethodGet && len(body) > 0 {
		var returned atc.Wall
		if err := json.Unmarshal(body, &returned); err != nil {
			return observation, fmt.Errorf("decode wall API response: %w", err)
		}
		observation.Message = returned.Message
		observation.TTL = returned.TTL
		return observation, nil
	}
	stored, err := wallDB.GetWall()
	if err != nil {
		return observation, fmt.Errorf("reload persisted wall: %w", err)
	}
	observation.Message = stored.Message
	observation.TTL = stored.TTL
	return observation, nil
}

func strictWallAPIRequest(profile string) (string, string, error) {
	switch profile {
	case "get-status", "get-content-type", "get-permanent-document", "get-expiring-document":
		return http.MethodGet, atc.GetWall, nil
	case "set-status", "set-state", "set-invalid-response", "set-invalid-state", "set-forbidden", "set-unauthorized":
		return http.MethodPut, atc.SetWall, nil
	case "clear-status", "clear-state", "clear-forbidden", "clear-unauthorized":
		return http.MethodDelete, atc.ClearWall, nil
	default:
		return "", "", fmt.Errorf("unknown wall API profile %q", profile)
	}
}

func strictWallIdentity(profile string) string {
	switch profile {
	case "set-forbidden", "clear-forbidden":
		return "member"
	case "set-unauthorized", "clear-unauthorized", "get-status", "get-content-type", "get-permanent-document", "get-expiring-document":
		return ""
	default:
		return "admin"
	}
}

func prepareStrictWallIdentity(database JetbridgeDB, identity string) (string, error) {
	if identity == "" {
		return "", nil
	}
	userID := "brine-wall-" + identity
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "brine-wall-" + identity})
	if err != nil {
		return "", err
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {wallStrictConnector + ":" + userID}},
	}); err != nil {
		return "", err
	}
	if identity == "admin" {
		if _, err := database.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, team.ID()); err != nil {
			return "", err
		}
	}
	payload, err := json.Marshal(map[string]any{
		"sub": userID, "preferred_username": userID,
		"aud": []any{wallStrictAudience}, "exp": time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": wallStrictConnector, "user_id": userID},
	})
	if err != nil {
		return "", err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	token := "brine-wall-token-" + identity
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(token, claims); err != nil {
		return "", err
	}
	return "bearer " + token, nil
}

func strictWallAPIRouter(action string, handler http.Handler) (http.Handler, error) {
	var routes rata.Routes
	for _, route := range atc.Routes {
		if route.Name == action {
			routes = append(routes, route)
		}
	}
	if len(routes) != 1 {
		return nil, fmt.Errorf("production wall route %q matched %d routes", action, len(routes))
	}
	return rata.NewRouter(routes, rata.Handlers{action: handler})
}
