package agenda

import (
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// hodlScores keeps funds still: idle dominates, with a small standing chance
// of a payment.
func hodlScores(a population.Actor, v View) []Score {
	return []Score{
		pick(profile.IntentSend, 0.02, purseOf(v, a.ID), "holder spends rarely"),
		{Kind: Idle, Value: 1, Why: "holder waits"},
	}
}
