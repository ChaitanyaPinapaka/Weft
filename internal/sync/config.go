package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"weft/internal/vault"
)

// Config is the per-vault sync setup, persisted at <vault>/.weft/sync/config.json
// (0600). Cloud credentials live here in P3; P5 moves them to the OS keychain.
type Config struct {
	Provider        string `json:"provider"` // fs | r2 | aws | minio | b2 | gcs | wasabi
	FSPath          string `json:"fs_path,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	Region          string `json:"region,omitempty"`
	Bucket          string `json:"bucket,omitempty"`
	Prefix          string `json:"prefix,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	PathStyle       bool   `json:"path_style,omitempty"`
}

const (
	configName  = "config.json"
	keyfileName = "keyfile.json"
	metaKey     = "meta/vault.json" // wrapped vault key, in the bucket, for join
)

func syncDir(v *vault.Vault) string { return filepath.Join(v.Root, ".weft", "sync") }

// backend builds the Backend for this config.
func (c Config) backend() (Backend, error) {
	switch c.Provider {
	case "fs":
		if c.FSPath == "" {
			return nil, errors.New("fs provider needs fs_path")
		}
		return NewFileBackend(c.FSPath)
	case "":
		return nil, errors.New("no sync provider configured")
	default: // r2/aws/minio/b2/gcs/wasabi all speak S3
		prefix := c.Prefix
		if prefix == "" {
			prefix = "weft/v1"
		}
		return NewS3Backend(S3Config{
			Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, Prefix: prefix,
			AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, PathStyle: c.PathStyle,
		}), nil
	}
}

// Init sets up sync on a fresh vault: it mints a vault key, wraps it under the
// passphrase, writes the wrapped key to the bucket (so other devices can join
// with the same passphrase) and a local copy + the config, then runs a first
// sync. Refuses if the bucket already holds a vault (use Join instead).
func Init(v *vault.Vault, cfg Config, passphrase string) (*Engine, error) {
	if passphrase == "" {
		return nil, errors.New("a passphrase is required")
	}
	dir := syncDir(v)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, configName)); err == nil {
		return nil, errors.New("sync already initialized for this vault")
	}
	be, err := cfg.backend()
	if err != nil {
		return nil, err
	}
	if ok, _ := be.Head(metaKey); ok {
		return nil, errors.New("this bucket already holds a Weft vault — use `weft sync join`")
	}

	vk, err := NewVaultKey()
	if err != nil {
		return nil, err
	}
	kf, err := WrapVaultKey(vk, passphrase)
	if err != nil {
		return nil, err
	}
	kfBytes, err := MarshalKeyFile(kf)
	if err != nil {
		return nil, err
	}
	if err := be.Put(metaKey, kfBytes); err != nil { // publish wrapped key for join
		return nil, err
	}
	if err := writeConfig(dir, cfg, kfBytes); err != nil {
		return nil, err
	}
	if err := persistSecrets(v, cfg, vk); err != nil {
		return nil, err
	}
	return NewEncrypted(v, be, vk)
}

// Join enrolls a second device using only the shared passphrase: it reads the
// passphrase-wrapped vault key from the bucket, unwraps it, persists the local
// config + keyfile, and returns an engine ready to pull. (P5 adds QR pairing
// without typing the passphrase on the new device.)
func Join(v *vault.Vault, cfg Config, passphrase string) (*Engine, error) {
	if passphrase == "" {
		return nil, errors.New("a passphrase is required")
	}
	dir := syncDir(v)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	be, err := cfg.backend()
	if err != nil {
		return nil, err
	}
	kfBytes, err := be.Get(metaKey)
	if err != nil {
		return nil, errors.New("no Weft vault on this bucket — run `weft sync init` first")
	}
	kf, err := ParseKeyFile(kfBytes)
	if err != nil {
		return nil, err
	}
	vk, err := kf.Unwrap(passphrase)
	if err != nil {
		return nil, err // wrong passphrase
	}
	if err := writeConfig(dir, cfg, kfBytes); err != nil {
		return nil, err
	}
	if err := persistSecrets(v, cfg, vk); err != nil {
		return nil, err
	}
	return NewEncrypted(v, be, vk)
}

// JoinWithKey enrolls this device using a vault key obtained out-of-band — a
// recovery phrase or device pairing — with NO passphrase typed here. It writes
// the config, caches the key + cloud secret, and returns a ready engine.
func JoinWithKey(v *vault.Vault, cfg Config, vk VaultKey) (*Engine, error) {
	dir := syncDir(v)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	be, err := cfg.backend()
	if err != nil {
		return nil, err
	}
	if err := writeConfig(dir, cfg, nil); err != nil {
		return nil, err
	}
	if err := persistSecrets(v, cfg, vk); err != nil {
		return nil, err
	}
	return NewEncrypted(v, be, vk)
}

