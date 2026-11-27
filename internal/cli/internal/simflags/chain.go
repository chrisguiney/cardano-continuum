package simflags

import (
	"flag"

	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Chain holds the flags that shape the fake chain and its pace.
type Chain struct {
	SlotMs     int64
	EpochSlots uint64
	K          uint64
	BlockEvery uint64
	TTL        uint64
	Floor      uint64
}

func (c *Chain) register(fs *flag.FlagSet) {
	fs.Int64Var(&c.SlotMs, "slot-ms", 1000, "slot length in milliseconds")
	fs.Uint64Var(&c.EpochSlots, "epoch-slots", 600, "slots per epoch")
	fs.Uint64Var(&c.K, "k", 5, "security parameter: blocks until a block is durable")
	fs.Uint64Var(&c.BlockEvery, "block-every", 10, "slots between fake-chain blocks")
	fs.Uint64Var(&c.TTL, "ttl", 120, "slots a payment stays valid")
	fs.Uint64Var(&c.Floor, "floor", 50, "stake floor: the percentage of each schedule's stake forging pools must hold, one pool's loss included")
}

// Slots is the slot shape --slot-ms and --epoch-slots give.
func (c *Chain) Slots() (sched.Slots, error) {
	return sched.NewSlots(sched.Time(c.SlotMs), c.EpochSlots)
}
