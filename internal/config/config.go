package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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
	ProductID             string `json:"product_id"`
	SandboxID             string `json:"sandbox_id"`
	DeploymentID          string `json:"deployment_id"`
	ClientID              string `json:"client_id"`
	ClientSecret          string `json:"client_secret,omitempty"`
	EncryptedClientSecret string `json:"encrypted_client_secret,omitempty"`
}

func (cfg Config) EffectiveClientSecret(environmentSecret string) string {
	if strings.TrimSpace(environmentSecret) != "" {
		return environmentSecret
	}
	return cfg.ClientSecret
}

func (cfg Config) ResolveClientSecret(environmentSecret, buildKeyHex string) (string, error) {
	if strings.TrimSpace(environmentSecret) != "" {
		return environmentSecret, nil
	}
	if strings.TrimSpace(cfg.ClientSecret) != "" {
		return cfg.ClientSecret, nil
	}
	if cfg.EncryptedClientSecret == "" {
		return "", nil
	}
	return DecryptClientSecret(cfg.EncryptedClientSecret, buildKeyHex)
}

func EncryptClientSecret(secret, buildKeyHex string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("client secret cannot be empty")
	}
	block, err := newSecretCipher(buildKeyHex)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("initialize AES-GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate config encryption nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(secret), []byte("AJB sidecar config v1"))
	return "v1:" + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func DecryptClientSecret(encrypted, buildKeyHex string) (string, error) {
	if !strings.HasPrefix(encrypted, "v1:") {
		return "", errors.New("unsupported encrypted client-secret format")
	}
	sealed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encrypted, "v1:"))
	if err != nil {
		return "", fmt.Errorf("decode encrypted client secret: %w", err)
	}
	block, err := newSecretCipher(buildKeyHex)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("initialize AES-GCM: %w", err)
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("encrypted client secret is truncated")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte("AJB sidecar config v1"))
	if err != nil {
		return "", errors.New("could not decrypt client secret; config may belong to another build or be corrupted")
	}
	return string(plaintext), nil
}

func newSecretCipher(buildKeyHex string) (cipher.Block, error) {
	key, err := hex.DecodeString(buildKeyHex)
	if err != nil || len(key) != 32 {
		return nil, errors.New("this build has no valid config encryption key; rebuild with scripts/build.sh")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize AES cipher: %w", err)
	}
	return block, nil
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
		{name: "client_secret or environment variable " + ClientSecretEnv, value: clientSecret},
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
  "client_id": "",
  "client_secret": "",
  "encrypted_client_secret": ""
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
