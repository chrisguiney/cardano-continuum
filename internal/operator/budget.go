package operator

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// Claim is one pool's demand on its network's budget.
type Claim struct {
	Name   string
	Demand Demand
}

// Admit decides whether pool self fits the budget b. queue is every pool of
// the network that renders a workload, oldest first. Pools are admitted
// greedily in that order, skipping any that does not fit, so every
// reconciler that sees the same pools reaches the same answer and a newer
// pool never displaces an older one. The error names the limit self broke.
func Admit(b v1alpha1.BudgetSpec, queue []Claim, self string) error {
	var pods int32
	var cpu, mem int64
	for _, c := range queue {
		err := fits(b, pods, cpu, mem, c.Demand)
		if c.Name == self {
			return err
		}
		if err == nil {
			pods++
			cpu += c.Demand.CPUMillis
			mem += c.Demand.MemoryMiB
		}
	}
	return fmt.Errorf("pool %s is not in its network's admission queue", self)
}

func fits(b v1alpha1.BudgetSpec, pods int32, cpu, mem int64, d Demand) error {
	switch {
	case pods+1 > b.MaxPools:
		return fmt.Errorf("pods budget %d is used up", b.MaxPools)
	case cpu+d.CPUMillis > b.CPUMillis:
		return fmt.Errorf("cpu budget %dm exceeded: %dm in use, %dm requested", b.CPUMillis, cpu, d.CPUMillis)
	case mem+d.MemoryMiB > b.MemoryMiB:
		return fmt.Errorf("memory budget %d MiB exceeded: %d MiB in use, %d MiB requested", b.MemoryMiB, mem, d.MemoryMiB)
	}
	return nil
}
