package engine

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/steer"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
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
			case "vote", "committee_vote":
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
		{RequestID: "probe", Op: "inject", Scenario: steer.ScenarioSPOProbe},
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

// fittedGovYAML is populationYAML with preprod's fitted governance rates
// (profiles/preprod.yaml) times 200, the --scale the live demo runs at:
// DReps vote 0.0106 times a day and 5.4% of them vote at all, pools
// 0.00012 times a day, and the network proposes about a third of an action
// a day. Per day of wall time they are next to nothing on a devnet whose
// proposal lives an hour.
var fittedGovYAML = strings.NewReplacer(
	"update_rate: 40\n", "update_rate: 0.426758\n      spo_vote_rate: 0.023068\n",
	"drep: {register_rate: 0, retire_rate: 2, vote_rate: 100, voting_share: 0.5}",
	"drep: {register_rate: 0, retire_rate: 4.42942, vote_rate: 2.11712, voting_share: 0.0540541}",
	"chain_count: 400\n    drep", "chain_count: 296\n    drep",
	"proposal_rate: {info: 30, parameter_change: 30, treasury_withdrawals: 30}",
	"proposal_rate: {info: 31.1112, parameter_change: 8.88888, treasury_withdrawals: 26.6666}",
).Replace(populationYAML)

// coverage is, over the proposals a run made and let expire, the share
// each voter class voted on, among those the class may vote on.
type coverage struct {
	proposals int
	drep, spo float64
}

func voteCoverage(t testing.TB, r eventlog.Reader, closed func(at sched.Time) bool) coverage {
	t.Helper()
	sub := map[uint64]Submitted{}
	props := map[string]bool{}
	voted := map[string]map[string]bool{}
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
				if closed(rec.At) {
					props[hex.EncodeToString(s.ID[:])+"#0"] = spoEligible[s.Action]
				}
			case "vote":
				if voted[s.Proposal] == nil {
					voted[s.Proposal] = map[string]bool{}
				}
				voted[s.Proposal][s.Voter] = true
			}
		}
	}
	var c coverage
	var spoProps int
	for id, pools := range props {
		c.proposals++
		if voted[id]["drep"] {
			c.drep++
		}
		if pools {
			spoProps++
			if voted[id]["spo"] {
				c.spo++
			}
		}
	}
	if c.proposals > 0 {
		c.drep /= float64(c.proposals)
	}
	if spoProps > 0 {
		c.spo /= float64(spoProps)
	}
	return c
}

// At the fitted rates, on a devnet whose epochs are minutes long, the DReps
// and pools still vote on most proposals before they expire: the vote
// clocks are scaled to the proposal lifetime, and a cut-down DRep
// population keeps the chain's voters.
func TestFittedRatesVoteOnMostProposals(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, fittedGovYAML))
	slots, err := sched.NewSlots(1000, 600)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Slots = slots
	until := sched.Time(6 * 3600 * 1000)
	life := sched.Time(cfg.GovLifetime+1) * slots.EpochStart(1)
	p := cleanRun(t, newWorld(t, cfg), until)
	c := voteCoverage(t, p.log, func(at sched.Time) bool { return at+life < until })
	t.Logf("coverage %+v", c)
	if c.proposals < 3 {
		t.Fatalf("%d closed proposals, too few to judge", c.proposals)
	}
	if c.drep < 0.8 || c.spo < 0.8 {
		t.Errorf("DReps voted on %.0f%%, pools on %.0f%% of proposals; want most", 100*c.drep, 100*c.spo)
	}
}

// paramSample is the fake chain's poolPledgeInfluence at one epoch.
type paramSample struct {
	epoch uint64
	a0    string
}

