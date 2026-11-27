package wire

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Coin is a spendable output offered to a coin selector.
type Coin struct {
	Ref      Ref
	Lovelace uint64
}

// SumCoins is the coins' lovelace.
func SumCoins(cs []Coin) uint64 {
	var sum uint64
	for _, c := range cs {
		sum += c.Lovelace
	}
	return sum
}

// Pending is an intent the planner's ledger holds unfinished. Its inputs
// and reservation are the ledger's. The pay component keeps it; the intent,
// attack, faucet and judge components read and fill its parts.
type Pending struct {
	ID    uint64
	Actor uint64
	// Refill marks the faucet's refill, which no retry machine drives.
	Refill bool
	Attempt
	Args
	Gov
	Faults
	Strike
}

// Attempt is the current attempt and the retry machine driving the intent.
type Attempt struct {
	// Tx and TTL are the current attempt's.
	Tx  TxID
	TTL uint64
	// Cbor is held while the current attempt may still be sent, so a
	// restart or a resend can submit the same bytes.
	Cbor     []byte
	Awaiting bool
	// M is the retry machine.
	M submit.Machine
	// Txs are every unfinished attempt of the intent, oldest first. Only
	// the ledger removes one: settled, dead, or refused by every node.
	Txs []TxID
	// NotBefore is when the current attempt may next be sent: the backoff.
	NotBefore sched.Time
}

// Args are what every attempt of the intent builds.
type Args struct {
	// Kind is the intent's builder; empty is a payment. To is the payee,
	// pool or DRep it names, Amount what a payment pays, a lock locks, a
	// mint or burn moves, or an unlock frees.
	Kind   string
	To     uint64
	Amount uint64
	// Locked is the script output an unlock spends; Contract the contract
	// a lock or unlock calls.
	Locked   *Ref
	Contract uint64
	// Epoch is the epoch a retirement takes effect.
	Epoch uint64
	// Register makes a delegation also register the stake key; Update
	// makes a pool registration a re-registration.
	Register bool
	Update   bool
}

// Gov are a governance intent's arguments.
type Gov struct {
	// Action is the governance action a vote is on, Choice the vote.
	Action *Ref
	Choice uint8
	// GovType is a proposal's governance action type.
	GovType string
	// Power is a vote's voting power when it was chosen. Probe marks a
	// vote the ledger must refuse (an SPO vote on an action pools may not
	// vote on): its expected verdict is DisallowedVoters.
	Power uint64
	Probe bool
}

// Faults are the mistakes the intent's attempts carry.
type Faults struct {
	// Injected is the mistake sampled for the intent; Carried those the
	// current attempt carries (Injected less every fix so far), and Lost
	// what the current attempt pays to a wrong address.
	Injected mistake.KindSet
	Carried  mistake.KindSet
	Lost     uint64
}

// Strike is an adversary's step.
type Strike struct {
	// Attack names the attack of an adversary's step, which no retry
	// machine drives; Want is the verdict its first send calls for, Rivals
	// a race's further transactions still to send, and Jump sends it ahead
	// of every other submission (a front-run).
	Attack string
	Want   *Want
	Rivals []Rival
	Jump   bool
}

// OneShot reports an intent no retry machine drives.
func (p *Pending) OneShot() bool { return p.Refill || p.Attack != "" }

// KindOf is the builder key of the intent; a payment has none.
func (p *Pending) KindOf() string {
	if p.Kind == "" {
		return string(profile.IntentSend)
	}
	return p.Kind
}

// Want is the verdict an attack's first send calls for (mistake.Expected).
type Want struct {
	Classes []string
	Accept  bool
}

// WantOf records x.
func WantOf(x mistake.Expected) *Want {
	w := &Want{Accept: x.Accept}
	for _, c := range x.Classes {
		w.Classes = append(w.Classes, string(c))
	}
	return w
}

// Expected is the verdict w records.
func (w *Want) Expected() mistake.Expected {
	x := mistake.Expected{Accept: w.Accept}
	for _, c := range w.Classes {
		x.Classes = append(x.Classes, submit.Class(c))
	}
	slices.Sort(x.Classes)
	return x
}
