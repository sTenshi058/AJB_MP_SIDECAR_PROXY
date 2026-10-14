package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesPrivateScaffold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings", "config.json")
	if err := Init(path, false); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions = %04o, want 0600", info.Mode().Perm())
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != (Config{}) {
		t.Fatalf("new config = %#v, want empty scaffold", cfg)
	}
}

func TestDefaultPathIsBesideExecutable(t *testing.T) {
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(executablePath), "config.json")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestInitDoesNotOverwriteWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"product_id":"keep"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(path, false); err == nil {
		t.Fatal("expected existing config to be preserved")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"product_id":"keep"}` {
		t.Fatalf("existing config changed: %s", data)
	}
}

func TestConfigValidationReportsMissingValues(t *testing.T) {
	err := (Config{}).Validate("")
	if err == nil {
		t.Fatal("expected empty config to fail validation")
	}
	for _, required := range []string{"product_id", "sandbox_id", "deployment_id", "client_id", ClientSecretEnv} {
		if !strings.Contains(err.Error(), required) {
			t.Errorf("validation error %q does not mention %q", err, required)
		}
	}
}

func TestLoadRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	for _, content := range []string{
		`{"product_id":"p","unexpected":true}`,
		`{"product_id":"p"} {}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("Load(%q) succeeded; expected malformed config error", content)
		}
	}
}
