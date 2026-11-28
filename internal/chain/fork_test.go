package chain

import (
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/protocol"
)

// forkWorld is delegated's view at protocol version 10.0 with actor 2's
// 3000 vote-delegated to DRep 8, committee seat 0 (hot key 50), and in
// epoch 1 (open through epoch 3) a hard fork to 11.0 and a parameter
// change, both voted yes by DRep 8 and the seat; the hard fork also by
// pool 9 when pool is set. Pool 9 holds actor 1's 5000 in the set
// snapshot from the boundary into epoch 2, the first that ratifies.
func forkWorld(t *testing.T, pool bool) (*View, Block, GovID, GovID) {
	t.Helper()
	v, blocks := delegated(t, 10)
	v.base = protocol.Version{Major: 10}
	v.cfg.GovLifetime = 2
	v.cfg.Book.AddCommittee(hash224(50), 0)
	d := hash224(8)
	b2 := nextAt(&blocks[0], 2, 2,
		certTx(70, &common.RegistrationDrepCertificate{DrepCredential: credOf(8)}),
		certTx(71, &common.VoteDelegationCertificate{
			StakeCredential: credOf(2), Drep: common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: d[:]},
		}))
	mustApply(t, v, b2)

	fork, change := GovID{Tx: txid(72)}, GovID{Tx: txid(72), Index: 1}
	to := protocol.Version{Major: 11}
	yes := func(kind uint8, h common.Blake2b224, id GovID) Vote {
		return Vote{Voter: Voter{Kind: kind, Hash: h}, Action: id, Choice: common.GovVoteYes}
	}
	tx := Tx{Hash: txid(72), Proposals: []Proposal{
		{ID: fork, Type: "hard_fork_initiation", Pools: true, Version: &to},
		{ID: change, Type: "parameter_change"},
	}, Votes: []Vote{
		yes(common.VoterTypeDRepKeyHash, d, fork), yes(common.VoterTypeConstitutionalCommitteeHotKeyHash, hash224(50), fork),
		yes(common.VoterTypeDRepKeyHash, d, change), yes(common.VoterTypeConstitutionalCommitteeHotKeyHash, hash224(50), change),
	}}
	if pool {
		tx.Votes = append(tx.Votes, yes(common.VoterTypeStakingPoolKeyHash, hash224(9), fork))
	}
	b3 := nextAt(&b2, 101, 3, tx)
	mustApply(t, v, b3)
	return v, b3, fork, change
}

// A hard fork the committee, the DRep and the pools accept is ratified at
// the boundary into epoch 2 and moves the protocol version at the one into
// epoch 3, as the ledger enacts it (Epoch.lagda.md EPOCH applies the
// ratification of the boundary before). It is a delaying action ratified
// ahead of parameter changes (Ratify.lagda.md delayingAction,
// Gov.lagda.md govActionPriority), so the change that also passes waits a
// boundary (HardForkNoDelayBug, HardForkParamFirstBug in
// models/specs/governance-change); and the version moves a boundary after
// the ratification, not at it (HardForkEnactNowBug).
func TestHardForkRatifiedThenEnacted(t *testing.T) {
	t.Parallel()
	v, b3, fork, change := forkWorld(t, true)
	b4 := nextAt(&b3, 201, 4)
	mustApply(t, v, b4)
	if got := v.VersionAt(2); got != (protocol.Version{Major: 10}) {
		t.Fatalf("epoch 2: version %v, want 10.0 until the next boundary", got)
	}

	p, ok := v.PendingAt(2)
	if !ok || p.ID != fork || p.At != 3 || p.Version != (protocol.Version{Major: 11}) {
		t.Fatalf("epoch 2: pending %+v %v, want the fork enacting at 3", p, ok)
	}

	if r := v.root; r != nil {
		t.Fatalf("epoch 2: parameter change %v ratified beside the hard fork", r)
	}

	for _, o := range v.OpenProposals() {
		if o.Type == "hard_fork_initiation" {
			t.Fatalf("ratified hard fork still open: %+v", o)
		}
	}

	b5 := nextAt(&b4, 301, 5)
	mustApply(t, v, b5)
	if got := v.VersionAt(3); got != (protocol.Version{Major: 11}) {
		t.Fatalf("epoch 3: version %v, want 11.0", got)
	}

	if _, ok := v.PendingAt(3); ok {
		t.Fatal("epoch 3: a fork still pending")
	}

	if v.root == nil || *v.root != change {
		t.Fatalf("epoch 3: parameter root %v, want the delayed change", v.root)
	}

	if r := v.forkRoot(); r == nil || *r != fork {
		t.Fatalf("hard-fork root %v, want %s", r, fork)
	}
}

