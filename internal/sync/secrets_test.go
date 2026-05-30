package sync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSecretStoreRoundTrip(t *testing.T) {
	f := fileStore{path: filepath.Join(t.TempDir(), "secrets.json")}
	if _, err := f.Get("x"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("absent secret should be ErrSecretNotFound, got %v", err)
	}
	if err := f.Set("x", "hello"); err != nil {
		t.Fatal(err)
	}
	if v, err := f.Get("x"); err != nil || v != "hello" {
		t.Fatalf("get x = %q, %v", v, err)
	}
	if info, _ := os.Stat(f.path); info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file must be 0600, got %v", info.Mode().Perm())
	}
	if err := f.Delete("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get("x"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatal("deleted secret should be ErrSecretNotFound")
	}
}

func TestVaultKeyStoreRoundTrip(t *testing.T) {
	f := fileStore{path: filepath.Join(t.TempDir(), "s.json")}
	vk, _ := NewVaultKey()
	if err := storeVaultKey(f, vk); err != nil {
		t.Fatal(err)
	}
	got, err := loadVaultKey(f)
	if err != nil {
		t.Fatal(err)
	}
	if got != vk {
		t.Fatal("vault key did not round-trip through the secret store")
	}
}

func TestVaultKeyMnemonicRoundTrip(t *testing.T) {
	vk, _ := NewVaultKey()
	m, err := VaultKeyMnemonic(vk)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(m)); n != 24 {
		t.Fatalf("vault key recovery phrase should be 24 words, got %d", n)
	}
	got, err := VaultKeyFromMnemonic(m)
	if err != nil {
		t.Fatal(err)
	}
	if got != vk {
		t.Fatal("recovery phrase did not round-trip to the same vault key")
	}
	// Case/whitespace tolerance on input.
	if got2, err := VaultKeyFromMnemonic("  " + strings.ToUpper(m) + "  "); err != nil || got2 != vk {
		t.Fatalf("recovery should tolerate case/whitespace: %v", err)
	}
}

func TestVaultKeyFromMnemonicRejectsGarbage(t *testing.T) {
	if _, err := VaultKeyFromMnemonic("not a real recovery phrase at all"); err == nil {
		t.Fatal("a garbage phrase must be rejected")
	}
}
