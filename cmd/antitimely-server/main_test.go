package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rian/antitimely/internal/syncclient"
	"github.com/rian/antitimely/internal/syncserver"
)

func TestProvisioningCommands(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, accountPath := filepath.Join(dir, "server.sqlite"), filepath.Join(dir, "account.token")
	args := []string{"create-account", "-db", dbPath, "-name", "test", "-out", accountPath}
	if err := run(ctx, args); err != nil {
		t.Fatal(err)
	}
	account, err := os.ReadFile(accountPath)
	if err != nil || len(strings.TrimSpace(string(account))) != 26 {
		t.Fatalf("invalid account credential (%v)", err)
	}
	if err := run(ctx, args); err == nil {
		t.Fatal("provisioning overwrote an existing credential")
	}
	db, err := syncserver.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	host := httptest.NewServer(syncserver.New(db).Handler())
	defer host.Close()
	devicePath := filepath.Join(dir, "device.json")
	err = run(ctx, []string{"register-device", "-url", host.URL, "-account-token-file", accountPath, "-name", "test device", "-out", devicePath})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := syncclient.ReadCredential(devicePath)
	if err != nil || cred.DeviceID == "" || cred.Token == "" {
		t.Fatalf("invalid device credential (%v)", err)
	}
}

func TestServeRejectsPlaintextOnPublicInterfaces(t *testing.T) {
	for _, addr := range []string{":8080", "0.0.0.0:8080", "[::]:8080"} {
		err := run(context.Background(), []string{"serve", "-listen", addr})
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("unexpected serve result for %q: %v", addr, err)
		}
	}
}
