package agenda

import (
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// The optional capabilities a View may offer. They are separate interfaces
// rather than View methods so that a view without them keeps compiling;
// each agenda that needs one fails closed without it.

// Floor is the engine's stake-floor guard (internal/policy) as the agendas
// see it. Every intent that can lower forging stake asks it first: GoDark,
// RetirePool, and a delegation off a forging pool or onto one that does not
// forge (models/specs/stake-floor, contract rule 1).
type Floor interface {
	// Forging reports whether the guard counts the pool actor's pool as
	// forging: registered, planned live, pod running, no retirement.
	Forging(pool population.ActorID) bool
	// Allows reports whether the engine may take in with the floor held in
	// every schedule it can reach. why explains a refusal.
	Allows(in Intent) (ok bool, why string)
}

// Pods is the engine's plan and the observed pod state for a pool actor.
type Pods interface {
	// Dark reports that the engine plans the pool not to forge.
	Dark(pool population.ActorID) bool
	// PodRunning reports that the pool's pod is observed running.
	PodRunning(pool population.ActorID) bool
}

// ProposalID names a governance action as "txhash#index".
type ProposalID string

// ProposalInfo is one governance action open for votes.
type ProposalInfo struct {
	ID ProposalID
	// Expires is the last epoch in which the action can be voted on.
	Expires uint64
	// Type is the action type as profile files name it, empty when unknown.
	Type string
	// Pools reports that stake pools may vote on the action.
	Pools bool
}

// Governance is the Conway governance state the civic agenda reads.
type Governance interface {
	// OpenProposals lists the governance actions open for votes.
	OpenProposals() []ProposalInfo
	// Voted reports that the actor has a vote on the proposal.
	Voted(actor population.ActorID, p ProposalID) bool
	// DRepRegistered reports that the actor's DRep credential is registered.
	DRepRegistered(actor population.ActorID) bool
	// DReps lists the registered DRep actors.
	DReps() []population.ActorID
	// VoteDelegated reports that the actor's stake has a vote delegation.
	VoteDelegated(actor population.ActorID) bool
}

// Deposits are the chain's deposits as the agendas see them.
type Deposits interface {
	// Deposit is the lovelace kind locks as a deposit on this chain; ok is
	// false for a kind that pays none.
	Deposit(kind profile.IntentKind) (lovelace profile.Lovelace, ok bool)
}

// Market is a signal of how busy the chain is.
type Market interface {
	// Burst is the recent transaction rate over its long-run baseline: 1 is
	// normal, above 1 a burst.
	Burst() float64
}

const noGuard = "no stake-floor guard in view"

// vetStep asks the floor about a pool step (GoDark, RetirePool) already
// scored positive.
func vetStep(v View, s Score, pool population.ActorID) Score {
	if s.Value <= 0 {
		return s
	}
	f, ok := v.(Floor)
	if !ok {
		return off(s.Kind, noGuard)
	}
	if allowed, why := f.Allows(Intent{Kind: s.Kind, Actor: pool}); !allowed {
		return off(s.Kind, "stake floor: "+why)
	}
	return s
}

// vetMove asks the floor about the delegation in s from pool cur (zero
// when undelegated). A move from a pool that does not forge onto one that
// does only adds forging stake, so it is never asked about: recovery is
// never blocked (contract rule 2).
func vetMove(v View, s Score, wallet, cur population.ActorID) Score {
	if s.Value <= 0 {
		return s
	}
	f, ok := v.(Floor)
	if !ok {
		return Score{Kind: s.Kind, Target: s.Target, Why: noGuard}
	}
	if cur != 0 && !f.Forging(cur) && f.Forging(s.Target) {
		return s
	}
	in := Intent{Kind: profile.IntentDelegate, Actor: wallet, Target: s.Target}
	if allowed, why := f.Allows(in); !allowed {
		return Score{Kind: s.Kind, Target: s.Target, Why: "stake floor: " + why}
	}
	return s
}
