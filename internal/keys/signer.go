package keys

import (
	"crypto/ed25519"
	"fmt"
	"log/slog"

	"github.com/blinklabs-io/bursa"
	"github.com/blinklabs-io/bursa/bip32"
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// signingKey is one ed25519 key in either form the keystore holds: a BIP32
// extended key (wallet, DRep and committee keys) or a plain seed (the pool
// cold key, whose identity is the plain ed25519 public key of the seed).
type signingKey interface {
	public() []byte
	sign(msg []byte) []byte
	// secret exposes the private bytes for the redaction test only.
	secret() []byte
}

type xprvKey struct{ k bip32.XPrv }

func (x xprvKey) public() []byte       { return x.k.PublicKey() }
func (x xprvKey) sign(m []byte) []byte { return x.k.Sign(m) }
func (x xprvKey) secret() []byte       { return x.k.PrivateKey() }

type seedKey struct{ k ed25519.PrivateKey }

func (s seedKey) public() []byte       { return s.k.Public().(ed25519.PublicKey) }
func (s seedKey) sign(m []byte) []byte { return ed25519.Sign(s.k, m) }
func (s seedKey) secret() []byte       { return s.k.Seed() }

// Signer is one signing key: a wallet's payment or stake key, a pool cold
// key, a DRep key or a committee cold or hot key. It renders redacted;
// SignTxBody is the only way to use it.
type Signer struct {
	kind string
	key  signingKey
}

// KeyHash returns the credential the key witnesses: Blake2b-224 of VKey.
func (s Signer) KeyHash() Credential { return Credential(common.Blake2b224Hash(s.key.public())) }

// VKey returns the 32-byte public key.
func (s Signer) VKey() []byte { return append([]byte(nil), s.key.public()...) }

// SignTxBody hashes the transaction body CBOR with Blake2b-256 and signs the
// hash.
func (s Signer) SignTxBody(body []byte) Witness {
	h := common.Blake2b256Hash(body)
	return Witness{VKey: s.VKey(), Signature: s.key.sign(h[:])}
}

func (s Signer) redaction() redaction {
	c := s.KeyHash()
	return redaction{"Signer " + s.kind, c[:4]}
}
func (s Signer) String() string               { return s.redaction().String() }
func (s Signer) GoString() string             { return s.redaction().String() }
func (s Signer) Format(f fmt.State, _ rune)   { s.redaction().format(f) }
func (s Signer) MarshalJSON() ([]byte, error) { return s.redaction().marshal() }
func (s Signer) LogValue() slog.Value         { return s.redaction().logValue() }

// PaymentSigner returns the wallet's payment key signer.
func (w Wallet) PaymentSigner() Signer { return Signer{"payment", xprvKey{w.paymentKey}} }

// StakeSigner returns the wallet's stake key signer, which witnesses stake
// certificates, withdrawals and pool ownership.
func (w Wallet) StakeSigner() Signer { return Signer{"stake", xprvKey{w.stakeKey}} }

// PoolSigner returns actor id's pool cold key signer; its KeyHash is the
// pool id.
func (s *Store) PoolSigner(id ActorID) (Signer, error) {
	if id > maxActorID {
		return Signer{}, fmt.Errorf("pool actor id %d out of range", id)
	}
	cold, err := s.coldSeed(id)
	if err != nil {
		return Signer{}, err
	}
	return Signer{"pool", seedKey{ed25519.NewKeyFromSeed(cold)}}, nil
}

// DRepSigner returns actor id's DRep key signer; its KeyHash is DRep(id).
func (s *Store) DRepSigner(id ActorID) Signer { return Signer{"drep", xprvKey{s.drepKey(id)}} }

// CommitteeSigners returns the cold and hot key signers of committee member
// i; their KeyHashes are Committee(i).
func (s *Store) CommitteeSigners(i uint32) (cold, hot Signer) {
	c, h := s.committeeKeys(i)
	return Signer{"committee cold", xprvKey{c}}, Signer{"committee hot", xprvKey{h}}
}

func (s *Store) drepKey(id ActorID) bip32.XPrv {
	acct, err := bursa.GetAccountKey(s.root, uint32(id))
	if err != nil {
		panic("keys: drep account: " + err.Error())
	}
	k, err := bursa.GetDRepKey(acct, 0)
	if err != nil {
		panic("keys: drep key: " + err.Error())
	}
	return k
}

func (s *Store) committeeKeys(i uint32) (cold, hot bip32.XPrv) {
	acct, err := bursa.GetAccountKey(s.root, uint32(committeeAccount))
	if err != nil {
		panic("keys: committee account: " + err.Error())
	}
	c, err := bursa.GetCommitteeColdKey(acct, i)
	if err != nil {
		panic("keys: committee cold: " + err.Error())
	}
	h, err := bursa.GetCommitteeHotKey(acct, i)
	if err != nil {
		panic("keys: committee hot: " + err.Error())
	}
	return c, h
}
