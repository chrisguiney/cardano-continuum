package agenda

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// adversary is an agenda that hides attacks among ordinary traffic. cover
// scores the ordinary traffic; attacks, when set, adds the attack steps.
// The adversary package supplies attacks through WithAttacks; until it
// does, an adversary only sends cover traffic.
type adversary struct {
	cover   func(population.Actor, View) []Score
	attacks Policy
}

func (d adversary) Score(a population.Actor, v View) []Score {
	ss := d.cover(a, v)
	if d.attacks != nil {
		ss = append(ss, d.attacks.Score(a, v)...)
	}
	return ss
}

// WithAttacks returns the adversarial agenda name with attacks scoring its
// attack steps alongside the cover traffic. Only disrupt and extract take
// attacks.
func WithAttacks(name profile.AgendaName, attacks Policy) (Policy, error) {
	p, ok := registry[name].(adversary)
	if !ok {
		return nil, fmt.Errorf("%w: %q takes no attacks", ErrUnknown, name)
	}
	p.attacks = attacks
	return p, nil
}

// disruptCover is a busy, noisy payer: the volume an attack hides in.
func disruptCover(a population.Actor, v View) []Score {
	bal := purseOf(v, a.ID)
	return []Score{
		{Kind: Idle, Value: 1, Why: "disruptor waits; attack steps come from its hook"},
		pick(profile.IntentSend, 0.3, bal, "cover traffic"),
	}
}

// extractCover collects every reward it can and moves funds on.
func extractCover(a population.Actor, v View) []Score {
	bal := purseOf(v, a.ID)
	ss := []Score{
		{Kind: Idle, Value: 1, Why: "extractor waits; attack steps come from its hook"},
		pick(profile.IntentSend, 0.1, bal, "moves value on"),
	}
	if !v.StakeRegistered(a.ID) {
		return append(ss, off(profile.IntentWithdraw, "stake key not registered"))
	}
	return append(ss, withdraw(a, v, 0.3, bal, "extracts rewards"))
}
