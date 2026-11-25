// Package keys derives every key in a run from the root seed and an actor
// identifier: wallet payment and stake keys (CIP-1852), pool cold, VRF and
// KES keys, operational certificates, DRep and committee credentials, and the
// faucet.
//
// A key depends only on (seed, network, actor, purpose), never on the order
// in which keys are requested. The mnemonic is never materialised: the root
// key is built straight from 32 bytes of HKDF output.
//
// Types that hold secret material render as a redacted fingerprint under
// every fmt verb, JSON and slog. The only ways out are the explicit
// accessors (TextEnvelope, SignTxBody, OpCert, the Signer constructors).
//
// Changing derivationLabel invalidates every recorded run.
package keys

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/blinklabs-io/bursa"
	"github.com/blinklabs-io/bursa/bip32"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

const derivationLabel = "keys/v1"

// ActorID names an actor. It is the hardened CIP-1852 account index, so it
// must be below 2^31, and the top reserved values are unavailable to actors.
type ActorID uint32

const (
	// FaucetID is the account index of the faucet wallet.
	FaucetID ActorID = 0x7FFFFFFF
	// committeeAccount is the account holding constitutional committee keys.
	committeeAccount ActorID = 0x7FFFFFFE
)

const maxActorID = committeeAccount - 1

// Credential is a 28-byte key hash.
type Credential [28]byte

// Store derives all keys for one run. It is safe for concurrent use: it is
// immutable after New.
type Store struct {
	root      bip32.XPrv
	seed      rng.Seed
	networkID uint8
}

// New builds a Store from the run's root seed. networkID is the address
// network tag (0 testnet, 1 mainnet).
func New(seed rng.Seed, networkID uint8) (*Store, error) {
	if networkID > 15 {
		return nil, fmt.Errorf("network id %d does not fit in an address header", networkID)
	}
	ent := seed.Derive(derivationLabel, "root")
	return &Store{root: rootFromEntropy(ent[:]), seed: seed, networkID: networkID}, nil
}

func rootFromEntropy(ent []byte) bip32.XPrv { return bursa.GetRootKey(ent, nil) }

func (s *Store) redaction() redaction {
	h := s.root.Public().PublicKey().Hash()
	return redaction{"Store", h[:4]}
}

func (s Store) String() string               { return s.redaction().String() }
func (s Store) GoString() string             { return s.redaction().String() }
func (s Store) Format(f fmt.State, _ rune)   { s.redaction().format(f) }
func (s Store) MarshalJSON() ([]byte, error) { return s.redaction().marshal() }
func (s Store) LogValue() slog.Value         { return s.redaction().logValue() }

// Wallet returns the wallet for actor id. It panics if id is reserved or
// out of range; use ValidActor to check untrusted input.
func (s *Store) Wallet(id ActorID) Wallet {
	if id > maxActorID {
		panic(fmt.Sprintf("keys: actor id %d out of range", id))
	}
	return s.mustWallet(id)
}

// Faucet returns the faucet wallet.
func (s *Store) Faucet() Wallet { return s.mustWallet(FaucetID) }

// ValidActor reports whether id may name an actor.
func ValidActor(id ActorID) bool { return id <= maxActorID }

func (s *Store) mustWallet(id ActorID) Wallet {
	w, err := walletFromRoot(s.root, s.networkID, uint32(id))
	if err != nil {
		// Unreachable: indices are range-checked above and bursa fails only
		// on out-of-range indices.
		panic("keys: derive wallet: " + err.Error())
	}
	return w
}

// PoolKeys holds a pool's keys for one KES index. The cold key stays inside
// the Store; OpCert signs with it.
type PoolKeys struct {
	PoolID      [28]byte
	VRFSKey     Envelope
	KESSKey     Envelope
	VRFVKeyHash [32]byte
	VRFVKey     [32]byte
	KESVKey     [32]byte
	ColdVKey    [32]byte
}

func (p PoolKeys) redaction() redaction         { return redaction{"PoolKeys", p.PoolID[:4]} }
func (p PoolKeys) String() string               { return p.redaction().String() }
func (p PoolKeys) GoString() string             { return p.redaction().String() }
func (p PoolKeys) Format(f fmt.State, _ rune)   { p.redaction().format(f) }
func (p PoolKeys) MarshalJSON() ([]byte, error) { return p.redaction().marshal() }
func (p PoolKeys) LogValue() slog.Value         { return p.redaction().logValue() }

