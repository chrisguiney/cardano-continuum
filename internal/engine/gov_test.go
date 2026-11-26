package engine

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// govYAML is populationYAML with pools that vote as SPOs.
var govYAML = strings.Replace(populationYAML, "update_rate: 40\n", "update_rate: 40\n      spo_vote_rate: 40\n", 1)

// spoEligible are the action types the proposer submits that stake pools
// may vote on: the builder's parameter change sets the maximum transaction
// size, a security-group parameter.
var spoEligible = map[string]bool{"info": true, "parameter_change": true}

// govLog is what a log says of governance: the confirmed votes by voter
// class, the action type of each vote's proposal, and the largest voting
// power a confirmed vote of each class carried.
type govLog struct {
	votes map[string]int
	types map[string][]string
	power map[string]uint64
}

func readGov(t testing.TB, r eventlog.Reader) govLog {
	t.Helper()
	g := govLog{votes: map[string]int{}, types: map[string][]string{}, power: map[string]uint64{}}
	sub, actionOf := map[uint64]Submitted{}, map[string]string{}
	for _, rec := range logRecords(t, r) {
		switch rec.Kind {
		case KindSubmitted:
			var s Submitted
			if err := eventlog.Decode(rec.Body, &s); err != nil {
				t.Fatal(err)
			}
			sub[s.Intent] = s
		case KindConfirmed:
			var c Confirmed
			if err := eventlog.Decode(rec.Body, &c); err != nil {
				t.Fatal(err)
			}
			s := sub[c.Intent]
			switch s.Kind {
			case "propose":
				actionOf[hex.EncodeToString(s.ID[:])+"#0"] = s.Action
			case "vote":
				g.votes[s.Voter]++
				g.types[s.Voter] = append(g.types[s.Voter], actionOf[s.Proposal])
				g.power[s.Voter] = max(g.power[s.Voter], s.Power)
			}
		}
	}
	return g
}

// Pools vote as SPOs on the open proposals stake pools may vote on, and
// only on those, beside the DReps; DRep votes carry the stake the citizens
// delegated to them; a clean run files no finding.
func TestPoolsVoteOnEligibleProposals(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	p := cleanRun(t, newWorld(t, cfg), 6*3600*1000)
	g := readGov(t, p.log)
	t.Logf("votes %v, power %v", g.votes, g.power)
	if g.votes["spo"] == 0 || g.votes["drep"] == 0 {
		t.Fatalf("votes %v: want SPO and DRep votes", g.votes)
	}
	for _, typ := range g.types["spo"] {
		if !spoEligible[typ] {
			t.Errorf("an SPO voted on a %q proposal", typ)
		}
	}
	if g.power["drep"] == 0 || g.power["spo"] == 0 {
		t.Errorf("power %v: DRep and SPO votes carry no stake", g.power)
	}
	if fs := findings(t, p.log); len(fs) > 0 {
		t.Errorf("%d findings, the first %+v", len(fs), fs[0])
	}
}

// With proposals rare, the proposer still opens one soon after it
// registers, so the voters have something to vote on from the start; the
// same seed opens the same one.
func TestProposerBootstrapsGovernance(t *testing.T) {
	t.Parallel()
	y := strings.Replace(govYAML, "proposal_rate: {info: 30, parameter_change: 30, treasury_withdrawals: 30}",
		"proposal_rate: {info: 0.001, treasury_withdrawals: 0.001}", 1)
	run := func() ([]byte, map[string]int) {
		cfg := populationConfig(t, 9, populationFile(t, y))
		w := newWorld(t, cfg)
		p := cleanRun(t, w, 2*3600*1000)
		kinds, _ := submittedKinds(t, p.log)
		if open := p.r.Engine().win.View().OpenProposals(); len(open) == 0 {
			t.Errorf("no open proposal after two hours (submitted %v)", kinds)
		}
		return w.logBytes(), kinds
	}
	a, kinds := run()
	if kinds["propose"] == 0 {
		t.Fatalf("submitted %v: no bootstrap proposal", kinds)
	}
	if kinds["propose"] > 2 {
		t.Errorf("%d proposals: the bootstrap kept proposing with one open", kinds["propose"])
	}
	if b, _ := run(); !bytes.Equal(a, b) {
		t.Fatal("the same seed gave a different log")
	}
}

// probeWorld runs govYAML's populations and, after three hours, asks a pool
// to vote on a treasury withdrawal, which stake pools may not vote on.
func probeWorld(t *testing.T, wrap func(*FakeNode) Node) *world {
	t.Helper()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	w := newWorld(t, cfg)
	w.wrap = wrap
	at := sched.Time(3 * 3600 * 1000)
	w.controls = &scriptControls{clock: w.clock, at: []sched.Time{at}, cs: []control{
		{RequestID: "probe", Op: "inject", Scenario: scenarioSPOProbe},
	}}
	w.run(4*3600*1000, 0, nil)
	return w
}

func verdictFindings(t *testing.T, r eventlog.Reader, k oracle.Kind) []oracle.Finding {
	t.Helper()
	var out []oracle.Finding
	for _, f := range findings(t, r) {
		if f.Kind == k {
			out = append(out, f)
		}
	}
	return out
}

