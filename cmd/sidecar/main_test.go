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

func TestConfigBundleWritesSecretWithoutPrintingIt(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{
  "product_id": "product",
  "sandbox_id": "sandbox",
  "deployment_id": "deployment",
  "client_id": "client"
}`), 0600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "bundle-config.json")
	const testSecret = "bundle-test-secret"
	oldBundleKeyHex := bundleKeyHex
	bundleKeyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	defer func() { bundleKeyHex = oldBundleKeyHex }()
	t.Setenv(config.ClientSecretEnv, testSecret)

	var stdout, stderr bytes.Buffer
	err := run([]string{"config", "bundle", "--config", configPath, "--out", outputPath}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String()+stderr.String(), testSecret) {
		t.Fatal("config bundle command printed the client secret")
	}

	bundledConfig, err := config.Load(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if bundledConfig.ClientSecret != "" {
		t.Fatal("bundle config unexpectedly contains a plaintext client secret")
	}
	if strings.Contains(string(mustReadFile(t, outputPath)), testSecret) {
		t.Fatal("bundle config exposes the client secret in plaintext")
	}
	decrypted, err := bundledConfig.ResolveClientSecret("", bundleKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != testSecret {
		t.Fatal("bundle config did not decrypt to the configured client secret")
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("bundle config permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPackageRequiresSecretWithoutPrintingIt(t *testing.T) {
	t.Setenv(config.ClientSecretEnv, "")
	var stdout, stderr bytes.Buffer
	err := run([]string{"package"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), config.ClientSecretEnv) {
		t.Fatalf("got error %v; want missing client-secret error", err)
	}
}

func TestDistributionNamesForSupportedPlatforms(t *testing.T) {
	for _, testCase := range []struct {
		goos      string
		goarch    string
		binary    string
		library   string
		supported bool
	}{
		{goos: "linux", goarch: "amd64", binary: "sidecar", library: "libEOSSDK-Linux-Shipping.so", supported: true},
		{goos: "linux", goarch: "arm64", binary: "sidecar", library: "libEOSSDK-LinuxArm64-Shipping.so", supported: true},
		{goos: "windows", goarch: "amd64", binary: "sidecar.exe", library: "EOSSDK-Win64-Shipping.dll", supported: true},
		{goos: "darwin", goarch: "amd64", binary: "sidecar", library: "libEOSSDK-Mac-Shipping.dylib", supported: true},
		{goos: "plan9", goarch: "amd64", supported: false},
	} {
		t.Run(testCase.goos+"/"+testCase.goarch, func(t *testing.T) {
			binary, library, err := distributionNames(testCase.goos, testCase.goarch)
			if !testCase.supported {
				if err == nil {
					t.Fatalf("distributionNames() = (%q, %q, nil), want unsupported platform error", binary, library)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if binary != testCase.binary || library != testCase.library {
				t.Fatalf("distributionNames() = (%q, %q), want (%q, %q)", binary, library, testCase.binary, testCase.library)
			}
		})
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
