package chain

import (
	"strconv"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// EffectKind names what a block or rollback did to the view.
type EffectKind uint8

// Effect kinds. Apply yields Confirmed, Spent, Created, CertSeen and
// Settled; Rollback yields Removed, Restored and RolledBack.
const (
	// Confirmed: Tx touched the view; its other effects follow.
	Confirmed EffectKind = iota + 1
	// Spent: Tx spent the tracked output Ref.
	Spent
	// Created: Tx created the tracked output Ref (Utxo).
	Created
	// CertSeen: Tx carried Cert for a tracked credential, pool or DRep.
	CertSeen
	// Settled: the block at Point got its Kth successor; Txs are its
	// tracked transactions. Point.Slot is what a TTL is compared with.
	Settled
	// RolledBack: Tx left the chain; its Removed and Restored precede it.
	RolledBack
	// Restored: a rollback put the output Ref (Utxo) back.
	Restored
	// Removed: a rollback took away the output Ref.
	Removed
	// GovSeen: Tx carried a proposal, or a vote on a tracked proposal by a
	// voter the book names.
	GovSeen
)

var effectNames = [...]string{"", "Confirmed", "Spent", "Created", "CertSeen", "Settled", "RolledBack", "Restored", "Removed", "GovSeen"}

func (k EffectKind) String() string {
	if int(k) < len(effectNames) && k > 0 {
		return effectNames[k]
	}
	return "EffectKind(" + strconv.Itoa(int(k)) + ")"
}

// Effect is one change to the view. Which fields are set depends on Kind.
type Effect struct {
	Kind  EffectKind
	Tx    TxID
	Ref   Ref
	Utxo  common.Utxo
	Cert  common.Certificate
	Point Point
	Txs   []TxID
}
