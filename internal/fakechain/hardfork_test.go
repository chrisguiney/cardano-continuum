package fakechain

import (
	"math/big"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// hardFork adds a hard fork to major.0 after prev, voted on by the DRep,
// by yes of the committee's hot keys and, when pool, by the one pool.
func (g govWorld) hardFork(tx byte, prev *common.GovActionId, major uint, pool bool, yes int) common.GovActionId {
	id := common.GovActionId{TransactionId: common.Blake2b256{tx}}
	act := &common.HardForkInitiationGovAction{Type: uint(common.GovActionTypeHardForkInitiation), ActionId: prev}
	act.ProtocolVersion.Major = major
	g.s.proposals[id] = proposal{state: common.GovActionState{ActionId: id, Action: act}, expires: 10}
	g.s.votes[voteKey{id, common.VoterTypeDRepKeyHash, h(20)}] = common.GovVoteYes
	for i := range yes {
		g.s.votes[voteKey{id, common.VoterTypeConstitutionalCommitteeHotKeyHash, h(byte(41 + i))}] = common.GovVoteYes
	}
	if pool {
		g.s.votes[voteKey{id, common.VoterTypeStakingPoolKeyHash, h(9)}] = common.GovVoteYes
	}
	return id
}

// withPool gives the world one pool holding all stake in both snapshots.
func (g govWorld) withPool() {
	op := common.PoolKeyHash(h(9))
	g.s.pools[op] = &pool{}
	g.s.accounts[cred(1)].pool = &op
}

func (g govWorld) version() (uint, uint) {
	v := g.s.enact.params.ProtocolVersion
	return v.Major, v.Minor
}

// A hard fork with the committee, the DRep and the pools in favour is
// ratified at the next boundary and enacted at the one after (the ledger's
// one-epoch enactment delay); a parameter change that passes at the
// ratifying boundary is delayed past it, as a hard fork is a delaying
// action ratified ahead of it (Ratify.lagda.md delayingAction,
// Gov.lagda.md govActionPriority).
func TestHardForkRatifiesThenEnacts(t *testing.T) {
	g := newGovWorld()
	g.withPool()
	newEpoch(g.s, 0, true, g.stake)
	newEpoch(g.s, 1, false, g.stake) // the pool's stake reaches mark
	newEpoch(g.s, 2, false, g.stake) // and set, which pool votes are weighed with
	major, _ := g.version()
	fork := g.hardFork(1, nil, major+1, true, 2)
	change := g.propose(2, nil, big.NewRat(1, 4), true, 3)

	newEpoch(g.s, 3, false, g.stake)
	if m, _ := g.version(); m != major || !g.s.proposals[fork].enacted || !sameAction(g.s.enact.hardFork, &fork) {
		t.Fatalf("epoch 3: version %d, ratified %v, root %v; want the fork ratified, not yet enacted", m, g.s.proposals[fork].enacted, g.s.enact.hardFork)
	}

	if g.s.proposals[change].enacted {
		t.Fatal("epoch 3: a parameter change enacted beside a hard fork")
	}

	newEpoch(g.s, 4, false, g.stake)
	if m, n := g.version(); m != major+1 || n != 0 {
		t.Fatalf("epoch 4: version %d.%d, want %d.0", m, n, major+1)
	}

	if !g.s.proposals[change].enacted {
		t.Fatal("epoch 4: the delayed parameter change did not enact")
	}
}

// A pool that does not vote on a hard fork counts as no (Ratify.lagda.md,
// AcceptedBySPO defaultVote), so the fork stays open; one naming a root
// that is not the last enacted hard fork never ratifies.
func TestHardForkNeedsPoolsAndItsRoot(t *testing.T) {
	g := newGovWorld()
	g.withPool()
	newEpoch(g.s, 0, true, g.stake)
	newEpoch(g.s, 1, false, g.stake)
	newEpoch(g.s, 2, false, g.stake)
	major, _ := g.version()
	silent := g.hardFork(1, nil, major+1, false, 3)
	stale := common.GovActionId{TransactionId: common.Blake2b256{99}}
	orphan := g.hardFork(2, &stale, major+1, true, 3)

	newEpoch(g.s, 3, false, g.stake)
	newEpoch(g.s, 4, false, g.stake)
	if g.s.proposals[silent].enacted || g.s.proposals[orphan].enacted {
		t.Fatalf("ratified: no pool vote %v, stale root %v", g.s.proposals[silent].enacted, g.s.proposals[orphan].enacted)
	}

	if m, _ := g.version(); m != major {
		t.Fatalf("version %d, want %d", m, major)
	}
}

// From protocol version 11 the mempool no longer refuses a committee vote
// carrying its own hot-key authorisation: cardano-node's pre-PV11 check
// (Rules/Mempool.hs unelectedCommitteeVoters) is gone and GOV judges the
// vote against the state after the certificates. The chain's rules follow
// the enacted version, not the genesis one.
func TestMempoolCommitteeRuleOffAfterFork(t *testing.T) {
	f, cold, hot := committeeFixture(t)
	f.chain.mu.Lock()
	f.chain.certs.enact.params.ProtocolVersion.Major = 11
	// After each block the mempool is rebuilt from the tip's state, as here.
	txpool{f.chain.world}.revalidate()
	f.chain.mu.Unlock()
	tx := f.voteTx(f.genesisRef(f.alice), testLovelace, []any{authCert(cold, hot)}, hotVote(t, hot, common.Blake2b256{7}), cold, hot)
	err := f.submit(tx.build(t))
	if err == nil {
		return
	}

	if cs := rejectClasses(t, err); hasClass(cs, MempoolFailure) {
		t.Fatalf("classes %v name %s at protocol version 11", cs, MempoolFailure)
	}
}
