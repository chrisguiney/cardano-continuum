package fakechain

import (
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

func h(b byte) common.Blake2b224 {
	var x common.Blake2b224
	for i := range x {
		x[i] = b
	}
	return x
}

func cred(b byte) credKey { return credKey{kind: common.CredentialTypeAddrKeyHash, hash: h(b)} }

func TestEpochTransition(t *testing.T) {
	s := newCertState(nil)
	id := common.PoolKeyHash(h(9))
	s.accounts[cred(1)] = &account{pool: &id}
	s.accounts[cred(2)] = &account{} // registered, delegated nowhere
	s.pools[id] = &pool{}
	stake := map[common.Blake2b224]uint64{h(1): 70, h(2): 30}

	newEpoch(s, 0, true, stake) // first block: no earlier epoch to close
	if s.epoch.label != 0 || s.epoch.mark != nil {
		t.Fatalf("first block rotated: %+v", s.epoch)
	}
	newEpoch(s, 1, false, stake)
	want := []Share{{Cred: h(1), Pool: h(9), Stake: 70}}
	if len(s.epoch.mark) != 1 || s.epoch.mark[0] != want[0] || s.epoch.set != nil {
		t.Fatalf("epoch 1: %+v", s.epoch)
	}
	// Retirement applies at its epoch, after the snapshot took the stake.
	e := uint64(2)
	s.pools[id].retire = &e
	newEpoch(s, 2, false, stake)
	if len(s.epoch.mark) != 1 || len(s.epoch.set) != 1 {
		t.Fatalf("epoch 2 snapshots: %+v", s.epoch)
	}
	if _, ok := s.pools[id]; ok || !s.epoch.retired[id] || s.accounts[cred(1)].pool != nil {
		t.Fatal("pool not retired or wallet still delegated")
	}
	saved := s.clone()
	newEpoch(s, 4, false, stake) // epoch 3 had no block
	if !s.epoch.frozen || s.epoch.label != 2 {
		t.Fatalf("not frozen: %+v", s.epoch)
	}
	if saved.epoch.frozen {
		t.Fatal("clone shares epoch state")
	}
}