// The network proposer changes a parameter, the DReps, pools and committee
// vote it through, the fake chain enacts it, and a later proposal naming
// the change as its predecessor reverts it: the chain's poolPledgeInfluence
// goes 3/10, 1/4, 3/10. A clean run files no finding.
func TestParameterChangeEnactsThenReverts(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	slots, err := sched.NewSlots(1000, 600)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Slots, cfg.Committee = slots, 3
	w := newWorld(t, cfg)
	p, err := w.start(nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	var seen []paramSample
	for ep := uint64(1); ep <= 30; ep++ {
		if err := p.r.Run(context.Background(), slots.EpochStart(ep)+1000); err != nil {
			t.Fatal(err)
		}
		a0 := w.chain.Params().A0.String()
		if len(seen) == 0 || seen[len(seen)-1].a0 != a0 {
			seen = append(seen, paramSample{ep, a0})
		}
	}
	t.Logf("a0 by epoch %v; votes %v", seen, readGov(t, p.log).votes)
	for _, rec := range logRecords(t, p.log) {
		var s Submitted
		if rec.Kind == KindSubmitted && eventlog.Decode(rec.Body, &s) == nil && s.Action == "parameter_change" {
			t.Logf("parameter change %x#0 submitted in epoch %d", s.ID[:4], slots.EpochAt(rec.At))
		}
	}
	if len(seen) < 3 || seen[0].a0 != "3/10" || seen[1].a0 != "1/4" || seen[2].a0 != "3/10" {
		t.Fatalf("a0 went %v, want 3/10, 1/4, 3/10", seen)
	}
	if fs := findings(t, p.log); len(fs) > 0 {
		t.Errorf("%d findings, the first %+v", len(fs), fs[0])
	}
}

// selfGovYAML is fittedGovYAML with no citizen delegating its vote, so a
// DRep's voting power can only be its own stake.
var selfGovYAML = strings.Replace(fittedGovYAML, "rates: {base: {delegate_drep: 0.01}}", "rates: {base: {delegate_drep: 0}}", 1)

// A registered DRep registers its stake key and delegates its vote to
// itself, so its votes carry its own stake. Before, a DRep woke only on its
// vote and retire clocks, never delegated, and on a devnet with few
// delegating citizens voted with no power, so nothing ratified.
func TestDRepsVoteWithTheirOwnStake(t *testing.T) {
	t.Parallel()
	if selfGovYAML == fittedGovYAML {
		t.Fatal("fixture no longer has the citizens' delegate_drep rate to zero out")
	}

	cfg := populationConfig(t, 4, populationFile(t, selfGovYAML))
	slots, err := sched.NewSlots(1000, 600)
	if err != nil {
		t.Fatal(err)
	}

	cfg.Slots = slots
	p := cleanRun(t, newWorld(t, cfg), sched.Time(3*3600*1000))
	g := readGov(t, p.log)
	if g.votes["drep"] == 0 {
		t.Fatalf("no DRep voted: %v", g.votes)
	}

	if g.power["drep"] == 0 {
		t.Fatalf("DRep votes carry no stake: votes %v, power %v", g.votes, g.power)
	}
}

// A population actor's decision names the clock that fired, and the scores
// it carries are that kind's alone: the chance in the UI is within the
// clock. An agenda actor's decision has no clock.
func TestPopulationDecisionRecordsClock(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 9, populationFile(t, govYAML))
	w := newWorld(t, cfg)
	p := cleanRun(t, w, 2*3600*1000)

	clocked := 0
	for _, rec := range logRecords(t, p.log) {
		if rec.Kind != KindIntentChosen {
			continue
		}
		var d agenda.Decision
		if err := eventlog.Decode(rec.Body, &d); err != nil {
			t.Fatal(err)
		}
		if d.Clock == "" {
			continue
		}
		clocked++
		for _, s := range d.Scores {
			if s.Kind != d.Clock && s.Kind != agenda.Idle {
				t.Fatalf("clock %s with a %s score: %+v", d.Clock, s.Kind, d)
			}
		}
	}
	if clocked == 0 {
		t.Fatal("no decision recorded a clock")
	}
}

