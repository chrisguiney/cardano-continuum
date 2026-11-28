package engine

import (
	"context"
	"fmt"
	"math/rand/v2"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/cohort"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pay"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/window"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

func blake2b256(b []byte) common.Blake2b256 { return common.Blake2b256Hash(b) }

// Restore rebuilds an engine from a Snapshot: each component loads its
// section. The census, queue and indexes are derived again: actors are
// respawned from the same root seed, which reproduces their traits, and
// walked to their recorded state. The ledger stores only its settled part,
// so the window is fed to it again.
//
// The load order is load-bearing. Loads that queue events (steer's
// adoptions, the cohort's hazards, acts and births, the faucet's round, the
// retry wakes) push them in this order, and the queue hands out events at
// one time in push order before sorting the batch, so moving a load
// reorders a run. The attack accounts load after the cohort, and the
// pending intents after the window is replayed onto the ledger.
func Restore(cfg Config, raw []byte) (*Engine, error) {
	// A CBOR build's snapshot is a CBOR map, never a JSON object.
	if len(raw) > 0 && raw[0] != '{' {
		return nil, fmt.Errorf("engine: snapshot: %w", eventlog.ErrOldFormat)
	}

	var s snapshot
	if err := eventlog.Decode(raw, &s); err != nil {
		return nil, fmt.Errorf("engine: decode snapshot: %w", err)
	}

	if s.Version != snapshotVersion {
		return nil, fmt.Errorf("engine: snapshot version %d, want %d", s.Version, snapshotVersion)
	}

	e, err := newEmpty(cfg)
	if err != nil {
		return nil, err
	}

	// Between inputs the last Tick's time is the clock.
	e.clk.Load(s.Now)
	e.armFork(s.Now)
	e.pc.Load(s.Market)
	if err := e.in.Load(s.Contracts); err != nil {
		return nil, err
	}

	e.st.Load(s.Steer)
	e.fl.Load(s.Floor)
	if err := e.co.Load(cfg.Profiles, s.Cohort.Actors, s.Cohort.Births); err != nil {
		return nil, err
	}
	e.co.Market.Load(s.Cohort.Quote)

	if cfg.Faucet > 0 {
		e.fc.Load(s.RefillAt)
	}
	// After the cohort: arming an adversary sets its ceiling, and the spends
	// load on top of it.
	e.adv.Load(s.Attack.Charges, s.Attack.Contested)
	if err := loadLedger(e, s); err != nil {
		return nil, err
	}

	if err := e.retry.Load(e.clk.Now(), s.Pay.Pending); err != nil {
		return nil, err
	}
	return e, nil
}

func loadLedger(e *Engine, s snapshot) error {
	if err := e.pay.Load(ledgerConfig(e), s.Pay); err != nil {
		return fmt.Errorf("engine: snapshot ledger: %w", err)
	}

	e.jd.Load(s.Lost)
	led := e.pay.Reads()
	conf := s.Window.Conf
	if pt, _ := chain.PointFromLog(conf); led.ResumePoint() != pt {
		return fmt.Errorf("engine: snapshot ledger settled at slot %d, engine confirmed slot %d", led.ResumePoint().Slot, conf.Slot)
	}

	// The window's blocks are fed to the ledger below, not by Load.
	if err := e.win.Load(window.Image{Saved: s.Window.Saved, View: s.Window.View}); err != nil {
		return err
	}

	ctx := context.Background()
	for _, b := range s.Window.Blocks {
		lb, vb, err := e.win.Blocks(b, e.co)
		if err != nil {
			return err
		}
		// Fewer than K+1 blocks above the settled point settle nothing, and
		// death is judged at the settled point, so replaying the window
		// finishes no payment.
		res, err := e.pay.Advance(ctx, chain.Event{Kind: chain.Forward, Block: lb, Point: lb.Point})
		if err != nil {
			return fmt.Errorf("engine: snapshot window: %w", err)
		}
		if len(res.Settled)+len(res.Dead) > 0 {
			return fmt.Errorf("engine: snapshot window at slot %d finishes a payment", b.Point.Slot)
		}
		if err := e.win.Push(b, vb); err != nil {
			return fmt.Errorf("engine: snapshot window: %w", err)
		}
	}
	e.builder.Fork(forkState(e.win.View(), e.cfg.HardFork).Version)
	return nil
}

func ledgerConfig(e *Engine) plan.Config {
	return plan.Config{K: e.cfg.K, Owners: owners{e}, Store: logged{}}
}

// owners maps addresses to actors for the ledger.
type owners struct{ e *Engine }

func (o owners) Owner(a common.Address) (population.ActorID, bool) {
	b, err := a.Bytes()
	if err != nil {
		return 0, false
	}
	return o.e.co.Owner(b)
}

// evsched is the event queue as the cohort schedules on it.
type evsched struct{ *clock.Clock }

func (c evsched) Hazard(at sched.Time, id population.ActorID) sched.Handle {
	return c.Push(at, clock.Event{Kind: clock.Hazard, Actor: id})
}

func (c evsched) Act(at sched.Time, id population.ActorID) sched.Handle {
	return c.Push(at, clock.Event{Kind: clock.Act, Actor: id})
}

// Retry implements pay.Scheduler.
func (c evsched) Retry(at sched.Time, id uint64) {
	c.Push(at, clock.Event{Kind: clock.Retry, Intent: id})
}

// Adopt implements steerClock.
func (c evsched) Adopt(at sched.Time, i uint64) {
	c.Push(at, clock.Event{Kind: clock.Adopt, Intent: i})
}

// Refill implements faucet.Clock.
func (c evsched) Refill(at sched.Time) { c.Push(at, clock.Event{Kind: clock.Refill}) }

// Deploy implements intent.Clock.
func (c evsched) Deploy(at sched.Time) { c.Push(at, clock.Event{Kind: clock.Deploy}) }

// Fork schedules the run's own hard-fork step.
func (c evsched) Fork(at sched.Time) { c.Push(at, clock.Event{Kind: clock.Fork}) }

// Quote implements cohort.Scheduler.
func (c evsched) Quote(at sched.Time) { c.Push(at, clock.Event{Kind: clock.Quote}) }

func (c evsched) Birth(at sched.Time, idx int) sched.Handle {
	return c.Push(at, clock.Event{Kind: clock.Birth, Birth: idx})
}

// hooks is the rest of the engine as the cohort reaches it: the adversaries
// and the pacing.
type hooks struct{ e *Engine }

func (h hooks) Arm(id population.ActorID, p profile.Profile, pol agenda.Policy) agenda.Policy {
	return h.e.adv.Arm(id, p, pol)
}

func (h hooks) Adversary(id population.ActorID) bool { return h.e.adv.Adversary(id) }

func (h hooks) Spent(id population.ActorID) uint64 { return h.e.adv.Spent(id) }

func (h hooks) Clocks(a population.Actor, p profile.Profile) ([]wire.Clock, profile.IntentKind) {
	cs, boot, _ := h.e.pc.Clocks(a, p)
	return cs, boot
}

func (h hooks) Wake(a population.Actor, st *cohort.Actor, now sched.Time) {
	h.e.pc.Next(a, subject(st), now)
}

func (h hooks) InFlight(id population.ActorID, kind string) bool { return h.e.pay.Has(id, kind) }

func (h hooks) Agenda() agenda.View { return viewOf(h.e) }

func (h hooks) RateK() uint32 { return h.e.st.RateK() }

func (h hooks) Weights() map[profile.IntentKind]float64 { return h.e.st.Weights() }

// Get implements pay.Actors.
func (h hooks) Get(id population.ActorID) (population.Actor, bool) { return h.e.co.Get(id) }

// Draw implements pay.Actors.
func (h hooks) Draw(a population.Actor, purpose string) *rand.Rand {
	return h.e.co.Rec(a.ID).Draw(a, purpose)
}

// Known implements pay.Actors.
func (h hooks) Known(id population.ActorID) bool { return h.e.co.Rec(id) != nil }

// frustration is what one give-up adds to an actor's frustration, which
// raises its dormancy hazard (population.DormancyOnsetRate).
const frustration = 1.0

// Learn raises the actor's competence toward 1 at its learning rate: a
// fix it made worked.
func (h hooks) Learn(id population.ActorID) error {
	e := h.e
	a, ok := e.co.Get(id)
	if !ok {
		return nil
	}
	return e.co.Pop().SetCompetence(id, a.Competence+a.Traits.LearningRate*(1-a.Competence))
}

// Frustrate raises the actor's frustration, and with it the dormancy
// hazard, which is drawn again from now.
func (h hooks) Frustrate(at sched.Time, id population.ActorID) error {
	e := h.e
	a, ok := e.co.Get(id)
	if !ok {
		return nil
	}
	if err := e.co.Pop().SetFrustration(id, a.Frustration+frustration); err != nil {
		return err
	}
	st := e.co.Rec(id)
	if st.HazardAt != population.Never {
		e.clk.Cancel(st.HazardH)
	}
	a, _ = e.co.Get(id)
	e.co.Life.NextHazard(a, st, at)
	return nil
}

// Attempt implements pay.Builder.
func (h hooks) Attempt(ctx context.Context, at sched.Time, p *wire.Pending) (build.Built, []Ref, uint64, string, error) {
	return h.e.in.Attempt(ctx, at, p)
}

// Submitted implements pay.Builder.
func (h hooks) Submitted(at sched.Time, p *wire.Pending, built build.Built, inputs []Ref) {
	h.e.in.Submitted(at, p, built, inputs)
}

// logged is the ledger's Committer inside the pure engine. It writes
// nothing: every ledger change is a deterministic result of a logged input,
// so the input being durable makes the change durable, and the Runner
// checkpoints the blob with the rest of the state in one transaction.
type logged struct{}

func (logged) Commit(context.Context, store.Blob) error { return nil }

// Discards tells the ledger not to encode or copy its state for a Commit
// that stores nothing.
func (logged) Discards() bool { return true }

// Submission is a payment waiting for the node's answer; see
// pay.Submission.
type Submission = pay.Submission

// ActorKinds returns every role an actor row can report: the profile kinds
// plus the proposer and adversary refinements.
func ActorKinds() []string { return cohort.ActorKinds() }

// actorRows is the read model the store pages for the UI. Each row carries
// the actor's public identity, holdings and delegation.
func actorRows(e *Engine) []store.ActorRow {
	hs := holdings(e)
	out := make([]store.ActorRow, 0, len(e.co.IDs()))
	for _, id := range e.co.IDs() {
		a, _ := e.co.Get(id)
		ident, err := e.co.Idents.Identity(a, e.co.Rec(id), hs).Encode()
		if err != nil {
			// Identity is a struct of plain fields; encoding cannot fail.
			panic(fmt.Sprintf("engine: actor %d identity: %v", id, err))
		}
		out = append(out, store.ActorRow{ID: uint64(id), Name: fmt.Sprintf("%s-%d", a.Profile, id), Role: string(a.State), Profile: a.Profile, Identity: ident})
	}
	return out
}

// holdings reads every actor's holdings in one pass over the ledger, the
// unfinished payments and the view's outputs.
func holdings(e *Engine) map[population.ActorID]*cohort.Holding {
	out := map[population.ActorID]*cohort.Holding{}
	of := func(id population.ActorID) *cohort.Holding {
		h := out[id]
		if h == nil {
			h = &cohort.Holding{}
			out[id] = h
		}
		return h
	}
	for _, u := range e.pay.Reads().Settled() {
		h := of(u.Owner)
		h.Settled += u.Lovelace
		h.UTxOs++
		if _, held := e.pay.Reads().Holder(u.Ref); held {
			h.Reserved += u.Lovelace
		}
	}
	for p := range e.pay.All() {
		if p.Refill {
			continue
		}
		h := of(population.ActorID(p.Actor))
		h.InFlight++
		h.Unconfirmed += p.Amount
	}
	for id, us := range e.win.View().Owned() {
		for _, u := range us {
			of(id).AddTokens(u)
		}
	}
	return out
}
