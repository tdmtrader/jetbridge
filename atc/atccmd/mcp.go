package atccmd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/mcp"
	"github.com/concourse/concourse/skymarshal/mcpauth"
	"github.com/concourse/concourse/skymarshal/storage"
	dex "github.com/concourse/dex/server"
	dexstorage "github.com/concourse/dex/storage"
	dexapi "github.com/dexidp/dex/api/v2"
	"github.com/tedsuo/ifrit"
	"golang.org/x/oauth2"
)

const mcpIdentityClientID = "jetbridge-mcp"

func (cmd *RunCommand) mcpIdentitySecret() string {
	key := deriveStateSigningKey(cmd.Server.ClientID, cmd.Server.ClientSecret, cmd.Postgres.User, cmd.Postgres.Password)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("jetbridge-mcp-upstream-client-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (cmd *RunCommand) mcpIdentityClients() []dexstorage.Client {
	if !cmd.EnableMCP {
		return nil
	}
	return []dexstorage.Client{{ID: mcpIdentityClientID, Secret: cmd.mcpIdentitySecret(), Name: "JetBridge MCP consent", RedirectURIs: []string{strings.TrimRight(cmd.ExternalURL.String(), "/") + "/mcp/oauth/callback"}}}
}

func newRefreshRevoker(store storage.Storage, logger lager.Logger) func(context.Context, string, string) error {
	api := dex.NewAPI(store, slog.New(lager.NewHandler(logger)), "", nil)
	return func(ctx context.Context, subject, clientID string) error {
		_, err := api.RevokeRefresh(ctx, &dexapi.RevokeRefreshReq{UserId: subject, ClientId: clientID})
		return err
	}
}

func (cmd *RunCommand) constructMCPHandler(logger lager.Logger, conn db.DbConn, client *http.Client, api http.Handler, accessFactory accessor.AccessFactory) error {
	cmd.mcpHandler, cmd.mcpCleanup = nil, nil
	if !cmd.EnableMCP {
		return nil
	}
	if cmd.ExternalURL.URL == nil || strings.Trim(cmd.ExternalURL.URL.Path, "/") != "" {
		return errors.New("MCP requires an external URL at the origin root")
	}
	if _, reserved := cmd.Auth.AuthFlags.Clients[mcpIdentityClientID]; reserved || cmd.Server.ClientID == mcpIdentityClientID {
		return errors.New("jetbridge-mcp is reserved for the MCP identity broker; remove it from ordinary auth clients")
	}
	if cmd.MCPClientConfig.Path() == "" {
		return errors.New("--enable-mcp requires --mcp-client-config")
	}
	file, err := os.Open(cmd.MCPClientConfig.Path())
	if err != nil {
		return fmt.Errorf("MCP client configuration: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024))
	decoder.DisallowUnknownFields()
	var clients []mcpauth.Client
	if err := decoder.Decode(&clients); err != nil {
		return fmt.Errorf("MCP client configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("MCP client configuration must contain one JSON array")
	}
	if len(clients) == 0 {
		return errors.New("MCP requires at least one registered client")
	}

	base := strings.TrimRight(cmd.ExternalURL.String(), "/")
	issuer := base + "/sky/issuer"
	// No discovery request at startup: the embedded Dex server starts alongside
	// this handler. Keys are fetched lazily through the configured TLS transport.
	boundedClient := *client
	boundedClient.Timeout = 15 * time.Second
	provider := mcpauth.OAuthProvider{
		Config: &oauth2.Config{
			ClientID: mcpIdentityClientID, ClientSecret: cmd.mcpIdentitySecret(), RedirectURL: base + "/mcp/oauth/callback",
			Endpoint: oauth2.Endpoint{AuthURL: issuer + "/auth", TokenURL: issuer + "/token", AuthStyle: oauth2.AuthStyleInHeader},
			Scopes:   []string{"openid", "profile", "email", "groups", "federated:id", "offline_access"},
		},
		HTTPClient:    &boundedClient,
		VerifyIDToken: mcpauth.NewOIDCVerifier(issuer, mcpIdentityClientID, &boundedClient),
	}
	store := mcpauth.NewSQLStore(conn)
	auth, err := mcpauth.NewServer(mcpauth.Config{
		Issuer: base + "/mcp/oauth", Resource: base + "/api/v1/mcp", Clients: clients,
		Store: store, Provider: provider, SecureCookies: cmd.Auth.AuthFlags.SecureCookies,
		IdleLifetime: cmd.Auth.AuthFlags.RefreshTokenIdleTimeout, AbsoluteLifetime: cmd.Auth.AuthFlags.RefreshTokenAbsoluteTimeout,
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/mcp", mcp.NewHandler(auth, api, mcp.HandlerOptions{AccessFactory: accessFactory, CustomRoles: cmd.customRoles}))
	mux.Handle("/", auth)
	cmd.mcpHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cmd.isTLSEnabled() && r.TLS == nil {
			http.Error(w, "MCP authorization requires HTTPS", http.StatusBadRequest)
			return
		}
		mux.ServeHTTP(w, r)
	})
	cmd.mcpCleanup = ifrit.RunFunc(func(signals <-chan os.Signal, ready chan<- struct{}) error {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		close(ready)
		for {
			select {
			case <-signals:
				return nil
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				err := store.Cleanup(ctx)
				cancel()
				if err != nil {
					logger.Error("mcp-auth-cleanup", err)
				}
			}
		}
	})
	return nil
}
