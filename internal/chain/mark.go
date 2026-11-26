package chain

// Mark is a Point with its block number: the height a node reports for the
// block. Two chains are compared at a common height, so a fork shows even
// when the branches' slots differ.
type Mark struct {
	Height uint64
	Point
}
