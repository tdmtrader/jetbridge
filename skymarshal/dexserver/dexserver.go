package dexserver

import (
	"context"
	"crypto/rsa"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/skymarshal/skycmd"
	s "github.com/concourse/concourse/skymarshal/storage"
	"github.com/concourse/dex/server"
	"github.com/concourse/dex/storage"
	"golang.org/x/crypto/bcrypt"
)

type DexConfig struct {
	Logger                      lager.Logger
	IssuerURL                   string
	SigningKey                  *rsa.PrivateKey
	Expiration                  time.Duration
	Clients                     map[string]string
	ExtraClients                []storage.Client
	Users                       map[string]string
	PasswordConnector           string
	RedirectURL                 string
	Storage                     s.Storage
	RefreshTokenIdleTimeout     time.Duration
	RefreshTokenAbsoluteTimeout time.Duration
	RefreshTokenReuseInterval   time.Duration
}

//go:embed web
var webFS embed.FS

func NewDexServer(config *DexConfig) (*server.Server, error) {

	newDexServerConfig, err := NewDexServerConfig(config)
	if err != nil {
		return nil, err
	}

	return server.NewServerWithKey(context.Background(), newDexServerConfig, config.SigningKey)
}

func NewDexServerConfig(config *DexConfig) (server.Config, error) {

	var clients []storage.Client
	var connectors []storage.Connector
	var passwords []storage.Password

	for username, password := range newLocalUsers(config) {
		passwords = append(passwords, storage.Password{
			UserID:   username,
			Username: username,
			Email:    username,
			Hash:     password,
		})
	}

	if len(passwords) > 0 {
		if config.PasswordConnector != "local" {
			return server.Config{}, errors.New("can only set --add-local-user with --password-connector=local")
		}
		connectors = append(connectors, storage.Connector{
			ID:   "local",
			Type: "local",
			Name: "Username/Password",
		})
	}

	redirectURI := strings.TrimRight(config.IssuerURL, "/") + "/callback"

	for _, connector := range skycmd.GetConnectors() {
		var id = connector.ID()
		if id == "cf" {
			id = "cloudfoundry"
		}
		if c, err := connector.Serialize(redirectURI); err == nil {
			connectors = append(connectors, storage.Connector{
				ID:     id,
				Type:   id,
				Name:   connector.Name(),
				Config: c,
			})
		}
	}

	for clientId, clientSecret := range config.Clients {
		if clientId == "fly-browser" {
			continue // The built-in desktop client is public, never confidential.
		}
		clients = append(clients, storage.Client{
			ID:           clientId,
			Secret:       clientSecret,
			RedirectURIs: []string{config.RedirectURL},
		})
	}
	clients = append(clients, storage.Client{ID: "fly-browser", Name: "fly CLI", Public: true})
	clients = append(clients, config.ExtraClients...)

	if err := replacePasswords(config.Storage, passwords); err != nil {
		return server.Config{}, err
	}

	if err := replaceClients(config.Storage, clients); err != nil {
		return server.Config{}, err
	}

	if err := replaceConnectors(config.Storage, connectors); err != nil {
		return server.Config{}, err
	}

	webFS, err := fs.Sub(webFS, "web")
	if err != nil {
		return server.Config{}, err
	}

	webConfig := server.WebConfig{
		LogoURL: "theme/logo.svg",
		WebFS:   webFS,
		Theme:   "concourse",
		Issuer:  "Concourse",
	}
	idle, absolute, reuse := config.RefreshTokenIdleTimeout, config.RefreshTokenAbsoluteTimeout, config.RefreshTokenReuseInterval
	if idle == 0 {
		idle = 30 * 24 * time.Hour
	}
	if absolute == 0 {
		absolute = 90 * 24 * time.Hour
	}
	if reuse == 0 {
		reuse = 5 * time.Second
	}
	if idle < 0 || absolute < 0 || reuse < 0 || reuse >= idle || idle > absolute {
		return server.Config{}, errors.New("refresh policy requires 0 < reuse < idle <= absolute lifetime")
	}
	// The pinned Dex constructor's boolean means disable rotation, despite its name.
	policy, err := server.NewRefreshTokenPolicy(slog.New(lager.NewHandler(config.Logger)), false, idle.String(), absolute.String(), reuse.String())
	if err != nil {
		return server.Config{}, err
	}

	return server.Config{
		RefreshTokenPolicy:     policy,
		PasswordConnector:      config.PasswordConnector,
		SupportedResponseTypes: []string{"code", "token", "id_token"},
		SkipApprovalScreen:     true,
		IDTokensValidFor:       config.Expiration,
		Issuer:                 config.IssuerURL,
		Storage:                config.Storage,
		Web:                    webConfig,
		Logger:                 slog.New(lager.NewHandler(config.Logger)),
	}, nil
}

func replacePasswords(store s.Storage, passwords []storage.Password) error {
	existing, err := store.ListPasswords(context.TODO())
	if err != nil {
		return err
	}

	for _, oldPass := range existing {
		err = store.DeletePassword(context.TODO(), oldPass.Email)
		if err != nil {
			return err
		}
	}

	for _, newPass := range passwords {
		err = store.CreatePassword(context.TODO(), newPass)
		//if this already exists, some other ATC process has created it already
		//we can assume that both ATCs have the same desired config.
		if err != nil && err != storage.ErrAlreadyExists {
			return err
		}
	}

	return nil
}

func replaceClients(store s.Storage, clients []storage.Client) error {
	existing, err := store.ListClients(context.TODO())
	if err != nil {
		return err
	}

	for _, oldClient := range existing {
		err = store.DeleteClient(context.TODO(), oldClient.ID)
		if err != nil {
			return err
		}
	}

	for _, newClient := range clients {
		err = store.CreateClient(context.TODO(), newClient)
		//if this already exists, some other ATC process has created it already
		//we can assume that both ATCs have the same desired config.
		if err != nil && err != storage.ErrAlreadyExists {
			return err
		}
	}

	return nil
}

func replaceConnectors(store s.Storage, connectors []storage.Connector) error {
	existing, err := store.ListConnectors(context.TODO())
	if err != nil {
		return err
	}

	for _, oldConn := range existing {
		err = store.DeleteConnector(context.TODO(), oldConn.ID)
		if err != nil {
			return err
		}
	}

	for _, newConn := range connectors {
		err = store.CreateConnector(context.TODO(), newConn)
		//if this already exists, some other ATC process has created it already
		//we can assume that both ATCs have the same desired config.
		if err != nil && err != storage.ErrAlreadyExists {
			return err
		}
	}

	return nil
}

func newLocalUsers(config *DexConfig) map[string][]byte {
	users := map[string][]byte{}

	for username, password := range config.Users {
		if username != "" && password != "" {

			var hashed []byte

			if _, err := bcrypt.Cost([]byte(password)); err != nil {
				if hashed, err = bcrypt.GenerateFromPassword([]byte(password), 0); err != nil {

					config.Logger.Error("bcrypt-local-user", err, lager.Data{
						"username": username,
					})

					continue
				}
			} else {
				hashed = []byte(password)
			}

			users[username] = hashed
		}
	}

	return users

}
