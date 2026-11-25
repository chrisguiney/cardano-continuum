package fakechain

import (
	"bytes"
	"errors"
	"testing"

	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

func drain(f *Follower) []Event {
	var out []Event
	for {
		ev, ok := f.Next()
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

func TestFollowerRollsForward(t *testing.T) {
	f := newFixture(t, nil)
	fl := f.chain.Follow(0)
	f.advance(6)
	evs := drain(fl)
	if len(evs) != 3 {
		t.Fatalf("%d events, want 3", len(evs))
	}
	for i, ev := range evs {
		if ev.Kind != RollForward || ev.Block.Number != uint64(i+1) {
			t.Fatalf("event %d = %v, want roll forward to block %d", i, ev, i+1)
		}
		if ev.Tip.Slot != 6 {
			t.Fatalf("event %d tip slot %d, want 6", i, ev.Tip.Slot)
		}
	}
	if _, ok := fl.Next(); ok {
		t.Fatal("caught-up follower produced an event")
	}
}

func TestFollowerRollsBackward(t *testing.T) {
	f := newFixture(t, nil)
	fl := f.chain.Follow(0)
	behind := f.chain.Follow(0)
	f.advance(8)
	drain(fl)
	if ev, _ := behind.Next(); ev.Block.Number != 1 {
		t.Fatal("behind follower did not read block 1")
	}
	keep := f.chain.Blocks()[1]
	if err := f.chain.Inject(Rollback{Depth: 2}); err != nil {
		t.Fatal(err)
	}
	f.advance(10)
	evs := drain(fl)
	if len(evs) != 2 || evs[0].Kind != RollBackward {
		t.Fatalf("events %v, want roll backward then forward", evs)
	}
	if evs[0].Point.Slot != keep.Slot || !bytes.Equal(evs[0].Point.Hash, keep.Hash[:]) {
		t.Fatalf("rolled back to %v, want block 2 at slot %d", evs[0].Point, keep.Slot)
	}
	if evs[1].Kind != RollForward || evs[1].Block.Slot != 10 || evs[1].Block.Number != 3 {
		t.Fatalf("second event %v, want block 3 at slot 10", evs[1])
	}
	// A follower that had not read past the fork sees no rollback.
	for _, ev := range drain(behind) {
		if ev.Kind == RollBackward {
			t.Fatal("follower behind the fork point was rolled back")
		}
	}
}

func TestFollowerLag(t *testing.T) {
	f := newFixture(t, nil)
	fl := f.chain.Follow(2)
	f.advance(10)
	evs := drain(fl)
	if len(evs) != 3 || evs[2].Block.Number != 3 {
		t.Fatalf("lagging follower saw %d blocks, want 3 (5 minus lag 2)", len(evs))
	}
}

// TestFollowFromIntersect models a restart: the engine offers its stored
// points, newest first, and the node answers with the newest still on the
// chain, which is older than the cursor when a rollback happened meanwhile.
func TestFollowFromIntersect(t *testing.T) {
	f := newFixture(t, nil)
	f.advance(8)
	bs := f.chain.Blocks()
	stored := []ocommon.Point{bs[3].Point(), bs[2].Point(), bs[0].Point()}
	if err := f.chain.Inject(Rollback{Depth: 2}); err != nil {
		t.Fatal(err)
	}
	f.advance(10)
	fl, at, err := f.chain.FollowFrom(stored, 0)
	if err != nil {
		t.Fatal(err)
	}
	if at.Slot != bs[0].Slot {
		t.Fatalf("intersected at slot %d, want block 1 at slot %d", at.Slot, bs[0].Slot)
	}
	evs := drain(fl)
	if len(evs) != 2 || evs[0].Block.Number != 2 || evs[1].Block.Slot != 10 {
		t.Fatalf("events after intersect %v, want blocks 2 and 3", evs)
	}
	if _, _, err := f.chain.FollowFrom([]ocommon.Point{bs[3].Point()}, 0); !errors.Is(err, ErrNoIntersect) {
		t.Fatalf("err = %v, want ErrNoIntersect", err)
	}
	if _, at, err := f.chain.FollowFrom([]ocommon.Point{ocommon.NewPointOrigin()}, 0); err != nil || at.Slot != 0 {
		t.Fatalf("origin intersect: %v %v", at, err)
	}
}
