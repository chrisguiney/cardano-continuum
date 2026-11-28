package chain

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

var _ agenda.View = (*View)(nil)

func TestApplyCreatesAndSpends(t *testing.T) {
	t.Parallel()
	a1, a2 := addrOf(t, 1), addrOf(t, 2)
	gen := utxo(txid(0), 0, a1, 100)
	v := newView(t, 3, gen)
	tx := mkTx(txid(1), []Ref{RefOf(gen.Id)}, pay{a2, 60}, pay{a1, 39})
	b := next(nil, 0, tx)

	eff := mustApply(t, v, b)

	want := []EffectKind{Confirmed, Spent, Created, Created}
	if got := kinds(eff); !slices.Equal(got, want) {
		t.Fatalf("effects = %v, want %v", got, want)
	}
	if eff[1].Ref != RefOf(gen.Id) || eff[2].Ref != (Ref{txid(1), 0}) || eff[3].Ref != (Ref{txid(1), 1}) {
		t.Errorf("effect refs out of tx order: %+v", eff)
	}
	if _, ok := v.Utxo(RefOf(gen.Id)); ok {
		t.Error("spent input still in view")
	}
	if got := v.UtxosAt(a2); len(got) != 1 || got[0].Output.Amount().Uint64() != 60 {
		t.Errorf("UtxosAt(a2) = %v", got)
	}
	if v.Balance(1) != 39 || v.Balance(2) != 60 {
		t.Errorf("balances = %d, %d; want 39, 60", v.Balance(1), v.Balance(2))
	}
	if got := v.UtxosOf(1); len(got) != 1 || got[0].Output.Amount().Uint64() != 39 || len(v.UtxosOf(3)) != 0 {
		t.Errorf("UtxosOf(1) = %v", got)
	}
	if v.Tip() != b.Point {
		t.Errorf("tip = %v, want %v", v.Tip(), b.Point)
	}
}

func TestApplyIgnoresForeign(t *testing.T) {
	t.Parallel()
	foreign := addrOf(t, 50)
	v := newView(t, 3)
	eff := mustApply(t, v, next(nil, 0, mkTx(txid(1), []Ref{{txid(99), 0}}, pay{foreign, 5})))
	if len(eff) != 0 {
		t.Errorf("foreign tx produced effects %v", kinds(eff))
	}
	if got := v.Snapshot().Utxos; len(got) != 0 {
		t.Errorf("view stored %d foreign outputs", len(got))
	}
}

func TestApplyRefusesDisconnectedBlock(t *testing.T) {
	t.Parallel()
	v := newView(t, 3)
	b1 := next(nil, 0)
	mustApply(t, v, b1)
	orphan := next(&b1, 7)
	orphan.Prev = txid(5)
	if _, err := v.Apply(orphan); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Apply(orphan) = %v, want ErrNotConnected", err)
	}
	if v.Tip() != b1.Point {
		t.Error("refused block moved the tip")
	}
}

