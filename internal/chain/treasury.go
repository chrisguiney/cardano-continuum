package chain

import (
	"maps"
	"math/big"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// Withdrawal is lovelace moving out of or into the reward account of a
// key-hash stake credential: one payee of a treasury withdrawal, or a
// transaction's withdrawal from its account.
type Withdrawal struct {
	// Cred is the stake credential's key hash.
	Cred common.Blake2b224
	// Amount is the lovelace.
	Amount uint64
}

// drepTreasury is the DReps' treasury-withdrawal threshold (P6) as the
// devnet genesis sets it (internal/genesis, dRepVotingThresholds
// treasuryWithdrawal).
var drepTreasury = big.NewRat(67, 100)

// purse is the treasury and the reward accounts as the view reckons them
// from blocks. The view cannot read the ledger's treasury: it starts from
// State.Treasury, and only enacted withdrawals move it. Withdrawals the
// view enacts at a boundary are paid at the next one, to reward accounts
// registered then; a share for any other stays in the treasury
// (formal-ledger-specifications f8059737f1db, Enact.lagda.md Enact-Wdrl,
// Epoch.lagda.md payout, refunds and unclaimed). The fake chain
// (internal/fakechain ratifyWithdrawal, pay) does the same.
type purse struct {
	// funds is the treasury's lovelace.
	funds uint64
	// rewards are reward balances by stake credential.
	rewards map[common.Blake2b224]uint64
	// payout are the withdrawals enacted at the last boundary.
	payout map[common.Blake2b224]uint64
}

func (p purse) clone() purse {
	p.rewards, p.payout = maps.Clone(p.rewards), maps.Clone(p.payout)
	return p
}

// Treasury is the treasury's lovelace as the view reckons it.
func (p *purse) Treasury() uint64 { return p.funds }

// pending is the lovelace enacted withdrawals pay at the next boundary.
func (p *purse) pending() uint64 {
	var sum uint64
	for _, n := range p.payout {
		sum += n
	}
	return sum
}

// Rewards is the reward balance of the key-hash stake credential cred.
func (p *purse) Rewards(cred common.Blake2b224) uint64 { return p.rewards[cred] }

// payOut credits the withdrawals enacted at the boundary before to the
// reward accounts registered now, out of the treasury.
func payOut(v *View, u *undo) {
	if len(v.payout) == 0 {
		return
	}

	v.touchGov(u)
	for cred, n := range v.payout {
		if st, ok := v.stake[cred]; ok && st.Registered {
			v.rewards[cred] += n
			v.funds -= n
		}
	}
	v.payout = map[common.Blake2b224]uint64{}
}

// ratifyWithdrawals enacts, at the boundary into ep, each open treasury
// withdrawal in ID order whose votes pass (Ratify.lagda.md threshold:
// TreasuryWdrl is CC and P6; pools do not vote) while the treasury covers
// it with every withdrawal enacted before it and not yet paid.
func ratifyWithdrawals(v *View, ep uint64, u *undo) {
	ps := v.Proposals("treasury_withdrawals")
	slices.SortFunc(ps, func(a, b ProposalState) int { return a.ID.Compare(b.ID) })

	for _, p := range ps {
		if p.Enacted || p.Expires+1 < ep || !withdrawalPasses(v, p) {
			continue
		}

		total := v.pending()
		for _, w := range p.Withdrawals {
			total += w.Amount
		}
		if total > v.funds {
			continue
		}
		enactWithdrawal(v, p, u)
	}
}

func withdrawalPasses(v *View, p ProposalState) bool {
	t := CountVotes(v, p.ID)
	return committeePasses(t) && t.DRepTotal > 0 && ratio(t.DRepYes, t.DRepTotal).Cmp(drepTreasury) >= 0
}

func enactWithdrawal(v *View, p ProposalState, u *undo) {
	v.touchGov(u)
	x := v.props[p.ID]
	x.enacted = true
	v.props[p.ID] = x
	for _, w := range p.Withdrawals {
		v.payout[w.Cred] += w.Amount
	}
}

// withdraw takes a transaction's reward withdrawals out of the balances.
func withdraw(v *View, ws []Withdrawal, u *undo) bool {
	if len(ws) == 0 {
		return false
	}

	v.touchGov(u)
	for _, w := range ws {
		v.rewards[w.Cred] -= min(v.rewards[w.Cred], w.Amount)
	}
	return true
}

// withdrawalsOf reads a treasury withdrawal's key-hash payees, sorted by
// credential; nil for any other action.
func withdrawalsOf(a common.GovAction) []Withdrawal {
	tw, ok := a.(*common.TreasuryWithdrawalGovAction)
	if !ok {
		return nil
	}
	return rewardWithdrawals(func(yield func(*common.Address, uint64) bool) {
		for addr, n := range tw.Withdrawals {
			if !yield(addr, n) {
				return
			}
		}
	})
}

// txWithdrawals reads a transaction's key-hash reward withdrawals.
func txWithdrawals(tx common.Transaction) []Withdrawal {
	return rewardWithdrawals(func(yield func(*common.Address, uint64) bool) {
		for addr, n := range tx.Withdrawals() {
			if n != nil && !yield(addr, n.Uint64()) {
				return
			}
		}
	})
}

func rewardWithdrawals(seq func(yield func(*common.Address, uint64) bool)) []Withdrawal {
	var out []Withdrawal
	for addr, n := range seq {
		if addr.Type() == common.AddressTypeNoneKey {
			out = append(out, Withdrawal{Cred: addr.StakeKeyHash(), Amount: n})
		}
	}
	slices.SortFunc(out, func(a, b Withdrawal) int { return compareHash(a.Cred, b.Cred) })
	return out
}

// sortedWithdrawals lists m by credential.
func sortedWithdrawals(m map[common.Blake2b224]uint64) []Withdrawal {
	var out []Withdrawal
	for _, k := range sortedKeys(m) {
		out = append(out, Withdrawal{Cred: k, Amount: m[k]})
	}
	return out
}
