package wire

import (
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Clock is one of a population actor's per-intent Poisson clocks. Action
// is the governance action type of a proposer's clock.
type Clock struct {
	Kind   profile.IntentKind
	Action build.ActionType
	Rate   float64 // per second
}

// Pace is an actor's wake-up state: the next act and the population clocks
// and Hawkes follow-ups that decide it. The cohort stores it; the pace
// component reads and writes it.
type Pace struct {
	ActAt sched.Time
	ActH  sched.Handle
	// Clocks are a population actor's intent clocks and Boot the intent it
	// registers with; Boot is empty for every other actor.
	Clocks []Clock
	Boot   profile.IntentKind
	// Booting is set while the pending wake was drawn at the boot rate:
	// it registers, and must not fire a fitted clock (a clock's first
	// event would come at the boot rate, far too soon).
	Booting bool
	// Owed is how many follow-ups the actor's transactions have begotten
	// and not yet made, when its profile has Hawkes parameters; FollowUp
	// marks the pending wake as one of them.
	Owed     uint32
	FollowUp bool
	// Waits is how many blocks a follow-up has waited for coins to act on.
	Waits uint8
}
