package attack

import (
	"bytes"
	"cmp"
	"maps"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Accounts are the adversaries: their attack policies, their cost
// accounts, and the shared outputs one tried to spend, so an honest spend
// of one may meet "inputs spent".
type Accounts struct {
	core      *core
	advs      map[population.ActorID]adversary.Policy
	costs     *adversary.Ledger
	contested map[wire.Ref]bool
}

// Arm returns pol with the actor's attacks, when the profile has them, and
// opens its cost account. The engine's config check has vetted the
// profile.
func (k *Accounts) Arm(id population.ActorID, p profile.Profile, pol agenda.Policy) agenda.Policy {
	if p.Adversary == nil {
		return pol
	}
	ap, err := adversary.NewPolicy(*p.Adversary)
	if err != nil {
		return pol
	}
	armed, err := agenda.WithAttacks(p.Agenda, ap)
	if err != nil {
		return pol
	}
	k.advs[id] = ap
	k.costs.Limit(id, uint64(p.Adversary.CostCeiling))
	return armed
}

// Adversary reports an actor that runs attack policies.
func (k *Accounts) Adversary(id population.ActorID) bool {
	_, ok := k.advs[id]
	return ok
}

// Spent is everything the actor's attacks cost.
func (k *Accounts) Spent(id population.ActorID) uint64 { return k.costs.Total(id) }

// Remaining implements adversary.View: what the actor may still spend
// this epoch.
func (k *Accounts) Remaining(id population.ActorID) uint64 {
	return k.costs.Remaining(id, k.core.spec.Slots.EpochAt(k.core.env.Clock.Now()))
}

// Contested reports a shared output an adversary tried to spend.
func (k *Accounts) Contested(ref wire.Ref) bool { return k.contested[ref] }

// Save is the accounts' snapshot: the spends by epoch and the contested
// outputs, by Ref.
func (k *Accounts) Save() ([]adversary.Charge, []wire.Ref) {
	return k.costs.Entries(), slices.SortedFunc(maps.Keys(k.contested), cmpLogRef)
}

// Load restores what Save returned. It runs after the actors are armed:
// their ceilings are set, the spends restored.
func (k *Accounts) Load(charges []adversary.Charge, contested []wire.Ref) {
	for _, r := range contested {
		k.contested[r] = true
	}
	k.costs.Restore(charges)
}

func cmpLogRef(a, b wire.Ref) int {
	return cmp.Or(bytes.Compare(a.Tx[:], b.Tx[:]), cmp.Compare(a.Index, b.Index))
}
