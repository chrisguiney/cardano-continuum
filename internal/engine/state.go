package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	evclock "github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/window"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
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
	s := snapshot{Version: snapshotVersion, Now: e.clk.Now(), NextIntent: e.nextID, Conf: sv.Conf, Fees: sv.Fees, Foreign: sv.Foreign, RefillAt: e.refillAt, Deposits: e.deposits, Market: e.market, Lost: e.lost,
		Contracts: e.contracts, DeployAt: e.deployAt, DeployN: e.deployN}
	for _, id := range e.co.ids() {
		a, _ := e.co.get(id)
		st := e.co.rec(id)
		s.Actors = append(s.Actors, actorRec{
			ID: uint64(id), Profile: a.Profile, State: string(a.State), Born: a.Born, Changed: a.Changed,
			Frustration: a.Frustration, Competence: a.Competence, HazardAt: st.hazardAt, HazardTo: string(st.hazardTo), ActAt: st.actAt, Draws: st.draws, Booting: st.booting,
			Owed: st.owed, Paid: idList(st.paid), FollowUp: st.followUp, Waits: st.waits,
		})
	}
	for i := range e.co.births.len() {
		name, at, n := e.co.births.stream(i)
		s.Births = append(s.Births, birthRec{Profile: name, At: at, N: n})
	}
	s.Window = e.win.Volatile()
	for _, id := range e.pendingIDs() {
		s.Pending = append(s.Pending, *e.pending[id])
	}
	blob, err := e.led.Blob()
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

func (e *Engine) pendingIDs() []uint64 {
	ids := make([]uint64, 0, len(e.pending))
	for id := range e.pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
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
	e.nextID, e.deposits = s.NextIntent, s.Deposits
	// Between inputs the last Tick's time is the clock.
	e.clk.Restore(s.Now)
	e.market, e.stalled, e.lost = s.Market, s.Stalled, s.Lost
	e.paused, e.rateK, e.waves, e.adoptions = s.Paused, s.RateK, s.Waves, s.Adoptions
	e.contracts, e.deployAt, e.deployN = max(s.Contracts, 1), s.DeployAt, s.DeployN
	for i := uint64(1); i < e.contracts; i++ {
		if err := e.bookContract(i); err != nil {
			return nil, err
		}
	}
	e.pushDeploy()
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
			e.clk.Push(a.At, evclock.Event{Kind: evclock.Adopt, Intent: uint64(i)})
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
	if len(s.Births) != e.co.births.len() {
		return nil, fmt.Errorf("engine: snapshot has %d birth streams, config %d", len(s.Births), e.co.births.len())
	}
	for i, r := range s.Births {
		if name, _, _ := e.co.births.stream(i); r.Profile != name {
			return nil, fmt.Errorf("engine: snapshot birth stream %q, config %q", r.Profile, name)
		}
		e.co.births.resume(i, r.At, r.N)
	}
	if cfg.Faucet > 0 {
		e.refillAt = s.RefillAt
		e.clk.Push(e.refillAt, evclock.Event{Kind: evclock.Refill})
	}
	// After restoreActor: the actors' ceilings are set, the spends restored.
	e.costs.Restore(s.Charges)
	if err := e.restoreLedger(s); err != nil {
		return nil, err
	}
	for i := range s.Pending {
		p := &s.Pending[i]
		e.addPending(p)
		if p.Awaiting && p.NotBefore > e.clk.Now() {
			e.wake(p)
		}
	}
	attempts := 0
	for _, p := range e.pending {
		attempts += len(p.Txs)
	}
	if n := len(e.led.Unfinished()); n != attempts {
		return nil, fmt.Errorf("engine: snapshot has %d attempts, ledger %d unfinished", attempts, n)
	}
	return e, nil
}

