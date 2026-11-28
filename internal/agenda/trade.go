package agenda

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Position is a wallet's place in the ADA market, in its primary currency.
type Position struct {
	// Currency is the wallet's primary currency's code.
	Currency string `json:"currency"`
	// Gain is ADA's mid rate over the wallet's cost basis, minus one: -0.2
	// is 20% under water.
	Gain float64 `json:"gain"`
	// Trend is the log change of ADA in the currency over the market's
	// window: positive is a rally.
	Trend float64 `json:"trend"`
}

// Trading is the ADA market as the agendas see it. A view without it, or
// an actor with no position (an exchange, a pool, a run without a market),
// scores no trade at all, so a run without a market decides as before.
type Trading interface {
	// Position is the actor's position; ok is false when it has none.
	Position(id population.ActorID) (pos Position, ok bool)
}

// Standing trade scores against idle's 1: a wallet trades now and then,
// about as often as a holder pays.
const (
	buyScore  = 0.02
	sellScore = 0.01
)

// positionOf is the actor's position when the view has a market.
func positionOf(v View, id population.ActorID) (Position, bool) {
	t, ok := v.(Trading)
	if !ok {
		return Position{}, false
	}
	return t.Position(id)
}

// trade scores buying ADA with fiat and selling it back. A buy spends
// fiat, so the actor's balance does not bound it; a sale needs ADA to
// sell.
func trade(a population.Actor, v View, bal purse) []Score {
	pos, ok := positionOf(v, a.ID)
	if !ok {
		return nil
	}
	return []Score{
		{Kind: profile.IntentBuyADA, Value: buyScore, Why: fmt.Sprintf("buys ADA with %s now and then", pos.Currency)},
		pick(profile.IntentSellADA, sellScore, bal, fmt.Sprintf("sells ADA for %s now and then", pos.Currency)),
	}
}