func probeAcks(t *testing.T, r eventlog.Reader) []string {
	t.Helper()
	var out []string
	for _, rec := range logRecords(t, r) {
		if rec.Kind == KindControlled {
			var c Controlled
			if err := eventlog.Decode(rec.Body, &c); err != nil {
				t.Fatal(err)
			}
			out = append(out, c.Why)
		}
	}
	return out
}

// The fake chain refuses an SPO vote on a treasury withdrawal with the
// ledger's DisallowedVoters, which is what the probe expects: no finding.
func TestSPOProbeRejected(t *testing.T) {
	t.Parallel()
	w := probeWorld(t, nil)
	if acks := probeAcks(t, w.log); len(acks) != 1 || acks[0] != "" {
		t.Fatalf("probe acknowledged %q", acks)
	}
	rejected := 0
	for _, rec := range logRecords(t, w.log) {
		if rec.Kind == eventlog.KindSubmitResult && bytes.Contains(rec.Body, []byte(fakechain.DisallowedVoters)) {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("no DisallowedVoters rejection")
	}
	if fs := findings(t, w.log); len(fs) > 0 {
		t.Errorf("%d findings, the first %+v", len(fs), fs[0])
	}
}

// poolVoteNode answers every transaction carrying an SPO vote with answer
// and passes the rest to the fake chain.
type poolVoteNode struct {
	*FakeNode
	answer error
}

func (n poolVoteNode) Submit(ctx context.Context, tx []byte) error {
	t, err := conway.NewConwayTransactionFromCbor(tx)
	if err == nil {
		for v := range t.VotingProcedures() {
			if v.Type == 4 {
				return n.answer
			}
		}
	}
	return n.FakeNode.Submit(ctx, tx)
}

// A node that accepts the ineligible SPO vote broke a ledger rule: a fatal
// finding.
func TestSPOProbeAcceptedIsFatal(t *testing.T) {
	t.Parallel()
	w := probeWorld(t, func(f *FakeNode) Node { return poolVoteNode{FakeNode: f} })
	fs := verdictFindings(t, w.log, oracle.UnexpectedAccept)
	if len(fs) == 0 || fs[0].Severity != oracle.Fatal {
		t.Fatalf("accepted ineligible SPO vote filed %+v", fs)
	}
}

// A node that refuses the pools' eligible votes refused valid
// transactions: a finding.
func TestValidSPOVoteRejectedIsFinding(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	w := newWorld(t, cfg)
	w.wrap = func(f *FakeNode) Node {
		return poolVoteNode{FakeNode: f, answer: &submit.RejectedError{Failures: []submit.Failure{{Class: submit.Class(fakechain.DisallowedVoters)}}}}
	}
	w.run(4*3600*1000, 0, nil)
	if fs := verdictFindings(t, w.log, oracle.UnexpectedReject); len(fs) == 0 {
		t.Fatal("rejected valid SPO votes filed no finding")
	}
}

// Governance is deterministic: two runs of one seed with proposals, DRep
// and SPO votes and a probe write byte-identical logs that replay exactly,
// and a run crashed at sampled points still replays its own log and
// files no finding.
func TestDeterminismGovernance(t *testing.T) {
	t.Parallel()
	var logs [][]byte
	for range 2 {
		w := probeWorld(t, nil)
		logs = append(logs, w.logBytes())
		w.finalSnapshot()
	}
	if !bytes.Equal(logs[0], logs[1]) {
		t.Fatal("two governance runs with one seed wrote different logs")
	}
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	until := sched.Time(2 * 3600 * 1000)
	ref := newWorld(t, cfg)
	ref.mem = &memStore{log: ref.log}
	hooks := 0
	ref.run(until, 15, func(int) bool { hooks++; return false })
	stride := max(hooks*crashStride/8, 1)
	for at := 1; at <= hooks; at += stride {
		t.Run(fmt.Sprintf("hook%d", at), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, cfg)
			w.mem = &memStore{log: w.log}
			if c := w.run(until, 15, func(n int) bool { return n == at }); c != 1 {
				t.Fatalf("crash at %d: %d crashes", at, c)
			}
			w.finalSnapshot()
			if fs := findings(t, w.log); len(fs) > 0 {
				t.Fatalf("crash at hook %d: %d findings, the first %+v", at, len(fs), fs[0])
			}
		})
	}
}

// Ten seeds of governance on the fake chain file no oracle finding.
func TestGovernanceCleanAcrossSeeds(t *testing.T) {
	t.Parallel()
	for seed := uint64(1); seed <= 10; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			cfg := populationConfig(t, seed, populationFile(t, govYAML))
			p := cleanRun(t, newWorld(t, cfg), 3*3600*1000)
			if g := readGov(t, p.log); g.votes["spo"] == 0 || g.votes["drep"] == 0 {
				t.Errorf("votes %v", g.votes)
			}
			if fs := findings(t, p.log); len(fs) > 0 {
				t.Errorf("%d findings, the first %+v", len(fs), fs[0])
			}
		})
	}
}
