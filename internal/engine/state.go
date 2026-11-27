package engine

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"

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
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

func blake2b256(b []byte) common.Blake2b256 { return common.Blake2b256Hash(b) }

// Snapshot implements eventlog.Engine: the whole state in canonical CBOR,
// the ledger's blob included.
func (e *Engine) Snapshot() ([]byte, error) {
	sv := e.win.Saved()
	nextID, deposits := e.pay.Counters()
	s := snapshot{Version: snapshotVersion, Now: e.clk.Now(), NextIntent: nextID, Conf: sv.Conf, Fees: sv.Fees, Foreign: sv.Foreign, RefillAt: e.refillAt, Deposits: deposits, Market: e.pc.Market(), Lost: e.lost,
		Contracts: e.in.n, DeployAt: e.in.at, DeployN: e.in.drawn}
	for _, id := range e.co.IDs() {
		a, _ := e.co.Get(id)
		st := e.co.Rec(id)
		s.Actors = append(s.Actors, actorRec{
			ID: uint64(id), Profile: a.Profile, State: string(a.State), Born: a.Born, Changed: a.Changed,
			Frustration: a.Frustration, Competence: a.Competence, HazardAt: st.HazardAt, HazardTo: string(st.HazardTo), ActAt: st.ActAt, Draws: st.Draws, Booting: st.Booting,
			Owed: st.Owed, Paid: idList(st.Paid), FollowUp: st.FollowUp, Waits: st.Waits,
		})
	}
	for i := range e.co.Births.Len() {
		name, at, n := e.co.Births.Stream(i)
		s.Births = append(s.Births, birthRec{Profile: name, At: at, N: n})
	}
	s.Window = e.win.Volatile()
	for _, id := range e.pay.IDs() {
		s.Pending = append(s.Pending, recOf(e.pay.Get(id)))
	}
	blob, err := e.pay.Blob()
	if err != nil {
		return nil, fmt.Errorf("engine: ledger: %w", err)
	}
	s.Ledger = ledgerBlob{Version: blob.Version, Data: blob.Data}
	s.Dark, s.Down, s.Repairs, s.Stalled = idSet(e.dark), idSet(e.down), idSet(e.repairs), e.stalled
	s.Paused, s.RateK, s.Waves, s.Adoptions = e.paused, e.rateK, e.waves, slices.Clone(e.adoptions)
	for _, k := range slices.Sorted(maps.Keys(e.weights)) {
		s.Weights = append(s.Weights, weightRec{Kind: string(k), Weight: e.weights[k]})
	}
	for _, id := range slices.Sorted(mapKeys(e.images)) {
		s.Images = append(s.Images, imageRec{Pool: uint64(id), Ref: e.images[id]})
	}
	s.Charges = e.costs.Entries()
	s.Contested = slices.SortedFunc(maps.Keys(e.contested), cmpLogRef)
	if s.View, err = encodeView(e.win.View().Base()); err != nil {
		return nil, err
	}
	return eventlog.Encode(s)
}

// Restore rebuilds an engine from a Snapshot. The census, queue and indexes
// are derived again: actors are respawned from the same root seed, which
// reproduces their traits, and walked to their recorded state. The ledger
// stores only its settled part, so the window is fed to it again.
func Restore(cfg Config, raw []byte) (*Engine, error) {
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
	e.clk.Restore(s.Now)
	e.pc.SetMarket(s.Market)
	e.paused, e.rateK, e.waves, e.adoptions, e.stalled, e.lost = s.Paused, s.RateK, s.Waves, s.Adoptions, s.Stalled, s.Lost
	e.in.n, e.in.at, e.in.drawn = max(s.Contracts, 1), s.DeployAt, s.DeployN
	for i := uint64(1); i < e.in.n; i++ {
		if err := e.in.bookContract(i); err != nil {
			return nil, err
		}
	}
	e.in.pushDeploy()
	if len(s.Weights) > 0 {
		e.weights = map[profile.IntentKind]float64{}
		for _, w := range s.Weights {
			e.weights[profile.IntentKind(w.Kind)] = w.Weight
		}
	}
	for _, r := range s.Images {
		e.images[population.ActorID(r.Pool)] = r.Ref
	}
	for _, r := range s.Contested {
		e.contested[r] = true
	}
	for i, a := range e.adoptions {
		if !a.Done {
			e.clk.Push(a.At, clock.Event{Kind: clock.Adopt, Intent: uint64(i)})
		}
	}
	for _, set := range []struct {
		m   map[population.ActorID]bool
		ids []uint64
	}{{e.dark, s.Dark}, {e.down, s.Down}, {e.repairs, s.Repairs}} {
		for _, id := range set.ids {
			set.m[population.ActorID(id)] = true
		}
	}
	for _, r := range s.Actors {
		if err := e.restoreActor(r); err != nil {
			return nil, err
		}
	}
	if len(s.Births) != e.co.Births.Len() {
		return nil, fmt.Errorf("engine: snapshot has %d birth streams, config %d", len(s.Births), e.co.Births.Len())
	}
	for i, r := range s.Births {
		if name, _, _ := e.co.Births.Stream(i); r.Profile != name {
			return nil, fmt.Errorf("engine: snapshot birth stream %q, config %q", r.Profile, name)
		}
		e.co.Births.Resume(i, r.At, r.N)
	}
	if cfg.Faucet > 0 {
		e.refillAt = s.RefillAt
		e.clk.Push(e.refillAt, clock.Event{Kind: clock.Refill})
	}
	// After restoreActor: the actors' ceilings are set, the spends restored.
	e.costs.Restore(s.Charges)
	if err := e.restoreLedger(s); err != nil {
		return nil, err
	}
	for i := range s.Pending {
		p := pendingOf(s.Pending[i])
		e.pay.Add(p)
		if p.Awaiting && p.NotBefore > e.clk.Now() {
			e.retry.Wake(p)
		}
	}
	attempts := 0
	for p := range e.pay.All() {
		attempts += len(p.Txs)
	}
	if n := len(e.pay.Reads().Unfinished()); n != attempts {
		return nil, fmt.Errorf("engine: snapshot has %d attempts, ledger %d unfinished", attempts, n)
	}
	return e, nil
}