// invalidTx encodes a Conway transaction flagged isValid=false that spends
// in, takes collateral coll and returns change to ret.
func invalidTx(t *testing.T, in, coll Ref, out, ret common.Address) common.Transaction {
	t.Helper()
	ob, err := out.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	rb, err := ret.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	body := map[uint]any{
		0:  []any{[]any{in.Tx[:], in.Index}},
		1:  []any{map[uint]any{0: ob, 1: uint64(10)}},
		2:  uint64(200_000),
		13: []any{[]any{coll.Tx[:], coll.Index}},
		16: map[uint]any{0: rb, 1: uint64(4_800_000)},
		17: uint64(5_000_000),
	}
	bb, err := cbor.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cbor.Encode([]any{cbor.RawMessage(bb), map[uint]any{}, false, nil})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conway.NewConwayTransactionFromCbor(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestInvalidTxUsesCollateral(t *testing.T) {
	t.Parallel()
	a1, a2 := addrOf(t, 1), addrOf(t, 2)
	in := utxo(txid(10), 0, a1, 50)
	coll := utxo(txid(11), 0, a1, 9_800_000)
	v := newView(t, 3, in, coll)
	ltx := invalidTx(t, RefOf(in.Id), RefOf(coll.Id), a2, a1)
	tx := TxFrom(ltx)

	mustApply(t, v, next(nil, 0, tx))

	if _, ok := v.Utxo(RefOf(in.Id)); !ok {
		t.Error("regular input of an invalid tx was spent")
	}
	if _, ok := v.Utxo(RefOf(coll.Id)); ok {
		t.Error("collateral of an invalid tx was not spent")
	}
	if got := v.UtxosAt(a2); len(got) != 0 {
		t.Errorf("invalid tx created its regular output: %v", got)
	}
	// The collateral return takes the index after the regular outputs.
	ret, ok := v.Utxo(Ref{ltx.Hash(), 1})
	if !ok || ret.Output.Amount().Uint64() != 4_800_000 {
		t.Errorf("collateral return = %v, %v", ret, ok)
	}
}

func TestRollbackRestores(t *testing.T) {
	t.Parallel()
	a1, a2 := addrOf(t, 1), addrOf(t, 2)
	gen := utxo(txid(0), 0, a1, 100)
	v := newView(t, 5, gen)
	b1 := next(nil, 0, mkTx(txid(1), []Ref{RefOf(gen.Id)}, pay{a1, 99}))
	b2 := next(&b1, 0, mkTx(txid(2), []Ref{{txid(1), 0}}, pay{a2, 98}))
	b3 := next(&b2, 0, mkTx(txid(3), []Ref{{txid(2), 0}}, pay{a1, 97}))
	mustApply(t, v, b1)
	after1 := v.Snapshot()
	mustApply(t, v, b2)
	mustApply(t, v, b3)

	eff, err := v.Rollback(b1.Point)
	if err != nil {
		t.Fatal(err)
	}

	if got := v.Snapshot(); !reflect.DeepEqual(got, after1) {
		t.Errorf("after rollback\n got %+v\nwant %+v", got, after1)
	}
	var rolled []TxID
	for _, e := range eff {
		if e.Kind == RolledBack {
			rolled = append(rolled, e.Tx)
		}
	}
	if want := []TxID{txid(3), txid(2)}; !slices.Equal(rolled, want) {
		t.Errorf("rolled back %v, want %v (newest first)", rolled, want)
	}
}

func TestSettleDepth(t *testing.T) {
	t.Parallel()
	a1 := addrOf(t, 1)
	gen := utxo(txid(0), 0, a1, 100)
	v := newView(t, 3, gen)
	b1 := next(nil, 0, mkTx(txid(1), []Ref{RefOf(gen.Id)}, pay{a1, 99}))
	chain := []Block{b1}
	for len(chain) < 6 {
		chain = append(chain, next(&chain[len(chain)-1], 0))
	}
	settledAt := map[Point]int{}
	for i, b := range chain[:3] {
		for _, e := range mustApply(t, v, b) {
			if e.Kind == Settled {
				t.Fatalf("block %d settled a block before k successors", i+1)
			}
		}
	}
	// A rollback that removes block 3 must not settle anything, and block
	// 3's replacement settles only after its own third successor.
	if _, err := v.Rollback(chain[1].Point); err != nil {
		t.Fatal(err)
	}
	fork := []Block{next(&chain[1], 1)}
	for len(fork) < 3 {
		fork = append(fork, next(&fork[len(fork)-1], 1))
	}
	for i, b := range fork {
		for _, e := range mustApply(t, v, b) {
			if e.Kind == Settled {
				settledAt[e.Point] = i
			}
		}
	}
	if len(settledAt) != 2 {
		t.Fatalf("settled %d blocks, want 2: %v", len(settledAt), settledAt)
	}
	if settledAt[b1.Point] != 1 || settledAt[chain[1].Point] != 2 {
		t.Errorf("settled at fork steps %v, want b1 at 1, b2 at 2", settledAt)
	}
	if _, ok := settledAt[chain[2].Point]; ok {
		t.Error("a rolled-back block was settled")
	}
}

func TestSettleReportsSlot(t *testing.T) {
	t.Parallel()
	v := newView(t, 2)
	b1 := next(nil, 0)
	b1.Point.Slot = 40
	b2 := next(&b1, 0)
	b3 := next(&b2, 0)
	mustApply(t, v, b1)
	mustApply(t, v, b2)
	eff := mustApply(t, v, b3)
	if len(eff) != 1 || eff[0].Kind != Settled || eff[0].Point.Slot != 40 {
		t.Fatalf("effects = %+v, want one Settled at slot 40", eff)
	}
	if v.Settled().Slot != 40 {
		t.Errorf("Settled() = %v, want slot 40", v.Settled())
	}
}

func TestSettledPointNeverMovesBack(t *testing.T) {
	t.Parallel()
	v := newView(t, 2)
	chain := []Block{next(nil, 0)}
	for len(chain) < 5 {
		chain = append(chain, next(&chain[len(chain)-1], 0))
	}
	for _, b := range chain {
		mustApply(t, v, b)
	}
	settled := v.Settled()
	if settled != chain[2].Point {
		t.Fatalf("settled = %v, want block 3", settled)
	}
	// Tip minus k would now be block 2; the settled point stays at 3.
	for _, to := range []Point{chain[3].Point, chain[2].Point} {
		if _, err := v.Rollback(to); err != nil {
			t.Fatal(err)
		}
		if v.Settled() != settled {
			t.Errorf("settled moved from %v to %v on rollback", settled, v.Settled())
		}
	}
	// A fresh block on the shortened chain does not settle anything until
	// it has k successors of its own.
	for _, e := range mustApply(t, v, next(&chain[2], 1)) {
		if e.Kind == Settled {
			t.Errorf("settled %v right after the rollback", e.Point)
		}
	}
	if v.Settled() != settled {
		t.Errorf("settled moved to %v", v.Settled())
	}
}

func TestRollbackDeeperThanKIsError(t *testing.T) {
	t.Parallel()
	v := newView(t, 2)
	chain := []Block{next(nil, 0)}
	for len(chain) < 4 {
		chain = append(chain, next(&chain[len(chain)-1], 0))
	}
	for _, b := range chain {
		mustApply(t, v, b)
	}
	before := v.Snapshot()
	for _, to := range []Point{chain[0].Point, {}, {Slot: 3, Hash: txid(77)}} {
		if _, err := v.Rollback(to); !errors.Is(err, ErrTooDeep) {
			t.Errorf("Rollback(%v) = %v, want ErrTooDeep", to, err)
		}
	}
	if !reflect.DeepEqual(v.Snapshot(), before) || v.Tip() != chain[3].Point {
		t.Error("a refused rollback changed the view")
	}
}

func TestRollbackToTipIsNoop(t *testing.T) {
	t.Parallel()
	v := newView(t, 2)
	b := next(nil, 0)
	mustApply(t, v, b)
	eff, err := v.Rollback(b.Point)
	if err != nil || len(eff) != 0 {
		t.Fatalf("Rollback(tip) = %v, %v", eff, err)
	}
}

func TestBaseReplaysToLive(t *testing.T) {
	t.Parallel()
	a1, a2 := addrOf(t, 1), addrOf(t, 2)
	gen := utxo(txid(0), 0, a1, 100)
	v := newView(t, 2, gen)
	b1 := next(nil, 0, mkTx(txid(1), []Ref{RefOf(gen.Id)}, pay{a2, 99}))
	b2 := next(&b1, 0, mkTx(txid(2), []Ref{{txid(1), 0}}, pay{a1, 98}))
	b3 := next(&b2, 0, mkTx(txid(3), []Ref{{txid(2), 0}}, pay{a2, 97}))
	b4 := next(&b3, 0)
	for _, b := range []Block{b1, b2, b3, b4} {
		mustApply(t, v, b)
	}
	base := v.Base()
	if base.At != b2.Point || base.Height != 2 {
		t.Fatalf("Base at %v height %d, want block 2", base.At, base.Height)
	}
	r, err := New(Config{K: 2, Slots: testSlots(t), Book: testBook(t)}, base)
	if err != nil {
		t.Fatal(err)
	}
	if r.Settled() != b2.Point || r.Tip() != b2.Point {
		t.Errorf("restored view settled %v tip %v", r.Settled(), r.Tip())
	}
	for _, b := range []Block{b3, b4} {
		mustApply(t, r, b)
	}
	if !reflect.DeepEqual(r.Snapshot(), v.Snapshot()) {
		t.Errorf("base + replay\n got %+v\nwant %+v", r.Snapshot(), v.Snapshot())
	}
}

func TestCertificatesTrackStakeAndPools(t *testing.T) {
	t.Parallel()
	v := newView(t, 2)
	pool := hash224(9)
	cred := common.Credential{Credential: hash224(1)}
	foreign := common.Credential{Credential: hash224(70)}
	tx := Tx{Hash: txid(1), Certs: []common.Certificate{
		&common.PoolRegistrationCertificate{Operator: pool},
		&common.RegistrationCertificate{StakeCredential: cred},
		&common.StakeDelegationCertificate{StakeCredential: &cred, PoolKeyHash: pool},
		&common.RegistrationCertificate{StakeCredential: foreign},
		&common.RegistrationDrepCertificate{DrepCredential: common.Credential{Credential: hash224(8)}},
	}}
	b1 := next(nil, 0, tx)
	eff := mustApply(t, v, b1)
	if want := []EffectKind{Confirmed, CertSeen, CertSeen, CertSeen, CertSeen}; !slices.Equal(kinds(eff), want) {
		t.Errorf("effects = %v, want %v", kinds(eff), want)
	}
	if !v.StakeRegistered(1) || v.StakeRegistered(2) {
		t.Error("stake registration wrong")
	}
	if p, ok := v.DelegatedTo(1); !ok || p != 9 {
		t.Errorf("DelegatedTo(1) = %d, %v", p, ok)
	}
	if !v.PoolRegistered(9) || !v.DRepRegistered(8) {
		t.Error("pool or drep not registered")
	}
	if got := v.Pools(); len(got) != 1 || got[0].ID != 9 {
		t.Errorf("Pools() = %v", got)
	}

	retire := Tx{Hash: txid(2), Certs: []common.Certificate{
		&common.PoolRetirementCertificate{PoolKeyHash: pool, Epoch: 1},
		&common.DeregistrationCertificate{StakeCredential: cred},
	}}
	b2 := next(&b1, 0, retire)
	mustApply(t, v, b2)
	if v.StakeRegistered(1) {
		t.Error("deregistered stake still registered")
	}
	if _, ok := v.DelegatedTo(1); ok {
		t.Error("deregistration kept the delegation")
	}
	if !v.PoolRegistered(9) {
		t.Error("pool retired before its epoch")
	}
	b3 := next(&b2, 0)
	b3.Point.Slot = 100 // epoch 1 at 100 slots per epoch
	mustApply(t, v, b3)
	if v.PoolRegistered(9) || v.Epoch() != 1 {
		t.Errorf("at epoch %d pool registered = %v", v.Epoch(), v.PoolRegistered(9))
	}

	if _, err := v.Rollback(b1.Point); err != nil {
		t.Fatal(err)
	}
	if !v.StakeRegistered(1) || !v.PoolRegistered(9) {
		t.Error("rollback did not restore certificate state")
	}
	if p, ok := v.DelegatedTo(1); !ok || p != population.ActorID(9) {
		t.Error("rollback did not restore the delegation")
	}
}

func TestSetReturnsFeedsPools(t *testing.T) {
	t.Parallel()
	v := newView(t, 2)
	v2, err := New(Config{K: 2, Slots: testSlots(t), Book: testBook(t)},
		State{Pools: []PoolState{{ID: hash224(9)}}})
	if err != nil {
		t.Fatal(err)
	}
	v2.SetReturns(map[population.ActorID]float64{9: 0.04})
	if got := v2.Pools(); len(got) != 1 || got[0].ROI != 0.04 {
		t.Errorf("Pools() = %v", got)
	}
	if len(v.Pools()) != 0 {
		t.Error("empty view lists pools")
	}
}

// TestTxFromFee: a valid transaction pays its body fee; an invalid one
// forfeits its total collateral, and the body fee never reaches the pot.
func TestTxFromFee(t *testing.T) {
	t.Parallel()
	a1, a2 := addrOf(t, 1), addrOf(t, 2)
	in := utxo(txid(10), 0, a1, 50)
	coll := utxo(txid(11), 0, a1, 9_800_000)
	if got := TxFrom(invalidTx(t, RefOf(in.Id), RefOf(coll.Id), a2, a1)).Fee; got != 5_000_000 {
		t.Errorf("invalid tx fee %d, want its total collateral 5000000", got)
	}
}
