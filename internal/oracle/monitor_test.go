package oracle

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"bytes"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

const k = 3

func guard(t testing.TB) policy.Guard {
	t.Helper()
	g, err := policy.New(policy.Config{Floor: 50, Margin: 1})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func pt(slot uint64, b byte) chain.Point {
	return chain.Point{Slot: slot, Hash: chain.TxID(bytes.Repeat([]byte{b}, 32))}
}

func txid(b byte) chain.TxID { return chain.TxID(bytes.Repeat([]byte{b}, 32)) }

// check runs the monitor called name over s.
func check(t *testing.T, name string, s Snapshot) []Finding {
	t.Helper()
	for _, m := range Monitors(k, guard(t)) {
		if m.Name() == name {
			s.Run, s.Seq = run, 9
			return m.Check(s)
		}
	}
	t.Fatalf("no monitor %q", name)
	return nil
}

func fires(t *testing.T, name string, s Snapshot, want Kind, sev Severity) Finding {
	t.Helper()
	fs := check(t, name, s)
	if len(fs) != 1 || fs[0].Kind != want || fs[0].Severity != sev {
		t.Fatalf("%s: findings %+v, want one %s %s", name, fs, sev, want)
	}
	if fs[0].ID == "" || fs[0].Seq != 9 || fs[0].Summary == "" || len(fs[0].Facts) == 0 {
		t.Fatalf("%s: finding lacks id, seq, summary or evidence: %+v", name, fs[0])
	}
	return fs[0]
}

func quiet(t *testing.T, name string, s Snapshot) {
	t.Helper()
	if fs := check(t, name, s); len(fs) != 0 {
		t.Fatalf("%s: unexpected findings %+v", name, fs)
	}
}

func TestMonitorNames(t *testing.T) {
	t.Parallel()
	var names []string
	for _, m := range Monitors(k, guard(t)) {
		names = append(names, m.Name())
	}
	want := []string{"value", "paid_once", "reservations", "settled", "view", "liveness", "tips", "stake_floor", "rogue", "block_budget"}
	if !slices.Equal(names, want) {
		t.Fatalf("monitors = %v, want %v", names, want)
	}
}

func value() *Value {
	return &Value{Genesis: 1000, Utxo: 900, Fees: 40, Deposits: 30, Rewards: 10, Treasury: 15, Reserves: 5}
}

func TestValueConserved(t *testing.T) {
	t.Parallel()
	quiet(t, "value", Snapshot{Value: value()})
	quiet(t, "value", Snapshot{})
	v := value()
	v.Treasury--
	f := fires(t, "value", Snapshot{Value: v}, ValueNotConserved, Fatal)
	if !hasFact(f, "delta", "-1") {
		t.Fatalf("deficit not named: %+v", f.Facts)
	}
	v = value()
	v.Utxo = ^uint64(0)
	fires(t, "value", Snapshot{Value: v}, ValueNotConserved, Fatal)
}

func TestPaidOnce(t *testing.T) {
	t.Parallel()
	quiet(t, "paid_once", Snapshot{Ends: []End{
		{Intent: 1, Tx: txid(1), Seq: 3}, {Intent: 2, Tx: txid(2), Seq: 4},
		{Intent: 3, Released: true, Seq: 5},
		// The same attempt confirmed again after a rollback is one payment.
		{Intent: 1, Tx: txid(1), Seq: 8},
	}})
	f := fires(t, "paid_once", Snapshot{Ends: []End{
		{Intent: 1, Tx: txid(1), Seq: 3}, {Intent: 1, Tx: txid(9), Seq: 7},
	}}, PaidTwice, Fatal)
	if !hasFact(f, "intent", "1") {
		t.Fatalf("intent not named: %+v", f.Facts)
	}
	fires(t, "paid_once", Snapshot{Ends: []End{
		{Intent: 4, Released: true, Seq: 3}, {Intent: 4, Tx: txid(4), Seq: 7},
	}}, PaidTwice, Fatal)
}

func ref(b byte, i uint32) chain.Ref { return chain.Ref{Tx: txid(b), Index: i} }

func pending(intent plan.IntentID, tx byte, in ...chain.Ref) plan.Pending {
	return plan.Pending{Intent: intent, Attempt: plan.Attempt{Tx: txid(tx), Actor: 1, Inputs: in}}
}

func TestReservations(t *testing.T) {
	t.Parallel()
	ok := &Reservations{
		Holds:      []Hold{{Ref: ref(1, 0), Intent: 1}, {Ref: ref(1, 1), Intent: 1}, {Ref: ref(2, 0), Intent: 2}},
		Unfinished: []plan.Pending{pending(1, 0xa, ref(1, 0)), pending(1, 0xb, ref(1, 1)), pending(2, 0xc, ref(2, 0))},
	}
	quiet(t, "reservations", Snapshot{Reservations: ok})
	quiet(t, "reservations", Snapshot{})

	leak := &Reservations{
		Holds:      []Hold{{Ref: ref(1, 0), Intent: 1}, {Ref: ref(3, 0), Intent: 3}},
		Unfinished: []plan.Pending{pending(1, 0xa, ref(1, 0))},
	}
	f := fires(t, "reservations", Snapshot{Reservations: leak}, ReservationLeaked, Error)
	if !hasFact(f, "intent", "3") {
		t.Fatalf("leaked intent not named: %+v", f.Facts)
	}

	double := &Reservations{
		Holds:      []Hold{{Ref: ref(1, 0), Intent: 1}, {Ref: ref(1, 0), Intent: 2}},
		Unfinished: []plan.Pending{pending(1, 0xa, ref(1, 0)), pending(2, 0xb, ref(1, 0))},
	}
	fires(t, "reservations", Snapshot{Reservations: double}, ReservationDouble, Fatal)

	other := &Reservations{
		Holds:      []Hold{{Ref: ref(1, 0), Intent: 1}},
		Unfinished: []plan.Pending{pending(1, 0xa, ref(1, 0)), pending(2, 0xb, ref(1, 0))},
	}
	fires(t, "reservations", Snapshot{Reservations: other}, ReservationDouble, Fatal)

	bare := &Reservations{Unfinished: []plan.Pending{pending(1, 0xa, ref(1, 0))}}
	fires(t, "reservations", Snapshot{Reservations: bare}, Unreserved, Fatal)
}

// fakeHolders is a ledger with a reservation on a settled output and on an
// unfinished attempt's change.
type fakeHolders struct{}

func (fakeHolders) Settled() []plan.Utxo {
	return []plan.Utxo{{Ref: ref(1, 0), Owner: 1}, {Ref: ref(1, 1), Owner: 1}}
}

func (fakeHolders) Unfinished() []plan.Pending {
	p := pending(1, 0xa, ref(1, 0))
	p.Change = []plan.Utxo{{Ref: chain.Ref{Tx: txid(0xa), Index: 1}, Owner: 1}}
	return []plan.Pending{p}
}

func (fakeHolders) Holder(r plan.Ref) (plan.IntentID, bool) {
	switch r {
	case ref(1, 0):
		return 1, true
	case chain.Ref{Tx: txid(0xa), Index: 1}:
		return 5, true
	}
	return 0, false
}

func TestReservationsOf(t *testing.T) {
	t.Parallel()
	r := ReservationsOf(fakeHolders{})
	want := []Hold{{Ref: ref(1, 0), Intent: 1}, {Ref: chain.Ref{Tx: txid(0xa), Index: 1}, Intent: 5}}
	if !slices.Equal(r.Holds, want) || len(r.Unfinished) != 1 {
		t.Fatalf("reservations = %+v", r)
	}
}

func TestSettledNeverMovesBack(t *testing.T) {
	t.Parallel()
	quiet(t, "settled", Snapshot{Settled: &SettledMove{From: pt(10, 1), To: pt(20, 2)}})
	quiet(t, "settled", Snapshot{Settled: &SettledMove{From: pt(10, 1), To: pt(10, 1)}})
	quiet(t, "settled", Snapshot{})
	fires(t, "settled", Snapshot{Settled: &SettledMove{From: pt(20, 2), To: pt(10, 1)}}, SettledBack, Fatal)
	fires(t, "settled", Snapshot{Settled: &SettledMove{From: pt(20, 2), To: pt(20, 3)}}, SettledBack, Fatal)
}

func TestViewMatchesNode(t *testing.T) {
	t.Parallel()
	cn := node("cardano-node", "p1")
	same := ViewCheck{Node: cn, At: pt(10, 1), NodeAt: pt(10, 1),
		Engine: map[population.ActorID]uint64{1: 5, 2: 7}, Chain: map[population.ActorID]uint64{1: 5, 2: 7}}
	quiet(t, "view", Snapshot{Views: []ViewCheck{same}})

	// Answers at different points cannot be compared.
	apart := same
	apart.NodeAt, apart.Chain = pt(12, 3), map[population.ActorID]uint64{1: 4}
	quiet(t, "view", Snapshot{Views: []ViewCheck{apart}})

	off := same
	off.Chain = map[population.ActorID]uint64{1: 5, 2: 6, 3: 1}
	fs := check(t, "view", Snapshot{Views: []ViewCheck{off}})
	if len(fs) != 2 || fs[0].Kind != ViewMismatch || fs[0].Severity != Error {
		t.Fatalf("findings %+v, want one per differing actor", fs)
	}
	if !hasFact(fs[0], "actor", "2") || !hasFact(fs[0], "engine", "7") || !hasFact(fs[0], "node", "6") {
		t.Fatalf("evidence %+v", fs[0].Facts)
	}
	if len(fs[0].Nodes) != 1 || fs[0].Nodes[0].Pool != "p1" {
		t.Fatalf("node not recorded: %+v", fs[0].Nodes)
	}
}

func TestLiveness(t *testing.T) {
	t.Parallel()
	quiet(t, "liveness", Snapshot{Policy: &policy.State{Epoch: 5, Label: 4}})
	quiet(t, "liveness", Snapshot{})
	f := fires(t, "liveness", Snapshot{Policy: &policy.State{Epoch: 6, Label: 4}}, Frozen, Fatal)
	if !hasFact(f, "epoch", "5") {
		t.Fatalf("empty epoch not named: %+v", f.Facts)
	}
}

func floorState(running bool) *policy.State {
	sched := policy.Schedule{{Wallet: 10, Pool: 1, Stake: 50}, {Wallet: 11, Pool: 2, Stake: 50}, {Wallet: 12, Pool: 3, Stake: 50}}
	return &policy.State{Epoch: 2, Label: 2, Set: sched, Mark: sched, Deleg: sched, Pools: []policy.Pool{
		{ID: 1, Reg: policy.Registered, Live: true, Running: true},
		{ID: 2, Reg: policy.Registered, Live: true, Running: running},
		{ID: 3, Reg: policy.Registered, Live: true, Running: running},
	}}
}

func TestStakeFloor(t *testing.T) {
	t.Parallel()
	quiet(t, "stake_floor", Snapshot{Policy: floorState(true)})
	quiet(t, "stake_floor", Snapshot{})
	f := fires(t, "stake_floor", Snapshot{Policy: floorState(false)}, FloorBreached, Error)
	if !hasFact(f, "epoch", "2") {
		t.Fatalf("epoch not named: %+v", f.Facts)
	}
}

func branch(pool, impl string, pts ...chain.Point) Branch {
	return Branch{Node: node(impl, pool), Points: pts}
}

func TestTipsAgree(t *testing.T) {
	t.Parallel()
	a := []chain.Point{pt(1, 1), pt(2, 2), pt(3, 3), pt(4, 4), pt(5, 5), pt(6, 6)}
	// One pool behind the other on the same chain is lag, not a fork.
	quiet(t, "tips", Snapshot{Tips: []Branch{branch("p1", "cardano-node", a...), branch("p2", "dingo", a[:3]...)}})
	// A short fork, k blocks deep on one side: it can still resolve.
	short := append(slices.Clone(a[:3]), pt(4, 0x14), pt(5, 0x15), pt(6, 0x16))
	quiet(t, "tips", Snapshot{Tips: []Branch{branch("p1", "cardano-node", a...), branch("p2", "dingo", short...)}})

	deep := append(slices.Clone(a[:2]), pt(3, 0x13), pt(4, 0x14), pt(5, 0x15), pt(6, 0x16))
	f := fires(t, "tips", Snapshot{Tips: []Branch{branch("p1", "cardano-node", a...), branch("p2", "dingo", deep...)}}, DeepFork, Fatal)
	if !hasFact(f, "fork_slot", "2") || len(f.Nodes) != 2 {
		t.Fatalf("fork point or pools missing: %+v %+v", f.Facts, f.Nodes)
	}
	// Windows that share nothing and both hold more than k blocks.
	other := []chain.Point{pt(3, 0x23), pt(4, 0x24), pt(5, 0x25), pt(6, 0x26)}
	fires(t, "tips", Snapshot{Tips: []Branch{branch("p1", "cardano-node", a[2:]...), branch("p2", "dingo", other...)}}, DeepFork, Fatal)
	// Windows too short to tell are not evaluable.
	quiet(t, "tips", Snapshot{Tips: []Branch{branch("p1", "cardano-node", pt(6, 6)), branch("p2", "dingo", pt(6, 0x26))}})
}

// TestRogueBlockAdopted: a block the forger built to break a rule must be
// on no honest node's chain. On one, it is a fatal finding carrying the
// block and the node's build.
func TestRogueBlockAdopted(t *testing.T) {
	t.Parallel()
	a := []chain.Point{pt(1, 1), pt(2, 2), pt(3, 3)}
	bad := RogueBlock{Point: pt(4, 0x44), Kind: "body-hash", Rule: "InvalidBodyHashBBODY", CBOR: []byte{0x85, 1, 2}}
	quiet(t, "rogue", Snapshot{
		Rogue: []RogueBlock{bad},
		Tips:  []Branch{branch("p1", "cardano-node", a...), branch("p2", "dingo", a...)},
	})
	n := node("dingo", "p2")
	n.Digest, n.Revisions = "sha256:beef", map[string]string{"dingo": "abc123"}
	with := Branch{Node: n, Points: append(slices.Clone(a), bad.Point)}
	f := fires(t, "rogue", Snapshot{
		Rogue: []RogueBlock{bad},
		Tips:  []Branch{branch("p1", "cardano-node", a...), with},
	}, RogueAdopted, Fatal)
	if !hasFact(f, "kind", "body-hash") || !hasFact(f, "rule", "InvalidBodyHashBBODY") || !hasFact(f, "pool", "p2") {
		t.Fatalf("facts = %+v", f.Facts)
	}
	if !slices.Equal(f.Block, bad.CBOR) || len(f.Nodes) != 1 || f.Nodes[0].Digest != "sha256:beef" ||
		f.Nodes[0].Revisions["dingo"] != "abc123" {
		t.Fatalf("finding lacks the block or the node's build: block %x nodes %+v", f.Block, f.Nodes)
	}
}

// TestBlockOverBudget: a block whose transactions declare more than
// maxBlockExUnits is fatal on either dimension; one at the limit is fine.
func TestBlockOverBudget(t *testing.T) {
	t.Parallel()
	max := common.ExUnits{Memory: 72_000_000, Steps: 20_000_000_000}
	at := pt(9, 9)
	quiet(t, "block_budget", Snapshot{Budget: &BlockBudget{At: at, Used: max, Max: max}})
	quiet(t, "block_budget", Snapshot{})
	f := fires(t, "block_budget", Snapshot{Budget: &BlockBudget{At: at, Used: common.ExUnits{Memory: max.Memory + 1, Steps: 1}, Max: max, Txs: 1}}, BlockOverBudget, Fatal)
	if !hasFact(f, "block", pointText(at)) {
		t.Fatalf("facts = %+v", f.Facts)
	}
	fires(t, "block_budget", Snapshot{Budget: &BlockBudget{At: at, Used: common.ExUnits{Memory: 1, Steps: max.Steps + 1}, Max: max}}, BlockOverBudget, Fatal)
}
