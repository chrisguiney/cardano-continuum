package intent

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Maker builds an intent's attempts and logs them.
type Maker struct {
	core
	kinds map[string]kind
}

// Submitted logs the reserved attempt of p.
func (m *Maker) Submitted(at sched.Time, p *wire.Pending, built build.Built, inputs []wire.Ref) {
	attack := p.Attack
	if attack == "" && m.env.Spend.Adversary(population.ActorID(p.Actor)) {
		attack = m.spec.Cover
	}
	s := wire.Submitted{
		Intent: p.ID, ID: wire.TxID(built.ID), To: p.To, Amount: p.Amount, Fee: built.Fee, TTL: p.TTL, Inputs: inputs, Tx: built.Tx, Kind: p.Kind,
		Mistakes: p.Carried, Attack: attack, Action: p.GovType, Contract: p.Contract,
	}
	if p.Kind == string(profile.IntentVote) && p.Action != nil {
		_, s.Voter = Voter(m.env.Ring, population.ActorID(p.Actor))
		s.Proposal = chain.GovID{Tx: chain.TxID(p.Action.Tx), Index: p.Action.Index}.String()
		s.Choice, s.Power = choiceNames[p.Choice], p.Power
	}
	m.env.Journal.Emit(at, wire.KindSubmitted, population.ActorID(p.Actor), s)
}

// Attempt builds one attempt of p on inputs the planner offers its
// intent. A failed build is a skip reason, not an error.
func (m *Maker) Attempt(ctx context.Context, at sched.Time, p *wire.Pending) (build.Built, []wire.Ref, uint64, string, error) {
	sp := m.kinds[p.KindOf()]
	a := population.ActorID(p.Actor)
	avail := m.env.Flight.Coins(a, plan.IntentID(p.ID))
	need := sp.need(&m.core, p) + sp.headroom
	coins := m.spec.Selector.Select(avail, need)
	if coins == nil {
		return build.Built{}, nil, 0, fmt.Sprintf("spendable %d below %d", wire.SumCoins(avail), need), nil
	}
	ttl := m.TTL(at, sp.guarded, sp.script)
	if ttl == 0 {
		return build.Built{}, nil, 0, "too close to the epoch boundary", nil
	}
	w := m.env.Roster.Wallet(a)
	// Only the first attempt is built before any send; it samples the
	// intent's mistake.
	first := p.M.Attempts == 0
	attempt := func() (build.Built, error) {
		b, err := sp.build(ctx, &m.core, w, build.Spend{Inputs: m.env.Spend.Utxos(w, coins), TTL: ttl}, p)
		if err != nil {
			return b, err
		}
		if first {
			m.env.Judge.Sample(p, w, b)
			first = false
		}
		return m.env.Judge.Mutate(p, w, b)
	}
	built, err := attempt()
	// A rebuild at the slot of the attempt it replaces, on the same inputs,
	// is the same transaction; a later TTL makes it a new one.
	for _, ok := m.env.Flight.Of(wire.TxID(built.ID)); err == nil && ok; _, ok = m.env.Flight.Of(wire.TxID(built.ID)) {
		ttl++
		built, err = attempt()
	}
	if err != nil {
		if ctx.Err() != nil {
			return build.Built{}, nil, 0, "", ctx.Err()
		}
		return build.Built{}, nil, 0, "build: " + err.Error(), nil
	}
	inputs := make([]wire.Ref, len(coins))
	for i, c := range coins {
		inputs[i] = c.Ref
	}
	return built, inputs, ttl, "", nil
}

// TTL is an attempt's invalid-hereafter slot. A guarded step's ends with
// the current epoch, so the epoch its effect reaches is known when the
// guard runs; zero means too little of the epoch is left.
func (m *Maker) TTL(at sched.Time, guarded, script bool) uint64 {
	// An overdue event runs at its own time, but its attempt is sent at
	// the Tick's (tickTo), so the interval starts there; a rebuild runs at
	// a block's time, which can lag the block's own slot.
	at = max(at, m.env.Clock.TickTo())
	slot := max(m.spec.Slots.SlotAt(at), m.env.Chain.Tip().Slot)
	ttl := slot + m.spec.TTL
	if script && m.spec.Horizon > 1 {
		// The node's tip may trail slot; half the window leaves it room.
		ttl = min(ttl, m.env.Chain.Tip().Slot+m.spec.Horizon/2)
	}
	if !guarded {
		return ttl
	}
	end := m.spec.Slots.SlotAt(m.spec.Slots.EpochStart(m.spec.Slots.EpochAt(at) + 1))
	ttl = min(ttl, end)
	if ttl <= slot+1 {
		return 0
	}
	return ttl
}

// Deposit is the lovelace a settled attempt of p moves into the ledger's
// deposits (negative: refunded), for the supply check.
func (m *Maker) Deposit(p *wire.Pending) int64 { return m.kinds[p.KindOf()].deposit(&m.core, p) }

// Unlock builds an unlock of p's locked output, as an honest unlock does.
func (m *Maker) Unlock(ctx context.Context, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	return buildUnlock(ctx, &m.core, w, sp, p)
}
