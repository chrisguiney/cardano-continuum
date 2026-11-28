package agenda

import (
	"fmt"
	"math"

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
	// Dry is true when no exchange has ADA free to pay a buy out, so the
	// wallet cannot buy now.
	Dry bool `json:"dry,omitempty"`
}

// Trading is the ADA market as the agendas see it. A view without it, or
// an actor with no position (an exchange, a pool, a run without a market),
// scores no trade at all, so a run without a market decides as before.
type Trading interface {
	// Position is the actor's position; ok is false when it has none.
	Position(id population.ActorID) (pos Position, ok bool)
}

// Trade scores against idle's 1. A wallet buys and sells now and then,
// about as often as a holder pays; the trend over the market window scales
// its buying (rallies draw buyers), a gain past takeGain draws profit
// taking, and a loss past LossGain is where traits split sellers from
// holders: the sale grows with loss aversion, impatience and the depth of
// the loss, while a hodl wallet holds for the recovery.
const (
	buyScore   = 0.02
	sellScore  = 0.01
	trendGain  = 3.0
	trendFloor = 0.25
	trendCeil  = 4.0
	takeGain   = 0.15
	takeScore  = 0.02
	LossGain   = 0.1
	panicScore = 0.2
	// patienceScale is the patience at which half the impatience is gone:
	// impatience is patienceScale / (patienceScale + patience).
	patienceScale = 3.0
)

// positionOf is the actor's position when the view has a market.
func positionOf(v View, id population.ActorID) (Position, bool) {
	t, ok := v.(Trading)
	if !ok {
		return Position{}, false
	}
	return t.Position(id)
}

// trade scores buying ADA with fiat and selling it back; nothing without a
// market. A buy spends fiat, so the balance does not bound it; a sale needs
// ADA to sell. hodl marks the holder's agenda, which never sells at a loss.
func trade(a population.Actor, v View, bal purse, hodl bool) []Score {
	pos, ok := positionOf(v, a.ID)
	if !ok {
		return nil
	}
	pull := min(trendCeil, max(trendFloor, math.Exp(trendGain*finite0(pos.Trend))))
	buy := Score{Kind: profile.IntentBuyADA, Value: buyScore * pull,
		Why: fmt.Sprintf("ADA %+.1f%% in %s over the window: buys at %.2fx", 100*pos.Trend, pos.Currency, pull)}
	if pos.Dry {
		buy = off(profile.IntentBuyADA, "no exchange has free ADA to pay a buy out")
	}
	return []Score{buy, sell(a, pos, bal, hodl)}
}

// sell scores a sale from the wallet's gain on its cost basis.
func sell(a population.Actor, pos Position, bal purse, hodl bool) Score {
	pct := 100 * pos.Gain
	switch {
	case pos.Gain >= takeGain:
		return pick(profile.IntentSellADA, takeScore*(1+pos.Gain), bal, fmt.Sprintf("up %.0f%% on cost in %s: takes profit", pct, pos.Currency))
	case pos.Gain > -LossGain:
		return pick(profile.IntentSellADA, sellScore, bal, fmt.Sprintf("%+.0f%% on cost in %s: sells now and then", pct, pos.Currency))
	case hodl:
		return off(profile.IntentSellADA, fmt.Sprintf("down %.0f%% in %s: hodl, holds for the recovery", -pct, pos.Currency))
	}
	averse := min(1, max(0, a.Traits.LossAversion))
	impatience := patienceScale / (patienceScale + max(0, a.Traits.Patience))
	value := panicScore * averse * impatience * -pos.Gain / LossGain
	return pick(profile.IntentSellADA, value, bal,
		fmt.Sprintf("down %.0f%% in %s: loss aversion %.2f, impatience %.2f: sells", -pct, pos.Currency, averse, impatience))
}

// finite0 is x, or zero when x is NaN or infinite.
func finite0(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return x
}
