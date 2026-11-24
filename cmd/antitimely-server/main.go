package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
	"github.com/rian/antitimely/internal/syncclient"
	"github.com/rian/antitimely/internal/syncserver"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("antitimely server", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("choose serve, create-account, register-device, generate-clickup-key, clickup-connect, clickup-oauth, or clickup-disconnect")
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:])
	case "create-account":
		return createAccount(ctx, args[1:])
	case "generate-clickup-key":
		return generateClickUpKey(args[1:])
	case "register-device":
		return registerDevice(ctx, args[1:])
	case "clickup-connect", "clickup-oauth", "clickup-disconnect":
		return clickUpAccount(ctx, args[0], args[1:])
	default:
		return errors.New("choose serve, create-account, register-device, generate-clickup-key, clickup-connect, clickup-oauth, or clickup-disconnect")
	}
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	filename := fs.String("db", "antitimely-server.sqlite", "server database path")
	addr := fs.String("listen", "127.0.0.1:8080", "listen address")
	cert := fs.String("tls-cert", "", "TLS certificate PEM")
	key := fs.String("tls-key", "", "TLS private key PEM")
	clickUpKey := fs.String("clickup-key-file", "", "ClickUp credential encryption key file")
	clickUpOAuth := fs.String("clickup-oauth-file", "", "ClickUp OAuth configuration JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*cert == "") != (*key == "") {
		return errors.New("supply both TLS certificate and key, or neither on loopback")
	}
	if *cert == "" {
		host, _, err := net.SplitHostPort(*addr)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("plaintext requires a literal loopback listen address")
		}
	}
	db, err := syncserver.Open(ctx, *filename)
	if err != nil {
		return err
	}
	defer db.Close()
	if *clickUpOAuth != "" && *clickUpKey == "" {
		return errors.New("ClickUp OAuth requires -clickup-key-file")
	}
	service := syncserver.New(db)
	base := service.Handler()
	if *clickUpKey != "" {
		contents, err := os.ReadFile(*clickUpKey)
		if err != nil {
			return err
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(contents)))
		if err != nil {
			return errors.New("invalid ClickUp key file")
		}
		extension, err := syncserver.NewClickUp(service, raw)
		if err != nil {
			return err
		}
		if *clickUpOAuth != "" {
			body, err := os.ReadFile(*clickUpOAuth)
			if err != nil {
				return err
			}
			var config syncserver.OAuthConfig
			if err := json.Unmarshal(body, &config); err != nil {
				return errors.New("invalid ClickUp OAuth configuration")
			}
			if err := extension.ConfigureOAuth(config); err != nil {
				return err
			}
		}
		base = service.Handler(extension)
	}
	srv := &http.Server{
		Addr: *addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			base.ServeHTTP(w, r)
			slog.Info("RPC request", "procedure", r.URL.Path, "duration", time.Since(start).Round(time.Millisecond))
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       12 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	done := make(chan struct{})
	drained := make(chan struct{})
	defer close(done)
	go func() {
		defer close(drained)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		case <-done:
		}
	}()
	slog.Info("sync RPC listening", "address", listener.Addr().String(), "tls", *cert != "")
	if *cert != "" {
		err = srv.ServeTLS(listener, *cert, *key)
	} else {
		err = srv.Serve(listener)
	}
	if errors.Is(err, http.ErrServerClosed) {
		<-drained
		return nil
	}
	return err
}

func createAccount(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("create-account", flag.ContinueOnError)
	filename := fs.String("db", "antitimely-server.sqlite", "server database path")
	name := fs.String("name", "", "account name")
	to := fs.String("out", "", "new file for account token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *name == "" || *to == "" {
		return errors.New("create-account requires -name and -out")
	}
	db, err := syncserver.Open(ctx, *filename)
	if err != nil {
		return err
	}
	defer db.Close()
	return writeSecret(*to, func(out *os.File) error {
		token, err := syncserver.New(db).CreateAccount(ctx, *name)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, token)
		return err
	})
}

func registerDevice(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("register-device", flag.ContinueOnError)
	endpoint := fs.String("url", "", "sync server URL")
	accountFile := fs.String("account-token-file", "", "account token file")
	name := fs.String("name", "", "device label")
	to := fs.String("out", "", "new file for device credentials")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *name == "" || *to == "" || *accountFile == "" {
		return errors.New("register-device requires -url, -account-token-file, -name, and -out")
	}
	server, err := syncclient.ValidateEndpoint(*endpoint)
	if err != nil {
		return err
	}
	accountToken, err := os.ReadFile(*accountFile)
	if err != nil {
		return err
	}
	return writeSecret(*to, func(out *os.File) error {
		req := connect.NewRequest(&v1.RegisterDeviceRequest{Name: *name})
		res, err := syncclient.NewClient(server).RegisterDevice(ctx, syncclient.Authorize(req, strings.TrimSpace(string(accountToken))))
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(syncclient.DeviceCredential{DeviceID: res.Msg.DeviceId, Token: res.Msg.Token})
	})
}

func generateClickUpKey(args []string) error {
	fs := flag.NewFlagSet("generate-clickup-key", flag.ContinueOnError)
	path := fs.String("out", "", "new file for encryption key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *path == "" {
		return errors.New("generate-clickup-key requires -out")
	}
	return writeSecret(*path, func(out *os.File) error {
		_, err := fmt.Fprintln(out, base64.StdEncoding.EncodeToString(syncserver.GenerateClickUpKey()))
		return err
	})
}

func writeSecret(path string, write func(*os.File) error) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	err = errors.Join(write(out), out.Close())
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
