package cohort

import (
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Actor is an actor's in-memory record: what the census does not hold.
type Actor struct {
	wire.Pace
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
	// Trader is a wallet's place in the market; nil without one, and for
	// the exchanges' actors.
	Trader *Trader
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
