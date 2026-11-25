package agenda

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// operatorScores registers the pool once, then maintains it (idle). Retirement
// grows with the operator's frustration.
func operatorScores(a population.Actor, v View) []Score {
	bal := v.Balance(a.ID)
	if !v.PoolRegistered(a.ID) {
		return []Score{
			pick(profile.IntentRegisterPool, 1, bal, "pool not registered"),
			{Kind: Idle, Value: 0.05, Why: "waiting to register"},
			off(profile.IntentRetirePool, "pool not registered"),
			off(profile.IntentWithdraw, "pool not registered"),
		}
	}
	retire := min(0.5, 0.01*a.Frustration)
	return []Score{
		off(profile.IntentRegisterPool, "pool already registered"),
		{Kind: Idle, Value: 1, Why: "maintaining pool"},
		pick(profile.IntentRetirePool, retire, bal, fmt.Sprintf("frustration %.2f", a.Frustration)),
		pick(profile.IntentWithdraw, 0.05, bal, "collect rewards"),
	}
}
