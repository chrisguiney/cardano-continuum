package chain

import "github.com/blinklabs-io/gouroboros/ledger/common"

// Mark is a Point with its block number: the height a node reports for the
// block. Two chains are compared at a common height, so a fork shows even
// when the branches' slots differ.
type Mark struct {
	Height uint64
	Point
	// Issuer is the hash of the cold key that signed the block, the PoolID
	// of the pool that forged it. It is zero where only a point is known,
	// such as the tip a node reports when a chain-sync starts.
	Issuer common.Blake2b224
}
