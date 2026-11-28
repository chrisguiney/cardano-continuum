package intent

import (
	"context"
	"iter"
	"math/rand/v2"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// never is the time of an event that is not scheduled.
const never = population.Never

// Selector picks the inputs of an attempt from the coins the planner offers.
type Selector interface {
	// Select returns coins from avail worth at least need, or nil if avail
	// cannot cover it.
	Select(avail []wire.Coin, need uint64) []wire.Coin
}

// Roster is the actors as the intent reads them.
type Roster interface {
	IDs() []population.ActorID
	Get(id population.ActorID) (population.Actor, bool)
	Wallet(id population.ActorID) keys.Wallet
	// Profile is the actor's profile, nil for an actor never spawned.
	Profile(id population.ActorID) *profile.Profile
	// Paid are the actor's payees, most recent last.
	Paid(id population.ActorID) []population.ActorID
}

// Ring is the keys of the actors that hold pools and DRep credentials.
type Ring interface {
	PoolOf(id population.ActorID) ([28]byte, bool)
	PoolKeys(id population.ActorID) (*keys.Store, keys.ActorID)
	PoolSigner(id population.ActorID) (keys.Signer, error)
	DRep(id population.ActorID) keys.Credential
	DRepSigner(id population.ActorID) keys.Signer
	// Committee is committee seat i's cold and hot signers.
	Committee(i uint32) (cold, hot keys.Signer)
}

// Chain is the window the intent reads the ledger from.
type Chain interface {
	View() *chain.View
	Tip() eventlog.ChainPoint
	Book() *chain.Book
}

// Flight is the unfinished intents and the coins the planner offers.
type Flight interface {
	// Has reports an unfinished intent of the actor of the kind.
	Has(id population.ActorID, kind string) bool
	All() iter.Seq[*wire.Pending]
	// Of is the intent holding the attempt tx.
	Of(tx wire.TxID) (uint64, bool)
	Coins(a population.ActorID, id plan.IntentID) []wire.Coin
	Spendable(id uint64) uint64
}

// Clock is the event queue and the time of the input being handled.
type Clock interface {
	// TickTo is the time of the Tick being handled.
	TickTo() sched.Time
	// Deploy schedules the next contract deployment.
	Deploy(at sched.Time)
}

// Journal is the decision log of the input being handled.
type Journal interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
}

// Judge is the oracle's hand in an attempt: the mistake it carries.
type Judge interface {
	// Sample draws the intent's mistake from its first honest attempt.
	Sample(p *wire.Pending, w keys.Wallet, built build.Built)
	// Mutate makes the mistakes the attempt carries in the honest built.
	Mutate(p *wire.Pending, w keys.Wallet, built build.Built) (build.Built, error)
}

// Spend is what the composite adds to an attempt's inputs.
type Spend interface {
	// Utxos turns coins into the outputs the builder spends.
	Utxos(w keys.Wallet, coins []wire.Coin) []common.Utxo
	// Adversary reports an actor that runs attack policies.
	Adversary(id population.ActorID) bool
}

// Market is the exchanges as a trade is priced on them.
type Market interface {
	// Order is the quote for the wallet's buy or sale and the actor of the
	// exchange it trades on; ok is false when no exchange quotes the
	// wallet's currencies. It draws from r.
	Order(at sched.Time, wallet population.ActorID, buy bool, r *rand.Rand) (o wire.Order, venue population.ActorID, ok bool)
}

// Extra is a kind another component builds: its attempts go through the
// same inputs, TTL and mistakes as the table's.
type Extra struct {
	// Headroom is the lovelace the inputs cover beside the kind's own need.
	Headroom uint64
	Build    func(ctx context.Context, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error)
}

// Spec is what an Intent is built from.
type Spec struct {
	Build    build.Params
	Builder  *build.Builder
	Selector Selector
	Slots    sched.Slots
	// TTL is how many slots after its decision an attempt stays valid;
	// Horizon is the stability window a script attempt's TTL stays inside.
	TTL, Horizon uint64
	NetworkID    uint8
	Seed         rng.Seed
	// Pool is the file's population called name.
	Pool func(name string) (profile.Population, bool)
	// Contract is the file's contract population, nil without one.
	Contract *profile.ContractParams
	// DeployRate is new contracts per second.
	DeployRate float64
	// Script owns the always-succeeds address in the view's book, so the
	// view tracks locked outputs.
	Script population.ActorID
	// Cover marks, in Submitted.Attack, an adversary's ordinary intent.
	Cover string
	// Extra are kinds other components build, by kind name.
	Extra map[string]Extra
}

// core is what each part of an Intent reads.
type core struct {
	spec Spec
	env  Env
}

// Env is what the intent shares with the rest of the engine.
type Env struct {
	Roster  Roster
	Ring    Ring
	Chain   Chain
	Flight  Flight
	Clock   Clock
	Journal Journal
	Judge   Judge
	Spend   Spend
	// Market prices trades; nil in a run without a market.
	Market Market
}
