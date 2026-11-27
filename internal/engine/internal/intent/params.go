package intent

import (
	"errors"
	"math/big"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// The network proposer's parameter changes walk paramSteps: change a
// parameter to its alternative, and once that is enacted revert it to the
// devnet's genesis value, naming the change as its predecessor, then move
// to the next step. The parameters touch neither the security group (so
// pools need not vote, and the genesis pools a live run may not operate
// cannot block them) nor anything the transaction builder reads, so the
// honest population builds the same transactions before and after.
//
// Conway accepts a parameter change only when its predecessor is the last
// enacted one or a live proposal (Proposals.hs, proposalsAddAction). The
// ledger's enactments are not on the chain the view follows, so the view
// repeats the ledger's ratification at each epoch boundary, with the stake
// of that boundary (chain ratify.go), and the proposer keeps one change
// open at a time, closed once the view enacts it or its last voting epoch
// passes. Every decision is a function of the view, so replay plans the
// same change.

// paramStep is one parameter the proposer changes and reverts.
type paramStep struct {
	// set returns the update setting the parameter to its alternative
	// (alt) or its genesis value.
	set func(alt bool) conway.ConwayProtocolParameterUpdate
}

func ratUpdate(field func(*conway.ConwayProtocolParameterUpdate, *cbor.Rat), base, alt *big.Rat) func(bool) conway.ConwayProtocolParameterUpdate {
	return func(a bool) conway.ConwayProtocolParameterUpdate {
		var u conway.ConwayProtocolParameterUpdate
		v := base
		if a {
			v = alt
		}
		field(&u, &cbor.Rat{Rat: new(big.Rat).Set(v)})
		return u
	}
}

// paramSteps are poolPledgeInfluence 0.3 to 0.25, treasuryCut 0.2 to 0.25
// and dRepActivity 20 to 25 epochs, each and back.
var paramSteps = []paramStep{
	{set: ratUpdate(func(u *conway.ConwayProtocolParameterUpdate, r *cbor.Rat) { u.A0 = r }, big.NewRat(3, 10), big.NewRat(1, 4))},
	{set: ratUpdate(func(u *conway.ConwayProtocolParameterUpdate, r *cbor.Rat) { u.Tau = r }, big.NewRat(1, 5), big.NewRat(1, 4))},
	{set: func(alt bool) conway.ConwayProtocolParameterUpdate {
		v := uint64(20)
		if alt {
			v = 25
		}
		return conway.ConwayProtocolParameterUpdate{DRepInactivityPeriod: &v}
	}},
}

// paramLink is one parameter change the view tracked, in proposal order.
type paramLink struct {
	// id names the change.
	id chain.GovID
	// closed is set once the view enacted the change or its last voting
	// epoch has passed.
	closed bool
	// enacted is set when the view's ratification enacted it.
	enacted bool
}

// paramPlan is the proposer's walk through paramSteps so far.
type paramPlan struct {
	// root is the last change believed enacted, nil while none is.
	root *chain.GovID
	// step indexes paramSteps; reverting is set when the step's change
	// is enacted and its revert is next.
	step      int
	reverting bool
}

// next is the change the plan proposes now.
func (pl paramPlan) next() paramChange {
	c := paramChange{update: paramSteps[pl.step%len(paramSteps)].set(!pl.reverting)}
	if pl.root != nil {
		r := *pl.root
		c.prev = &r
	}
	return c
}

// enacted advances the plan past the link id, believed enacted.
func (pl paramPlan) enacted(id chain.GovID) paramPlan {
	pl.root = &id
	if pl.reverting {
		pl.step++
	}
	pl.reverting = !pl.reverting
	return pl
}

// planParams replays the links and returns the change to propose next, or
// false while a link is still open.
func planParams(links []paramLink) (paramChange, bool) {
	var pl paramPlan
	for _, l := range links {
		if !l.closed {
			return paramChange{}, false
		}
		if l.enacted {
			pl = pl.enacted(l.id)
		}
	}
	return pl.next(), true
}

// paramChange is a planned change: the update, and the link it names as
// predecessor.
type paramChange struct {
	update conway.ConwayProtocolParameterUpdate
	prev   *chain.GovID
}

// errParamOpen is why no parameter change is built beside an open one.
var errParamOpen = errors.New("a parameter change is still open")

// paramLinks lists the parameter changes the view tracked, in proposal
// order (last voting epoch, then ID), each with whether the view enacted
// it.
func paramLinks(v *chain.View) []paramLink {
	var out []paramLink
	for _, p := range v.Proposals("parameter_change") {
		out = append(out, paramLink{id: p.ID, closed: p.Enacted || p.Expires < v.Epoch(), enacted: p.Enacted})
	}
	return out
}
