// Package policy is the engine's stake-floor guard and its recovery
// planner. A devnet stalls when the pools that really forge hold too little
// of the stake the leader schedule is drawn from, and the chain feels the
// engine's decisions late: the schedule of epoch e is the stake at the end
// of e - 2, and a retired pool's stake stays in it for two more epochs. The
// guard therefore checks every schedule a decision can still reach, not the
// stake as it is now.
//
// The package is pure: Check, Endangered and Recover are functions of the
// State they are handed and read no clock, chain or census. The contract
// they implement is the STAKE-FLOOR GUARD CONTRACT in
// models/specs/stake-floor/README.md, and Check agrees with the model's
// Allowed (StakeFloor.tla) over the model's domain.
//
//	agenda (operator, yield, ...) --Allows--> engine adapter --Check--> Guard
//	engine, each block and pod event -------------------------> Recover
package policy

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Reg is a pool's registration as the confirmed ledger has it.
type Reg int

// Registration states. The zero value is no registration at all.
const (
	// Registered pools have no retirement confirmed.
	Registered Reg = iota + 1
	// Retiring pools have a confirmed retirement certificate whose epoch
	// has not been applied.
	Retiring
	// Retired pools were retired by an epoch transition.
	Retired
)

// Pool is one pool as the guard sees it.
type Pool struct {
	ID  population.ActorID
	Reg Reg
	// Live is the engine's plan: the pool should be forging.
	Live bool
	// Running is the observed pod health (operator status), not the plan:
	// false for a lost node, a crash loop or an expired KES key.
	Running bool
}

// Delegation is one wallet's entry in a stake distribution. Pool zero
// means the wallet's stake is delegated nowhere and so is not in the
// distribution's total.
type Delegation struct {
	Wallet population.ActorID
	Pool   population.ActorID
	Stake  uint64
}

// Schedule is a stake distribution: a snapshot the leader schedule is
// drawn from, or the confirmed delegation. Each wallet appears at most
// once; order does not matter.
type Schedule []Delegation

// Op is an engine step the guard can be asked about.
type Op int

// Steps. GoDark, Retire, Delegate and Undelegate can lower forging stake
// and are guarded; GoLive, Repair and Reregister only add and never are.
const (
	GoDark Op = iota + 1
	GoLive
	Retire
	Reregister
	Repair
	Delegate
	Undelegate
)

var opNames = map[Op]string{
	GoDark: "go dark", GoLive: "go live", Retire: "retire", Reregister: "re-register",
	Repair: "repair", Delegate: "delegate", Undelegate: "undelegate",
}

func (o Op) String() string {
	if n, ok := opNames[o]; ok {
		return n
	}
	return "unknown op"
}

// Intent is one step: a pool step names Pool, a delegation names Wallet
// and the target Pool, an undelegation names Wallet.
type Intent struct {
	Op     Op
	Wallet population.ActorID
	Pool   population.ActorID
}

// State is everything the guard reads. The engine builds it from the
// confirmed ledger view, its own transactions in flight, its plan and the
// operator's pod status.
type State struct {
	// Epoch is the current epoch by slot time. Label is the last epoch
	// whose epoch transition the ledger has run: the epoch of the newest
	// block the follower has seen. Label == Epoch means this epoch's
	// transition ran; Label == Epoch-1 means no block of this epoch has
	// been seen yet.
	Epoch, Label uint64
	// Set and Mark are the ledger's stake snapshots from the last two
	// transitions; Deleg is the confirmed delegation now.
	Set, Mark, Deleg Schedule
	// Pools lists every pool the engine knows of.
	Pools []Pool
	// InFlight lists the engine's submitted transactions that have neither
	// confirmed nor expired: Delegate, Undelegate, Retire and Reregister.
	// Their TTL ends inside the current epoch (contract rule 10).
	InFlight []Intent
}

// Frozen reports that an epoch passed with no block. The ledger then never
// runs another epoch transition, so the run has stalled: report it as a
// fatal incident rather than recover (contract rule 14).
func (s State) Frozen() bool { return s.Epoch > s.Label+1 }

// retiring reports pools with a retirement in flight, the candidate's
// included.
func (s State) retiring(extra *Intent) map[population.ActorID]bool {
	out := map[population.ActorID]bool{}
	for _, in := range s.InFlight {
		if in.Op == Retire {
			out[in.Pool] = true
		}
	}
	if extra != nil && extra.Op == Retire {
		out[extra.Pool] = true
	}
	return out
}

// forging is the set of pools the guard counts as forging with the
// candidate applied: registered with no retirement confirmed or in flight,
// planned live and observed running. A re-registration in flight does not
// count until it confirms.
func (s State) forging(extra *Intent) map[population.ActorID]bool {
	ret := s.retiring(extra)
	out := map[population.ActorID]bool{}
	for _, p := range s.Pools {
		if extra != nil && extra.Op == GoDark && extra.Pool == p.ID {
			continue
		}
		if p.Reg == Registered && p.Live && p.Running && !ret[p.ID] {
			out[p.ID] = true
		}
	}
	return out
}

// horizon lists the schedules a decision taken now can still reach. They
// depend on whether this epoch's transition has run, not on the epoch
// number: before the epoch's first block the schedule in use is already
// the mark the transition will install, and the next one is today's
// delegation. The projection is the confirmed delegation with everything in
// flight landed. Later schedules differ from the projection only by the
// stake of retired pools, which the guard already counts as dark.
func (s State) horizon(proj Schedule) ([]window, error) {
	switch {
	case s.Label > s.Epoch:
		return nil, fmt.Errorf("last transition epoch %d is after epoch %d", s.Label, s.Epoch)
	case s.Frozen():
		return []window{{"set", s.Set}}, nil
	case s.Label == s.Epoch:
		return []window{{"set", s.Set}, {"mark", s.Mark}, {"projection", proj}}, nil
	}
	return []window{{"mark", s.Mark}, {"delegation", s.Deleg}, {"projection", proj}}, nil
}
