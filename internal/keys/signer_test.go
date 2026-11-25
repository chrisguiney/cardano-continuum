package keys

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

func TestSignersMatchCredentials(t *testing.T) {
	s := newStore(t)
	w := s.Wallet(3)
	pool, err := s.Pool(4, 0)
	if err != nil {
		t.Fatal(err)
	}
	poolSigner, err := s.PoolSigner(4)
	if err != nil {
		t.Fatal(err)
	}
	cold, hot := s.Committee(1)
	coldSigner, hotSigner := s.CommitteeSigners(1)
	tests := []struct {
		name string
		sig  Signer
		want Credential
	}{
		{"payment", w.PaymentSigner(), w.PaymentKeyHash()},
		{"stake", w.StakeSigner(), w.StakeKeyHash()},
		{"pool cold", poolSigner, Credential(pool.PoolID)},
		{"drep", s.DRepSigner(5), s.DRep(5)},
		{"committee cold", coldSigner, cold},
		{"committee hot", hotSigner, hot},
	}
	body := []byte("tx body")
	h := common.Blake2b256Hash(body)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sig.KeyHash(); got != tt.want {
				t.Fatalf("key hash %x, want %x", got, tt.want)
			}
			if got := Credential(common.Blake2b224Hash(tt.sig.VKey())); got != tt.want {
				t.Fatalf("vkey hashes to %x, want %x", got, tt.want)
			}
			wit := tt.sig.SignTxBody(body)
			if !bytes.Equal(wit.VKey, tt.sig.VKey()) {
				t.Fatal("witness vkey differs from signer vkey")
			}
			if err := common.VerifyVKeySignature(wit.VKey, wit.Signature, h[:]); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPaymentSignerMatchesWallet(t *testing.T) {
	w := newStore(t).Wallet(3)
	body := []byte("body")
	a, b := w.SignTxBody(body), w.PaymentSigner().SignTxBody(body)
	if !bytes.Equal(a.Signature, b.Signature) || !bytes.Equal(a.VKey, b.VKey) {
		t.Fatal("PaymentSigner signs differently from Wallet.SignTxBody")
	}
}

func TestSignersRedacted(t *testing.T) {
	s := newStore(t)
	w := s.Wallet(3)
	poolSigner, err := s.PoolSigner(4)
	if err != nil {
		t.Fatal(err)
	}
	cold, hot := s.CommitteeSigners(0)
	cs, err := s.coldSeed(4)
	if err != nil {
		t.Fatal(err)
	}
	secrets := [][]byte{w.stakeKey.PrivateKey(), w.paymentKey.PrivateKey(), cs}
	for _, sg := range []Signer{w.StakeSigner(), w.PaymentSigner(), poolSigner, s.DRepSigner(5), cold, hot} {
		secrets = append(secrets, sg.key.secret())
	}
	for _, v := range []any{w.StakeSigner(), poolSigner, s.DRepSigner(5), cold, &hot} {
		var buf bytes.Buffer
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%x", "%d"} {
			fmt.Fprintf(&buf, f, v)
		}
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(j)
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "v", v)
		out := strings.ToLower(buf.String())
		for _, sec := range secrets {
			if strings.Contains(out, hex.EncodeToString(sec[:8])) {
				t.Fatalf("%T leaks key bytes: %s", v, out)
			}
		}
		if !strings.Contains(out, "redacted") {
			t.Fatalf("%T output not marked redacted: %s", v, out)
		}
	}
}
