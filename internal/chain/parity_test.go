package chain

import (
	"context"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// par drives a fakechain with random certificate, payment and governance
// transactions, follows it with a View, and compares the View with the
// fake's ledger after every step. The fake keeps its own delegation, pool
// and snapshot state computed from its UTxO set (fakechain/epoch.go), so
// agreement means the View's fold over blocks reproduces it.
type par struct {
	t       *testing.T
	r       *rand.Rand
	store   *keys.Store
	chain   *fakechain.Chain
	view    *View
	fol     *fakechain.Follower
	wallets []keys.Wallet
	pools   []keys.PoolKeys
	drep    keys.Credential
	pp      conway.ConwayProtocolParameters
	// rolled and dereg note what the last step did, for tally.
	rolled, dereg bool
}

const (
	parK      = 3
	parWallet = 300_000_000_000
)

var (
	parDRepID = population.ActorID(21)
	parPools  = []population.ActorID{11, 12}
)

func newPar(t *testing.T, seed uint64) *par {
	t.Helper()
	store, err := keys.New(rng.Seed{byte(seed), 5}, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &par{t: t, r: rand.New(rand.NewPCG(seed, 11)), store: store, pp: fakechain.DefaultParams()}
	book := NewBook()
	var gen []fakechain.GenesisOutput
	var gutxo []common.Utxo
	for i := range 3 {
		w := store.Wallet(keys.ActorID(i + 1))
		p.wallets = append(p.wallets, w)
		gen = append(gen, fakechain.GenesisOutput{Address: w.Address(), Lovelace: parWallet})
		if err := book.AddAddress(w.Address(), population.ActorID(i+1)); err != nil {
			t.Fatal(err)
		}
		book.AddStake(common.Blake2b224(w.StakeKeyHash()), population.ActorID(i+1))
		b, _ := w.Address().Bytes()
		gutxo = append(gutxo, utxo(common.Blake2b256Hash(b), 0, w.Address(), parWallet))
	}
	for i, id := range parPools {
		pk, err := store.Pool(keys.ActorID(id), 0)
		if err != nil {
			t.Fatal(err)
		}
		p.pools = append(p.pools, pk)
		book.AddPool(common.Blake2b224(pk.PoolID), parPools[i])
	}
	p.drep = store.DRep(keys.ActorID(parDRepID))
	book.AddDRep(common.Blake2b224(p.drep), parDRepID)
	p.chain, err = fakechain.New(fakechain.Config{Slots: testSlots(t), K: parK, BlockEvery: 2, Genesis: gen})
	if err != nil {
		t.Fatal(err)
	}
	p.view, err = New(Config{K: parK, Slots: testSlots(t), Book: book, GovLifetime: uint64(p.pp.GovActionValidityPeriod)}, State{Utxos: gutxo})
	if err != nil {
		t.Fatal(err)
	}
	p.fol = p.chain.Follow(0)
	return p
}

func credCBOR(h []byte) []any { return []any{0, h} }

func (p *par) drepCBOR() []any { return []any{0, p.drep[:]} }

// op is one random transaction: the certificates and body fields it adds,
// the signers they need, and the net deposit the transaction pays.
type op struct {
	certs   []any
	body    map[uint]any
	signers []keys.Signer
	deposit int64
	pay     uint64
}

func (p *par) pick(n int) int { return p.r.IntN(n) }

func (p *par) op(w keys.Wallet, ledger fakechain.Ledger) op {
	var o op
	stake := w.StakeKeyHash()
	pi := p.pick(len(p.pools))
	pool := p.pools[pi]
	psig, err := p.store.PoolSigner(keys.ActorID(parPools[pi]))
	if err != nil {
		p.t.Fatal(err)
	}
	kd := int64(p.pp.KeyDeposit)
	switch p.pick(13) {
	case 0, 1:
		o.certs = []any{[]any{7, credCBOR(stake[:]), uint64(kd)}}
		o.deposit, o.signers = kd, []keys.Signer{w.StakeSigner()}
	case 2:
		o.certs = []any{[]any{8, credCBOR(stake[:]), uint64(kd)}}
		o.deposit, o.signers = -kd, []keys.Signer{w.StakeSigner()}
	case 3, 4:
		o.certs = []any{[]any{2, credCBOR(stake[:]), pool.PoolID[:]}}
		o.signers = []keys.Signer{w.StakeSigner()}
	case 5:
		o.certs = []any{[]any{9, credCBOR(stake[:]), p.drepCBOR()}}
		o.signers = []keys.Signer{w.StakeSigner()}
	case 6:
		o.certs = []any{[]any{9, credCBOR(stake[:]), []any{2}}}
		o.signers = []keys.Signer{w.StakeSigner()}
	case 7:
		o.certs = []any{[]any{3, pool.PoolID[:], pool.VRFVKeyHash[:], uint64(0), p.pp.MinPoolCost,
			cbor.Tag{Number: 30, Content: []any{1, 100}}, rewardAcct(p.t, w), []any{}, []any{}, nil}}
		if !hasPool(ledger, pool.PoolID) {
			o.deposit = int64(p.pp.PoolDeposit)
		}
		o.signers = []keys.Signer{psig}
	case 8:
		cur := p.chain.Epoch()
		o.certs = []any{[]any{4, pool.PoolID[:], cur + 1 + uint64(p.pick(3))}}
		o.signers = []keys.Signer{psig}
	case 9:
		d := int64(p.pp.DRepDeposit)
		if p.pick(3) == 0 {
			p.dereg = true
			o.certs = []any{[]any{17, p.drepCBOR(), uint64(d)}}
			o.deposit = -d
		} else {
			o.certs = []any{[]any{16, p.drepCBOR(), uint64(d), nil}}
			o.deposit = d
		}
		o.signers = []keys.Signer{p.store.DRepSigner(keys.ActorID(parDRepID))}
	case 10:
		o.pay = 2_000_000 + uint64(p.pick(50_000_000))
	case 11:
		o.body = map[uint]any{20: []any{[]any{p.pp.GovActionDeposit, rewardAcct(p.t, w), []any{6}, []any{"https://example.invalid", make([]byte, 32)}}}}
		o.deposit = int64(p.pp.GovActionDeposit)
	default:
		if len(ledger.Proposals) == 0 {
			o.pay = 2_000_000
			break
		}
		id := ledger.Proposals[p.pick(len(ledger.Proposals))].ID
		o.body = map[uint]any{19: voteCBOR(p.t, p.drep, id, uint8(p.pick(3)))}
		o.signers = []keys.Signer{p.store.DRepSigner(keys.ActorID(parDRepID))}
	}
	return o
}

func hasPool(l fakechain.Ledger, id [28]byte) bool {
	return slices.ContainsFunc(l.Pools, func(s fakechain.PoolStatus) bool { return s.ID == id })
}

func rewardAcct(t *testing.T, w keys.Wallet) []byte {
	t.Helper()
	b, err := w.StakeAddress().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// voteCBOR encodes {voter: {action: [vote, null]}} by hand: a Go map cannot
// have array keys.
func voteCBOR(t *testing.T, drep keys.Credential, id common.GovActionId, choice uint8) cbor.RawMessage {
	t.Helper()
	enc := func(v any) []byte {
		b, err := cbor.Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	inner := append([]byte{0xa1}, enc([]any{id.TransactionId[:], id.GovActionIdx})...)
	inner = append(inner, enc([]any{choice, nil})...)
	out := append([]byte{0xa1}, enc([]any{2, drep[:]})...)
	return append(out, inner...)
}

// submit builds, signs and submits o from w's confirmed outputs, paying
// o.pay to the next wallet and the rest back to w. A rejection is not a
// failure: the fake's rules decide what is valid, and the comparison only
// needs the chain that results.
func (p *par) submit(w keys.Wallet, to keys.Wallet, o op) {
	p.t.Helper()
	us := p.chain.UtxosAt(w.Address())
	var in uint64
	var ins []any
	for _, u := range us {
		in += u.Output.Amount().Uint64()
		ref := RefOf(u.Id)
		ins = append(ins, []any{ref.Tx[:], ref.Index})
	}
	if len(us) == 0 {
		return
	}
	addr := func(a common.Address) []byte {
		b, err := a.Bytes()
		if err != nil {
			p.t.Fatal(err)
		}
		return b
	}
	build := func(fee uint64) []byte {
		change := int64(in) - int64(o.pay) - int64(fee) - o.deposit
		if change < 2_000_000 {
			return nil
		}
		outs := []any{map[uint]any{0: addr(w.Address()), 1: uint64(change)}}
		if o.pay > 0 {
			outs = append(outs, map[uint]any{0: addr(to.Address()), 1: o.pay})
		}
		m := map[uint]any{0: ins, 1: outs, 2: fee}
		if len(o.certs) > 0 {
			m[4] = o.certs
		}
		for k, v := range o.body {
			m[k] = v
		}
		body, err := cbor.Encode(m)
		if err != nil {
			p.t.Fatal(err)
		}
		sig := w.SignTxBody(body)
		wits := []any{[]any{sig.VKey, sig.Signature}}
		for _, s := range o.signers {
			x := s.SignTxBody(body)
			wits = append(wits, []any{x.VKey, x.Signature})
		}
		tx, err := cbor.Encode([]any{cbor.RawMessage(body), map[uint]any{0: wits}, true, nil})
		if err != nil {
			p.t.Fatal(err)
		}
		return tx
	}
	probe := build(1_000_000)
	if probe == nil {
		return
	}
	pt, err := conway.NewConwayTransactionFromCbor(probe)
	if err != nil {
		p.t.Fatal(err)
	}
	fee, err := conway.MinFeeTx(pt, &p.pp)
	if err != nil {
		p.t.Fatal(err)
	}
	if tx := build(fee); tx != nil {
		_ = p.chain.Submit(context.Background(), fakechain.EraConway, tx)
	}
}

func (p *par) step() {
	p.t.Helper()
	ledger := p.chain.Ledger()
	order := p.r.Perm(len(p.wallets))
	for _, i := range order[:1+p.pick(len(order))] {
		p.submit(p.wallets[i], p.wallets[(i+1)%len(p.wallets)], p.op(p.wallets[i], ledger))
	}
	adv := 2 * uint64(1+p.pick(3))
	if p.pick(5) == 0 {
		adv += 20 + uint64(p.pick(110))
	}
	if err := p.chain.AdvanceTo(p.chain.Slot() + adv); err != nil {
		p.t.Fatal(err)
	}
	if room := len(p.chain.Blocks()) - p.chain.Immutable(); room > 0 && p.pick(5) == 0 {
		depth := 1 + uint64(p.pick(room))
		p.rolled = true
		if err := p.chain.Inject(fakechain.Rollback{Depth: depth, Readd: p.pick(2) == 0}); err != nil {
			p.t.Fatal(err)
		}
	}
}

// drain applies every event the follower has.
func (p *par) drain() {
	p.t.Helper()
	conv := &fakeSource{}
	for {
		fe, ok := p.fol.Next()
		if !ok {
			return
		}
		ev, err := conv.convert(fe)
		if err != nil {
			p.t.Fatal(err)
		}
		switch ev.Kind {
		case Forward:
			mustApply(p.t, p.view, ev.Block)
		case Backward:
			if _, err := p.view.Rollback(ev.Point); err != nil {
				p.t.Fatalf("rollback to slot %d: %v", ev.Point.Slot, err)
			}
		}
	}
}

func shares(s Snapshot) []fakechain.Share {
	var out []fakechain.Share
	for _, e := range s {
		out = append(out, fakechain.Share{Cred: e.Cred, Pool: e.Pool, Stake: e.Stake})
	}
	return out
}

func norm[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return s
}

// compare reports every difference between the view and the fake's ledger.
func (p *par) compare(seed uint64, step int) {
	p.t.Helper()
	l, s := p.chain.Ledger(), p.view.Snapshot()
	fail := func(what string, got, want any) {
		p.t.Fatalf("seed %d step %d: %s\n view %+v\n fake %+v", seed, step, what, got, want)
	}
	if s.Label != l.Label || s.Frozen != l.Frozen {
		fail("label", [2]any{s.Label, s.Frozen}, [2]any{l.Label, l.Frozen})
	}
	if got, want := norm(shares(s.Mark)), norm(l.Mark); !reflect.DeepEqual(got, want) {
		fail("mark", got, want)
	}
	if got, want := norm(shares(s.Set)), norm(l.Set); !reflect.DeepEqual(got, want) {
		fail("set", got, want)
	}
	p.compareAccounts(s, l, fail)
	p.comparePools(s, l, fail)
	if got, want := norm(s.DReps), norm(l.DReps); !reflect.DeepEqual(got, want) {
		fail("dreps", got, want)
	}
	p.compareGov(s, l, fail)
}

func (p *par) compareAccounts(s State, l fakechain.Ledger, fail func(string, any, any)) {
	type acct struct {
		Cred    common.Blake2b224
		Pool    *common.Blake2b224
		DKind   int
		DCred   common.Blake2b224
		HasDRep bool
	}
	var got, want []acct
	for _, st := range s.Stake {
		if !st.Registered {
			continue
		}
		a := acct{Cred: st.Cred}
		if st.Delegated {
			pool := st.Pool
			a.Pool = &pool
		}
		if st.Votes {
			a.HasDRep, a.DKind, a.DCred = true, st.DRep.Kind, st.DRep.Cred
		}
		got = append(got, a)
	}
	for _, x := range l.Accounts {
		a := acct{Cred: x.Cred, Pool: x.Pool}
		if x.DRep != nil {
			a.HasDRep, a.DKind = true, x.DRep.Type
			copy(a.DCred[:], x.DRep.Credential)
		}
		want = append(want, a)
	}
	if !reflect.DeepEqual(norm(got), norm(want)) {
		fail("accounts", got, want)
	}
}

func (p *par) comparePools(s State, l fakechain.Ledger, fail func(string, any, any)) {
	type pl struct {
		ID       common.Blake2b224
		RetireAt *uint64
	}
	var got, want []pl
	var gotRetired []common.Blake2b224
	for _, x := range s.Pools {
		switch {
		case x.Retired:
			gotRetired = append(gotRetired, x.ID)
		case x.Retiring:
			at := x.RetireAt
			got = append(got, pl{x.ID, &at})
		default:
			got = append(got, pl{x.ID, nil})
		}
	}
	for _, x := range l.Pools {
		want = append(want, pl{x.ID, x.RetireAt})
	}
	if !reflect.DeepEqual(norm(got), norm(want)) {
		fail("pools", got, want)
	}
	if !reflect.DeepEqual(norm(gotRetired), norm(l.Retired)) {
		fail("retired pools", gotRetired, l.Retired)
	}
}

func (p *par) compareGov(s State, l fakechain.Ledger, fail func(string, any, any)) {
	var gotP, wantP []ProposalState
	gotP = s.Proposals
	for _, x := range l.Proposals {
		wantP = append(wantP, ProposalState{
			ID:      GovID{Tx: x.ID.TransactionId, Index: x.ID.GovActionIdx},
			Expires: testSlots(p.t).EpochOfSlot(x.ExpirySlot),
		})
	}
	if !reflect.DeepEqual(norm(gotP), norm(wantP)) {
		fail("proposals", gotP, wantP)
	}
	var gotV, wantV []VoteState
	gotV = s.Votes
	for _, x := range l.Votes {
		wantV = append(wantV, VoteState{
			Action: GovID{Tx: x.Action.TransactionId, Index: x.Action.GovActionIdx},
			Voter:  Voter{Kind: x.Kind, Hash: x.Hash}, Choice: x.Choice,
		})
	}
	if !reflect.DeepEqual(norm(gotV), norm(wantV)) {
		fail("votes", gotV, wantV)
	}
}

// TestPropFoldedLedgerMatchesFake: after random transactions, epoch
// boundaries and rollbacks of up to K blocks, the view's delegation, pools,
// DReps, proposals, votes and mark and set snapshots equal the fake
// chain's own.
func TestPropFoldedLedgerMatchesFake(t *testing.T) {
	seeds, steps := uint64(16), 50
	if testing.Short() {
		seeds, steps = 3, 25
	}
	seen := map[string]int{}
	for seed := range seeds {
		p := newPar(t, seed)
		for i := range steps {
			p.step()
			p.drain()
			p.compare(seed, i)
			p.tally(seen)
		}
		// The restart path agrees too.
		w, err := New(p.view.cfg, p.view.Base())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(w.Snapshot(), p.view.Base()) {
			t.Fatalf("seed %d: restart from base differs", seed)
		}
	}
	// The run must have reached the states it claims to compare.
	for _, k := range []string{"mark", "set", "retired", "retiring", "proposal", "vote", "votedeleg", "dereg drep", "rollback"} {
		if seen[k] == 0 {
			t.Errorf("random runs never reached %q", k)
		}
	}
	t.Logf("states reached: %v", seen)
}

func (p *par) tally(seen map[string]int) {
	l := p.chain.Ledger()
	for k, ok := range map[string]bool{
		"mark": len(l.Mark) > 0, "set": len(l.Set) > 0, "retired": len(l.Retired) > 0,
		"proposal": len(l.Proposals) > 0, "vote": len(l.Votes) > 0,
		"rollback": p.rolled,
	} {
		if ok {
			seen[k]++
		}
	}
	for _, x := range l.Pools {
		if x.RetireAt != nil {
			seen["retiring"]++
		}
	}
	for _, a := range l.Accounts {
		if a.DRep != nil {
			seen["votedeleg"]++
		}
	}
	if p.dereg {
		seen["dereg drep"]++
	}
	p.rolled, p.dereg = false, false
}
