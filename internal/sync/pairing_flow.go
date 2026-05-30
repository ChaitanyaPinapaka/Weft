package sync

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"weft/internal/vault"
)

// Pairing transport over the bucket. Objects live under pairing/<reqid>/:
//
//	req     {initiator_pub}            posted by the NEW device
//	key     {responder_pub}            posted by the ENROLLED device (lets both show the SAS)
//	sealed  vault key sealed for the hop  posted by the ENROLLED device AFTER the human confirms the SAS
//
// The two-phase order matters: the enrolled device publishes only its public key
// first, so BOTH screens can display the SAS for comparison, and seals the vault
// key only once the human confirms the codes match — so the key is never sealed
// to an unconfirmed (possibly attacker-substituted) public key.

type pairReq struct {
	InitiatorPub []byte `json:"initiator_pub"`
}
type pairKeyMsg struct {
	ResponderPub []byte `json:"responder_pub"`
}

func pairPrefix(reqID string) string { return "pairing/" + reqID + "/" }

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Pairing is the NEW device's side of an exchange.
type Pairing struct {
	v        *vault.Vault
	cfg      Config
	be       Backend
	kp       pairKeypair
	reqID    string
	respPub  [32]byte
	haveResp bool
}

// StartPairing posts a pairing request from the new device. The user then runs
// `weft sync pair-approve <reqID>` on an already-enrolled device. cfg carries the
// cloud config entered on the new device (no passphrase).
func StartPairing(v *vault.Vault, cfg Config) (*Pairing, error) {
	be, err := cfg.backend()
	if err != nil {
		return nil, err
	}
	kp, err := newPairKeypair()
	if err != nil {
		return nil, err
	}
	reqID, err := randHex(4)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(pairReq{InitiatorPub: kp.Pub[:]})
	if err := be.Put(pairPrefix(reqID)+"req", body); err != nil {
		return nil, err
	}
	return &Pairing{v: v, cfg: cfg, be: be, kp: kp, reqID: reqID}, nil
}

// ReqID is the short code the user reads to the enrolled device.
func (p *Pairing) ReqID() string { return p.reqID }

// FetchResponderKey checks once for the enrolled device's public key. Returns
// true once present (after which SAS is available).
func (p *Pairing) FetchResponderKey() (bool, error) {
	data, err := p.be.Get(pairPrefix(p.reqID) + "key")
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var m pairKeyMsg
	if err := json.Unmarshal(data, &m); err != nil || len(m.ResponderPub) != 32 {
		return false, errors.New("sync: malformed pairing key")
	}
	copy(p.respPub[:], m.ResponderPub)
	p.haveResp = true
	return true, nil
}

// SAS is the 6-digit code to compare against the enrolled device's screen.
func (p *Pairing) SAS() string {
	return pairSAS(p.kp.Pub, p.respPub)
}

// Finish checks once for the sealed vault key; if present, unseals it and enrolls
// this device (no passphrase). Returns (nil, nil) if not posted yet. Call only
// after the user confirms the SAS matches.
func (p *Pairing) Finish() (*Engine, error) {
	if !p.haveResp {
		return nil, errors.New("sync: call FetchResponderKey first")
	}
	sealed, err := p.be.Get(pairPrefix(p.reqID) + "sealed")
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pk, err := pairKey(p.kp.priv, p.respPub, p.kp.Pub, p.respPub)
	if err != nil {
		return nil, err
	}
	vk, err := openVaultKey(pk, sealed)
	if err != nil {
		return nil, err
	}
	eng, err := JoinWithKey(p.v, p.cfg, vk)
	if err != nil {
		return nil, err
	}
	p.cleanup()
	return eng, nil
}

func (p *Pairing) cleanup() {
	for _, s := range []string{"req", "key", "sealed"} {
		_ = p.be.Delete(pairPrefix(p.reqID) + s)
	}
}

// ListPairingRequests returns reqIDs that have a pending request (a req object
// but no sealed response yet).
func ListPairingRequests(be Backend) ([]string, error) {
	keys, err := be.List("pairing/")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, k := range keys {
		// pairing/<reqid>/req
		parts := strings.Split(k, "/")
		if len(parts) == 3 && parts[2] == "req" {
			ids = append(ids, parts[1])
		}
	}
	return ids, nil
}

// ApprovePairing is the ENROLLED device's side: it reads the request, posts its
// own public key so both screens can show the SAS, and returns that SAS plus a
// finalize() that seals the vault key for the new device — call finalize ONLY
// after the human confirms the two SAS codes match.
func ApprovePairing(be Backend, reqID string, vk VaultKey) (sas string, finalize func() error, err error) {
	reqBody, err := be.Get(pairPrefix(reqID) + "req")
	if err != nil {
		return "", nil, errors.New("sync: no such pairing request")
	}
	var req pairReq
	if json.Unmarshal(reqBody, &req) != nil || len(req.InitiatorPub) != 32 {
		return "", nil, errors.New("sync: malformed pairing request")
	}
	var initiatorPub [32]byte
	copy(initiatorPub[:], req.InitiatorPub)

	kp, err := newPairKeypair()
	if err != nil {
		return "", nil, err
	}
	keyBody, _ := json.Marshal(pairKeyMsg{ResponderPub: kp.Pub[:]})
	if err := be.Put(pairPrefix(reqID)+"key", keyBody); err != nil {
		return "", nil, err
	}

	sas = pairSAS(initiatorPub, kp.Pub)
	finalize = func() error {
		pk, err := pairKey(kp.priv, initiatorPub, initiatorPub, kp.Pub)
		if err != nil {
			return err
		}
		sealed, err := sealVaultKey(pk, vk)
		if err != nil {
			return err
		}
		return be.Put(pairPrefix(reqID)+"sealed", sealed)
	}
	return sas, finalize, nil
}

// LocalVaultKey returns an enrolled device's vault key: from the keychain if
// cached, else by unwrapping the local keyfile with the passphrase. Used to drive
// pairing approval and to show the recovery phrase.
func LocalVaultKey(v *vault.Vault, passphrase string) (VaultKey, error) {
	if store, _ := newSecretStore(v); store != nil {
		if vk, err := loadVaultKey(store); err == nil {
			return vk, nil
		}
	}
	kfb, err := os.ReadFile(filepath.Join(syncDir(v), keyfileName))
	if err != nil {
		return VaultKey{}, ErrNeedPassphrase
	}
	kf, err := ParseKeyFile(kfb)
	if err != nil {
		return VaultKey{}, err
	}
	return kf.Unwrap(passphrase)
}
