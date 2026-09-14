package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/skymarshal/skyserver"
	dex "github.com/concourse/dex/server"
	dexapi "github.com/dexidp/dex/api/v2"
	"sigs.k8s.io/yaml"
)

type AuthScenario struct {
	Fixture       *AuthFixture
	Before, After rc.TargetToken
	Output        string
	Err           error
	CSRF          string
	Concurrent    []string
}

func configureAuthRevocation(f *AuthFixture, config *skyserver.SkyConfig) {
	api := dex.NewAPI(f.Store, slog.Default(), "brine", f.Dex)
	config.RevokeRefresh = func(ctx context.Context, subject, client string) error {
		_, err := api.RevokeRefresh(ctx, &dexapi.RevokeRefreshReq{UserId: subject, ClientId: client})
		return err
	}
}

func AuthenticationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, AuthScenario]("an authentication server with a private pipeline", []string{"auth-server"},
			func(_ brine.Empty, _ brine.Params, recorder *brine.Recorder, res brine.Resources) (AuthScenario, error) {
				fixture := res.Get("auth-server").(*AuthFixture)
				recorder.Log("Authentication fixture: " + fixture.URL)
				return AuthScenario{Fixture: fixture}, nil
			}),
		brine.DefineMap[AuthScenario, AuthScenario]("fly logs in using {string}", func(in AuthScenario, p brine.Params, _ *brine.Recorder) (AuthScenario, error) {
			flow, _ := p.GetString(0)
			var err error
			if flow == "browser" {
				err = in.Fixture.flyBrowserLogin()
			} else if flow == "password" {
				_, err = in.Fixture.fly("login", "-c", in.Fixture.URL, "-n", "auth-team", "-u", "owner", "-p", authPassword)
			} else {
				err = fmt.Errorf("unsupported authentication flow %q", flow)
			}
			if err != nil {
				return in, err
			}
			in.Before, err = in.Fixture.savedFlyToken()
			if err != nil {
				return in, err
			}
			if in.Before.RefreshToken == "" {
				return in, errors.New("fly login did not persist a refresh credential")
			}
			return in, nil
		}),
		brine.DefineMap[AuthScenario, AuthScenario]("fly renews the login in a second process", func(in AuthScenario, _ brine.Params, _ *brine.Recorder) (AuthScenario, error) {
			in.Output, in.Err = in.Fixture.fly("pipelines")
			var err error
			in.After, err = in.Fixture.savedFlyToken()
			return in, err
		}),
		CheckThat[AuthScenario]("the refreshed fly login reads the private pipeline", func(in AuthScenario) error {
			if in.Err != nil {
				return in.Err
			}
			if !strings.Contains(in.Output, "private") {
				return errors.New("fly pipelines did not return the private pipeline")
			}
			return nil
		}),
		CheckThat[AuthScenario]("fly has persisted the replacement refresh credential", func(in AuthScenario) error {
			if in.After.RefreshToken == "" || in.After.RefreshToken == in.Before.RefreshToken {
				return errors.New("fly did not persist a rotated refresh credential")
			}
			// The next process must load that replacement rather than reuse the
			// original credential which is already outside the reuse interval.
			out, err := in.Fixture.fly("pipelines")
			if err != nil {
				return err
			}
			if !strings.Contains(out, "private") {
				return errors.New("third fly process could not use the persisted credential")
			}
			return nil
		}),
		brine.DefineMap[AuthScenario, AuthScenario]("fly logs out", func(in AuthScenario, _ brine.Params, _ *brine.Recorder) (AuthScenario, error) {
			_, err := in.Fixture.fly("logout")
			return in, err
		}),
		CheckThat[AuthScenario]("the saved fly refresh credential is rejected by the issuer", func(in AuthScenario) error {
			clientID := in.Before.OAuthClientID
			if clientID == "" {
				clientID = "fly"
			}
			values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {in.Before.RefreshToken}, "client_id": {clientID}}
			if clientID == "fly" {
				values.Set("client_secret", "Zmx5")
			}
			resp, err := in.Fixture.Client.PostForm(in.Fixture.URL+"/sky/issuer/token", values)
			if err != nil {
				return err
			}
			_, _ = authReadBody(resp)
			if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnauthorized {
				return fmt.Errorf("issuer accepted a refresh credential after logout: HTTP %d", resp.StatusCode)
			}
			return nil
		}),
		brine.DefineMap[AuthScenario, AuthScenario]("two fly processes renew the same login concurrently", func(in AuthScenario, _ brine.Params, _ *brine.Recorder) (AuthScenario, error) {
			results := make(chan struct {
				output string
				err    error
			}, 2)
			start := make(chan struct{})
			for range 2 {
				go func() {
					<-start
					out, err := in.Fixture.fly("pipelines")
					results <- struct {
						output string
						err    error
					}{out, err}
				}()
			}
			close(start)
			for range 2 {
				result := <-results
				in.Concurrent = append(in.Concurrent, result.output)
				in.Err = errors.Join(in.Err, result.err)
			}
			return in, nil
		}),
		CheckThat[AuthScenario]("both fly processes read the private pipeline", func(in AuthScenario) error {
			if in.Err != nil {
				return in.Err
			}
			if len(in.Concurrent) != 2 {
				return errors.New("two fly results were not recorded")
			}
			for _, out := range in.Concurrent {
				if !strings.Contains(out, "private") {
					return errors.New("concurrent fly process could not read the pipeline")
				}
			}
			return nil
		}),
		CheckThat[AuthScenario]("a later fly process can use the saved login", func(in AuthScenario) error {
			out, err := in.Fixture.fly("pipelines")
			if err != nil {
				return err
			}
			if !strings.Contains(out, "private") {
				return errors.New("saved login could not read the pipeline")
			}
			return nil
		}),
		CheckThat[AuthScenario]("a different OAuth client cannot renew that fly login", func(in AuthScenario) error {
			resp, err := in.Fixture.Client.PostForm(in.Fixture.URL+"/sky/issuer/token", url.Values{
				"grant_type": {"refresh_token"}, "refresh_token": {in.Before.RefreshToken}, "client_id": {"brine-web"}, "client_secret": {"brine-web-secret"},
			})
			if err != nil {
				return err
			}
			_, _ = authReadBody(resp)
			if resp.StatusCode != http.StatusBadRequest {
				return fmt.Errorf("wrong client refresh: HTTP %d", resp.StatusCode)
			}
			_, err = in.Fixture.fly("pipelines")
			return err
		}),
		brine.DefineMap[AuthScenario, AuthScenario]("the browser logs in to Sky", func(in AuthScenario, _ brine.Params, _ *brine.Recorder) (AuthScenario, error) {
			start := in.Fixture.URL + "/sky/login?" + url.Values{"redirect_uri": {"/api/v1/teams/auth-team/pipelines/private"}}.Encode()
			resp, err := in.Fixture.loginForm(in.Fixture.Client, start, "owner")
			if err != nil {
				return in, err
			}
			_, _ = authReadBody(resp)
			if resp.StatusCode != http.StatusOK {
				return in, fmt.Errorf("browser login: HTTP %d", resp.StatusCode)
			}
			in.CSRF = resp.Request.URL.Query().Get("csrf_token")
			if in.CSRF == "" {
				return in, errors.New("browser callback omitted CSRF token")
			}
			return in, nil
		}),
		brine.DefineMap[AuthScenario, AuthScenario]("the browser loses its expired access and CSRF cookies", func(in AuthScenario, _ brine.Params, _ *brine.Recorder) (AuthScenario, error) {
			origin, _ := url.Parse(in.Fixture.URL)
			in.Fixture.Client.Jar.SetCookies(origin, []*http.Cookie{
				{Name: "skymarshal_auth", Path: "/", MaxAge: -1}, {Name: "skymarshal_csrf", Path: "/", MaxAge: -1},
			})
			resp, err := in.Fixture.Client.Get(in.Fixture.URL + "/api/v1/teams/auth-team/pipelines/private")
			if err != nil {
				return in, err
			}
			_, _ = authReadBody(resp)
			if resp.StatusCode != http.StatusUnauthorized {
				return in, fmt.Errorf("missing access cookie: HTTP %d", resp.StatusCode)
			}
			return in, nil
		}),
		CheckThat[AuthScenario]("a foreign or missing Origin cannot renew the browser login", func(in AuthScenario) error {
			for _, origin := range []string{"", "https://foreign.example"} {
				resp, err := in.Fixture.browserRefresh(origin)
				if err != nil {
					return err
				}
				_, _ = authReadBody(resp)
				if resp.StatusCode != http.StatusForbidden {
					return fmt.Errorf("untrusted browser origin: HTTP %d", resp.StatusCode)
				}
			}
			return nil
		}),
		brine.DefineMap[AuthScenario, AuthScenario]("the browser renews its login from the same origin", func(in AuthScenario, _ brine.Params, _ *brine.Recorder) (AuthScenario, error) {
			resp, err := in.Fixture.browserRefresh(in.Fixture.URL)
			if err != nil {
				return in, err
			}
			body, err := authReadBody(resp)
			if err != nil {
				return in, err
			}
			if resp.StatusCode != http.StatusOK {
				return in, fmt.Errorf("browser refresh: HTTP %d", resp.StatusCode)
			}
			var data struct {
				CSRF string `json:"csrf_token"`
			}
			if err := json.Unmarshal(body, &data); err != nil {
				return in, err
			}
			if data.CSRF == "" || data.CSRF == in.CSRF {
				return in, errors.New("browser refresh did not rotate its CSRF token")
			}
			in.CSRF = data.CSRF
			return in, nil
		}),
		CheckThat[AuthScenario]("the replacement CSRF token authorizes a pipeline mutation", func(in AuthScenario) error {
			req, err := http.NewRequest(http.MethodPut, in.Fixture.URL+"/api/v1/teams/auth-team/pipelines/private/pause", nil)
			if err != nil {
				return err
			}
			req.Header.Set(auth.CSRFHeaderName, in.CSRF)
			resp, err := in.Fixture.Client.Do(req)
			if err != nil {
				return err
			}
			_, _ = authReadBody(resp)
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("browser pipeline mutation: HTTP %d", resp.StatusCode)
			}
			resp, err = in.Fixture.Client.Get(in.Fixture.URL + "/api/v1/teams/auth-team/pipelines/private")
			if err != nil {
				return err
			}
			body, err := authReadBody(resp)
			if err != nil {
				return err
			}
			var pipeline struct {
				Paused bool `json:"paused"`
			}
			if err := json.Unmarshal(body, &pipeline); err != nil {
				return err
			}
			if resp.StatusCode != http.StatusOK || !pipeline.Paused {
				return errors.New("browser mutation did not persist the paused pipeline")
			}
			return nil
		}),
	}
}

