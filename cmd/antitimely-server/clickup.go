package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
	"github.com/rian/antitimely/internal/syncapi/v1/syncv1connect"
	"github.com/rian/antitimely/internal/syncclient"
)

func clickUpAccount(ctx context.Context, action string, args []string) error {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	endpoint := fs.String("url", "", "server URL")
	accountFile := fs.String("account-token-file", "", "account token file")
	personalFile := fs.String("personal-token-file", "", "ClickUp personal token file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *accountFile == "" {
		return errors.New("supply -url and -account-token-file")
	}
	host, err := syncclient.ValidateEndpoint(*endpoint)
	if err != nil {
		return err
	}
	accountToken, err := os.ReadFile(*accountFile)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(accountToken))
	client := syncv1connect.NewClickUpServiceClient(syncclient.HTTPClient(), host)
	switch action {
	case "clickup-connect":
		if *personalFile == "" {
			return errors.New("supply -personal-token-file")
		}
		personal, err := os.ReadFile(*personalFile)
		if err != nil {
			return err
		}
		req := connect.NewRequest(&v1.ConnectPersonalRequest{Token: strings.TrimSpace(string(personal))})
		response, err := client.ConnectPersonal(ctx, syncclient.Authorize(req, token))
		if err != nil {
			return err
		}
		fmt.Println("Connected ClickUp user", response.Msg.Username)
	case "clickup-oauth":
		response, err := client.BeginOAuth(ctx, syncclient.Authorize(connect.NewRequest(&v1.BeginClickUpOAuthRequest{}), token))
		if err != nil {
			return err
		}
		fmt.Println(response.Msg.AuthorizationUrl)
	case "clickup-disconnect":
		if _, err := client.Disconnect(ctx, syncclient.Authorize(connect.NewRequest(&v1.DisconnectClickUpRequest{}), token)); err != nil {
			return err
		}
		fmt.Println("ClickUp disconnected.")
	}
	return nil
}
