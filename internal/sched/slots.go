package sched

import "fmt"

// Slots maps simulated time onto the network's slot and epoch grid. Slot 0
// starts at Time 0 (system start).
type Slots struct {
	slotMs   Time
	epochLen uint64
}

// NewSlots returns the grid for slotMs-millisecond slots and epochs of
// epochSlots slots. Both must be positive. Validating epoch length against
// k/f is left to the genesis component.
func NewSlots(slotMs Time, epochSlots uint64) (Slots, error) {
	if slotMs <= 0 {
		return Slots{}, fmt.Errorf("slots: slot length %dms must be positive", slotMs)
	}
	if epochSlots == 0 {
		return Slots{}, fmt.Errorf("slots: epoch length must be positive")
	}
	return Slots{slotMs: slotMs, epochLen: epochSlots}, nil
}

// SlotAt returns the slot containing t; times before system start map to 0.
func (g Slots) SlotAt(t Time) uint64 {
	if t < 0 {
		return 0
	}
	return uint64(t / g.slotMs)
}

// SlotStart returns the time at which slot begins.
func (g Slots) SlotStart(slot uint64) Time { return Time(slot) * g.slotMs }

// EpochOfSlot returns the epoch containing slot.
func (g Slots) EpochOfSlot(slot uint64) uint64 { return slot / g.epochLen }

// EpochAt returns the epoch containing t.
func (g Slots) EpochAt(t Time) uint64 { return g.EpochOfSlot(g.SlotAt(t)) }

// EpochStart returns the time at which epoch begins.
func (g Slots) EpochStart(epoch uint64) Time { return g.SlotStart(epoch * g.epochLen) }

// NextEpochStart returns the first epoch boundary strictly after t, which
// is when an epoch tick fires.
func (g Slots) NextEpochStart(t Time) Time { return g.EpochStart(g.EpochAt(t) + 1) }
