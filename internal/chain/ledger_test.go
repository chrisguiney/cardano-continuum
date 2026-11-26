package chain

import (
	"reflect"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// The test grid has 100-slot epochs.

// nextAt returns a block extending prev at slot.
func nextAt(prev *Block, slot, salt uint64, txs ...Tx) Block {
	b := next(prev, salt, txs...)
	b.Point.Slot = slot
	b.Point.Hash = common.Blake2b256Hash(append(b.Point.Hash[:], byte(slot), byte(salt)))
	return b
}

func credOf(n byte) common.Credential { return common.Credential{Credential: hash224(n)} }

func certTx(id uint64, certs ...common.Certificate) Tx {
	return Tx{Hash: txid(id), Certs: certs}
}

// delegated builds a view where actor 1 (5000 lovelace) is registered and
// delegated to pool 9, and actor 2 (3000) is registered only. Block 1 is at
// slot 1, in epoch 0.
func delegated(t *testing.T, k uint64) (*View, []Block) {
	t.Helper()
	c1, c2, pool := credOf(1), credOf(2), hash224(9)
	v := newView(t, k, utxo(txid(0), 0, addrOf(t, 1), 5000), utxo(txid(0), 1, addrOf(t, 2), 3000))
	b1 := nextAt(nil, 1, 1, certTx(1,
		&common.PoolRegistrationCertificate{Operator: pool},
		&common.StakeRegistrationCertificate{StakeCredential: c1},
		&common.StakeRegistrationCertificate{StakeCredential: c2},
		&common.StakeDelegationCertificate{StakeCredential: &c1, PoolKeyHash: pool},
	))
	mustApply(t, v, b1)
	return v, []Block{b1}
}

func entry(cred byte, pool byte, stake uint64) StakeEntry {
	e := StakeEntry{Cred: hash224(cred), Stake: stake}
	if pool != 0 {
		e.Pool = hash224(pool)
	}
	return e
}

func TestSnapshotsRotateAtBoundaries(t *testing.T) {
	v, chain := delegated(t, 10)
	step := func(slot uint64, txs ...Tx) {
		b := nextAt(&chain[len(chain)-1], slot, uint64(len(chain))+1, txs...)
		chain = append(chain, b)
		mustApply(t, v, b)
	}
	if s := v.Snapshot(); s.Label != 0 || s.Mark != nil || s.Set != nil {
		t.Fatalf("epoch 0: label %d mark %v set %v", s.Label, s.Mark, s.Set)
	}
	// Epoch 1: mark takes the delegation as it stands, set takes the old mark.
	step(101)
	s := v.Snapshot()
	if want := (Snapshot{entry(1, 9, 5000)}); s.Label != 1 || !reflect.DeepEqual(s.Mark, want) || s.Set != nil {
		t.Fatalf("epoch 1: label %d mark %v set %v", s.Label, s.Mark, s.Set)
	}
	// A delegation made in epoch 1 reaches mark in epoch 2 and set in epoch 3.
	c2 := credOf(2)
	step(102, certTx(50, &common.StakeDelegationCertificate{StakeCredential: &c2, PoolKeyHash: hash224(9)}))
	step(103) // same epoch: nothing rotates
	if got := v.Snapshot(); !reflect.DeepEqual(got.Mark, Snapshot{entry(1, 9, 5000)}) {
		t.Fatalf("mid epoch 1: mark %v", got.Mark)
	}
	step(201)
	s = v.Snapshot()
	both := Snapshot{entry(1, 9, 5000), entry(2, 9, 3000)}
	if s.Label != 2 || !reflect.DeepEqual(s.Mark, both) || !reflect.DeepEqual(s.Set, Snapshot{entry(1, 9, 5000)}) {
		t.Fatalf("epoch 2: label %d mark %v set %v", s.Label, s.Mark, s.Set)
	}
	step(301)
	if s = v.Snapshot(); !reflect.DeepEqual(s.Set, both) {
		t.Fatalf("epoch 3: set %v", s.Set)
	}
}

func TestEmptyEpochFreezesRotation(t *testing.T) {
	v, chain := delegated(t, 10)
	b2 := nextAt(&chain[0], 101, 2)
	b3 := nextAt(&b2, 301, 3) // epoch 2 has no block
	b4 := nextAt(&b3, 401, 4)
	mustApply(t, v, b2)
	mustApply(t, v, b3)
	mustApply(t, v, b4)
	s := v.Snapshot()
	if !s.Frozen || s.Label != 1 || len(s.Mark) != 1 || s.Set != nil {
		t.Fatalf("frozen %v label %d mark %v set %v", s.Frozen, s.Label, s.Mark, s.Set)
	}
}

func TestRetirementAppliesAtBoundary(t *testing.T) {
	// K = 1: the guard reads pools and delegation from the settled point,
	// so a block after each step settles it.
	v, chain := delegated(t, 1)
	retire := certTx(60, &common.PoolRetirementCertificate{PoolKeyHash: hash224(9), Epoch: 2})
	b2 := nextAt(&chain[0], 101, 2, retire)
	b3 := nextAt(&b2, 102, 3)
	b4 := nextAt(&b3, 201, 4)
	b5 := nextAt(&b4, 202, 5)
	mustApply(t, v, b2)
	mustApply(t, v, b3)
	if got := v.PolicyState(1, podsAllLive{}, nil).Pools; len(got) != 1 || got[0].Reg != policy.Retiring {
		t.Fatalf("before the boundary: %+v", got)
	}
	mustApply(t, v, b4)
	mustApply(t, v, b5)
	if got := v.PolicyState(2, podsAllLive{}, nil).Pools; len(got) != 1 || got[0].Reg != policy.Retired {
		t.Fatalf("after the boundary: %+v", got)
	}
	// SNAP runs before POOLREAP: the retired pool's stake is in the new
	// mark, and its delegators are gone from the delegation.
	if s := v.Snapshot(); !reflect.DeepEqual(s.Mark, Snapshot{entry(1, 9, 5000)}) {
		t.Fatalf("mark %v", s.Mark)
	}
	if _, ok := v.DelegatedTo(1); ok {
		t.Fatal("wallet still delegated to a retired pool")
	}
	if v.PoolRegistered(9) {
		t.Fatal("retired pool still registered")
	}
}

func TestRollbackRestoresTransition(t *testing.T) {
	v, chain := delegated(t, 10)
	before := v.Snapshot()
	retire := certTx(60, &common.PoolRetirementCertificate{PoolKeyHash: hash224(9), Epoch: 1})
	b2 := nextAt(&chain[0], 101, 2, retire)
	b3 := nextAt(&b2, 201, 3)
	mustApply(t, v, b2)
	mustApply(t, v, b3)
	if _, err := v.Rollback(chain[0].Point); err != nil {
		t.Fatal(err)
	}
	if got := v.Snapshot(); !reflect.DeepEqual(got, before) {
		t.Fatalf("after rollback\n got %+v\nwant %+v", got, before)
	}
	// Restart from the settled base resumes the same epoch state.
	w, err := New(Config{K: 10, Slots: testSlots(t), Book: testBook(t)}, v.Base())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w.Snapshot(), v.Base()) {
		t.Fatal("restart differs from base")
	}
}

