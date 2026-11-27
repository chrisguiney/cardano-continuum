package judge

import (
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Monitors runs the invariant monitors over the engine's state.
type Monitors struct {
	spec Spec
	env  Env
	log  *Log
	// monitors are the invariant checks; budget is the block the current
	// input applied, for the block-budget monitor.
	monitors []oracle.Monitor
	budget   *oracle.BlockBudget
}

// Begin starts an input: no block has been applied yet.
func (j *Monitors) Begin() { j.budget = nil }

// Watch runs the invariant monitors over the engine's state after an input:
// value, the settled point's move since before it (from), the intents that
// ended in it, the reservations and the stake floor, plus whatever the
// nodes answered (views).
func (j *Monitors) Watch(at sched.Time, from eventlog.ChainPoint, views []oracle.ViewCheck, tips []oracle.Branch) error {
	before, _ := chain.PointFromLog(from)
	conf, _ := chain.PointFromLog(j.env.Chain.Confirmed())
	res := oracle.ReservationsOf(j.env.Ledger.Holders())
	s := oracle.Snapshot{
		Seq: j.env.Journal.Seq(), Run: j.spec.Run,
		Value:        &oracle.Value{Genesis: j.spec.Genesis, Utxo: j.env.Ledger.Supply(), At: conf},
		Ends:         j.env.Journal.Ends(),
		Reservations: &res,
		Settled:      &oracle.SettledMove{From: before, To: conf},
		Views:        views,
		Tips:         tips,
		Budget:       j.budget,
	}
	// Without pools there is no forging stake to hold a floor: the fake
	// chain forges anyway, so the floor and liveness are not evaluable.
	if pol := j.env.Floor.State(at); len(pol.Pools) > 0 {
		s.Policy = &pol
	}
	return j.log.Report(at, oracle.Check(s, j.monitors))
}

// blockBudget sums the execution units a block's transactions declare, for
// the block-budget monitor; nil when the run has no block budget.
func (j *Monitors) Block(at eventlog.ChainPoint, txs []wire.TxDigest) {
	j.budget = j.blockBudget(at, txs)
}

func (j *Monitors) blockBudget(at eventlog.ChainPoint, txs []wire.TxDigest) *oracle.BlockBudget {
	max := j.spec.Build.MaxBlockExUnits
	if max.Memory == 0 && max.Steps == 0 {
		return nil
	}
	p, _ := chain.PointFromLog(at)
	b := &oracle.BlockBudget{At: p, Max: max, Txs: len(txs)}
	for _, tx := range txs {
		if tx.Units != nil {
			b.Used.Memory += tx.Units.Memory
			b.Used.Steps += tx.Units.Steps
		}
	}
	return b
}

// Snapshot fills in the engine's side of each view the runner
// asked a node for: the settled balances, at the settled point.
func (j *Monitors) Snapshot(at sched.Time, from eventlog.ChainPoint, m wire.MonitorSnapshot) error {
	conf, _ := chain.PointFromLog(j.env.Chain.Confirmed())
	bal := map[population.ActorID]uint64{}
	for id, v := range j.env.Ledger.Balances() {
		bal[population.ActorID(id)] = v
	}
	for i := range m.Views {
		m.Views[i].At, m.Views[i].Engine = conf, bal
	}
	return j.Watch(at, from, m.Views, m.Tips)
}
