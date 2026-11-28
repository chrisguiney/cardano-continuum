package population

import (
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// HumanID identifies a human: the person who owns wallets. Without a humans
// section every wallet is its own owner and HumanID equals its ActorID.
type HumanID uint64

// Purpose is what a wallet is for to its owner. It limits which intents the
// wallet's agenda may score (agenda.Restrict).
type Purpose string

// Wallet purposes. General allows everything and is what every wallet has
// without a humans section.
const (
	General    Purpose = "general"
	Savings    Purpose = "savings"
	Spending   Purpose = "spending"
	Staking    Purpose = "staking"
	Governance Purpose = "governance"
	Trading    Purpose = "trading"
)

// Purposes lists every wallet purpose, sorted.
func Purposes() []Purpose {
	return []Purpose{General, Governance, Savings, Spending, Staking, Trading}
}

// Human is one person: the owner of one or more wallet actors. The owner
// carries the temperament (traits and learned competence) every wallet it
// owns acts with; each wallet keeps its own activity level, frustration and
// lifecycle.
type Human struct {
	// ID is the human's identifier.
	ID HumanID
	// Motivation is the agenda the owner's wallets score intents with.
	Motivation profile.AgendaName
	// Traits are the owner's drawn traits. Census.Get copies them onto
	// every wallet the human owns.
	Traits profile.SampledTraits
	// Competence is the owner's competence as learning has moved it.
	Competence float64
	// Born is when the human appeared: its first wallet's spawn time.
	Born sched.Time
	// Wallets are the actors the human owns, in spawn order.
	Wallets []ActorID
}

// Owner says who owns a wallet being spawned (Census.SpawnOwned).
type Owner struct {
	// Human is the owner; zero spawns an actor no person owns.
	Human HumanID
	// Purpose is what the wallet is for to its owner.
	Purpose Purpose
}