// chainVersion answers view queries with the fake chain's protocol version
// at its tip. It answers at the tip, not at the point asked for, so the
// balance comparison (which needs the same point) is not made.
type chainVersion struct {
	c     *fakechain.Chain
	slots sched.Slots
	seen  *[]protocol.Version
}

func (v chainVersion) View(_ context.Context, _ chain.Point, _ []common.Address) (ViewAnswer, error) {
	pv := v.c.Params().ProtocolVersion
	ver := protocol.Version{Major: pv.Major, Minor: pv.Minor}
	*v.seen = append(*v.seen, ver)
	// A hash no block has keeps the answer apart from every settled point.
	tip := chain.Point{Slot: v.c.Slot(), Hash: common.Blake2b256{0xff}}
	return ViewAnswer{Node: oracle.Node{Impl: "fakechain", Pool: "fake"}, At: tip, Version: &ver, Epoch: v.slots.EpochOfSlot(v.c.Slot())}, nil
}

// protocolRecords are the run's Protocol decisions.
func protocolRecords(t *testing.T, r eventlog.Reader) []Protocol {
	t.Helper()
	var out []Protocol
	for _, rec := range logRecords(t, r) {
		if rec.Kind != KindProtocol {
			continue
		}
		var p Protocol
		if err := eventlog.Decode(rec.Body, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// The hard_fork scenario takes the devnet from 10.0 to 11.0 in process: the
// proposal, the committee's, the DReps' and the pools' votes, the
// ratification at a boundary and the enactment at the next, in the fake
// chain and in the engine's view alike. The engine logs the pending fork
// and the new version, the node it asks reports the version the view
// expects (no protocol_version_mismatch), and after the fork the chain
// takes a committee vote that authorises its own hot key, which it
// refused before. A second hard_fork while the first is in flight is
// refused (HardForkNoPendingGuardBug in models/specs/governance-change).
func TestHardForkThroughFakeChain(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	cfg.Committee = 3
	w := newWorld(t, cfg)
	var seen []protocol.Version
	w.monitor = chainVersion{c: w.chain, slots: cfg.Slots, seen: &seen}
	w.controls = &scriptControls{clock: w.clock, at: []sched.Time{3 * 3600 * 1000, 3*3600*1000 + 60_000}, cs: []control{
		{RequestID: "fork", Op: "inject", Scenario: steer.ScenarioHardFork},
		{RequestID: "again", Op: "inject", Scenario: steer.ScenarioHardFork + ":11.0"},
	}}
	w.run(10*3600*1000, 50, nil)

	if acks := probeAcks(t, w.log); len(acks) != 2 || acks[0] != "" || acks[1] == "" {
		t.Fatalf("hard_fork acknowledged %q, want the first taken and the second refused", acks)
	}

	p, err := w.start(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	v := p.r.Engine().win.View()
	want := protocol.Version{Major: 11}
	if got := v.VersionAt(v.Epoch()); got != want {
		t.Fatalf("view at %v in epoch %d, want %v (forks %+v)", got, v.Epoch(), want, v.Forks())
	}

	if pv := w.chain.Params().ProtocolVersion; pv.Major != 11 || pv.Minor != 0 {
		t.Fatalf("fake chain at %d.%d, want 11.0", pv.Major, pv.Minor)
	}

	recs := protocolRecords(t, w.log)
	if len(recs) < 3 || recs[0].Version != (protocol.Version{Major: 10}) || recs[0].Pending != nil || recs[1].Pending == nil ||
		recs[len(recs)-1].Version != want || recs[len(recs)-1].Pending != nil {
		t.Fatalf("protocol records %+v: want 10.0 from genesis, a pending fork, then 11.0", recs)
	}

	if fork := v.Forks()[0]; recs[len(recs)-1].Epoch != fork.At {
		t.Fatalf("enacted in epoch %d, the view's fork says %d", recs[len(recs)-1].Epoch, fork.At)
	}

	if fs := findings(t, w.log); len(fs) > 0 {
		t.Fatalf("%d findings, the first %+v", len(fs), fs[0])
	}

	if !slices.Contains(seen, want) {
		t.Fatalf("the node never reported %v: %v", want, seen)
	}
	jointVote(t, w, p.r.Engine())
}

// From protocol version 11 the committee's votes carry their own hot-key
// authorisation: a devnet starting at 11.0 sends no committee_auth, and
// its committee votes still land, with no finding.
func TestCommitteeVotesAuthoriseThemselvesFromV11(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	cfg.Committee = 3
	pp := fakechain.DefaultParams()
	pp.ProtocolVersion.Major = 11
	cfg.ChainParams = &pp
	cfg.Build.Protocol = protocol.Version{Major: 11}
	p := cleanRun(t, newWorld(t, cfg), 4*3600*1000)
	kinds, _ := submittedKinds(t, p.log)
	if g := readGov(t, p.log); kinds[string(agenda.CommitteeAuth)] != 0 || g.votes["committee"] == 0 {
		t.Fatalf("kinds %v, committee votes %d: want votes and no separate authorisation", kinds, g.votes["committee"])
	}

	if fs := findings(t, p.log); len(fs) > 0 {
		t.Fatalf("%d findings, the first %+v", len(fs), fs[0])
	}
}

// jointVote has committee seat 0's cold key authorise a fresh hot key and
// vote with it in one transaction, which the chain at 11.0 takes.
func jointVote(t *testing.T, w *world, e *Engine) {
	t.Helper()
	root, err := keys.New(w.cfg.Seed, w.cfg.NetworkID)
	if err != nil {
		t.Fatal(err)
	}
	cold, _ := root.CommitteeSigners(0)
	_, hot := root.CommitteeSigners(7)
	open := e.win.View().OpenProposals()
	if len(open) == 0 {
		t.Fatal("no open proposal to vote on")
	}

	ref, err := intent.ParseProposal(open[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := scenarioRoute{e}.richest(func(population.ActorID) bool { return true })
	payer := e.co.Rec(id).Wallet
	got, err := e.builder.Gov().Vote(context.Background(), payer, build.Vote{
		Spend: build.Spend{Inputs: w.chain.UtxosAt(payer.Address()), TTL: w.chain.Slot() + 100},
		Voter: hot, Auth: &cold, Role: build.RoleCommittee,
		Action: common.GovActionId{TransactionId: common.Blake2b256(ref.Tx), GovActionIdx: ref.Index}, Choice: common.GovVoteYes,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := w.chain.Submit(context.Background(), fakechain.EraConway, got.Tx); err != nil {
		t.Fatalf("a vote authorising its own hot key refused at 11.0: %v", err)
	}
}

// A live-like run (faucet-funded, the pools a profile population's) whose
// pool actors operate the genesis pools, as liveConfig now has them do,
// files no stake_floor_breached and its SPO votes carry stake. Left
// unoperated (GenesisPools 0, what --pools 0 gave a live run), the
// genesis pools' stake is invisible to the view: the pools it knows hold
// none, so the floor reads as breached every epoch and every SPO vote
// carries no power. One cause, two symptoms.
func TestLiveLikeRunCreditsGenesisPools(t *testing.T) {
	t.Parallel()
	run := func(genesis int) (int, uint64) {
		cfg := populationConfig(t, 4, populationFile(t, govYAML))
		cfg.Faucet, cfg.RefillEvery = 50_000_000_000_000, 8
		cfg.GenesisPools = genesis
		p := cleanRun(t, newWorld(t, cfg), 6*3600*1000)
		return len(verdictFindings(t, p.log, oracle.FloorBreached)), readGov(t, p.log).power["spo"]
	}

	if n, power := run(3); n != 0 || power == 0 {
		t.Fatalf("genesis pools operated: %d stake_floor_breached, SPO power %d; want none and some", n, power)
	}

	if n, _ := run(0); n == 0 {
		t.Fatal("genesis pools unoperated: no stake_floor_breached; the test no longer shows the cause")
	}
}
