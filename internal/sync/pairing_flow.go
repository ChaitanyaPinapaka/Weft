package sync

import (
	"crypto/hmac"
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
//	req     {initiator_pub, commit}    posted by the NEW device (commits its nonce)
//	key     {responder_pub, nb}        posted by the ENROLLED device (its nonce, in clear)
//	reveal  {na}                       posted by the NEW device (reveals its committed nonce)
//	sealed  vault key sealed for the hop   posted by the ENROLLED device AFTER the human confirms the SAS
//
// The commit-reveal ordering is what makes the short SAS sound (see pairing.go):
// the initiator commits its nonce in `req` BEFORE the responder picks `nb`, and
// the responder picks `nb` BEFORE it learns `na` — so neither side (nor a
// substituting MITM) can adapt a nonce to force a chosen SAS. The vault key is
// sealed only in Finalize, after the human confirms the codes match, so it is
// never sealed to an unconfirmed (possibly attacker-substituted) public key.

type pairReq struct {
	InitiatorPub []byte `json:"initiator_pub"`
	Commit       []byte `json:"commit"`
}
type pairKeyMsg struct {
	ResponderPub []byte `json:"responder_pub"`
	Nb           []byte `json:"nb"`
}
type pairReveal struct {
	Na []byte `json:"na"`
}

func pairPrefix(reqID string) string { return "pairing/" + reqID + "/" }

func randBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

func randHex(n int) (string, error) {
	b, err := randBytes(n)
	return hex.EncodeToString(b), err
}

// --- initiator (the NEW device) --------------------------------------------

// Pairing is the new device's side of an exchange.
type Pairing struct {
	v     *vault.Vault
	cfg   Config
	be    Backend
	kp    pairKeypair
	na    []byte
	reqID string

	respPub  [32]byte
	nb       []byte
	haveResp bool
}

// StartPairing posts a pairing request from the new device: its public key plus a
// commitment to its nonce. The user then runs `weft sync pair-approve <reqID>` on
// an already-enrolled device. cfg carries the new device's cloud config (no passphrase).
func StartPairing(v *vault.Vault, cfg Config) (*Pairing, error) {
	be, err := cfg.backend()
	if err != nil {
		return nil, err
	}
	kp, err := newPairKeypair()
	if err != nil {
		return nil, err
	}
	na, err := randBytes(16)
	if err != nil {
		return nil, err
	}
	reqID, err := randHex(4)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(pairReq{InitiatorPub: kp.Pub[:], Commit: commitNonce(kp.Pub, na)})
	if err := be.Put(pairPrefix(reqID)+"req", body); err != nil {
		return nil, err
	}
	return &Pairing{v: v, cfg: cfg, be: be, kp: kp, na: na, reqID: reqID}, nil
}

// ReqID is the short code the user reads to the enrolled device.
func (p *Pairing) ReqID() string { return p.reqID }

// FetchResponderKey checks once for the enrolled device's key + nonce; once
// present it reveals our own nonce (so the responder can verify the commitment
// and both can compute the SAS). Returns true when ready.
func (p *Pairing) FetchResponderKey() (bool, error) {
	data, err := p.be.Get(pairPrefix(p.reqID) + "key")
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var m pairKeyMsg
	if err := json.Unmarshal(data, &m); err != nil || len(m.ResponderPub) != 32 || len(m.Nb) == 0 {
		return false, errors.New("sync: malformed pairing key")
	}
	copy(p.respPub[:], m.ResponderPub)
	p.nb = m.Nb
	reveal, _ := json.Marshal(pairReveal{Na: p.na}) // reveal our nonce now that they committed
	if err := p.be.Put(pairPrefix(p.reqID)+"reveal", reveal); err != nil {
		return false, err
	}
	p.haveResp = true
	return true, nil
}

// SAS is the 8-digit code to compare against the enrolled device's screen.
func (p *Pairing) SAS() string {
	return pairSAS(p.kp.Pub, p.respPub, p.na, p.nb)
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
	for _, s := range []string{"req", "key", "reveal", "sealed"} {
		_ = p.be.Delete(pairPrefix(p.reqID) + s)
	}
}

// --- responder (an already-enrolled device) --------------------------------

// PairApproval is the enrolled device's side of an exchange.
type PairApproval struct {
	be    Backend
	reqID string
	vk    VaultKey

	initiatorPub [32]byte
	commit       []byte
	kp           pairKeypair
	nb           []byte

	na         []byte
	haveReveal bool
}

// ListPairingRequests returns reqIDs that have a pending request.
func ListPairingRequests(be Backend) ([]string, error) {
	keys, err := be.List("pairing/")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, k := range keys {
		parts := strings.Split(k, "/") // pairing/<reqid>/req
		if len(parts) == 3 && parts[2] == "req" {
			ids = append(ids, parts[1])
		}
	}
	return ids, nil
}

// BeginApprove reads a pairing request and posts the enrolled device's key + nonce
// (its nonce chosen BEFORE it learns the initiator's, which is still committed).
func BeginApprove(be Backend, reqID string, vk VaultKey) (*PairApproval, error) {
	reqBody, err := be.Get(pairPrefix(reqID) + "req")
	if err != nil {
		return nil, errors.New("sync: no such pairing request")
	}
	var req pairReq
	if json.Unmarshal(reqBody, &req) != nil || len(req.InitiatorPub) != 32 || len(req.Commit) == 0 {
		return nil, errors.New("sync: malformed pairing request")
	}
	a := &PairApproval{be: be, reqID: reqID, vk: vk, commit: req.Commit}
	copy(a.initiatorPub[:], req.InitiatorPub)

	kp, err := newPairKeypair()
	if err != nil {
		return nil, err
	}
	nb, err := randBytes(16)
	if err != nil {
		return nil, err
	}
	a.kp, a.nb = kp, nb
	body, _ := json.Marshal(pairKeyMsg{ResponderPub: kp.Pub[:], Nb: nb})
	if err := be.Put(pairPrefix(reqID)+"key", body); err != nil {
		return nil, err
	}
	return a, nil
}

// AwaitReveal checks once for the initiator's revealed nonce and verifies it
// against the commitment from the request. Returns true when ready; errors if the
// reveal doesn't match the commitment (a tampered/rolled request — abort).
func (a *PairApproval) AwaitReveal() (bool, error) {
	data, err := a.be.Get(pairPrefix(a.reqID) + "reveal")
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var r pairReveal
	if json.Unmarshal(data, &r) != nil || len(r.Na) == 0 {
		return false, errors.New("sync: malformed pairing reveal")
	}
	if !hmac.Equal(commitNonce(a.initiatorPub, r.Na), a.commit) {
		return false, errors.New("sync: pairing commitment mismatch — aborting")
	}
	a.na = r.Na
	a.haveReveal = true
	return true, nil
}

// SAS is the 8-digit code to compare against the new device's screen.
func (a *PairApproval) SAS() string {
	return pairSAS(a.initiatorPub, a.kp.Pub, a.na, a.nb)
}

// Finalize seals the vault key for the new device. Call ONLY after the human
// confirms the two SAS codes match.
func (a *PairApproval) Finalize() error {
	if !a.haveReveal {
		return errors.New("sync: call AwaitReveal first")
	}
	pk, err := pairKey(a.kp.priv, a.initiatorPub, a.initiatorPub, a.kp.Pub)
	if err != nil {
		return err
	}
	sealed, err := sealVaultKey(pk, a.vk)
	if err != nil {
		return err
	}
	return a.be.Put(pairPrefix(a.reqID)+"sealed", sealed)
}

// LocalVaultKey returns an enrolled device's vault key: from the secret store if
// cached, else by unwrapping the local keyfile with the passphrase. Used to drive
// pairing approval and to show the recovery phrase.
func LocalVaultKey(v *vault.Vault, passphrase string) (VaultKey, error) {
	if vk, err := loadVaultKey(newSecretStore(v)); err == nil {
		return vk, nil
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
