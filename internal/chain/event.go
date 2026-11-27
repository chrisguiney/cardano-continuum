package chain

import "strconv"

// EventKind is a chain-sync instruction to the engine.
type EventKind uint8

// Event kinds.
const (
	// Forward: apply Block.
	Forward EventKind = iota + 1
	// Backward: roll back to Point.
	Backward
	// Intersected: the answer to FindIntersect after a (re)connect. The
	// engine logs it as a rollback to Point when it is below its tip
	// (eventlog.Session.Intersect) and rolls the view back to it.
	Intersected
)

func (k EventKind) String() string {
	switch k {
	case Forward:
		return "Forward"
	case Backward:
		return "Backward"
	case Intersected:
		return "Intersected"
	}
	return "EventKind(" + strconv.Itoa(int(k)) + ")"
}

// Event is one chain-sync message. Tip is the node's tip when it was sent.
type Event struct {
	Kind  EventKind
	Block Block
	Point Point
	Tip   Point
}
