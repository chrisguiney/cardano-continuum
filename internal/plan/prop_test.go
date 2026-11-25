package plan

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// TestPropPlannerMatchesModel is the model's PropPlannerMatchesModel. It drives the ledger through random interleavings of
// the PlannerReservations model's actions against the fake chain: reserve,
// send, rebuild, block production, lagging chain-sync, mempool drops and drop
// notices, rollbacks of up to K blocks, forced inclusion, delays, outside
// spends of the actors' outputs and restarts. After every step it checks the
// model's store invariants on the ledger and its truth invariants on the
// chain.
func TestPropPlannerMatchesModel(t *testing.T) {
	seeds := 12
	if testing.Short() {
		seeds = 3
	}
	for seed := range uint64(seeds) {
		p := &prop{r: newRig(t, 3, 2), rnd: rand.New(rand.NewPCG(seed, 17))}
		for step := range 400 {
			p.step()
			if err := p.r.l.st.consistent(); err != nil {
				t.Fatalf("seed %d step %d (%s): %v", seed, step, p.last, err)
			}
			p.r.truth()
		}
		p.r.blocks(rigTTL)
		p.r.sync(-1)
		p.r.truth()
		if n := len(p.r.l.Unfinished()); n != 0 && p.unsettledAfterQuiet() {
			t.Fatalf("seed %d: %d attempts unfinished after the chain went quiet", seed, n)
		}
	}
}

type prop struct {
	r    *rig
	rnd  *rand.Rand
	last string
}

func (p *prop) actor() population.ActorID { return p.r.actors[p.rnd.IntN(len(p.r.actors))] }

func (p *prop) step() {
	r := p.r
	switch n := p.rnd.IntN(100); {
	case n < 18:
		p.last = "new intent"
		from, to := p.actor(), p.actor()
		if from == to {
			return
		}
		in := r.newIntent(from, to, 2_000_000+p.rnd.Uint64N(3_000_000))
		if a, err := r.attempt(in); err == nil && p.rnd.IntN(5) > 0 {
			r.send(a.Tx)
		}
	case n < 28:
		p.last = "rebuild"
		p.rebuild()
	case n < 36:
		p.last = "resend"
		if u := r.l.Unfinished(); len(u) > 0 {
			r.send(u[p.rnd.IntN(len(u))].Tx)
		}
	case n < 54:
		p.last = "blocks"
		r.blocks(1 + p.rnd.IntN(2))
	case n < 70:
		p.last = "sync"
		r.sync(1 + p.rnd.IntN(3))
	case n < 75:
		p.last = "drop"
		if m := r.chain.Mempool(); len(m) > 0 {
			p.inject(fakechain.Drop{Tx: m[p.rnd.IntN(len(m))]})
		}
	case n < 80:
		p.last = "drop notice"
		for _, u := range r.l.Unfinished() {
			if !r.chain.HasTx(fakechain.TxID(u.Tx)) {
				r.l.DropNotice(u.Tx)
			}
		}
	case n < 85:
		p.last = "rollback"
		d := 1 + p.rnd.IntN(int(r.k))
		if len(r.chain.Blocks())-d >= r.floor {
			p.inject(fakechain.Rollback{Depth: uint64(d), Readd: p.rnd.IntN(2) == 0})
		}
	case n < 89:
		p.last = "include"
		if m := r.chain.Mempool(); len(m) > 0 {
			p.inject(fakechain.Include{Txs: []fakechain.TxID{m[len(m)-1]}})
		}
	case n < 92:
		p.last = "delay"
		p.inject(fakechain.Delay{Slots: 1 + p.rnd.Uint64N(3)})
	case n < 96:
		p.last = "outside spend"
		p.outside()
	default:
		p.last = "restart"
		r.restart()
	}
}

func (p *prop) inject(f fakechain.Fault) {
	if err := p.r.chain.Inject(f); err != nil {
		p.r.t.Fatalf("%s: %v", p.last, err)
	}
}

// rebuild makes another attempt at an intent with an unfinished attempt: on
// whatever Available gives, which Reserve may refuse; on the earliest
// attempt's change, which it must refuse; or on the earliest attempt's
// inputs, which it must accept while they are still offered.
func (p *prop) rebuild() {
	r := p.r
	open := map[IntentID]Pending{}
	var ids []IntentID
	for _, u := range r.l.Unfinished() {
		if _, ok := open[u.Intent]; !ok {
			open[u.Intent] = u
			ids = append(ids, u.Intent)
		}
	}
	if len(ids) == 0 {
		return
	}
	id := ids[p.rnd.IntN(len(ids))]
	in, earliest := r.intents[id-1], open[id]
	switch p.rnd.IntN(3) {
	case 0:
		if a, err := r.attempt(in); err == nil {
			r.send(a.Tx)
		}
		return
	case 1:
		// On the earliest attempt's change: it lands beside the earliest,
		// so Reserve must refuse it (PlannerConflictWithLatestOnlyBug).
		ch := slices.DeleteFunc(r.l.Available(in.from, id, Opts{Chain: true}), func(u Utxo) bool { return u.Ref.Tx != earliest.Tx })
		if len(ch) == 0 {
			return
		}
		if a, tx, err := r.build(in, ch); err == nil {
			if err := r.reserve(in, a, tx); err == nil {
				r.t.Fatalf("an attempt on the earliest attempt's change was accepted")
			}
		}
		return
	}
	var ins []Utxo
	for _, u := range r.l.Available(in.from, id, Opts{Chain: true}) {
		if slices.Contains(earliest.Inputs, u.Ref) {
			ins = append(ins, u)
		}
	}
	if len(ins) != len(earliest.Inputs) {
		return // some input is spent in the view
	}
	a, tx, err := r.build(in, ins)
	if err != nil || r.cbor[a.Tx] != nil {
		return // in the same slot a rebuild can be an earlier transaction
	}
	if err := r.reserve(in, a, tx); err != nil {
		if errors.Is(err, ErrPaid) {
			return
		}
		r.t.Fatalf("rebuild on the earliest attempt's inputs refused: %v", err)
	}
	r.send(a.Tx)
}

// outside spends one of an actor's confirmed outputs without the planner,
// as the actor's other wallet would: contention the planner only learns
// about from the chain.
func (p *prop) outside() {
	r := p.r
	a := p.actor()
	us := r.chain.UtxosAt(r.wallet(a).Address())
	if len(us) == 0 {
		return
	}
	u := us[p.rnd.IntN(len(us))]
	if u.Output.Amount().Uint64() < 5_000_000 {
		return
	}
	in := &intent{from: a, to: a, amount: 2_000_000}
	_, tx, err := r.build(in, []Utxo{{Ref: Ref{Tx: TxID(u.Id.Id()), Index: u.Id.Index()}, Owner: a,
		Lovelace: u.Output.Amount().Uint64()}})
	if err != nil {
		return
	}
	// Refused or not, it is not the planner's: its outcome is the chain's.
	_ = r.chain.Submit(context.Background(), fakechain.EraConway, tx)
}

// unsettledAfterQuiet reports whether some unfinished attempt is past every
// way of finishing: its TTL is settled, so it should be dead.
func (p *prop) unsettledAfterQuiet() bool {
	settled := p.r.l.ResumePoint().Slot
	for _, u := range p.r.l.Unfinished() {
		if u.TTL != 0 && settled >= u.TTL {
			return true
		}
	}
	return false
}