type podsAllLive struct{}

func (podsAllLive) Dark(population.ActorID) bool       { return false }
func (podsAllLive) PodRunning(population.ActorID) bool { return true }

func TestPoolStakeAndSaturation(t *testing.T) {
	v, _ := delegated(t, 10)
	// Without NOpt no cap is known.
	for _, p := range v.Pools() {
		if p.Stake != 5000 || p.Saturation != 0 {
			t.Fatalf("no nopt: %+v", p)
		}
	}
	tests := []struct {
		nopt uint64
		want uint64
	}{{1, 5000}, {2, 2500}, {3, 1666}, {0, 0}}
	for _, tc := range tests {
		v.cfg.NOpt = tc.nopt
		ps := v.Pools()
		if len(ps) != 1 || uint64(ps[0].Saturation) != tc.want {
			t.Errorf("nopt %d: %+v, want saturation %d", tc.nopt, ps, tc.want)
		}
	}
}

func TestGovernance(t *testing.T) {
	v, chain := delegated(t, 10)
	v.cfg.GovLifetime = 2
	var _ agenda.Governance = v
	d := hash224(8)
	prop := GovID{Tx: txid(70), Index: 0}
	b2 := nextAt(&chain[0], 101, 2,
		certTx(70, &common.RegistrationDrepCertificate{DrepCredential: credOf(8)}),
	)
	b2.Txs[0].Proposals = []Proposal{{ID: prop}}
	b2.Txs[0].Votes = []Vote{
		{Voter: Voter{Kind: common.VoterTypeDRepKeyHash, Hash: d}, Action: prop, Choice: common.GovVoteYes},
		{Voter: Voter{Kind: common.VoterTypeDRepKeyHash, Hash: hash224(77)}, Action: prop, Choice: common.GovVoteNo}, // not ours
	}
	mustApply(t, v, b2)
	want := []agenda.ProposalInfo{{ID: agenda.ProposalID(prop.String()), Expires: 3}}
	if got := v.OpenProposals(); !reflect.DeepEqual(got, want) {
		t.Fatalf("open %v, want %v", got, want)
	}
	if !v.Voted(8, agenda.ProposalID(prop.String())) {
		t.Error("actor 8 voted")
	}
	if v.Voted(9, agenda.ProposalID(prop.String())) || v.Voted(8, "nope#0") {
		t.Error("unexpected vote")
	}
	if !v.DRepRegistered(8) || !reflect.DeepEqual(v.DReps(), []population.ActorID{8}) {
		t.Errorf("dreps %v", v.DReps())
	}
	c2 := credOf(2)
	if v.VoteDelegated(2) {
		t.Fatal("actor 2 has no vote delegation yet")
	}
	b3 := nextAt(&b2, 102, 3, certTx(71, &common.VoteDelegationCertificate{
		StakeCredential: c2, Drep: common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: d[:]},
	}))
	mustApply(t, v, b3)
	if !v.VoteDelegated(2) || v.VoteDelegated(1) {
		t.Error("vote delegation")
	}
	// The proposal can be voted on through epoch 3; in epoch 4 it is closed.
	b4 := nextAt(&b3, 401, 4)
	mustApply(t, v, b4)
	if got := v.OpenProposals(); len(got) != 0 {
		t.Fatalf("expired proposal listed: %v", got)
	}
	if _, err := v.Rollback(b2.Point); err != nil {
		t.Fatal(err)
	}
	if v.VoteDelegated(2) || len(v.OpenProposals()) != 1 {
		t.Error("rollback did not restore governance")
	}
	if _, err := v.Rollback(chain[0].Point); err != nil {
		t.Fatal(err)
	}
	if len(v.OpenProposals()) != 0 || v.Voted(8, agenda.ProposalID(prop.String())) {
		t.Error("rollback kept the proposal or its vote")
	}
}

