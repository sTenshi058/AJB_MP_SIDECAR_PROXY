package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sTenshi058/AJB_MP_SIDECAR_PROXY/internal/config"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"version"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), version) {
		t.Fatalf("version output %q does not contain %q", stdout.String(), version)
	}
}

func TestConfigInitAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar.json")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"config", "init", "--path", path}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(`{
  "product_id": "product",
  "sandbox_id": "sandbox",
  "deployment_id": "deployment",
  "client_id": "client"
}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ClientSecretEnv, "test-secret")
	stdout.Reset()
	if err := run([]string{"config", "validate", "--path", path}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "complete") {
		t.Fatalf("unexpected validation output: %q", stdout.String())
	}
}

func TestHostValidatesEOSConfigBeforeStarting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar.json")
	if err := config.Init(path, false); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{"host", "--config", path}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "missing required EOS settings") {
		t.Fatalf("got error %v; want EOS configuration validation error", err)
	}
}

func TestGameAddressMustBeLoopback(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"host", "--game-addr", "0.0.0.0:1170"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("got error %v; want loopback validation error", err)
	}
}

func TestSessionCommandsRejectUnusedAddressFlags(t *testing.T) {
	for _, args := range [][]string{
		{"host", "--listen-addr", "127.0.0.1:1170"},
		{"join", "--game-addr", "127.0.0.1:1170"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(args, &stdout, &stderr); err == nil {
			t.Fatalf("run(%v) unexpectedly accepted an unused address flag", args)
		}
	}
}
