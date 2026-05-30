package sync

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"

	"weft/internal/vault"
)

// SecretStore holds a vault's secrets — the vault key and the cloud secret-access
// key — OUT of the plaintext config. The OS keychain (macOS Keychain, Linux
// Secret Service, Windows Credential Manager) is preferred; on a host with no
// keychain the secrets rest in a 0600 file under the vault instead (never in the
// plaintext config, never pushed to the bucket — .weft is excluded from sync).
// Secrets are scoped per vault (by a hash of its path) so multiple vaults on one
// host don't collide.
type SecretStore interface {
	Get(name string) (string, error) // ErrSecretNotFound if absent
	Set(name, value string) error
	Delete(name string) error
}

// ErrSecretNotFound is returned by Get when a secret is absent.
var ErrSecretNotFound = errors.New("sync: secret not found")

const (
	keyringService = "weft-sync"
	secretVaultKey = "vault-key"    // base64(VaultKey)
	secretCloud    = "cloud-secret" // S3 secret access key
)

func vaultID(v *vault.Vault) string {
	sum := sha256.Sum256([]byte(v.Root))
	return hex.EncodeToString(sum[:8])
}

// newSecretStore returns the keychain store if a trivial probe write succeeds,
// otherwise a 0600 file store under the vault. The probe avoids surprising a
// headless host (no Secret Service / locked keyring) with a hard failure later.
// WEFT_SECRET_STORE=file forces the file store (headless/tests).
func newSecretStore(v *vault.Vault) SecretStore {
	prefix := vaultID(v) + ":"
	if os.Getenv("WEFT_SECRET_STORE") == "file" {
		return fileStore{path: filepath.Join(syncDir(v), "secrets.json")}
	}
	probe := prefix + "_probe"
	if err := keyring.Set(keyringService, probe, "1"); err == nil {
		_ = keyring.Delete(keyringService, probe)
		return keyringStore{prefix: prefix}
	}
	return fileStore{path: filepath.Join(syncDir(v), "secrets.json")}
}

type keyringStore struct{ prefix string }

func (k keyringStore) Get(name string) (string, error) {
	v, err := keyring.Get(keyringService, k.prefix+name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return v, err
}
func (k keyringStore) Set(name, value string) error {
	return keyring.Set(keyringService, k.prefix+name, value)
}
func (k keyringStore) Delete(name string) error {
	if err := keyring.Delete(keyringService, k.prefix+name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// fileStore is the headless fallback: a 0600 JSON map beside the sync config.
type fileStore struct{ path string }

func (f fileStore) load() (map[string]string, error) {
	m := map[string]string{}
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(data, &m)
}

func (f fileStore) Get(name string) (string, error) {
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[name]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (f fileStore) Set(name, value string) error {
	m, err := f.load()
	if err != nil {
		return err
	}
	m[name] = value
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(f.path, data, 0o600)
}

func (f fileStore) Delete(name string) error {
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, name)
	data, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(f.path, data, 0o600)
}

// storeVaultKey / loadVaultKey persist the raw vault key in the secret store, so a
// desktop with an unlocked keychain unlocks the vault without a passphrase prompt.
func storeVaultKey(s SecretStore, vk VaultKey) error {
	return s.Set(secretVaultKey, base64.StdEncoding.EncodeToString(vk[:]))
}

func loadVaultKey(s SecretStore) (VaultKey, error) {
	var vk VaultKey
	enc, err := s.Get(secretVaultKey)
	if err != nil {
		return vk, err
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return vk, err
	}
	if len(raw) != len(vk) {
		return vk, errors.New("sync: stored vault key has wrong length")
	}
	copy(vk[:], raw)
	return vk, nil
}