func TestPolicyState(t *testing.T) {
	v, chain := delegated(t, 1)
	b2 := nextAt(&chain[0], 101, 2)
	mustApply(t, v, b2)
	pods := darkPods{dark: 9}
	st := v.PolicyState(1, pods, []policy.Intent{{Op: policy.GoDark, Pool: 9}})
	if st.Epoch != 1 || st.Label != 1 {
		t.Errorf("epoch %d label %d", st.Epoch, st.Label)
	}
	wantMark := policy.Schedule{{Wallet: 1, Pool: 9, Stake: 5000}}
	if !reflect.DeepEqual(st.Mark, wantMark) || st.Set != nil {
		t.Errorf("mark %v set %v", st.Mark, st.Set)
	}
	wantDeleg := policy.Schedule{{Wallet: 1, Pool: 9, Stake: 5000}, {Wallet: 2, Stake: 3000}}
	if !reflect.DeepEqual(st.Deleg, wantDeleg) {
		t.Errorf("deleg %v", st.Deleg)
	}
	if want := []policy.Pool{{ID: 9, Reg: policy.Registered, Live: false, Running: true}}; !reflect.DeepEqual(st.Pools, want) {
		t.Errorf("pools %+v", st.Pools)
	}
	if len(st.InFlight) != 1 {
		t.Errorf("in flight %v", st.InFlight)
	}
}

type darkPods struct{ dark population.ActorID }

func (d darkPods) Dark(p population.ActorID) bool   { return p == d.dark }
func (darkPods) PodRunning(population.ActorID) bool { return true }