// Pool derives actor id's pool keys. The cold and VRF keys depend on id only;
// the KES key also depends on kesIndex, so rotating the KES key keeps the
// pool identity.
func (s *Store) Pool(id ActorID, kesIndex uint32) (PoolKeys, error) {
	if id > maxActorID {
		return PoolKeys{}, fmt.Errorf("pool actor id %d out of range", id)
	}
	cold, err := s.coldSeed(id)
	if err != nil {
		return PoolKeys{}, err
	}
	coldVKey := ed25519.NewKeyFromSeed(cold).Public().(ed25519.PublicKey)

	vrfSeed, err := bursa.GetVRFSeed(s.root, uint32(id))
	if err != nil {
		return PoolKeys{}, fmt.Errorf("vrf seed for actor %d: %w", id, err)
	}
	vrfVKey, vrfSKey, err := bursa.GetVRFKeyPair(vrfSeed)
	if err != nil {
		return PoolKeys{}, fmt.Errorf("vrf key pair for actor %d: %w", id, err)
	}
	vrfFile, err := bursa.GetVRFSKey(vrfSKey)
	if err != nil {
		return PoolKeys{}, fmt.Errorf("vrf envelope for actor %d: %w", id, err)
	}

	// bursa derives KES seeds from a single 31-bit index, which cannot carry
	// (actor, kesIndex), so this seed comes from the run seed instead.
	kesSeed := s.seed.Derive(derivationLabel, "kes", fmt.Sprint(id), fmt.Sprint(kesIndex))
	kesSK, kesVKey, err := bursa.GetKESKeyPair(kesSeed[:])
	if err != nil {
		return PoolKeys{}, fmt.Errorf("kes key pair for actor %d index %d: %w", id, kesIndex, err)
	}
	kesFile, err := bursa.GetKESSKey(kesSK)
	if err != nil {
		return PoolKeys{}, fmt.Errorf("kes envelope for actor %d index %d: %w", id, kesIndex, err)
	}

	vrfEnv, err := envelopeFromFile(vrfFile)
	if err != nil {
		return PoolKeys{}, err
	}
	kesEnv, err := envelopeFromFile(kesFile)
	if err != nil {
		return PoolKeys{}, err
	}
	p := PoolKeys{
		PoolID:      common.Blake2b224Hash(coldVKey),
		VRFSKey:     vrfEnv,
		KESSKey:     kesEnv,
		VRFVKeyHash: common.Blake2b256Hash(vrfVKey),
	}
	copy(p.VRFVKey[:], vrfVKey)
	copy(p.KESVKey[:], kesVKey)
	copy(p.ColdVKey[:], coldVKey)
	return p, nil
}

func (s *Store) coldSeed(id ActorID) ([]byte, error) {
	k, err := bursa.GetPoolColdKey(s.root, 0, uint32(id))
	if err != nil {
		return nil, fmt.Errorf("pool cold key for actor %d: %w", id, err)
	}
	return k.PrivateKey()[:ed25519.SeedSize], nil
}

// OpCert issues the operational certificate for actor id's pool binding the
// KES key at kesIndex, as the cardano-cli CBOR
// [[kes_vkey, counter, kes_period, cold_sig], cold_vkey].
func (s *Store) OpCert(id ActorID, kesIndex uint32, counter, kesPeriod uint64) ([]byte, error) {
	p, err := s.Pool(id, kesIndex)
	if err != nil {
		return nil, err
	}
	cold, err := s.coldSeed(id)
	if err != nil {
		return nil, err
	}
	oc, err := bursa.CreateOperationalCertificate(p.KESVKey[:], counter, kesPeriod, cold)
	if err != nil {
		return nil, fmt.Errorf("opcert for actor %d: %w", id, err)
	}
	out, err := cbor.Encode([]any{
		[]any{oc.KesVkey, oc.IssueNumber, oc.KesPeriod, oc.ColdSignature},
		oc.ColdVkey,
	})
	if err != nil {
		return nil, fmt.Errorf("encode opcert: %w", err)
	}
	return out, nil
}

// DRep returns actor id's DRep key credential.
func (s *Store) DRep(id ActorID) Credential {
	return Credential(s.drepKey(id).Public().PublicKey().Hash())
}

// Committee returns the cold and hot credentials of committee member i.
func (s *Store) Committee(i uint32) (cold, hot Credential) {
	c, h := s.committeeKeys(i)
	return Credential(c.Public().PublicKey().Hash()), Credential(h.Public().PublicKey().Hash())
}

// Envelope is a cardano-cli text envelope holding a secret: the type string,
// description and raw CBOR payload. It renders redacted; TextEnvelope is the
// only way to serialise it.
type Envelope struct {
	Type        string
	Description string
	CBOR        []byte
}

func envelopeFromFile(f bursa.KeyFile) (Envelope, error) {
	raw, err := hex.DecodeString(f.CborHex)
	if err != nil {
		return Envelope{}, fmt.Errorf("decode %s: %w", f.Type, err)
	}
	return Envelope{Type: f.Type, Description: f.Description, CBOR: raw}, nil
}

// TextEnvelope returns the cardano-cli JSON text envelope.
func (e Envelope) TextEnvelope() ([]byte, error) {
	if e.Type == "" || len(e.CBOR) == 0 {
		return nil, errors.New("empty envelope")
	}
	return json.Marshal(struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		CborHex     string `json:"cborHex"`
	}{e.Type, e.Description, hex.EncodeToString(e.CBOR)})
}

func (e Envelope) redaction() redaction         { return redaction{"Envelope " + e.Type, nil} }
func (e Envelope) String() string               { return e.redaction().String() }
func (e Envelope) GoString() string             { return e.redaction().String() }
func (e Envelope) Format(f fmt.State, _ rune)   { e.redaction().format(f) }
func (e Envelope) MarshalJSON() ([]byte, error) { return e.redaction().marshal() }
func (e Envelope) LogValue() slog.Value         { return e.redaction().logValue() }
