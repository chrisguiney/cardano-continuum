package cohort

import (
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Roster is the actors: the census, each actor's in-memory record, and the
// index from address to actor.
type Roster struct {
	census population.Census
	actors map[population.ActorID]*Actor
	order  []population.ActorID // ascending
	byAddr map[string]population.ActorID
	// seed is the census's stream root.
	seed rng.Seed
	// Ring holds the actors' keys and the genesis pools.
	Ring   *Keyring
	faucet keys.Wallet
	env    Env
}

func newRoster(seed rng.Seed, ring *Keyring, v Env) *Roster {
	return &Roster{actors: map[population.ActorID]*Actor{}, byAddr: map[string]population.ActorID{}, seed: seed, Ring: ring, env: v}
}

// Get is the census entry of an actor.
func (r *Roster) Get(id population.ActorID) (population.Actor, bool) { return r.census.Get(id) }

// Rec is the actor's record, nil when there is none.
func (r *Roster) Rec(id population.ActorID) *Actor { return r.actors[id] }

// IDs lists every actor, ascending. The slice is the roster's own.
func (r *Roster) IDs() []population.ActorID { return r.order }

// Pop is the population's census.
func (r *Roster) Pop() *population.Census { return &r.census }

// Owner is the actor whose address is addr, or the faucet.
func (r *Roster) Owner(addr []byte) (population.ActorID, bool) {
	id, ok := r.byAddr[string(addr)]
	return id, ok
}

// Address is the wallet address of a ledger owner: an actor or the faucet.
func (r *Roster) Address(id population.ActorID) common.Address { return r.Wallet(id).Address() }

// Wallet is the wallet of a ledger owner: an actor or the faucet.
func (r *Roster) Wallet(id population.ActorID) keys.Wallet {
	if id == Faucet {
		return r.faucet
	}
	return r.actors[id].Wallet
}

// BindFaucet names the faucet's wallet as the owner of its address.
func (r *Roster) BindFaucet(w keys.Wallet) error {
	addr, err := w.Address().Bytes()
	if err != nil {
		return fmt.Errorf("engine: faucet address: %w", err)
	}
	r.faucet = w
	r.byAddr[string(addr)] = Faucet
	_ = r.env.Chain.Book().AddAddress(w.Address(), Faucet)
	return nil
}

// Revive spawns the actor a snapshot recorded, so it gets the same traits.
func (r *Roster) Revive(p profile.Profile, born sched.Time) population.Actor {
	return r.census.Spawn(p, r.seed, born)
}

// Register builds the in-memory side of an actor: policy, wallet, address
// index. It schedules nothing.
func (r *Roster) Register(a population.Actor, p profile.Profile) *Actor {
	pol, err := agenda.For(p.Agenda)
	if err != nil {
		pol = nil // the actor scores nothing and stays idle
	}
	pol = r.env.Att.Arm(a.ID, p, pol)
	st := &Actor{Prof: p, Policy: pol, Rate: baseRate(p), Wallet: r.Ring.actors.Wallet(keys.ActorID(a.ID))}
	st.HazardAt, st.ActAt = population.Never, population.Never
	st.Clocks, st.Boot = r.env.Pace.Clocks(a, p)
	r.actors[a.ID] = st
	r.order = append(r.order, a.ID)
	if addr, err := st.Wallet.Address().Bytes(); err == nil {
		r.byAddr[string(addr)] = a.ID
	}
	r.Ring.bind(r.env.Chain.Book(), a.ID, p, st.Wallet)
	return st
}

// baseRate sums the base rates in sorted key order: float addition is not
// associative, so summing in map order would make the rate vary by run.
func baseRate(p profile.Profile) float64 {
	ks := make([]string, 0, len(p.Rates.Base))
	for k := range p.Rates.Base {
		ks = append(ks, string(k))
	}
	slices.Sort(ks)
	var sum float64
	for _, k := range ks {
		sum += p.Rates.Base[profile.IntentKind(k)]
	}
	return sum
}
