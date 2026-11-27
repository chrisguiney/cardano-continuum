package build

import (
	"errors"
	"math/big"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

func (h *harness) poolReq(id keys.ActorID, sp Spend) PoolRegistration {
	h.t.Helper()
	pk, err := h.store.Pool(id, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	cold, err := h.store.PoolSigner(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return PoolRegistration{
		Spend: sp, Cold: cold, VRFKeyHash: pk.VRFVKeyHash,
		Pledge: 1_000_000, Cost: h.pp.MinPoolCost, Margin: big.NewRat(1, 100),
	}
}

// registerPool registers pool id owned by alice and returns its pool id.
func (h *harness) registerPool(id keys.ActorID) [28]byte {
	h.t.Helper()
	got, err := h.b.Pool().RegisterPool(ctx, h.alice, h.poolReq(id, h.spend()))
	if err != nil {
		h.t.Fatal(err)
	}
	h.commit(got)
	pk, _ := h.store.Pool(id, 0)
	return pk.PoolID
}

func TestPoolRegistration(t *testing.T) {
	h := newHarness(t)
	sp := h.spend()
	req := h.poolReq(9, sp)
	got, err := h.b.Pool().RegisterPool(ctx, h.alice, req)
	if err != nil {
		t.Fatal(err)
	}
	pk, _ := h.store.Pool(9, 0)
	tx := h.check(got, sp.Inputs, want{
		deposit: uint64(h.pp.PoolDeposit),
		signers: []keys.Credential{h.alice.PaymentKeyHash(), h.alice.StakeKeyHash(), keys.Credential(pk.PoolID)},
	}, func() (Built, error) { return h.b.Pool().RegisterPool(ctx, h.alice, req) })
	if op := tx.Certificates()[0]; op == nil {
		t.Fatal("no certificate")
	}
	h.commit(got)
	if h.chain.Deposits() != uint64(h.pp.PoolDeposit) {
		t.Fatalf("deposits %d", h.chain.Deposits())
	}

	// A re-registration updates the parameters and takes no deposit.
	sp = h.spend()
	upd := h.poolReq(9, sp)
	upd.Cost++
	got, err = h.b.Pool().UpdatePool(ctx, h.alice, upd)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{
		signers: []keys.Credential{h.alice.PaymentKeyHash(), h.alice.StakeKeyHash(), keys.Credential(pk.PoolID)},
	}, func() (Built, error) { return h.b.Pool().UpdatePool(ctx, h.alice, upd) })
	h.commit(got)

	cheap := h.poolReq(10, h.spend())
	cheap.Cost = h.pp.MinPoolCost - 1
	bad, err := h.b.Pool().RegisterPool(ctx, h.alice, cheap)
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.PoolCostTooLow)
}

func TestPoolRetirement(t *testing.T) {
	h := newHarness(t)
	h.registerPool(9)
	cold, _ := h.store.PoolSigner(9)
	epoch := h.chain.Epoch()
	sp := h.spend()
	req := PoolRetirement{Spend: sp, Cold: cold, Epoch: epoch + 1, CurrentEpoch: epoch}
	got, err := h.b.Pool().RetirePool(ctx, h.alice, req)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{signers: []keys.Credential{h.alice.PaymentKeyHash(), cold.KeyHash()}},
		func() (Built, error) { return h.b.Pool().RetirePool(ctx, h.alice, req) })
	h.commit(got)

	far := req
	far.Spend, far.Epoch = h.spend(), epoch+uint64(h.pp.MaxEpoch)+1
	var re *RetirementEpochError
	if _, err := h.b.Pool().RetirePool(ctx, h.alice, far); !errors.As(err, &re) {
		t.Fatalf("want RetirementEpochError past eMax, got %v", err)
	}

	other, _ := h.store.PoolSigner(10)
	bad, err := h.b.Pool().RetirePool(ctx, h.alice, PoolRetirement{Spend: h.spend(), Cold: other, Epoch: epoch + 1, CurrentEpoch: epoch})
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.PoolNotRegistered)
}

// A pool with no relays encodes them as an empty list. cardano-node's
// decoder wants a list there and drops the submission connection on the
// null a nil slice encodes as ("expected list len or indef"), so no
// population pool ever registered on a live devnet.
func TestPoolCertEncodesNoRelaysAsList(t *testing.T) {
	h := newHarness(t)
	c, err := h.b.Pool().poolCert(h.alice, h.poolReq(9, h.spend()))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cbor.Encode(&c)
	if err != nil {
		t.Fatal(err)
	}
	var fields []cbor.RawMessage
	if _, err := cbor.Decode(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 10 {
		t.Fatalf("certificate has %d fields, want 10", len(fields))
	}
	if relays := fields[8]; len(relays) == 0 || relays[0]>>5 != 4 {
		t.Fatalf("relays encode as %x, want a CBOR list", []byte(relays))
	}
}
