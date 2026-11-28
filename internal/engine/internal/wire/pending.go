package wire

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
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
// attack, faucet and judge components read and fill its parts. It is also
// the snapshot's record of the intent: the parts are inlined, and changing
// a field changes the checkpoint bytes.
type Pending struct {
	// ID is the intent's ID.
	ID uint64 `json:"id"`
	// Actor is the actor the intent is for.
	Actor uint64 `json:"actor"`
	// Refill marks the faucet's refill, which no retry machine drives.
	Refill bool `json:"refill,omitzero"`
	// Attempt is the current attempt and its retry machine.
	Attempt
	// Args are what every attempt builds.
	Args
	// Gov are a governance intent's arguments.
	Gov
	// Faults are the mistakes the attempts carry.
	Faults
	// Strike is an adversary's step.
	Strike
	// Order is an exchange trade's quote; nil for any other intent.
	Order *Order `json:"order,omitempty"`
}

// Attempt is the current attempt and the retry machine driving the intent.
type Attempt struct {
	// Tx is the current attempt's transaction ID.
	Tx TxID `json:"tx"`
	// TTL is the current attempt's invalid-hereafter slot.
	TTL uint64 `json:"ttl"`
	// Cbor is held while the current attempt may still be sent, so a
	// restart or a resend can submit the same bytes.
	Cbor []byte `json:"cbor"`
	// Awaiting marks the current attempt as waiting for a send.
	Awaiting bool `json:"awaiting"`
	// M is the retry machine.
	M submit.Machine `json:"m"`
	// Txs are every unfinished attempt of the intent, oldest first. Only
	// the ledger removes one: settled, dead, or refused by every node.
	Txs []TxID `json:"txs"`
	// NotBefore is when the current attempt may next be sent: the backoff.
	NotBefore sched.Time `json:"not_before,omitzero"`
}

// Args are what every attempt of the intent builds.
type Args struct {
	// Kind is the intent's builder; empty is a payment.
	Kind string `json:"kind,omitempty"`
	// To is the payee, pool or DRep the intent names.
	To uint64 `json:"to"`
	// Amount is what a payment pays, a lock locks, a mint or burn moves,
	// or an unlock frees.
	Amount uint64 `json:"amount"`
	// Locked is the script output an unlock spends.
	Locked *Ref `json:"locked,omitzero"`
	// Contract is the contract a lock or unlock calls.
	Contract uint64 `json:"contract,omitzero"`
	// Call is what a lock or unlock does with its contract: lock, claim,
	// or an escrow's release or refund.
	Call string `json:"call,omitempty"`
	// Epoch is the epoch a retirement takes effect.
	Epoch uint64 `json:"epoch,omitzero"`
	// Register makes a delegation also register the stake key.
	Register bool `json:"register,omitzero"`
	// Update makes a pool registration a re-registration.
	Update bool `json:"update,omitzero"`
}

// Gov are a governance intent's arguments.
type Gov struct {
	// Action is the governance action a vote is on.
	Action *Ref `json:"action,omitzero"`
	// Choice is the vote.
	Choice uint8 `json:"choice,omitzero"`
	// GovType is a proposal's governance action type.
	GovType string `json:"gov_type,omitempty"`
	// Power is a vote's voting power when it was chosen.
	Power uint64 `json:"power,omitzero"`
	// Probe marks a vote the ledger must refuse (an SPO vote on an action
	// pools may not vote on): its expected verdict is DisallowedVoters.
	Probe bool `json:"probe,omitzero"`
	// Fork is the protocol version a hard fork proposes.
	Fork *protocol.Version `json:"fork,omitempty"`
	// Auth makes a committee vote carry its hot key's authorisation, which
	// a node takes from protocol version 11 (protocol.VanRossem).
	Auth bool `json:"auth,omitzero"`
}

// Faults are the mistakes the intent's attempts carry.
type Faults struct {
	// Injected is the mistake sampled for the intent.
	Injected mistake.KindSet `json:"injected,omitzero"`
	// Carried are those the current attempt carries: Injected less every
	// fix so far.
	Carried mistake.KindSet `json:"carried,omitzero"`
	// Lost is what the current attempt pays to a wrong address.
	Lost uint64 `json:"lost,omitzero"`
}

// Strike is an adversary's step.
type Strike struct {
	// Attack names the attack of an adversary's step, which no retry
	// machine drives.
	Attack string `json:"attack,omitempty"`
	// Want is the verdict the first send calls for.
	Want *Want `json:"want,omitzero"`
	// Rivals are a race's further transactions still to send.
	Rivals []Rival `json:"rivals,omitempty"`
	// Jump sends the step ahead of every other submission (a front-run).
	Jump bool `json:"jump,omitzero"`
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
	// Classes are the rejection classes the verdict must carry.
	Classes []string `json:"classes,omitempty"`
	// Accept is whether the verdict must be an acceptance.
	Accept bool `json:"accept,omitzero"`
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
