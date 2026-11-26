package httpapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func block(t testing.TB, at sched.Time, slot uint64, txs ...engine.TxDigest) eventlog.Record {
	t.Helper()
	pt := eventlog.ChainPoint{Slot: slot, Hash: bytes.Repeat([]byte{byte(slot)}, 32)}
	return eventlog.Record{At: at, Class: eventlog.Input, Kind: eventlog.KindBlockApplied,
		Body: enc(t, eventlog.BlockApplied{Point: pt, Content: enc(t, engine.BlockContent{Txs: txs})})}
}

func submitted(t testing.TB, at sched.Time, actor uint64, s engine.Submitted) eventlog.Record {
	t.Helper()
	return eventlog.Record{At: at, Class: eventlog.Decision, Kind: engine.KindSubmitted, Actor: eventlog.ActorID(actor), Body: enc(t, s)}
}

// govFixture: actor 5 proposes an info action (slot 10) and a treasury
// withdrawal (slot 20); pool actor 7 votes yes on the info action with
// 5000 lovelace behind it, DRep actor 8 votes no with 3000, then changes its
// vote to abstain; a vote from a voter the run does not know counts with
// no power. Epochs are 100 slots of one second.
func govFixture(t testing.TB) []eventlog.Record {
	t.Helper()
	prop, treasury, v1, v2, v3, v4 := txid(0x10), txid(0x11), txid(0x20), txid(0x21), txid(0x22), txid(0x23)
	info := hex.EncodeToString(prop[:]) + "#0"
	vote := func(id engine.TxID, kind uint8, h byte, choice uint8) engine.TxDigest {
		return engine.TxDigest{ID: id, Votes: []engine.VoteDigest{{Kind: kind, Hash: [28]byte{h}, Tx: prop, Choice: choice}}}
	}
	return []eventlog.Record{
		{At: 0, Class: eventlog.Decision, Kind: engine.KindSpawned, Actor: 5, Body: enc(t, engine.Spawned{Profile: "network"})},
		submitted(t, 1000, 5, engine.Submitted{Intent: 1, ID: prop, Kind: "propose", Action: "info"}),
		block(t, 10000, 10, engine.TxDigest{ID: prop, Proposals: 1, Gov: []engine.ProposalDigest{{Type: "info", Pools: true, Deposit: 100_000}}}),
		submitted(t, 11000, 5, engine.Submitted{Intent: 2, ID: treasury, Kind: "propose", Action: "treasury_withdrawals"}),
		block(t, 20000, 20, engine.TxDigest{ID: treasury, Proposals: 1, Gov: []engine.ProposalDigest{{Type: "treasury_withdrawals", Deposit: 100_000}}}),
		submitted(t, 21000, 7, engine.Submitted{Intent: 3, ID: v1, Kind: "vote", Proposal: info, Choice: "yes", Voter: "spo", Power: 5000}),
		submitted(t, 21000, 8, engine.Submitted{Intent: 4, ID: v2, Kind: "vote", Proposal: info, Choice: "no", Voter: "drep", Power: 3000}),
		block(t, 30000, 30, vote(v1, 4, 7, 1), vote(v2, 2, 8, 0)),
		submitted(t, 31000, 8, engine.Submitted{Intent: 5, ID: v3, Kind: "vote", Proposal: info, Choice: "abstain", Voter: "drep", Power: 3000}),
		block(t, 40000, 40, vote(v3, 2, 8, 2), vote(v4, 2, 9, 1)),
	}
}

func fold(t testing.TB, recs []eventlog.Record) *Projection {
	t.Helper()
	p := NewProjection()
	for i, r := range recs {
		r.Seq = uint64(i + 1)
		p.Apply(r)
	}
	return p
}

