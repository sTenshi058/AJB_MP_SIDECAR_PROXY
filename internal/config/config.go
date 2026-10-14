package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const ClientSecretEnv = "AJB_EOS_CLIENT_SECRET"

type Config struct {
	ProductID    string `json:"product_id"`
	SandboxID    string `json:"sandbox_id"`
	DeploymentID string `json:"deployment_id"`
	ClientID     string `json:"client_id"`
}

func DefaultPath() (string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve sidecar executable path: %w", err)
	}
	return filepath.Join(filepath.Dir(executablePath), "config.json"), nil
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, fmt.Errorf("decode config %q: contains multiple JSON values", path)
		}
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	return cfg, nil
}

func (cfg Config) Validate(clientSecret string) error {
	missing := make([]string, 0, 5)
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "product_id", value: cfg.ProductID},
		{name: "sandbox_id", value: cfg.SandboxID},
		{name: "deployment_id", value: cfg.DeploymentID},
		{name: "client_id", value: cfg.ClientID},
		{name: "client secret environment variable " + ClientSecretEnv, value: clientSecret},
	} {
		if strings.TrimSpace(required.value) == "" {
			missing = append(missing, required.name)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required EOS settings: %s", strings.Join(missing, ", "))
	}
	return nil
}

func Init(path string, force bool) error {
	const scaffold = `{
  "product_id": "",
  "sandbox_id": "",
  "deployment_id": "",
  "client_id": ""
}
`

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config directory %q: %w", dir, err)
	}

	temp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set config file permissions: %w", err)
	}
	if _, err := temp.WriteString(scaffold); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write config scaffold: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync config scaffold: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close config scaffold: %w", err)
	}

	if force {
		if err := os.Rename(tempName, path); err != nil {
			return fmt.Errorf("replace config %q: %w", path, err)
		}
		return nil
	}
	if err := os.Link(tempName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("config %q already exists; refusing to overwrite it (use --force to replace)", path)
		}
		return fmt.Errorf("create config %q: %w", path, err)
	}
	return nil
}
