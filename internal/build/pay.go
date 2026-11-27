package build

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// Payment is a simple ADA payment from one wallet to one address.
type Payment struct {
	// Inputs are the UTxOs the builder may spend. The builder spends only
	// these; choosing them is the planner's job.
	Inputs []common.Utxo
	To     common.Address
	Amount uint64
	// TTL is the absolute slot after which the transaction is invalid.
	TTL uint64
	// Tip is paid in fee above the minimum (Spend.Tip).
	Tip uint64
}