func (e *Engine) restoreLedger(s snapshot) error {
	led, err := plan.Open(e.ledgerConfig(), store.Blob{Version: s.Ledger.Version, Data: s.Ledger.Data})
	if err != nil {
		return fmt.Errorf("engine: snapshot ledger: %w", err)
	}
	e.led = led
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
		res, err := led.Advance(ctx, chain.Event{Kind: chain.Forward, Block: lb, Point: lb.Point})
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
	a := e.co.revive(p, r.Born)
	if uint64(a.ID) != r.ID {
		return fmt.Errorf("engine: snapshot actor %d out of order (got %d)", r.ID, a.ID)
	}
	if err := e.co.life.walk(a, population.State(r.State), r.Changed); err != nil {
		return err
	}
	if err := e.co.pop().SetFrustration(a.ID, r.Frustration); err != nil {
		return err
	}
	if err := e.co.pop().SetCompetence(a.ID, r.Competence); err != nil {
		return err
	}
	st := e.co.register(a, p)
	st.hazardAt, st.hazardTo, st.actAt, st.draws = r.HazardAt, population.State(r.HazardTo), r.ActAt, r.Draws
	st.booting = r.Booting
	st.owed = r.Owed
	st.followUp, st.waits = r.FollowUp, r.Waits
	for _, id := range r.Paid {
		st.paid = append(st.paid, population.ActorID(id))
	}
	if st.hazardAt != population.Never {
		st.hazardH = e.clk.Push(st.hazardAt, evclock.Event{Kind: evclock.Hazard, Actor: a.ID})
	}
	if st.actAt != population.Never {
		st.actH = e.clk.Push(st.actAt, evclock.Event{Kind: evclock.Act, Actor: a.ID})
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
type evsched struct{ *evclock.Clock }

func (c evsched) Hazard(at sched.Time, id population.ActorID) sched.Handle {
	return c.Push(at, evclock.Event{Kind: evclock.Hazard, Actor: id})
}

func (c evsched) Birth(at sched.Time, idx int) sched.Handle {
	return c.Push(at, evclock.Event{Kind: evclock.Birth, Birth: idx})
}

// hooks is the rest of the engine as the cohort reaches it: the adversaries
// and the pacing.
type hooks struct{ e *Engine }

func (h hooks) Arm(id population.ActorID, p profile.Profile, pol agenda.Policy) agenda.Policy {
	return h.e.arm(id, p, pol)
}

func (h hooks) Adversary(id population.ActorID) bool { return h.e.isAdversary(id) }

func (h hooks) Spent(id population.ActorID) uint64 { return h.e.costs.Total(id) }

func (h hooks) Clocks(a population.Actor, p profile.Profile) ([]clock, profile.IntentKind) {
	cs, boot, _ := h.e.clocks(a, p)
	return cs, boot
}

func (h hooks) Wake(a population.Actor, st *actor, now sched.Time) { h.e.nextAct(a, st, now) }

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
	return sumCoins(e.coins(population.ActorID(id), plan.IntentID(e.nextID+1)))
}

// Balances returns each actor's settled lovelace, reserved or not, by
// actor ID.
func (e *Engine) Balances() map[uint64]uint64 {
	out := map[uint64]uint64{}
	for _, u := range e.led.Settled() {
		out[uint64(u.Owner)] += u.Lovelace
	}
	return out
}

// Lost is the lovelace settled payments sent to addresses nobody holds.
func (e *Engine) Lost() uint64 { return e.lost }

// Supply is everything the settled state accounts for: owned outputs, fees,
// outputs to addresses outside the population (locked script outputs among
// them) and the deposits the engine's certificates and proposals hold. It
// equals the genesis total while value is conserved.
func (e *Engine) Supply() uint64 {
	var sum uint64
	for _, u := range e.led.Settled() {
		sum += u.Lovelace
	}
	sv := e.win.Saved()
	return sum + sv.Fees + sv.Foreign + e.deposits
}

// Reservations is the ledger's blob, for store.Checkpoint.Reservations. The
// snapshot carries the same bytes.
func (e *Engine) Reservations() (store.Blob, error) { return e.led.Blob() }

// Actors is the number of actors ever spawned.
func (e *Engine) Actors() int { return e.co.pop().Len() }

// IntentOf is the intent an unfinished attempt belongs to.
func (e *Engine) IntentOf(tx TxID) (uint64, bool) {
	id, ok := e.byTx[tx]
	return id, ok
}

// Pending is the number of payments not yet confirmed or released.
func (e *Engine) Pending() int { return len(e.pending) }

// Submission is a payment waiting for the node's answer. Twice asks for the
// bytes to be sent a second time (a DoubleSubmit mistake).
type Submission struct {
	Intent uint64
	Actor  uint64
	ID     TxID
	Tx     []byte
	Twice  bool
	// Rivals are sent right after Tx: a double-spend race.
	Rivals []Rival
}

// InFlight returns the payments decided but not yet answered by the node,
// oldest first, leaving out those still backing off; a front-run goes
// ahead of all of them. It is the loop's effect queue: derived from state,
// so a restart resumes exactly the submissions a crash interrupted.
func (e *Engine) InFlight() []Submission {
	out := make([]Submission, 0, len(e.awaiting))
	for _, jump := range []bool{true, false} {
		for _, id := range e.awaiting {
			p := e.pending[id]
			if p.NotBefore > e.clk.Now() || p.Jump != jump {
				continue
			}
			out = append(out, Submission{Intent: id, Actor: p.Actor, ID: p.Tx, Tx: p.Cbor, Twice: p.Carried.Has(mistake.DoubleSubmit), Rivals: p.Rivals})
		}
	}
	return out
}

// ActorRows is the read model the store pages for the UI. Each row carries
// the actor's public identity, holdings and delegation.
func (e *Engine) ActorRows() []store.ActorRow {
	hs := e.holdings()
	out := make([]store.ActorRow, 0, len(e.co.ids()))
	for _, id := range e.co.ids() {
		a, _ := e.co.get(id)
		ident, err := e.co.idents.identityOf(a, e.co.rec(id), hs).Encode()
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
func (e *Engine) holdings() map[population.ActorID]*holding {
	out := map[population.ActorID]*holding{}
	of := func(id population.ActorID) *holding {
		h := out[id]
		if h == nil {
			h = &holding{}
			out[id] = h
		}
		return h
	}
	for _, u := range e.led.Settled() {
		h := of(u.Owner)
		h.settled += u.Lovelace
		h.utxos++
		if _, held := e.led.Holder(u.Ref); held {
			h.reserved += u.Lovelace
		}
	}
	for _, p := range e.pending {
		if p.Refill {
			continue
		}
		h := of(population.ActorID(p.Actor))
		h.inflight++
		h.unconfirmed += p.Amount
	}
	for id, us := range e.win.View().Owned() {
		for _, u := range us {
			of(id).addTokens(u)
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