func (f *AuthFixture) browserRefresh(origin string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, f.URL+"/sky/token/refresh", nil)
	if err != nil {
		return nil, err
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return f.Client.Do(req)
}

func (f *AuthFixture) fly(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.Bin.Fly, append([]string{"-t", "auth"}, args...)...)
	cmd.Env = append(os.Environ(), "FLY_HOME="+f.Home)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("fly %s: %w: %s", args[0], err, redactAuthOutput(string(out)))
	}
	return string(out), nil
}

func (f *AuthFixture) savedFlyToken() (rc.TargetToken, error) {
	body, err := os.ReadFile(filepath.Join(f.Home, ".flyrc"))
	if err != nil {
		return rc.TargetToken{}, err
	}
	var config rc.RC
	if err := yaml.Unmarshal(body, &config); err != nil {
		return rc.TargetToken{}, err
	}
	target, ok := config.Targets["auth"]
	if !ok || target.Token == nil {
		return rc.TargetToken{}, errors.New("fly did not save its auth target")
	}
	return *target.Token, nil
}

var authURLPattern = regexp.MustCompile(`https?://[^\s\x1b]+`)
var authSecretPattern = regexp.MustCompile(`(?i)(token|code|state|code_challenge)=([^&\s]+)`)

func redactAuthOutput(s string) string { return authSecretPattern.ReplaceAllString(s, "$1=[redacted]") }

