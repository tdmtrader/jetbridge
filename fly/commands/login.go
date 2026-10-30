package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/interaction"
	"github.com/concourse/concourse/fly/pty"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/go-concourse/concourse"
	"golang.org/x/oauth2"
)

type LoginCommand struct {
	ATCURL         string       `short:"c" long:"concourse-url" description:"Concourse URL to authenticate with"`
	Insecure       bool         `short:"k" long:"insecure" description:"Skip verification of the endpoint's SSL certificate"`
	Username       string       `short:"u" long:"username" description:"Username for basic auth"`
	Password       string       `short:"p" long:"password" description:"Password for basic auth"`
	TeamName       string       `short:"n" long:"team-name" description:"Team to authenticate with. Defaults to the 'main' team or the team previously used for the specified Target."`
	CACert         atc.PathFlag `long:"ca-cert" description:"Path to Concourse PEM-encoded CA certificate file."`
	ClientCertPath atc.PathFlag `long:"client-cert" description:"Path to a PEM-encoded client certificate file."`
	ClientKeyPath  atc.PathFlag `long:"client-key" description:"Path to a PEM-encoded client key file."`
	OpenBrowser    bool         `short:"b" long:"open-browser" description:"Open browser to the auth endpoint"`
	Manual         bool         `long:"manual" description:"Use a one-time authorization code instead of a local browser callback"`
}

func (command *LoginCommand) Execute(args []string) error {
	if Fly.Target == "" {
		return errors.New("name for the target must be specified (--target/-t)")
	}

	var target rc.Target
	var err error

	var caCert string
	if command.CACert != "" {
		caCertBytes, err := os.ReadFile(string(command.CACert))
		if err != nil {
			return err
		}
		caCert = string(caCertBytes)
	}

	if command.ATCURL != "" {
		if command.TeamName == "" {
			command.TeamName = atc.DefaultTeamName
		}

		target, err = rc.NewUnauthenticatedTarget(
			Fly.Target,
			command.ATCURL,
			command.TeamName,
			command.Insecure,
			caCert,
			string(command.ClientCertPath),
			string(command.ClientKeyPath),
			Fly.Verbose,
		)
	} else {
		target, err = rc.LoadUnauthenticatedTarget(
			Fly.Target,
			command.TeamName,
			command.Insecure,
			caCert,
			string(command.ClientCertPath),
			string(command.ClientKeyPath),
			Fly.Verbose,
		)
	}
	if err != nil {
		return err
	}

	client := target.Client()
	command.TeamName = target.Team().Name()

	fmt.Printf("logging in to team '%s'\n\n", command.TeamName)

	if len(args) != 0 {
		return errors.New("unexpected argument [" + strings.Join(args, ", ") + "]")
	}

	err = target.ValidateWithWarningOnly()
	if err != nil {
		return err
	}

	var tokenType string
	var tokenValue string
	var refreshToken string
	var oauthClientID string

	if command.Username != "" && command.Password != "" {
		tokenType, tokenValue, refreshToken, err = command.passwordGrant(client, command.Username, command.Password)
		oauthClientID = "fly"
	} else {
		var loginToken *rc.TargetToken
		loginToken, err = command.renewableBrowserGrant(client)
		if err == nil {
			tokenType, tokenValue, refreshToken, oauthClientID = loginToken.Type, loginToken.Value, loginToken.RefreshToken, loginToken.OAuthClientID
		}
	}

	if errors.Is(err, pty.ErrInterrupted) {
		fmt.Println("^C\r")
		return nil
	}

	if err != nil {
		return err
	}

	fmt.Println("")

	err = command.verifyTeamExists(client.URL(), rc.TargetToken{
		Type:  tokenType,
		Value: tokenValue,
	}, target.CACert(), target.ClientCertPath(), target.ClientKeyPath())

	if err != nil {
		return err
	}

	return command.saveTarget(
		client.URL(),
		&rc.TargetToken{
			Type:          tokenType,
			Value:         tokenValue,
			RefreshToken:  refreshToken,
			OAuthClientID: oauthClientID,
		},
		target.CACert(),
		target.ClientCertPath(),
		target.ClientKeyPath(),
	)
}

func (command *LoginCommand) passwordGrant(client concourse.Client, username, password string) (string, string, string, error) {

	oauth2Config := oauth2.Config{
		ClientID:     "fly",
		ClientSecret: "Zmx5",
		Endpoint:     oauth2.Endpoint{TokenURL: client.URL() + "/sky/issuer/token"},
		Scopes:       []string{"openid", "profile", "email", "federated:id", "groups", "offline_access"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, rc.AuthHTTPClient(client.HTTPClient()))

	token, err := oauth2Config.PasswordCredentialsToken(ctx, username, password)
	if err != nil {
		return "", "", "", errors.New("password login failed; verify credentials and server availability")
	}

	// Prefer ID token over access token (Dex's ID token contains our claims)
	idToken, _ := token.Extra("id_token").(string)
	if idToken != "" {
		return token.TokenType, idToken, token.RefreshToken, nil
	}

	return token.TokenType, token.AccessToken, token.RefreshToken, nil
}

func waitForTokenInput(prg *tea.Program, tokenChannel chan string, errorChannel chan error) {
	fmt.Println()

	model, err := prg.Run()
	if err != nil {
		errorChannel <- err
		return
	}
	in, ok := model.(interaction.TokenModel)
	if !ok {
		errorChannel <- errors.New("unknown model returned by bubbletea")
		return
	}
	tokenChannel <- in.Token()
}

func (command *LoginCommand) saveTarget(url string, token *rc.TargetToken, caCert string, clientCertPath string, clientKeyPath string) error {
	err := rc.SaveTarget(
		Fly.Target,
		url,
		command.Insecure,
		command.TeamName,
		token,
		caCert,
		clientCertPath,
		clientKeyPath,
	)
	if err != nil {
		return err
	}

	fmt.Println("\rtarget saved\r")

	return nil
}

func (command *LoginCommand) verifyTeamExists(clientUrl string, token rc.TargetToken, caCert string, clientCertPath string,
	clientKeyPath string) error {
	verifyTarget, err := rc.NewAuthenticatedTarget("verify",
		clientUrl,
		command.TeamName,
		command.Insecure,
		&token,
		caCert,
		clientCertPath,
		clientKeyPath,
		false)
	if err != nil {
		return err
	}

	userInfo, err := verifyTarget.Client().UserInfo()
	if err != nil {
		return err
	}

	if !userInfo.IsAdmin {
		if userInfo.Teams != nil {
			_, ok := userInfo.Teams[command.TeamName]
			if !ok {
				return errors.New("you are not a member of '" + command.TeamName + "' or the team does not exist")
			}
		} else {
			return errors.New("unable to verify role on team")
		}
	} else {
		teams, err := verifyTarget.Client().ListTeams()
		if err != nil {
			return err
		}
		var found bool
		for _, team := range teams {
			if team.Name == command.TeamName {
				found = true
				break
			}
		}
		if !found {
			return errors.New("team '" + command.TeamName + "' does not exist")
		}
	}

	return nil
}
