// Package adversary holds the tier-1 adversaries: actors living in the
// engine whose agenda is to hurt the network, the chain and other users.
// Each is a profile with the disrupt or extract agenda and an adversary
// block (attacks, cost_ceiling, success_metric); its attacks score beside
// the agenda's cover traffic through agenda.WithAttacks.
//
// The package is pure, like internal/agenda: it scores attacks, plans each
// step from a read-only view and a random stream, keeps the cost account,
// mutates transaction bytes and compares run metrics. The engine builds,
// reserves, submits and judges what it plans, and logs every step as a
// decision, so a replay makes the same attacks.
//
//	attack            plan (this package)               engine
//	dust-flood        many min-UTxO outputs, script     one FanOut from the adversary
//	                  address and honest wallets
//	utxo-contention   a shared script output, one an    an Unlock of it
//	                  honest spend races for first
//	malformed-cbor    a corpus mutation (malformed.go)  a payment, mutated or raw-built
//	front-run         reacts to an honest unlock: the   an Unlock outbidding it, sent
//	                  same output, a higher fee         ahead of it
//	double-spend      2-3 payees                        one payment per payee, all on
//	                                                    the same inputs
//
// No adversary runs outside a devnet: the engine refuses an adversary
// actor unless the run holds a Clearance, which only Clear makes, from
// chain.Guard over the node's genesis.
package adversary

import (
	"errors"
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Errors.
var (
	// ErrNotDevnet: the genesis is a public network's, or not the run's.
	ErrNotDevnet = errors.New("adversary: refusing to attack anything but the run's devnet")
	// ErrUnknown: an attack or success metric the package does not have.
	ErrUnknown = errors.New("adversary: unknown")
	// ErrCeiling: a charge past the actor's cost ceiling for the epoch.
	ErrCeiling = errors.New("adversary: cost ceiling")
)

// Name is a tier-1 attack, as a profile's adversary.attacks names it.
type Name string

// The tier-1 attacks.
const (
	DustFlood   Name = "dust-flood"
	Contention  Name = "utxo-contention"
	Malformed   Name = "malformed-cbor"
	FrontRun    Name = "front-run"
	DoubleSpend Name = "double-spend"
)

var names = []Name{DustFlood, Contention, Malformed, FrontRun, DoubleSpend}

// Names returns every attack.
func Names() []Name { return slices.Clone(names) }

// Parse returns the attack named s.
func Parse(s string) (Name, error) {
	if n := Name(s); slices.Contains(names, n) {
		return n, nil
	}
	return "", fmt.Errorf("%w attack %q", ErrUnknown, s)
}

// Kind is the agenda intent kind the attack scores under.
func (n Name) Kind() profile.IntentKind { return profile.IntentKind(n) }

// Of returns the attack an intent kind names.
func Of(k profile.IntentKind) (Name, bool) {
	n := Name(k)
	return n, slices.Contains(names, n)
}

// Clearance is the proof that the run's node is on the run's devnet. Only
// Clear makes a cleared one; the zero value is not.
type Clearance struct {
	cleared bool
}

// Clear checks the node's genesis (got) against the run's (want) with
// chain.Guard, which refuses a public network whatever else matches.
func Clear(want, got chain.Genesis) (Clearance, error) {
	if err := chain.Guard(want, got); err != nil {
		return Clearance{}, fmt.Errorf("%w: %w", ErrNotDevnet, err)
	}
	return Clearance{cleared: true}, nil
}

// Cleared reports whether adversaries may run.
func (c Clearance) Cleared() bool { return c.cleared }
