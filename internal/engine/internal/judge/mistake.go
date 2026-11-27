package judge

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Mistakes samples an intent's mistake and injects it into each attempt.
type Mistakes struct {
	spec Spec
	env  Env
}

// Sample draws the intent's mistake from its first honest attempt. The
// stream is the actor's, named by the intent, so it is the same draw
// whatever else the actor did.
func (j *Mistakes) Sample(p *wire.Pending, w keys.Wallet, built build.Built) {
	if !j.spec.Mistakes {
		return
	}
	a, _ := j.env.Roster.Get(population.ActorID(p.Actor))
	t := profile.SampledTraits{Competence: a.Competence, Care: a.Traits.Care}
	// The engine's scripts (build.AlwaysSucceeds, build.FreeMint) accept
	// any redeemer, so a wrong one is no mistake: the node rightly
	// accepts it. Withheld mistakes cannot run yet (mistake.Withheld).
	ok := mistake.Applicable(built.Tx, j.Ctx(p, w)).Without(withheldKinds)
	p.Injected = mistake.Sample(t, ok, a.Stream("mistake", strconv.FormatUint(p.ID, 10)))
}

// withheldKinds are the mistakes the engine never samples.
var withheldKinds = func() mistake.KindSet {
	s := mistake.Of(mistake.WrongDatum)
	for _, k := range mistake.Kinds() {
		if mistake.Withheld(k) != "" {
			s |= mistake.Of(k)
		}
	}
	return s
}()

// Mutate makes the mistakes the attempt carries in the honest built. A
// mistake that this attempt's shape does not allow (its rebuild lost the
// change output, say) is left out of it, and Carried says so.
func (j *Mistakes) Mutate(p *wire.Pending, w keys.Wallet, built build.Built) (build.Built, error) {
	p.Carried, p.Lost = mistake.Carry(p.Injected, p.M.Fixed), 0
	if p.Carried == 0 {
		return built, nil
	}
	tx, want, err := mistake.Inject(built.Tx, p.Carried, j.Ctx(p, w))
	if errors.Is(err, mistake.ErrNotApplicable) || errors.Is(err, mistake.ErrNoSigner) {
		p.Carried = 0
		return built, nil
	}
	if err != nil {
		return build.Built{}, fmt.Errorf("engine: intent %d: inject %v: %w", p.ID, p.Carried, err)
	}
	t, err := conway.NewConwayTransactionFromCbor(tx)
	if err != nil {
		return build.Built{}, fmt.Errorf("engine: intent %d: decode mutant: %w", p.ID, err)
	}
	built.Tx, built.ID, p.Lost = tx, t.Hash(), want.Lost
	return built, nil
}

// Ctx is what Inject needs to re-sign an edited body: every key the
// actor's transactions are signed with.
func (j *Mistakes) Ctx(p *wire.Pending, w keys.Wallet) mistake.Ctx {
	id := population.ActorID(p.Actor)
	signers := []keys.Signer{w.PaymentSigner(), w.StakeSigner()}
	if id != j.spec.Faucet {
		signers = append(signers, j.env.Ring.DRepSigner(id))
	}
	if _, ok := j.env.Ring.PoolOf(id); ok {
		if s, err := j.env.Ring.PoolSigner(id); err == nil {
			signers = append(signers, s)
		}
	}
	return mistake.Ctx{Params: j.spec.Build, Signers: signers, Change: w.Address(), Slot: j.env.Chain.Tip().Slot, Stale: &staleInput}
}

// staleInput is the output a StaleInput mistake spends: one that never
// existed, which the ledger answers like a spent one. Its ID is the largest
// there is, so it sorts after every real input and the redeemers' input
// indices still point at the script inputs; an input sorting before one
// would also make the node report the redeemers' pointers wrong.
var staleInput = shelley.ShelleyTransactionInput{TxId: common.Blake2b256(bytes.Repeat([]byte{0xff}, 32))}
