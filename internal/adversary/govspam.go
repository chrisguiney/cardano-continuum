package adversary

import (
	"fmt"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Governance proposal spam. A proposer opens many governance actions to drown
// honest ones and tie up the voting committee. Conway charges a refundable
// deposit (GovActionDeposit) for every action and returns it only when the
// action is ratified or expires, so the deposit, not a per-epoch count limit,
// is what binds the attack: holding N actions open costs N deposits locked.
// Ratification throughput is a separate question: Conway votes on every open
// action each epoch, so an honest action is ratified on its own schedule
// regardless of how many spam actions are open. This model shows both, so a
// report can say the economics bind and the honest path still works.

// ProposalSpam is the deposit economics of a spam run.
type ProposalSpam struct {
	// Deposit is GovActionDeposit, the lovelace each proposal locks.
	Deposit uint64
	// Budget is the proposer's cost ceiling, the lovelace it can lock.
	Budget uint64
	// ExpiryEpochs is how long an unratified action stays open, holding its
	// deposit.
	ExpiryEpochs uint64
	// HonestRatifyEpochs is how many epochs an honest action takes to ratify;
	// the model holds this flat against spam count to show throughput is not
	// the binding constraint.
	HonestRatifyEpochs uint64
}

// Count is how many proposals the budget can keep open at once.
func (s ProposalSpam) Count() uint64 {
	if s.Deposit == 0 {
		return 0
	}
	return s.Budget / s.Deposit
}

// Locked is the lovelace the spam run ties up at its peak.
func (s ProposalSpam) Locked() uint64 { return s.Count() * s.Deposit }

// Impact compares no spam against the most spam the budget allows: proposals
// open, lovelace locked, and the honest action's ratification latency, which
// the deposit economics leave unchanged.
func (s ProposalSpam) Impact() []Impact {
	return []Impact{
		{Metric: "proposals_open", Control: 0, Treated: float64(s.Count())},
		{Metric: "deposit_locked_ada", Control: 0, Treated: float64(s.Locked()) / 1e6},
		{Metric: "honest_ratify_epochs", Control: float64(s.HonestRatifyEpochs), Treated: float64(s.HonestRatifyEpochs)},
	}
}

// ChargeAll charges the ledger for as many proposals as the budget allows in
// epoch, then reports that the next proposal is refused: the cost ceiling
// binds the spam at Count(). It returns the number charged and the refusal.
func (s ProposalSpam) ChargeAll(l *Ledger, actor uint64, epoch uint64) (uint64, error) {
	id := population.ActorID(actor)
	l.Limit(id, s.Budget)
	var charged uint64
	for {
		if err := l.Charge(id, epoch, s.Deposit); err != nil {
			return charged, err
		}
		charged++
	}
}

// GovSpamTable renders a proposal-spam impact as a fixed-width table.
func GovSpamTable(rows []Impact) string {
	return labelledTable("metric", "no spam", "spam", rows)
}

func labelledTable(head, control, treated string, rows []Impact) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-22s %14s %14s %14s\n", head, control, treated, "delta")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-22s %14s %14s %14s\n", r.Metric, num(r.Control), num(r.Treated), num(r.Delta()))
	}
	return b.String()
}
