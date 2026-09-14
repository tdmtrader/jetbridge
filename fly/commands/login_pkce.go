package commands

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/concourse/concourse/fly/commands/internal/interaction"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/skratchdot/open-golang/open"
	"golang.org/x/oauth2"
)

func (command *LoginCommand) renewableBrowserGrant(client concourse.Client) (*rc.TargetToken, error) {
	conf, err := rc.OAuthClient(client.URL(), rc.FlyBrowserClientID)
	if err != nil {
		return nil, err
	}
	verifier, state := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, rc.AuthHTTPClient(client.HTTPClient()))
	codes, errs, manual := make(chan string, 1), make(chan error, 1), make(chan string, 1)
	conf.RedirectURL = "urn:ietf:wg:oauth:2.0:oob"
	if !command.Manual {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("start login callback (use --manual if unavailable): %w", err)
		}
		conf.RedirectURL = "http://" + listener.Addr().String() + "/callback"
		server := &http.Server{ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
		var accepted atomic.Bool
		server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			if r.Method != http.MethodGet || r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
				http.Error(w, "Invalid login state", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("error") != "" {
				select {
				case errs <- errors.New("browser login was rejected"):
				default:
				}
				http.Error(w, "Login was rejected", http.StatusBadRequest)
				return
			}
			code := r.URL.Query().Get("code")
			if code == "" {
				http.Error(w, "Missing authorization code", http.StatusBadRequest)
				return
			}
			if !accepted.CompareAndSwap(false, true) {
				http.Error(w, "Authorization already received", http.StatusConflict)
				return
			}
			select {
			case codes <- code:
				fmt.Fprintln(w, "Authorization received. Return to fly to complete login.")
			default:
				http.Error(w, "Authorization already received", http.StatusConflict)
			}
		})
		defer func() {
			stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = server.Shutdown(stop)
		}()
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				select {
				case errs <- err:
				default:
				}
			}
		}()
	}
	loginURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier))
	fmt.Printf("navigate to the following URL in your browser:\n\n  %s\n", loginURL)
	if command.Manual {
		fmt.Println("Enter the displayed one-time code as: code YOUR_CODE")
	}
	if command.OpenBrowser {
		_ = open.Start(loginURL)
	}
	program := interaction.TokenProgram()
	defer program.Quit()
	go waitForTokenInput(program, manual, errs)
	var code string
	select {
	case code = <-codes:
	case input := <-manual:
		fields := strings.Fields(input)
		if len(fields) != 2 {
			return nil, errors.New("login canceled")
		}
		if strings.EqualFold(fields[0], "code") {
			code = fields[1]
		} else if strings.EqualFold(fields[0], "bearer") {
			fmt.Println("Pasted bearer credentials cannot renew automatically; use browser login to obtain a renewable session.")
			return &rc.TargetToken{Type: "bearer", Value: fields[1]}, nil
		} else {
			return nil, errors.New("enter a one-time code or a bearer credential")
		}
	case err := <-errs:
		return nil, err
	case <-ctx.Done():
		return nil, errors.New("browser login timed out; run fly login again")
	}
	exchangeCtx, exchangeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer exchangeCancel()
	result, err := conf.Exchange(exchangeCtx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, errors.New("could not exchange authorization code; run fly login again")
	}
	token, err := rc.TargetTokenFromOAuth(result, rc.FlyBrowserClientID)
	if err != nil {
		return nil, err
	}
	if token.RefreshToken == "" {
		fmt.Println("This identity provider did not issue a refresh credential; login will be required again after expiry.")
	}
	return token, nil
}
