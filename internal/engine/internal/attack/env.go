package attack

import (
	"context"
	"iter"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Selector picks the inputs of a step from the coins the planner offers.
type Selector interface {
	// Select returns coins from avail worth at least need, or nil if avail
	// cannot cover it.
	Select(avail []wire.Coin, need uint64) []wire.Coin
}

// Roster is the actors as an attack reads them.
type Roster interface {
	IDs() []population.ActorID
	Get(id population.ActorID) (population.Actor, bool)
	Wallet(id population.ActorID) keys.Wallet
	// Profile is the actor's profile, nil for an actor never spawned.
	Profile(id population.ActorID) *profile.Profile
}

// Ledger is the planner's reservations as a step claims them.
type Ledger interface {
	// Next is the ID the next intent takes; Claim takes it.
	Next() uint64
	Claim(id uint64)
	Reserve(ctx context.Context, id uint64, a population.ActorID, tx wire.TxID, inputs []wire.Ref, ttl uint64) error
	Coins(a population.ActorID, id plan.IntentID) []wire.Coin
	Add(p *wire.Pending)
}

// Flight is the unfinished intents as an answer settles them.
type Flight interface {
	All() iter.Seq[*wire.Pending]
	Unawait(id uint64)
	// Refused writes the attempt tx off and returns the attempts it killed.
	Refused(ctx context.Context, tx plan.TxID) ([]plan.TxID, error)
}

// Retry is the submit-retry bridge as a one-shot answer reaches it.
type Retry interface {
	// Release frees the dead attempts' reservations, for why.
	Release(ctx context.Context, at sched.Time, dead []plan.TxID, why string) error
	// Outcome rebuilds the node's answer from the logged result.
	Outcome(r wire.SubmitResult) (submit.Outcome, error)
	// Refusable reports an answer that proves its attempt never reached
	// a mempool.
	Refusable(v wire.Verdict, resend bool) bool
	// Reason is the Refused record's reason for r.
	Reason(r wire.SubmitResult) string
}

// Judge is the oracle bridge as an attack's verdicts reach it.
type Judge interface {
	// Ctx is what a mistake injected into p's attempt needs.
	Ctx(p *wire.Pending, w keys.Wallet) mistake.Ctx
	// Report logs the findings not logged before.
	Report(at sched.Time, fs []oracle.Finding) error
	// Expect is the verdict an attempt carrying ks calls for; spent when
	// its inputs may already be gone, failing with again too.
	Expect(ks mistake.KindSet, spent bool, again []submit.Class) mistake.Expected
	// WithClasses adds cs to e's classes.
	WithClasses(e mistake.Expected, cs ...submit.Class) mistake.Expected
	// AnswerOutcome is a rival's answer as the oracle reads it.
	AnswerOutcome(a wire.Answer) submit.Outcome
}

// Maker is the intent component's attempt making that a step shares.
type Maker interface {
	// TTL is the validity end of an attempt decided at at.
	TTL(at sched.Time, guarded, script bool) uint64
	// Unlock builds the spend of p's locked output.
	Unlock(ctx context.Context, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error)
	// Submitted logs and sends an attempt.
	Submitted(at sched.Time, p *wire.Pending, built build.Built, inputs []wire.Ref)
}

// Spend is the planner ledger's outputs as a step spends and picks them.
type Spend interface {
	// Utxos turns coins into the outputs the builder spends.
	Utxos(w keys.Wallet, coins []wire.Coin) []common.Utxo
	// Settled lists the settled outputs.
	Settled() []plan.Utxo
	// Holder is the intent holding ref, if one does.
	Holder(ref plan.Ref) (plan.IntentID, bool)
}

// Journal is the decision log of the input being handled.
type Journal interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
	Seq() uint64
}

// Pace is the market burst a step excites.
type Pace interface {
	Excite(at sched.Time)
}

// Chain is the window a step reads the tip and the script outputs from.
type Chain interface {
	View() *chain.View
	Tip() eventlog.ChainPoint
}

// Clock is the time of the input being handled.
type Clock interface {
	Now() sched.Time
}

// Env is what an attack shares with the rest of the engine.
type Env struct {
	Roster  Roster
	Ledger  Ledger
	Flight  Flight
	Retry   Retry
	Judge   Judge
	Maker   Maker
	Spend   Spend
	Journal Journal
	Pace    Pace
	Chain   Chain
	Clock   Clock
	// View is the agenda view with the adversary's side, which Plan reads.
	View adversary.View
}

// Spec is what an Attack is built from.
type Spec struct {
	Build    build.Params
	Builder  *build.Builder
	Selector Selector
	Slots    sched.Slots
	// BlockEvery is the slots between blocks, which a TTL near the tip
	// must outlast.
	BlockEvery uint64
	NetworkID  uint8
	// Run names the run in each finding's replay command.
	Run oracle.Run
	// Unlock is the unlock intent's kind, and Again the classes its
	// spend fails with when the same transaction already landed.
	Unlock string
	Again  []submit.Class
	// FeeHeadroom and ScriptHeadroom are what inputs cover beside a
	// payment's or a script spend's own need; MinPayment is what a probe
	// payment sends.
	FeeHeadroom, ScriptHeadroom, MinPayment uint64
	// Faucet is the faucet's actor, whose outputs no steal takes.
	Faucet population.ActorID
	// Datum is the datum an actor locks under, Key what datums are
	// compared by, and Unit the unit redeemer.
	Datum func(id population.ActorID) common.Datum
	Key   func(d common.Datum) []byte
	Unit  common.Datum
}

// core is what each part of an Attack reads.
type core struct {
	spec Spec
	env  Env
}
