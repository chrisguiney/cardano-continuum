// Package intent turns an agenda intent into a built transaction: the
// per-kind spec table, the parts an intent names only by kind (payee, pool,
// proposal, locked output), the TTL, and the contracts a lock may go to.
//
//	Chooser    Prepare: the pending record of an intent, or why it cannot be made
//	Maker      Attempt: one signed attempt on the inputs the planner offers
//	Contracts  the deployed contracts and the deployment clock
//
// The composite orders every call. Prepare and Attempt draw only from the
// stream they are given or from the actor's own, so the order of draws is
// the caller's order of calls. Intent satisfies the Builder the pay
// component's retry machine declares (Attempt, Submitted).
//
// The TTL of a guarded kind ends inside the current epoch, which is what the
// stake-floor guard (models/specs/stake-floor, rule 10) relies on to know the
// epoch an effect reaches: Maker.TTL and the floor component's recovery
// moves are the two halves of that rule.
package intent

import (
	"context"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Intent is the intent component: its three parts, built over one spec and
// one environment.
type Intent struct {
	*Contracts
	*Chooser
	*Maker
}

// New returns the intent component with contract 0 deployed and no
// deployment scheduled.
func New(spec Spec, env Env) *Intent {
	c := core{spec: spec, env: env}
	ct := &Contracts{core: c, n: 1, at: never}
	kinds := make(map[string]kind, len(specs)+len(spec.Extra))
	for k, v := range specs {
		kinds[k] = v
	}
	for k, x := range spec.Extra {
		kinds[k] = kind{need: none, headroom: x.Headroom, deposit: noDeposit, build: func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
			return x.Build(ctx, w, sp, p)
		}}
	}
	return &Intent{Contracts: ct, Chooser: &Chooser{core: c, ct: ct}, Maker: &Maker{core: c, kinds: kinds}}
}

// Again are the classes a kind's certificates fail with when the same
// transaction already landed: what the oracle expects beside "inputs
// spent" from a resend.
func Again(kind string) []submit.Class { return specs[kind].again }

// AllAgain is Again over every kind of the table.
func AllAgain() []submit.Class {
	var out []submit.Class
	for _, sp := range specs {
		out = append(out, sp.again...)
	}
	return out
}
