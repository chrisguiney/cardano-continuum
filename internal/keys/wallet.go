package keys

import (
	"fmt"
	"log/slog"

	"github.com/blinklabs-io/bursa"
	"github.com/blinklabs-io/bursa/bip32"
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// Wallet is one actor's CIP-1852 payment and stake key pair (account index =
// actor id, address index 0).
type Wallet struct {
	paymentKey bip32.XPrv
	stakeKey   bip32.XPrv
	networkID  uint8
}

// Witness is a vkey witness: the public key and its signature.
type Witness struct {
	VKey      []byte
	Signature []byte
}

func walletFromRoot(root bip32.XPrv, networkID uint8, account uint32) (Wallet, error) {
	acct, err := bursa.GetAccountKey(root, account)
	if err != nil {
		return Wallet{}, fmt.Errorf("account %d: %w", account, err)
	}
	pay, err := bursa.GetPaymentKey(acct, 0)
	if err != nil {
		return Wallet{}, fmt.Errorf("payment key: %w", err)
	}
	stake, err := bursa.GetStakeKey(acct, 0)
	if err != nil {
		return Wallet{}, fmt.Errorf("stake key: %w", err)
	}
	return Wallet{paymentKey: pay, stakeKey: stake, networkID: networkID}, nil
}

// PaymentKeyHash returns the payment credential.
func (w Wallet) PaymentKeyHash() Credential {
	return Credential(w.paymentKey.Public().PublicKey().Hash())
}

// StakeKeyHash returns the stake credential.
func (w Wallet) StakeKeyHash() Credential {
	return Credential(w.stakeKey.Public().PublicKey().Hash())
}

// PaymentVKey returns the 32-byte payment public key.
func (w Wallet) PaymentVKey() []byte { return append([]byte(nil), w.paymentKey.PublicKey()...) }

// Address returns the base address (payment key, stake key).
func (w Wallet) Address() common.Address {
	p, s := w.PaymentKeyHash(), w.StakeKeyHash()
	return mustAddress(common.AddressTypeKeyKey, w.networkID, p[:], s[:])
}

// StakeAddress returns the reward address.
func (w Wallet) StakeAddress() common.Address {
	s := w.StakeKeyHash()
	return mustAddress(common.AddressTypeNoneKey, w.networkID, nil, s[:])
}

func mustAddress(t uint8, network uint8, pay, stake []byte) common.Address {
	a, err := common.NewAddressFromParts(t, network, pay, stake)
	if err != nil {
		// Unreachable: credentials are 28 bytes and the network id is checked in New.
		panic("keys: address: " + err.Error())
	}
	return a
}

// SignTxBody hashes the transaction body CBOR with Blake2b-256 and signs the
// hash with the payment key.
func (w Wallet) SignTxBody(body []byte) Witness {
	h := common.Blake2b256Hash(body)
	return Witness{VKey: w.PaymentVKey(), Signature: w.paymentKey.Sign(h[:])}
}

func (w Wallet) redaction() redaction {
	c := w.PaymentKeyHash()
	return redaction{"Wallet", c[:4]}
}
func (w Wallet) String() string               { return w.redaction().String() }
func (w Wallet) GoString() string             { return w.redaction().String() }
func (w Wallet) Format(f fmt.State, _ rune)   { w.redaction().format(f) }
func (w Wallet) MarshalJSON() ([]byte, error) { return w.redaction().marshal() }
func (w Wallet) LogValue() slog.Value         { return w.redaction().logValue() }

// PaymentSigner returns the wallet's payment key signer.
func (w Wallet) PaymentSigner() Signer { return Signer{"payment", xprvKey{w.paymentKey}} }

// StakeSigner returns the wallet's stake key signer, which witnesses stake
// certificates, withdrawals and pool ownership.
func (w Wallet) StakeSigner() Signer { return Signer{"stake", xprvKey{w.stakeKey}} }