type authLoginOutput struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	URL  chan string
	sent bool
}

func (o *authLoginOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.buf.Write(p)
	if !o.sent {
		for _, match := range authURLPattern.FindAllString(o.buf.String(), -1) {
			if strings.Contains(match, "/auth?") || strings.Contains(match, "fly_port=") {
				o.sent = true
				o.URL <- match
				break
			}
		}
	}
	return n, err
}

func (f *AuthFixture) flyBrowserLogin() error {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.Bin.Fly, "-t", "auth", "login", "-c", f.URL, "-n", "auth-team")
	cmd.Env = append(os.Environ(), "FLY_HOME="+f.Home)
	output := &authLoginOutput{URL: make(chan string, 1)}
	cmd.Stdout, cmd.Stderr = output, output
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case start := <-output.URL:
		resp, err := f.loginForm(f.Client, start, "owner")
		if err != nil {
			cancel()
			<-done
			return err
		}
		_, _ = authReadBody(resp)
		if resp.StatusCode != http.StatusOK {
			cancel()
			<-done
			return fmt.Errorf("fly browser callback: HTTP %d", resp.StatusCode)
		}
	case err := <-done:
		return fmt.Errorf("fly exited before printing its browser URL: %w", err)
	case <-ctx.Done():
		<-done
		return errors.New("fly did not print its browser authorization URL")
	}
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("fly browser login failed: %w", err)
		}
		return nil
	case <-ctx.Done():
		<-done
		return errors.New("fly did not finish after its browser callback")
	}
}

var _ io.Writer = (*authLoginOutput)(nil)
