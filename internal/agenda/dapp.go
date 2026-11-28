package agenda

import (
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// dappScore is the standing score of each step a script user takes against
// 1 for idling. It equals hodl's payment score, so a fitted cluster's
// wake-up rate converts to transactions the same way whichever of the two
// agendas it runs (lab/agendas.py, TX_SCORE).
const dappScore = 0.02

// dappScores uses contracts: it pays, locks and unlocks script outputs
// (script_call) and mints, each at the same standing score, so the
// profile's weights alone set the mix. Unlike opportunist it does not wait
// for a burst: chain users who mostly call scripts do so at a steady rate.
func dappScores(a population.Actor, v View) []Score {
	bal := purseOf(v, a.ID)
	return append([]Score{
		{Kind: Idle, Value: 1, Why: "script user waits"},
		pick(profile.IntentSend, dappScore, bal, "pays"),
		pick(profile.IntentScriptCall, dappScore, bal, "locks or unlocks a script output"),
		pick(profile.IntentMint, dappScore, bal, "mints"),
	}, trade(a, v, bal, false)...)
}
