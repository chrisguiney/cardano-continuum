package engine

import (
	"context"
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
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
	s := snapshot{Version: snapshotVersion, Now: e.now, NextIntent: e.nextID, Conf: e.conf, Fees: e.fees, Foreign: e.foreign, RefillAt: e.refillAt}
	for _, id := range e.order {
		a, _ := e.census.Get(id)
		st := e.actors[id]
		s.Actors = append(s.Actors, actorRec{
			ID: uint64(id), Profile: a.Profile, State: string(a.State), Born: a.Born, Changed: a.Changed,
			Frustration: a.Frustration, HazardAt: st.hazardAt, HazardTo: string(st.hazardTo), ActAt: st.actAt, Draws: st.draws,
		})
	}
	for _, b := range e.births {
		s.Births = append(s.Births, birthRec{Profile: b.prof.Name, At: b.at, N: b.n})
	}
	s.Window = slices.Clone(e.window)
	for _, id := range e.pendingIDs() {
		s.Pending = append(s.Pending, *e.pending[id])
	}
	blob, err := e.led.Blob()
	if err != nil {
		return nil, fmt.Errorf("engine: ledger: %w", err)
	}
	s.Ledger = ledgerBlob{Version: blob.Version, Data: blob.Data}
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
	e.now, e.nextID, e.conf, e.fees, e.foreign = s.Now, s.NextIntent, s.Conf, s.Fees, s.Foreign
	for _, r := range s.Actors {
		if err := e.restoreActor(r); err != nil {
			return nil, err
		}
	}
	if len(s.Births) != len(e.births) {
		return nil, fmt.Errorf("engine: snapshot has %d birth streams, config %d", len(s.Births), len(e.births))
	}
	for i, r := range s.Births {
		b := e.births[i]
		if r.Profile != b.prof.Name {
			return nil, fmt.Errorf("engine: snapshot birth stream %q, config %q", r.Profile, b.prof.Name)
		}
		b.at, b.n = r.At, r.N
		e.pushBirth(b)
	}
	if cfg.Faucet > 0 {
		e.refillAt = s.RefillAt
		e.queue.Push(e.refillAt, qev{kind: evRefill})
	}
	if err := e.restoreLedger(s); err != nil {
		return nil, err
	}
	for i := range s.Pending {
		p := &s.Pending[i]
		e.addPending(p)
		if p.Awaiting && p.NotBefore > e.now {
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
	if conf, _ := chain.PointFromLog(e.conf); led.ResumePoint() != conf {
		return fmt.Errorf("engine: snapshot ledger settled at slot %d, engine confirmed slot %d", led.ResumePoint().Slot, e.conf.Slot)
	}
	ctx := context.Background()
	for _, b := range s.Window {
		lb, err := e.ledgerBlock(e.Tip(), b)
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
		e.window = append(e.window, b)
	}
	return nil
}

func (e *Engine) restoreActor(r actorRec) error {
	p, ok := e.cfg.Profiles.Get(r.Profile)
	if !ok {
		return fmt.Errorf("engine: snapshot actor %d has unknown profile %q", r.ID, r.Profile)
	}
	a := e.census.Spawn(p, e.root, r.Born)
	if uint64(a.ID) != r.ID {
		return fmt.Errorf("engine: snapshot actor %d out of order (got %d)", r.ID, a.ID)
	}
	// Walk the lifecycle to the recorded state. Every wallet state is
	// reachable from born through active, and none of the edges is
	// chain-gated.
	to := population.State(r.State)
	path := []population.State{}
	if to != population.Born {
		path = append(path, population.Active)
	}
	if to != population.Born && to != population.Active {
		path = append(path, to)
	}
	for _, s := range path {
		if err := e.census.Apply(population.Transition{ID: a.ID, To: s, At: r.Changed, Cause: population.Timer{}}); err != nil {
			return fmt.Errorf("engine: restore actor %d: %w", r.ID, err)
		}
	}
	if err := e.census.SetFrustration(a.ID, r.Frustration); err != nil {
		return err
	}
	st := e.register(a, p)
	st.hazardAt, st.hazardTo, st.actAt, st.draws = r.HazardAt, population.State(r.HazardTo), r.ActAt, r.Draws
	if st.hazardAt != population.Never {
		st.hazardH = e.queue.Push(st.hazardAt, qev{kind: evHazard, actor: a.ID})
	}
	if st.actAt != population.Never {
		st.actH = e.queue.Push(st.actAt, qev{kind: evAct, actor: a.ID})
	}
	return nil
}

// ledgerUtxos turns coins into the outputs the builder spends.
func (e *Engine) ledgerUtxos(w keys.Wallet, coins []Coin) []common.Utxo {
	out := make([]common.Utxo, len(coins))
	for i, c := range coins {
		out[i] = ownedUtxo(w.Address(), c.Ref, c.Lovelace)
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
	id, ok := o.e.byAddr[string(b)]
	return id, ok
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

func ownedUtxo(addr common.Address, r Ref, lovelace uint64) common.Utxo {
	o := babbage.BabbageTransactionOutput{
		OutputAddress: addr,
		OutputAmount:  mary.MaryTransactionOutputValue{Amount: lovelace},
	}
	return common.Utxo{Id: shelley.ShelleyTransactionInput{TxId: common.Blake2b256(r.Tx), OutputIndex: r.Index}, Output: &o}
}

// view adapts the engine's own chain view to agenda.View. Decisions read
// only this, never the live chain, so replay sees the same world.
type view struct{ e *Engine }

func (v view) Balance(id population.ActorID) profile.Lovelace {
	return profile.Lovelace(v.e.Spendable(uint64(id)))
}
func (view) Pools() []agenda.PoolInfo                                  { return nil }
func (v view) Epoch() uint64                                           { return v.e.cfg.Slots.EpochAt(v.e.now) }
func (view) StakeRegistered(population.ActorID) bool                   { return false }
func (view) DelegatedTo(population.ActorID) (population.ActorID, bool) { return 0, false }
func (view) PoolRegistered(population.ActorID) bool                    { return false }

// Now is the engine's clock: the time of the last Tick.
func (e *Engine) Now() sched.Time { return e.now }

// NextEvent is when the engine next wants a Tick.
func (e *Engine) NextEvent() (sched.Time, bool) { return e.queue.Peek() }

// Confirmed is the K-deep point durable state comes from.
func (e *Engine) Confirmed() eventlog.ChainPoint { return e.conf }

// Points are the engine's intersection points, oldest first: the confirmed
// point and every volatile block above it, so FindIntersect never has to fall
// back below the confirmed point.
func (e *Engine) Points() []eventlog.ChainPoint {
	out := []eventlog.ChainPoint{e.conf}
	for _, b := range e.window {
		out = append(out, b.Point)
	}
	return out
}

// Tip is the last block the engine applied.
func (e *Engine) Tip() eventlog.ChainPoint {
	if n := len(e.window); n > 0 {
		return e.window[n-1].Point
	}
	return e.conf
}

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

// Supply is everything the settled state accounts for: owned outputs, fees
// and outputs to addresses outside the population. It equals the genesis
// total while value is conserved.
func (e *Engine) Supply() uint64 {
	var sum uint64
	for _, u := range e.led.Settled() {
		sum += u.Lovelace
	}
	return sum + e.fees + e.foreign
}

// Reservations is the ledger's blob, for store.Checkpoint.Reservations. The
// snapshot carries the same bytes.
func (e *Engine) Reservations() (store.Blob, error) { return e.led.Blob() }

// Actors is the number of actors ever spawned.
func (e *Engine) Actors() int { return e.census.Len() }

// Pending is the number of payments not yet confirmed or released.
func (e *Engine) Pending() int { return len(e.pending) }

// Submission is a payment waiting for the node's answer.
type Submission struct {
	Intent uint64
	Actor  uint64
	ID     TxID
	Tx     []byte
}

// InFlight returns the payments decided but not yet answered by the node,
// oldest first, leaving out those still backing off. It is the loop's effect queue: derived from state, so a
// restart resumes exactly the submissions a crash interrupted.
func (e *Engine) InFlight() []Submission {
	out := make([]Submission, 0, len(e.awaiting))
	for _, id := range e.awaiting {
		p := e.pending[id]
		if p.NotBefore > e.now {
			continue
		}
		out = append(out, Submission{Intent: id, Actor: p.Actor, ID: p.Tx, Tx: p.Cbor})
	}
	return out
}

// ActorRows is the read model the store pages for the UI.
func (e *Engine) ActorRows() []store.ActorRow {
	out := make([]store.ActorRow, 0, len(e.order))
	for _, id := range e.order {
		a, _ := e.census.Get(id)
		out = append(out, store.ActorRow{ID: uint64(id), Name: fmt.Sprintf("%s-%d", a.Profile, id), Role: string(a.State)})
	}
	return out
}
