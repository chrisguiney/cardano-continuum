package explorer

import (
	"encoding/hex"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
)

// poolReg is a pool registration of operator fill(op, 28): pledge, cost,
// a 1/20 margin, one owner and a metadata URL.
func poolReg(t *testing.T, op byte, pledge uint64) []any {
	margin := append([]byte{0xd8, 0x1e}, enc(t, []any{uint64(1), uint64(20)})...)
	return []any{
		uint(3), fill(op, 28), fill(0x0f, 32), pledge, uint64(340_000_000), cbor.RawMessage(margin),
		append([]byte{0xe0}, fill(0x21, 28)...), []any{fill(0x21, 28)}, []any{},
		[]any{"https://example.test/pool.json", fill(0x13, 32)},
	}
}

func certTx(t *testing.T, in byte, certs ...[]any) []byte {
	cs := make([]any, len(certs))
	for i, c := range certs {
		cs[i] = c
	}
	return txSpec{body: map[uint]any{
		0: []any{[]any{fill(in, 32), uint32(0)}},
		1: []any{map[uint]any{0: shelleyAddr(0xB1), 1: uint64(1_000_000)}},
		2: uint64(200_000),
		4: cs,
	}}.cbor(t)
}

func poolByID(ps []PoolStat, id string) (PoolStat, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return PoolStat{}, false
}

// TestPoolsFromCertificatesAndIssuers: a pool shows its latest registration
// terms, a pending retirement and the blocks it issued; a re-registration
// cancels the retirement, and a rollback undoes the certificates its blocks
// carried.
func TestPoolsFromCertificatesAndIssuers(t *testing.T) {
	g := newGen(t, 7)
	x := newIndex(t, 10)
	pool := hex.EncodeToString(fill(0x55, 28))
	mustAdd(t, x, g.next(certTx(t, 1, poolReg(t, 0x55, 1_000))))
	mustAdd(t, x, g.next(certTx(t, 2, []any{uint(4), fill(0x55, 28), uint64(7)})))

	ps := x.Pools()
	p, ok := poolByID(ps, pool)
	if !ok || p.Reg == nil || p.Reg.Pledge != 1_000 || p.Reg.Cost != 340_000_000 || p.Reg.Margin != 0.05 ||
		p.Reg.Owners != 1 || p.Reg.MetadataURL != "https://example.test/pool.json" || p.Reg.Slot != 5 || p.Blocks != 0 {
		t.Fatalf("registered pool %+v in %+v", p, ps)
	}
	if p.RetireEpoch == nil || *p.RetireEpoch != 7 {
		t.Fatalf("retirement %+v", p.RetireEpoch)
	}
	issuer, ok := poolByID(ps, hex.EncodeToString(g.issuer.Hash().Bytes()))
	if !ok || issuer.Blocks != 2 || issuer.Reg != nil {
		t.Fatalf("issuer %+v", issuer)
	}

	mustAdd(t, x, g.next(certTx(t, 3, poolReg(t, 0x55, 2_000))))
	if p, _ := poolByID(x.Pools(), pool); p.RetireEpoch != nil || p.Reg.Pledge != 2_000 {
		t.Fatalf("re-registered %+v", p)
	}

	if err := x.Rollback(g.slotOf(0), g.hash(0)); err != nil {
		t.Fatal(err)
	}
	p, _ = poolByID(x.Pools(), pool)
	if p.RetireEpoch != nil || p.Reg == nil || p.Reg.Pledge != 1_000 {
		t.Fatalf("after rollback %+v", p)
	}
	if issuer, _ := poolByID(x.Pools(), hex.EncodeToString(g.issuer.Hash().Bytes())); issuer.Blocks != 1 {
		t.Fatalf("issuer after rollback %+v", issuer)
	}
}
