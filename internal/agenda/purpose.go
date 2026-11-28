package agenda

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// purposes lists, per wallet purpose, the profile intents a wallet of that
// purpose may score. General is absent: it allows everything. Every
// purpose allows delegate_drep, since Conway refuses a reward withdrawal
// from an account with no vote delegation, and transfer, the owner moving
// ADA between its own wallets. A savings wallet receives the owner's
// purchases but never trades itself: to sell, the owner first moves the
// ADA to a spending or trading wallet.
var purposes = map[population.Purpose][]profile.IntentKind{
	population.Savings: {
		profile.IntentDelegate, profile.IntentDelegateDRep, profile.IntentRegisterStake,
		profile.IntentTransfer, profile.IntentWithdraw,
	},
	population.Spending: {
		profile.IntentBuyADA, profile.IntentDelegate, profile.IntentDelegateDRep, profile.IntentRegisterStake,
		profile.IntentSellADA, profile.IntentSend, profile.IntentTransfer, profile.IntentWithdraw,
	},
	population.Staking: {
		profile.IntentDelegate, profile.IntentDelegateDRep, profile.IntentRegisterStake,
		profile.IntentTransfer, profile.IntentWithdraw,
	},
	population.Governance: {
		profile.IntentDelegateDRep, profile.IntentRegisterDRep, profile.IntentRegisterStake,
		profile.IntentRetireDRep, profile.IntentTransfer, profile.IntentVote,
	},
	population.Trading: {
		profile.IntentBuyADA, profile.IntentBurn, profile.IntentDelegate, profile.IntentDelegateDRep, profile.IntentMint,
		profile.IntentRegisterStake, profile.IntentScriptCall, profile.IntentSellADA, profile.IntentSend, profile.IntentTransfer, profile.IntentWithdraw,
	},
}

// Allowed lists the profile intents a wallet of purpose p may score, sorted;
// nil for a purpose that allows all of them.
func Allowed(p population.Purpose) []profile.IntentKind { return slices.Clone(purposes[p]) }

// Allows reports whether a wallet of purpose p may score kind. The engine's
// own steps (Idle, GoDark, GoLive, the committee's) are never restricted.
func Allows(p population.Purpose, kind profile.IntentKind) bool {
	ks, ok := purposes[p]
	if !ok || slices.Contains(Kinds(), kind) {
		return true
	}
	return slices.Contains(ks, kind)
}

// Restrict returns pol limited to what a wallet of purpose p does: every
// score of a kind the purpose rules out becomes zero, saying why. General
// (and an unset purpose) returns pol itself, so an unrestricted wallet's
// decisions are unchanged byte for byte. A nil pol stays nil.
func Restrict(pol Policy, p population.Purpose) Policy {
	if _, ok := purposes[p]; !ok || pol == nil {
		return pol
	}

	return descriptor{func(a population.Actor, v View) []Score {
		ss := pol.Score(a, v)
		for i, s := range ss {
			if !Allows(p, s.Kind) {
				ss[i] = off(s.Kind, string(p)+" wallet: not used for "+string(s.Kind))
				ss[i].Target, ss[i].Proposal = s.Target, s.Proposal
			}
		}
		return ss
	}}
}
