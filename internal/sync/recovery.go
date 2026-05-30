package sync

import (
	"errors"
	"strings"

	"github.com/tyler-smith/go-bip39"
)

// Recovery phrase: the vault key as a 24-word BIP39 mnemonic. The vault key's 32
// bytes ARE the 256-bit entropy, so the phrase recovers a vault even if every
// device and the passphrase are lost — write it down once, offline.

// VaultKeyMnemonic encodes the vault key as its 24-word recovery phrase.
func VaultKeyMnemonic(vk VaultKey) (string, error) {
	return bip39.NewMnemonic(vk[:])
}

// VaultKeyFromMnemonic recovers the vault key from a recovery phrase.
func VaultKeyFromMnemonic(mnemonic string) (VaultKey, error) {
	var vk VaultKey
	mnemonic = strings.Join(strings.Fields(strings.ToLower(mnemonic)), " ")
	if !bip39.IsMnemonicValid(mnemonic) {
		return vk, errors.New("sync: invalid recovery phrase (check the words and their order)")
	}
	entropy, err := bip39.EntropyFromMnemonic(mnemonic)
	if err != nil {
		return vk, err
	}
	if len(entropy) != len(vk) {
		return vk, errors.New("sync: recovery phrase does not encode a 256-bit vault key")
	}
	copy(vk[:], entropy)
	return vk, nil
}
