package build

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// Output is one payment of a fan-out.
type Output struct {
	To     common.Address
	Amount uint64
}
