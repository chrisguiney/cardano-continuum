package keys

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/blinklabs-io/bursa"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

var update = flag.Bool("update", false, "rewrite testdata golden files")

func seedOne() rng.Seed {
	var s rng.Seed
	s[31] = 1
	return s
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(seedOne(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := "testdata/" + name
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("%s mismatch\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

// The "abandon ... about" mnemonic has 16 zero bytes of entropy, and its
// first mainnet base address is a published CIP-1852 reference value (it
// appears in the cardano-serialization-lib and Ledger test suites).
func TestRootFromEntropyReference(t *testing.T) {
	t.Parallel()
	const want = "addr1qy8ac7qqy0vtulyl7wntmsxc6wex80gvcyjy33qffrhm7sh927ysx5sftuw0dlft05dz3c7revpf7jx0xnlcjz3g69mq4afdhv"
	w, err := walletFromRoot(rootFromEntropy(make([]byte, 16)), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.Address().String(); got != want {
		t.Fatalf("address = %s, want %s", got, want)
	}
}

// Not parallel: rewrites its golden file under -update.
func TestWalletGolden(t *testing.T) {
	s := newStore(t)
	var b strings.Builder
	for _, id := range []ActorID{0, 1} {
		w := s.Wallet(id)
		pk, sk := w.PaymentKeyHash(), w.StakeKeyHash()
		fmt.Fprintf(&b, "actor %d\naddress: %s\nstake: %s\npayment hash: %x\nstake hash: %x\n",
			id, w.Address(), w.StakeAddress(), pk, sk)
	}
	checkGolden(t, "wallet.golden", b.String())
}

func TestWalletDistinctAndStable(t *testing.T) {
	t.Parallel()
	a, b := newStore(t), newStore(t)
	if a.Wallet(3).Address().String() != b.Wallet(3).Address().String() {
		t.Fatal("same seed gave different wallet")
	}
	if a.Wallet(3).Address().String() == a.Wallet(4).Address().String() {
		t.Fatal("actors share an address")
	}
	if a.Faucet().Address().String() == a.Wallet(0).Address().String() {
		t.Fatal("faucet collides with actor 0")
	}
	other, _ := New(rng.Seed{}, 0)
	if other.Wallet(3).Address().String() == a.Wallet(3).Address().String() {
		t.Fatal("different seeds gave the same wallet")
	}
}

// Actor 7's keys must not depend on which other actors were touched first.
func TestOrderIndependence(t *testing.T) {
	t.Parallel()
	a := newStore(t)
	want := a.Wallet(7).Address().String()
	b := newStore(t)
	for i := ActorID(1); i <= 6; i++ {
		b.Wallet(i)
		if _, err := b.Pool(i, 0); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.Wallet(7).Address().String(); got != want {
		t.Fatalf("address depends on derivation order: %s vs %s", got, want)
	}
}

func TestNetworkID(t *testing.T) {
	t.Parallel()
	main, _ := New(seedOne(), 1)
	test, _ := New(seedOne(), 0)
	if !strings.HasPrefix(main.Wallet(0).Address().String(), "addr1") ||
		!strings.HasPrefix(test.Wallet(0).Address().String(), "addr_test1") {
		t.Fatal("network prefix wrong")
	}
}

// Not parallel: rewrites its golden file under -update.
func TestPoolKeysGolden(t *testing.T) {
	s := newStore(t)
	p, err := s.Pool(5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.VRFSKey.Type != "VrfSigningKey_PraosVRF" || p.KESSKey.Type != "KesSigningKey_ed25519_kes_2^6" {
		t.Fatalf("envelope types: %q %q", p.VRFSKey.Type, p.KESSKey.Type)
	}
	p2, _ := s.Pool(5, 1)
	if p2.PoolID != p.PoolID || bytes.Equal(p2.KESSKey.CBOR, p.KESSKey.CBOR) {
		t.Fatal("kes index must change the KES key and keep the pool id")
	}
	if p3, _ := s.Pool(6, 0); p3.PoolID == p.PoolID {
		t.Fatal("pools share an id")
	}
	checkGolden(t, "pool.golden", fmt.Sprintf("pool id: %x\nvrf vkey hash: %x\nkes vkey: %x\n",
		p.PoolID, p.VRFVKeyHash, p.KESVKey))
}

func TestTextEnvelope(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	p, _ := s.Pool(5, 0)
	raw, err := p.KESSKey.TextEnvelope()
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Type, Description, CborHex string }
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "KesSigningKey_ed25519_kes_2^6" || got.CborHex != hex.EncodeToString(p.KESSKey.CBOR) {
		t.Fatalf("bad envelope: %s", raw)
	}
}

func TestOpCertVerifies(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	p, _ := s.Pool(5, 0)
	raw, err := s.OpCert(5, 0, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	d, err := bursa.DecodeOpCert(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.IssueNumber != 3 || d.KESPeriod != 5 || !bytes.Equal(d.KESVKey, p.KESVKey[:]) {
		t.Fatalf("decoded %+v", d)
	}
	if err := ledger.VerifyOpCertSignature(d.LedgerOpCert(), d.ColdVKey); err != nil {
		t.Fatal(err)
	}
	if common.Blake2b224Hash(d.ColdVKey) != common.Blake2b224(p.PoolID) {
		t.Fatal("cold vkey does not match the pool id")
	}
}

func TestSignTxBody(t *testing.T) {
	t.Parallel()
	w := newStore(t).Wallet(2)
	body := []byte("tx body bytes")
	wit := w.SignTxBody(body)
	h := common.Blake2b256Hash(body)
	if err := common.VerifyVKeySignature(wit.VKey, wit.Signature, h[:]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wit.VKey, w.PaymentVKey()) {
		t.Fatal("witness vkey is not the payment key")
	}
}

func TestDRepAndCommittee(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if s.DRep(1) == s.DRep(2) || s.DRep(1) != newStore(t).DRep(1) {
		t.Fatal("drep credential not distinct and stable")
	}
	c1, h1 := s.Committee(0)
	c2, _ := s.Committee(1)
	if c1 == c2 || c1 == h1 {
		t.Fatal("committee credentials collide")
	}
}

func TestSecretsRedacted(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	p, _ := s.Pool(5, 0)
	w := s.Wallet(0)
	secrets := [][]byte{p.KESSKey.CBOR, p.VRFSKey.CBOR, w.paymentKey.PrivateKey(), s.root.PrivateKey()}
	for _, v := range []any{s, *s, p, &p, w, &w, p.KESSKey, p.VRFSKey} {
		var buf bytes.Buffer
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%x", "%d"} {
			fmt.Fprintf(&buf, f, v)
		}
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(j)
		var lb bytes.Buffer
		slog.New(slog.NewJSONHandler(&lb, nil)).Info("x", "v", v)
		buf.Write(lb.Bytes())
		out := strings.ToLower(buf.String())
		for _, sec := range secrets {
			for _, enc := range []string{hex.EncodeToString(sec), hex.EncodeToString(sec[:8])} {
				if strings.Contains(out, enc) {
					t.Fatalf("%T leaks key bytes: %s", v, out)
				}
			}
		}
		if !strings.Contains(out, "redacted") {
			t.Fatalf("%T output not marked redacted: %s", v, out)
		}
	}
}
