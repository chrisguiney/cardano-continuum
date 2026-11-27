package cohort

import (
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Clock is one of a population actor's per-intent Poisson clocks. Action
// is the governance action type of a proposer's clock.
type Clock struct {
	Kind   profile.IntentKind
	Action build.ActionType
	Rate   float64 // per second
}

// Pace is an actor's wake-up state: the next act and the population clocks
// and Hawkes follow-ups that decide it. The cohort stores it and the
// composite's pacing reads and writes it.
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

// Actor is an actor's in-memory record: what the census does not hold.
type Actor struct {
	Pace
	Prof     profile.Profile
	Policy   agenda.Policy
	Rate     float64 // sum of the profile's base rates, before RateMult
	Wallet   keys.Wallet
	HazardAt sched.Time
	HazardTo population.State
	HazardH  sched.Handle
	Draws    uint64
	// Paid are the actor's payees, most recent last, at most MaxPaid.
	Paid []population.ActorID
	// Ident is the actor's unchanging public identity (identity.go),
	// computed on first use.
	Ident   store.Identity
	IdentOK bool
}

// Draw returns a fresh stream for the actor's next random decision. Each
// draw has its own derived stream, so the snapshot only needs the counter.
func (st *Actor) Draw(a population.Actor, purpose string) *rand.Rand {
	st.Draws++
	return a.Stream(purpose, strconv.FormatUint(st.Draws, 10))
}

// MaxPaid bounds the payees an actor remembers.
const MaxPaid = 32

// Remember records a payee as the actor's most recent.
func (st *Actor) Remember(to population.ActorID) {
	st.Paid = slices.DeleteFunc(st.Paid, func(id population.ActorID) bool { return id == to })
	st.Paid = append(st.Paid, to)
	if len(st.Paid) > MaxPaid {
		st.Paid = slices.Delete(st.Paid, 0, len(st.Paid)-MaxPaid)
	}
}