// OpenLocal opens the engine from the vault key cached in the OS keychain — so a
// desktop with an unlocked keychain unlocks the vault with no passphrase prompt.
// Returns ErrNeedPassphrase when no key is cached (headless / file-fallback host).
func OpenLocal(v *vault.Vault) (*Engine, error) {
	vk, err := loadVaultKey(newSecretStore(v))
	if err != nil {
		return nil, ErrNeedPassphrase
	}
	cfg, err := LoadConfig(v)
	if err != nil {
		return nil, err
	}
	be, err := cfg.backend()
	if err != nil {
		return nil, err
	}
	return NewEncrypted(v, be, vk)
}

// ErrNeedPassphrase signals that no cached vault key is available and the caller
// must fall back to passphrase-based Open.
var ErrNeedPassphrase = errors.New("sync: no cached vault key — passphrase required")

// persistSecrets moves the secrets out of the plaintext config into the secret
// store (OS keychain, or a 0600 file on a keychain-less host): the cloud secret
// access key and the vault key, so the device unlocks without a passphrase.
func persistSecrets(v *vault.Vault, cfg Config, vk VaultKey) error {
	store := newSecretStore(v)
	if cfg.SecretAccessKey != "" {
		if err := store.Set(secretCloud, cfg.SecretAccessKey); err != nil {
			return err
		}
	}
	return storeVaultKey(store, vk)
}

// Open loads an already-configured vault's engine, unwrapping the vault key
// from the local keyfile with the passphrase.
func Open(v *vault.Vault, passphrase string) (*Engine, error) {
	dir := syncDir(v)
	cfg, err := LoadConfig(v)
	if err != nil {
		return nil, err
	}
	kfb, err := os.ReadFile(filepath.Join(dir, keyfileName))
	if err != nil {
		return nil, err
	}
	kf, err := ParseKeyFile(kfb)
	if err != nil {
		return nil, err
	}
	vk, err := kf.Unwrap(passphrase)
	if err != nil {
		return nil, err
	}
	be, err := cfg.backend()
	if err != nil {
		return nil, err
	}
	return NewEncrypted(v, be, vk)
}

// OpenBackend builds the backend from a vault's saved sync config (credentials
// rehydrated from the secret store). Used by flows that talk to the bucket
// without a full engine — e.g. approving a device pairing.
func OpenBackend(v *vault.Vault) (Backend, error) {
	cfg, err := LoadConfig(v)
	if err != nil {
		return nil, err
	}
	return cfg.backend()
}

// Configured reports whether the vault has sync set up.
func Configured(v *vault.Vault) bool {
	_, err := os.Stat(filepath.Join(syncDir(v), configName))
	return err == nil
}

// LoadConfig reads a vault's saved sync config, rehydrating the cloud secret
// access key from the secret store (it's never written to the plaintext config).
func LoadConfig(v *vault.Vault) (Config, error) {
	var cfg Config
	cb, err := os.ReadFile(filepath.Join(syncDir(v), configName))
	if err != nil {
		return cfg, errors.New("sync is not configured")
	}
	if err := json.Unmarshal(cb, &cfg); err != nil {
		return cfg, err
	}
	if cfg.SecretAccessKey == "" {
		if s, err := newSecretStore(v).Get(secretCloud); err == nil {
			cfg.SecretAccessKey = s
		}
	}
	return cfg, nil
}

// CheckConfig verifies the backend end-to-end with a probe object
// (put → get → head → list → delete), confirming credentials + connectivity
// without touching vault data. Run it against a real bucket before trusting
// sync. The probe lives under a _weftcheck/ prefix and is cleaned up.
func CheckConfig(cfg Config) error {
	be, err := cfg.backend()
	if err != nil {
		return err
	}
	const key = "_weftcheck/probe"
	payload := []byte("weft round-trip probe")

	if err := be.Put(key, payload); err != nil {
		return fmt.Errorf("put: %w", err)
	}
	got, err := be.Get(key)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	if string(got) != string(payload) {
		return fmt.Errorf("get returned %d bytes, expected %d", len(got), len(payload))
	}
	if ok, err := be.Head(key); err != nil {
		return fmt.Errorf("head: %w", err)
	} else if !ok {
		return errors.New("head: probe not found after put")
	}
	keys, err := be.List("_weftcheck/")
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	found := false
	for _, k := range keys {
		if k == key {
			found = true
		}
	}
	if !found {
		return errors.New("list: probe not listed under its prefix")
	}
	if err := be.Delete(key); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if ok, _ := be.Head(key); ok {
		return errors.New("delete: probe still present")
	}
	return nil
}

func writeConfig(dir string, cfg Config, keyfile []byte) error {
	cfg.SecretAccessKey = "" // the secret lives in the SecretStore, never plaintext config
	cb, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, configName), cb, 0o600); err != nil {
		return err
	}
	if keyfile == nil {
		// Pairing/recovery: the key is cached in the secret store, not wrapped on
		// disk. Remove any stale keyfile from a prior enrollment so re-enrolling
		// (e.g. a key rotation) invalidates the previously-wrapped key on disk.
		if err := os.Remove(filepath.Join(dir, keyfileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(filepath.Join(dir, keyfileName), keyfile, 0o600)
}
