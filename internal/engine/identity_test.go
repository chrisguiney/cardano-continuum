package engine

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/blinklabs-io/bursa"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

func identityWorld(t *testing.T) (*Engine, Config, map[uint64]uint64) {
	t.Helper()
	cfg := testConfig(t, 7, 6)
	w := newWorld(t, cfg)
	p, err := w.start(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.r.Run(context.Background(), 90_000); err != nil {
		t.Fatal(err)
	}
	return p.r.eng, cfg, derive(t, cfg, w.chain)
}

func TestActorRowsCarryPublicIdentity(t *testing.T) {
	e, _, chainBal := identityWorld(t)
	bal := e.Balances()
	rows := e.ActorRows()
	if len(rows) < 6 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, r := range rows {
		id, err := store.DecodeIdentity(r.Identity)
		if err != nil {
			t.Fatal(err)
		}
		w := e.co.wallet(population.ActorID(r.ID))
		if id.PaymentAddr != w.Address().String() || id.StakeAddr != w.StakeAddress().String() {
			t.Errorf("actor %d addresses %q %q", r.ID, id.PaymentAddr, id.StakeAddr)
		}
		pk, sk := w.PaymentKeyHash(), w.StakeKeyHash()
		if id.PaymentKeyHash != hex.EncodeToString(pk[:]) || id.StakeKeyHash != hex.EncodeToString(sk[:]) {
			t.Errorf("actor %d key hashes", r.ID)
		}
		if !strings.HasPrefix(id.PaymentAddr, "addr_test1") || id.Kind != "wallet" || id.Agenda == "" {
			t.Errorf("actor %d identity %+v", r.ID, id)
		}
		if id.Settled != bal[r.ID] || id.Settled != chainBal[r.ID] {
			t.Errorf("actor %d settled %d, ledger %d, chain %d", r.ID, id.Settled, bal[r.ID], chainBal[r.ID])
		}
		if id.Competence == 0 {
			t.Errorf("actor %d has no sampled traits", r.ID)
		}
	}
}

// The identity JSON must never hold a secret: the payment extended key,
// its scalar half, or the root entropy.
func TestActorRowsHoldNoSecretBytes(t *testing.T) {
	e, cfg, _ := identityWorld(t)
	ent := cfg.Seed.Derive("keys").Derive("keys/v1", "root")
	root := bursa.GetRootKey(ent[:], nil)
	secrets := [][]byte{ent[:], root}
	for _, r := range e.ActorRows() {
		acct, err := bursa.GetAccountKey(root, uint32(r.ID))
		if err != nil {
			t.Fatal(err)
		}
		pay, err := bursa.GetPaymentKey(acct, 0)
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, pay, pay[:32])
		for _, s := range secrets {
			if strings.Contains(strings.ToLower(r.Identity), hex.EncodeToString(s)) {
				t.Fatalf("actor %d identity holds secret bytes", r.ID)
			}
		}
	}
}