// A pool that did not vote counts as no on a hard fork (Ratify.lagda.md
// AcceptedBySPO defaultVote), so with the only pool silent the fork is not
// ratified and the change it would have delayed is.
func TestHardForkNeedsPools(t *testing.T) {
	t.Parallel()
	v, b3, _, change := forkWorld(t, false)
	mustApply(t, v, nextAt(&b3, 201, 4))
	if _, ok := v.PendingAt(2); ok {
		t.Fatal("ratified without the pools")
	}

	if v.root == nil || *v.root != change {
		t.Fatalf("parameter root %v, want the change, not delayed", v.root)
	}
}

// The ratification is folded state: a rollback across the boundary takes
// it back, and a view started from a snapshot keeps the fork and its root
// and does not take the hard fork for a parameter change.
func TestHardForkRollsBackAndRestarts(t *testing.T) {
	t.Parallel()
	v, b3, fork, _ := forkWorld(t, true)
	b4 := nextAt(&b3, 201, 4)
	mustApply(t, v, b4)
	if _, err := v.Rollback(b3.Point); err != nil {
		t.Fatal(err)
	}

	if _, ok := v.PendingAt(2); ok || v.forkRoot() != nil {
		t.Fatalf("rollback kept the fork: root %v", v.forkRoot())
	}

	mustApply(t, v, b4)
	cfg := v.cfg
	cfg.Version = protocol.Version{Major: 10}
	r, err := New(cfg, v.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	if p, ok := r.PendingAt(2); !ok || p.ID != fork || r.VersionAt(3) != (protocol.Version{Major: 11}) {
		t.Fatalf("restarted: pending %+v %v, version at 3 %v", p, ok, r.VersionAt(3))
	}

	if r.root != nil {
		t.Fatalf("restarted parameter root %v: the hard fork taken for a change", r.root)
	}
}

// forkOf reads a hard fork's predecessor and version; any other action has
// neither.
func TestForkOf(t *testing.T) {
	t.Parallel()
	parent := common.GovActionId{TransactionId: txid(5), GovActionIdx: 1}
	a := &common.HardForkInitiationGovAction{ActionId: &parent}
	a.ProtocolVersion.Major, a.ProtocolVersion.Minor = 12, 0
	prev, ver := forkOf(a)
	if prev == nil || *prev != (GovID{Tx: txid(5), Index: 1}) || ver == nil || *ver != (protocol.Version{Major: 12}) {
		t.Fatalf("fork: prev %v, version %v", prev, ver)
	}

	if prev, ver := forkOf(&common.InfoGovAction{}); prev != nil || ver != nil {
		t.Fatalf("info: prev %v, version %v", prev, ver)
	}
}

func rb(blocks uint64) Rollback { return Rollback{Blocks: blocks, Slots: blocks * 10} }

func TestRollbackLogCountsDeepestAndLast(t *testing.T) {
	t.Parallel()
	var l RollbackLog
	for _, b := range []uint64{2, 5, 5, 1} {
		l.Add(rb(b))
	}
	if l.Count != 4 || l.Deepest.Blocks != 5 || l.Last.Blocks != 1 {
		t.Fatalf("log %+v", l)
	}
}

func TestRollbackLogKeepsNewestFirstAndBounded(t *testing.T) {
	t.Parallel()
	var l RollbackLog
	for b := uint64(1); b <= RecentRollbacks+3; b++ {
		l.Add(rb(b))
	}
	if len(l.Recent) != RecentRollbacks || l.Recent[0].Blocks != RecentRollbacks+3 || l.Recent[RecentRollbacks-1].Blocks != 4 {
		t.Fatalf("recent %+v", l.Recent)
	}
}

func TestRollbackLogCloneDoesNotAlias(t *testing.T) {
	t.Parallel()
	var l RollbackLog
	l.Add(rb(1))
	c := l.Clone()
	c.Recent[0].Blocks = 99
	if l.Recent[0].Blocks != 1 {
		t.Fatal("clone aliases Recent")
	}
}