func TestGovernanceFold(t *testing.T) {
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	g := fold(t, govFixture(t)).Governance(&slots, 2)
	if len(g.Proposals) != 2 {
		t.Fatalf("proposals %+v", g.Proposals)
	}
	info, tw := g.Proposals[0], g.Proposals[1]
	if info.Type != "info" || !info.PoolsMayVote || info.Deposit != 100_000 || info.Proposer == nil || *info.Proposer != 5 ||
		info.Profile != "network" || info.Status != "open" || info.Expires == nil || *info.Expires != 2 {
		t.Errorf("info proposal %+v", info)
	}
	if tw.Type != "treasury_withdrawals" || tw.PoolsMayVote {
		t.Errorf("treasury proposal %+v", tw)
	}
	spo, drep := info.Tally["spo"], info.Tally["drep"]
	if spo != (Tally{Yes: 1, YesPower: 5000}) {
		t.Errorf("spo tally %+v", spo)
	}
	// The DRep's second vote replaces its first; the unknown voter's yes
	// counts with no power.
	if drep != (Tally{Yes: 1, Abstain: 1, AbstainPower: 3000}) {
		t.Errorf("drep tally %+v", drep)
	}
	if len(g.Votes) != 4 || g.Votes[0].Slot != 40 || g.Votes[len(g.Votes)-1].Slot != 30 {
		t.Errorf("recent votes %+v", g.Votes)
	}
	if len(g.Pools) != 1 || g.Pools[0].Actor != 7 || len(g.Pools[0].Votes) != 1 || g.Pools[0].Votes[0].Choice != "yes" {
		t.Errorf("pool votes %+v", g.Pools)
	}

	// A rollback to slot 30 takes the slot-40 votes back off.
	recs := append(govFixture(t), eventlog.Record{At: 50000, Class: eventlog.Input, Kind: eventlog.KindRolledBack,
		Body: enc(t, eventlog.RolledBack{To: eventlog.ChainPoint{Slot: 30, Hash: bytes.Repeat([]byte{30}, 32)}})})
	g = fold(t, recs).Governance(&slots, 2)
	if d := g.Proposals[0].Tally["drep"]; d != (Tally{No: 1, NoPower: 3000}) {
		t.Errorf("after rollback drep tally %+v", d)
	}
	if len(g.Votes) != 2 {
		t.Errorf("after rollback %d recent votes, want 2", len(g.Votes))
	}
	// Past its last epoch a proposal is expired.
	recs = append(govFixture(t), block(t, 400000, 400))
	if g = fold(t, recs).Governance(&slots, 2); g.Proposals[0].Status != "expired" {
		t.Errorf("status %q past the last epoch", g.Proposals[0].Status)
	}
}

func TestDecisionVoteFilter(t *testing.T) {
	vote := Decision{Kind: engine.KindIntentChosen, Intent: "vote"}
	sub := Decision{Kind: engine.KindSubmitted, Intent: "vote"}
	send := Decision{Kind: engine.KindIntentChosen, Intent: "send"}
	f := Filter{Vote: true}
	if !f.Match(vote) || !f.Match(sub) || f.Match(send) {
		t.Fatal("vote filter")
	}
	d, err := view.ToDecision(submitted(t, 0, 7, engine.Submitted{Intent: 3, Kind: "vote", Voter: "spo"}), "pools")
	if err != nil || d.Intent != "vote" {
		t.Fatalf("submitted vote decision %+v, %v", d, err)
	}
	v, err := parseFilter(map[string][]string{"vote": {"true"}})
	if err != nil || !v.Vote {
		t.Fatalf("parse vote filter %+v %v", v, err)
	}
	if _, err := parseFilter(map[string][]string{"vote": {"maybe"}}); err == nil {
		t.Fatal("vote=maybe accepted")
	}
}

func TestGovernanceEndpoint(t *testing.T) {
	srv, _ := newServer(t, nil)
	code, body := get(t, srv.URL+"/api/governance")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	golden(t, "governance", body)
}

func TestLogSourceGovernance(t *testing.T) {
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	src := NewLogSource(LogSourceConfig{Projection: fold(t, govFixture(t)), Slots: &slots, GovLifetime: 2})
	g, err := src.Governance(context.Background())
	if err != nil || len(g.Proposals) != 2 || g.Lifetime != 2 {
		t.Fatalf("governance %+v, %v", g, err)
	}
}
