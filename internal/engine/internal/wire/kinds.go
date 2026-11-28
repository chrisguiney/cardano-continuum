package wire

// Decision kinds the engine emits. Inputs use the eventlog kinds.
const (
	KindSpawned      = "Spawned"
	KindTransition   = "Transition"
	KindIntentChosen = "IntentChosen"
	KindSubmitted    = "Submitted"
	KindSkipped      = "Skipped"
	KindReleased     = "Released"
	KindConfirmed    = "Confirmed"
	KindRefused      = "Refused"
	KindGaveUp       = "GaveUp"
	KindRefill       = "Refill"
	KindGuarded      = "Guarded"
	KindPoolTier     = "PoolTier"
	KindRepair       = "Repair"
	KindIncident     = "Incident"
	KindControlled   = "Controlled"
	KindImageChange  = "ImageChange"
	KindDeployed     = "Deployed"
	KindProtocol     = "Protocol"
	KindQuote        = "Quote"
	KindTraded       = "Traded"
	KindTreasury     = "Treasury"
	// KindHumanBorn and KindHumanLeft are a human's founding and leaving,
	// in a run with a humans section.
	KindHumanBorn = "HumanBorn"
	KindHumanLeft = "HumanLeft"
	// KindIncome, KindExpense, KindEmployment and KindInvested are a
	// human's household money.
	KindIncome     = "Income"
	KindExpense    = "Expense"
	KindEmployment = "Employment"
	KindInvested   = "Invested"
)

// DecisionKinds returns every decision kind the engine emits.
func DecisionKinds() []string {
	return []string{
		KindSpawned, KindTransition, KindIntentChosen, KindSubmitted, KindSkipped, KindReleased,
		KindConfirmed, KindRefused, KindGaveUp, KindRefill, KindGuarded, KindPoolTier, KindRepair,
		KindIncident, KindControlled, KindImageChange, KindDeployed, KindProtocol, KindQuote, KindTraded,
		KindTreasury, KindHumanBorn, KindHumanLeft, KindIncome, KindExpense, KindEmployment, KindInvested,
	}
}
