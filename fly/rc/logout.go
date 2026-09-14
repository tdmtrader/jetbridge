package rc

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LogoutTargetRemotely holds the same lock as renewal until both remote
// revocation and local cleanup finish. Failed revocation never prevents cleanup.
func LogoutTargetRemotely(name TargetName) error {
	return withTargetsLock(func() error {
		targets, err := LoadTargets()
		if err != nil {
			return err
		}
		props, ok := targets[name]
		if !ok {
			return UnknownTargetError{name}
		}
		var remoteErr error
		if props.Token != nil && props.Token.RefreshToken != "" {
			clientID := props.Token.OAuthClientID
			if clientID == "" {
				clientID = "fly"
			}
			if _, err := OAuthClient(props.API, clientID); err != nil {
				remoteErr = err
			} else {
				pool, err := loadCACertPool(props.CACert)
				if err != nil {
					remoteErr = err
				} else {
					certs, err := loadClientCertificate(props.ClientCertPath, props.ClientKeyPath)
					if err != nil {
						remoteErr = err
					} else {
						client := defaultHttpClient(nil, props.Insecure, pool, certs, name, props.API)
						client.Timeout = 15 * time.Second
						client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
						form := url.Values{"refresh_token": {props.Token.RefreshToken}, "client_id": {clientID}}
						req, err := http.NewRequest(http.MethodPost, strings.TrimRight(props.API, "/")+"/sky/logout", strings.NewReader(form.Encode()))
						if err != nil {
							remoteErr = err
						} else {
							req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
							resp, err := client.Do(req)
							if err != nil {
								remoteErr = errors.New("server could not be reached")
							} else {
								resp.Body.Close()
								if resp.StatusCode != http.StatusOK {
									remoteErr = fmt.Errorf("server returned HTTP %d", resp.StatusCode)
								}
							}
						}
					}
				}
			}
		}
		props.Token = &TargetToken{}
		targets[name] = props
		saveErr := writeTargets(flyrcPath(), targets)
		if remoteErr != nil {
			remoteErr = fmt.Errorf("local login cleared; remote renewal revocation was not confirmed: %w", remoteErr)
		}
		if saveErr != nil {
			return errors.Join(errors.New("could not clear local login credentials"), saveErr, remoteErr)
		}
		return remoteErr
	})
}
