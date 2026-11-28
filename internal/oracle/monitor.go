package oracle

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
)

// Snapshot is everything the monitors read, assembled by the engine at one
// point of the run. A nil or empty part is not evaluable at this snapshot
// (a node that cannot answer the query, a part the engine does not track),
// and its monitor reports nothing rather than failing.
type Snapshot struct {
	// Seq is the log seq of the input the snapshot was taken after.
	Seq uint64
	Run Run

	Value        *Value
	Ends         []End
	Reservations *Reservations
	Settled      *SettledMove
	Views        []ViewCheck
	// Policy is the stake-floor guard's input (chain.View.PolicyState).
	Policy *policy.State
	Tips   []Branch
	// Rogue is every block the rogue pool forged to be rejected
	// (internal/forger logs each as a "forged" decision expecting reject).
	Rogue []RogueBlock
	// Budget is the block an input applied, with its declared execution
	// units, when it applied one.
	Budget *BlockBudget
}

// RogueBlock is a block built to break one consensus rule. No honest node
// may have it on its chain.
type RogueBlock struct {
	Point chain.Point
	// Kind is the misbehaviour (forger.Kind) and Rule the rule it breaks.
	Kind, Rule string
	CBOR       []byte
}

// Value is the lovelace accounting at one point. Every lovelace of the
// genesis total is in exactly one of the parts.
type Value struct {
	Genesis uint64
	// Utxo is the lovelace in unspent outputs. The engine's Supply already
	// adds its fees and deposits; an engine passing it here leaves those
	// parts zero.
	Utxo, Fees, Deposits, Rewards, Treasury, Reserves uint64
	At                                                chain.Point
}

// End is an intent's attempt settling, or the planner writing the intent
// off. The engine logs these as Confirmed and Released decisions.
type End struct {
	Intent   uint64
	Tx       chain.TxID
	Released bool
	Seq      uint64
}

// Reservations is the plan ledger's reservation state.
type Reservations struct {
	Holds      []Hold
	Unfinished []plan.Pending
}

// Hold is one output reserved for one intent.
type Hold struct {
	Ref    chain.Ref
	Intent plan.IntentID
}

// Holders is the part of *plan.Ledger ReservationsOf reads.
type Holders interface {
	Settled() []plan.Utxo
	Unfinished() []plan.Pending
	Holder(r plan.Ref) (plan.IntentID, bool)
}

// ReservationsOf reads a ledger's reservations. An output can only be
// reserved while it is offered by Available: settled and owned, or the
// change of an unfinished attempt; so asking for the holder of each finds
// every reservation.
func ReservationsOf(l Holders) Reservations {
	un := l.Unfinished()
	var refs []chain.Ref
	for _, u := range l.Settled() {
		refs = append(refs, u.Ref)
	}
	for _, p := range un {
		for _, c := range p.Change {
			refs = append(refs, c.Ref)
		}
	}
	r := Reservations{Unfinished: un}
	for _, ref := range refs {
		if id, ok := l.Holder(ref); ok {
			r.Holds = append(r.Holds, Hold{Ref: ref, Intent: id})
		}
	}
	return r
}

// SettledMove is the settled (K-deep) point at the previous snapshot and
// now.
type SettledMove struct{ From, To chain.Point }

// ViewCheck compares the engine's per-actor balances with the node's.
type ViewCheck struct {
	Node Node
	// At is where the engine's balances were taken and NodeAt where the
	// node answered. Only answers at the same point are comparable.
	At, NodeAt    chain.Point
	Engine, Chain map[population.ActorID]uint64
	// Version is the protocol version the node reported in epoch Epoch,
	// nil when it reported none; Want is the version the engine's view of
	// the ratified hard forks puts in force then, nil when the view has not
	// reached that epoch and cannot judge it.
	Version, Want *protocol.Version
	Epoch         uint64
}

// Balances sums utxos per owner; outputs owner does not name are left out.
// It turns a Querier.UtxosAt answer into ViewCheck.Chain.
func Balances(utxos []common.Utxo, owner func(common.Address) (population.ActorID, bool)) map[population.ActorID]uint64 {
	out := map[population.ActorID]uint64{}
	for _, u := range utxos {
		if id, ok := owner(u.Output.Address()); ok {
			out[id] += u.Output.Amount().Uint64()
		}
	}
	return out
}

// Branch is a pool's chain as its tip reports it: the newest blocks,
// oldest first, ending at the tip. More than k+1 points let TipsAgree see a
// fork's depth.
type Branch struct {
	Node   Node
	Points []chain.Point
}

// Monitor checks one invariant over a snapshot. Check is pure.
type Monitor interface {
	Name() string
	Check(s Snapshot) []Finding
}

// Monitors returns every monitor, for a network with security parameter k
// and the engine's stake-floor guard.
func Monitors(k uint64, g policy.Guard) []Monitor {
	return []Monitor{
		monitor{"value", checkValue},
		monitor{"paid_once", checkPaid},
		monitor{"reservations", checkReservations},
		monitor{"settled", checkSettled},
		monitor{"view", checkViews},
		monitor{"liveness", checkLiveness},
		monitor{"tips", func(s Snapshot) []Finding { return checkTips(s, k) }},
		monitor{"stake_floor", func(s Snapshot) []Finding { return checkFloor(s, g) }},
		monitor{"rogue", checkRogue},
		monitor{"block_budget", checkBudget},
		monitor{"protocol", checkProtocol},
	}
}

// Check runs every monitor in ms over s.
func Check(s Snapshot, ms []Monitor) []Finding {
	var out []Finding
	for _, m := range ms {
		out = append(out, m.Check(s)...)
	}
	return out
}

type monitor struct {
	name  string
	check func(Snapshot) []Finding
}

func (m monitor) Name() string { return m.name }

// Check implements Monitor, sealing each finding with the snapshot's run.
func (m monitor) Check(s Snapshot) []Finding {
	fs := m.check(s)
	for i := range fs {
		fs[i] = seal(fs[i], s.Run, s.Seq)
	}
	return fs
}
