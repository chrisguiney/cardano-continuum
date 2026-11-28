package view

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
)

// A treasury withdrawal or parameter change whose votes meet the devnet's
// thresholds shows as ratified; one short of the committee's two thirds or
// the DReps' 67%, an info action, or a security-group change without the
// pools does not.
func TestRatifiedByTally(t *testing.T) {
	pass := map[string]Tally{
		classCommittee: {Yes: 2, No: 1},
		classDRep:      {Yes: 3, YesPower: 80, NoPower: 20},
	}
	shortCC := map[string]Tally{classCommittee: {Yes: 1, No: 2}, classDRep: pass[classDRep]}
	shortDRep := map[string]Tally{classCommittee: pass[classCommittee], classDRep: {Yes: 1, YesPower: 60, NoPower: 40}}
	withPools := map[string]Tally{classCommittee: pass[classCommittee], classDRep: pass[classDRep], classSPO: {Yes: 1, YesPower: 60, NoPower: 40}}
	for _, tc := range []struct {
		name string
		p    ProposalRow
		want bool
	}{
		{"treasury", ProposalRow{Type: "treasury_withdrawals", Tally: pass}, true},
		{"parameter", ProposalRow{Type: "parameter_change", Tally: pass}, true},
		{"info", ProposalRow{Type: "info", Tally: pass}, false},
		{"committee short", ProposalRow{Type: "parameter_change", Tally: shortCC}, false},
		{"DReps short", ProposalRow{Type: "parameter_change", Tally: shortDRep}, false},
		{"security without pools", ProposalRow{Type: "parameter_change", PoolsMayVote: true, Tally: pass}, false},
		{"security with pools", ProposalRow{Type: "parameter_change", PoolsMayVote: true, Tally: withPools}, true},
	} {
		if got := ratified(tc.p); got != tc.want {
			t.Errorf("%s: ratified = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// The Governance view and the Overview show the protocol version the
// engine's Protocol decisions report: the genesis version, a ratified hard
// fork as pending with its proposal ratified, then the new version with
// the proposal enacted.
func TestGovernanceFollowsProtocolDecisions(t *testing.T) {
	p := NewProjection()
	id := proposalID(engine.TxID{7}, 0)
	block := engine.BlockContent{Txs: []engine.TxDigest{{ID: engine.TxID{7}, Proposals: 1, Gov: []engine.ProposalDigest{{Type: "hard_fork_initiation", Pools: true}}}}}
	body, err := eventlog.Encode(eventlog.BlockApplied{Point: eventlog.ChainPoint{Slot: 10, Hash: make([]byte, 32)}, Content: mustEncode(t, block)})
	if err != nil {
		t.Fatal(err)
	}
	p.Apply(eventlog.Record{Seq: 1, At: 10_000, Class: eventlog.Input, Kind: eventlog.KindBlockApplied, Body: body})

	decide := func(seq uint64, pr engine.Protocol) {
		p.Apply(eventlog.Record{Seq: seq, At: 10_000, Class: eventlog.Decision, Kind: engine.KindProtocol, Body: mustEncode(t, pr)})
	}
	v10, v11 := protocol.Version{Major: 10}, protocol.Version{Major: 11}
	status := func() (GovernanceView, string) {
		g := p.Governance(nil, 2)
		for _, r := range g.Proposals {
			if r.ID == id {
				return g, r.Status
			}
		}
		t.Fatalf("proposal %s missing: %+v", id, g.Proposals)
		return g, ""
	}

	if g, _ := status(); g.Protocol != nil || p.Snapshot(nil).Protocol != nil {
		t.Fatalf("a version before any Protocol decision: %v", g.Protocol)
	}

	decide(2, engine.Protocol{Epoch: 0, Version: v10})
	decide(3, engine.Protocol{Epoch: 1, Version: v10, Pending: &engine.Fork{Proposal: id, Version: v11, At: 2}})
	if g, st := status(); *g.Protocol != v10 || g.Fork == nil || g.Fork.Version != v11 || st != "ratified" {
		t.Fatalf("pending: version %v, fork %+v, status %q", g.Protocol, g.Fork, st)
	}

	decide(4, engine.Protocol{Epoch: 2, Version: v11})
	if g, st := status(); *g.Protocol != v11 || g.Fork != nil || st != "enacted" || *p.Snapshot(nil).Protocol != v11 {
		t.Fatalf("enacted: version %v, fork %+v, status %q", g.Protocol, g.Fork, st)
	}
}

// A run with a schedule says so on both views until its version is in
// force: the Protocol decisions carry the plan, and the views show it with
// the open hard-fork proposal's status between.
func TestViewsShowTheScheduledFork(t *testing.T) {
	p := NewProjection()
	v10, v11 := protocol.Version{Major: 10}, protocol.Version{Major: 11}
	plan := &protocol.Schedule{To: v11, Epoch: 3}
	decide := func(seq uint64, pr engine.Protocol) {
		p.Apply(eventlog.Record{Seq: seq, At: 10_000, Class: eventlog.Decision, Kind: engine.KindProtocol, Body: mustEncode(t, pr)})
	}

	if g := p.Governance(nil, 2); g.Plan != nil || p.Snapshot(nil).Plan != nil {
		t.Fatal("a plan before any Protocol decision")
	}

	decide(1, engine.Protocol{Epoch: 0, Version: v10, Plan: plan})
	g, s := p.Governance(nil, 2), p.Snapshot(nil)
	if g.Plan == nil || *g.Plan != *plan || s.Plan == nil || *s.Plan != *plan {
		t.Fatalf("scheduled: governance %+v, overview %+v", g.Plan, s.Plan)
	}

	decide(2, engine.Protocol{Epoch: 5, Version: v10, Pending: &engine.Fork{Proposal: "ab#0", Version: v11, At: 6}, Plan: plan})
	if s := p.Snapshot(nil); s.Fork == nil || s.Fork.Version != v11 {
		t.Fatalf("pending fork missing from the overview: %+v", s.Fork)
	}

	decide(3, engine.Protocol{Epoch: 6, Version: v11})
	if g, s := p.Governance(nil, 2), p.Snapshot(nil); g.Plan != nil || s.Plan != nil || s.Fork != nil {
		t.Fatalf("enacted: plan %+v %+v, fork %+v", g.Plan, s.Plan, s.Fork)
	}
}

func mustEncode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := eventlog.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The Governance view shows the treasury the engine's Treasury decisions
// report: its lovelace now and each payout's before and after, and a
// proposal's detail carries the treasury it would draw on.
func TestGovernanceShowsTreasury(t *testing.T) {
	p := NewProjection()
	if g := p.Governance(nil, 2); g.Treasury != nil {
		t.Fatalf("a treasury before any Treasury decision: %+v", g.Treasury)
	}
	decide := func(seq uint64, tr engine.Treasury) {
		p.Apply(eventlog.Record{Seq: seq, At: 10_000, Class: eventlog.Decision, Kind: engine.KindTreasury, Body: mustEncode(t, tr)})
	}
	decide(1, engine.Treasury{Epoch: 0, Before: 1000, After: 1000})
	decide(2, engine.Treasury{Epoch: 2, Before: 1000, After: 700})
	g := p.Governance(nil, 2)
	if g.Treasury == nil || g.Treasury.Now != 700 || len(g.Treasury.Moves) != 1 || g.Treasury.Moves[0] != (TreasuryMove{Epoch: 2, Before: 1000, After: 700}) {
		t.Fatalf("treasury %+v", g.Treasury)
	}
}