func (e *Engine) restoreLedger(s snapshot) error {
	if err := e.pay.Open(e.ledgerConfig(), store.Blob{Version: s.Ledger.Version, Data: s.Ledger.Data}, s.NextIntent, s.Deposits); err != nil {
		return fmt.Errorf("engine: snapshot ledger: %w", err)
	}
	led := e.pay.Reads()
	if conf, _ := chain.PointFromLog(s.Conf); led.ResumePoint() != conf {
		return fmt.Errorf("engine: snapshot ledger settled at slot %d, engine confirmed slot %d", led.ResumePoint().Slot, s.Conf.Slot)
	}
	vs, err := decodeView(s.View)
	if err != nil {
		return err
	}
	if err := e.win.Open(vs, window.Saved{Conf: s.Conf, Fees: s.Fees, Foreign: s.Foreign}); err != nil {
		return fmt.Errorf("engine: snapshot view: %w", err)
	}
	ctx := context.Background()
	for _, b := range s.Window {
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
	return nil
}

func (e *Engine) restoreActor(r actorRec) error {
	p, ok := e.cfg.Profiles.Get(r.Profile)
	if !ok {
		return fmt.Errorf("engine: snapshot actor %d has unknown profile %q", r.ID, r.Profile)
	}
	a := e.co.Revive(p, r.Born)
	if uint64(a.ID) != r.ID {
		return fmt.Errorf("engine: snapshot actor %d out of order (got %d)", r.ID, a.ID)
	}
	if err := e.co.Life.Walk(a, population.State(r.State), r.Changed); err != nil {
		return err
	}
	if err := e.co.Pop().SetFrustration(a.ID, r.Frustration); err != nil {
		return err
	}
	if err := e.co.Pop().SetCompetence(a.ID, r.Competence); err != nil {
		return err
	}
	st := e.co.Register(a, p)
	st.HazardAt, st.HazardTo, st.ActAt, st.Draws = r.HazardAt, population.State(r.HazardTo), r.ActAt, r.Draws
	st.Booting = r.Booting
	st.Owed = r.Owed
	st.FollowUp, st.Waits = r.FollowUp, r.Waits
	for _, id := range r.Paid {
		st.Paid = append(st.Paid, population.ActorID(id))
	}
	if st.HazardAt != population.Never {
		st.HazardH = e.clk.Push(st.HazardAt, clock.Event{Kind: clock.Hazard, Actor: a.ID})
	}
	if st.ActAt != population.Never {
		st.ActH = e.clk.Push(st.ActAt, clock.Event{Kind: clock.Act, Actor: a.ID})
	}
	return nil
}

// ledgerUtxos turns coins into the outputs the builder spends. The ledger
// knows only lovelace; the view holds each output as the block carried it,
// so an input holding tokens hands them to the builder, which returns them
// in the change. Spending a token as plain lovelace leaves the
// transaction's value unbalanced (ValueNotConservedUTxO). A coin the view
// lacks is the change of an attempt not yet in a block, built from
// lovelace-only inputs.
func (e *Engine) ledgerUtxos(w keys.Wallet, coins []Coin) []common.Utxo {
	out := make([]common.Utxo, len(coins))
	for i, c := range coins {
		if u, ok := e.win.View().Utxo(chain.Ref{Tx: chain.TxID(c.Ref.Tx), Index: c.Ref.Index}); ok {
			out[i] = u
			continue
		}
		out[i] = window.Utxo(w.Address(), c.Ref, c.Lovelace)
	}
	return out
}

func (e *Engine) ledgerConfig() plan.Config {
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

func (c evsched) Birth(at sched.Time, idx int) sched.Handle {
	return c.Push(at, clock.Event{Kind: clock.Birth, Birth: idx})
}

// hooks is the rest of the engine as the cohort reaches it: the adversaries
// and the pacing.
type hooks struct{ e *Engine }

func (h hooks) Arm(id population.ActorID, p profile.Profile, pol agenda.Policy) agenda.Policy {
	return h.e.arm(id, p, pol)
}

func (h hooks) Adversary(id population.ActorID) bool { return h.e.isAdversary(id) }

func (h hooks) Spent(id population.ActorID) uint64 { return h.e.costs.Total(id) }

func (h hooks) Clocks(a population.Actor, p profile.Profile) ([]wire.Clock, profile.IntentKind) {
	cs, boot, _ := h.e.pc.Clocks(a, p)
	return cs, boot
}

func (h hooks) Wake(a population.Actor, st *cohort.Actor, now sched.Time) {
	h.e.pc.Next(a, subject(st), now)
}

func (h hooks) InFlight(id population.ActorID, kind string) bool { return h.e.pay.Has(id, kind) }

func (h hooks) Agenda() agenda.View { return view(h) }

func (h hooks) RateK() uint32 { return h.e.rateK }

func (h hooks) Weights() map[profile.IntentKind]float64 { return h.e.weights }

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
	return h.e.in.attemptOf(ctx, at, p)
}

// Submitted implements pay.Builder.
func (h hooks) Submitted(at sched.Time, p *wire.Pending, built build.Built, inputs []Ref) {
	h.e.in.emitSubmitted(at, p, built, inputs)
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

// Now is the engine's clock: the time of the last Tick.
func (e *Engine) Now() sched.Time { return e.clk.Now() }

// NextEvent is when the engine next wants a Tick.
func (e *Engine) NextEvent() (sched.Time, bool) { return e.clk.Peek() }

// Confirmed is the K-deep point durable state comes from.
func (e *Engine) Confirmed() eventlog.ChainPoint { return e.win.Confirmed() }

// Points are the engine's intersection points, oldest first: the confirmed
// point and every volatile block above it, so FindIntersect never has to fall
// back below the confirmed point.
func (e *Engine) Points() []eventlog.ChainPoint { return e.win.Points() }

// Tip is the last block the engine applied.
func (e *Engine) Tip() eventlog.ChainPoint { return e.win.Tip() }

// Spendable is the lovelace the ledger would offer the actor's next
// payment: its settled outputs that are neither reserved nor spent in a
// block above the settled point.
func (e *Engine) Spendable(id uint64) uint64 {
	return e.pay.Spendable(id)
}

// Balances returns each actor's settled lovelace, reserved or not, by
// actor ID.
func (e *Engine) Balances() map[uint64]uint64 {
	return e.pay.Balances()
}

// Lost is the lovelace settled payments sent to addresses nobody holds.
func (e *Engine) Lost() uint64 { return e.lost }

// Supply is everything the settled state accounts for: owned outputs, fees,
// outputs to addresses outside the population (locked script outputs among
// them) and the deposits the engine's certificates and proposals hold. It
// equals the genesis total while value is conserved.
func (e *Engine) Supply() uint64 {
	sv := e.win.Saved()
	return e.pay.Supply() + sv.Fees + sv.Foreign
}

// Reservations is the ledger's blob, for store.Checkpoint.Reservations. The
// snapshot carries the same bytes.
func (e *Engine) Reservations() (store.Blob, error) { return e.pay.Blob() }

// Actors is the number of actors ever spawned.
func (e *Engine) Actors() int { return e.co.Pop().Len() }

// IntentOf is the intent an unfinished attempt belongs to.
func (e *Engine) IntentOf(tx TxID) (uint64, bool) {
	return e.pay.Of(tx)
}

// Pending is the number of payments not yet confirmed or released.
func (e *Engine) Pending() int { return e.pay.Len() }

// Submission is a payment waiting for the node's answer; see
// pay.Submission.
type Submission = pay.Submission

// InFlight returns the payments decided but not yet answered by the node,
// oldest first, leaving out those still backing off; a front-run goes
// ahead of all of them. It is the loop's effect queue: derived from state,
// so a restart resumes exactly the submissions a crash interrupted.
func (e *Engine) InFlight() []Submission {
	return e.pay.InFlight(e.clk.Now())
}

// ActorKinds returns every role an actor row can report: the profile kinds
// plus the proposer and adversary refinements.
func ActorKinds() []string { return cohort.ActorKinds() }

// ActorRows is the read model the store pages for the UI. Each row carries
// the actor's public identity, holdings and delegation.
func (e *Engine) ActorRows() []store.ActorRow {
	hs := e.holdings()
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
func (e *Engine) holdings() map[population.ActorID]*cohort.Holding {
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

func idList(ids []population.ActorID) []uint64 {
	out := make([]uint64, len(ids))
	for i, id := range ids {
		out[i] = uint64(id)
	}
	return out
}

// idSet is a set of actors, ascending.
func idSet(m map[population.ActorID]bool) []uint64 {
	out := make([]uint64, 0, len(m))
	for id := range m {
		out = append(out, uint64(id))
	}
	slices.Sort(out)
	return out
}
