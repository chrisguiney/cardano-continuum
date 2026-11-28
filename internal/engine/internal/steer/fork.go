package steer

import (
	"context"

	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// This file is the run's own hard fork (protocol.Schedule). At each epoch
// boundary until the version is reached, the network proposer gets the
// committee's hot keys authorised and, from the scheduled epoch on and once
// governance is ready, proposes the fork through the hard_fork scenario's
// path, so the one-fork-at-a-time guard and the builder are the manual
// scenario's. A refusal, or a proposal the ledger rejects or lets expire, is
// retried at the next boundary: one attempt per epoch is the backoff.
//
// The step reads only the chain view, the in-flight payments and the
// schedule, so a restart re-arms from the restored clock and the snapshot
// holds nothing of it.

// ForkMove is what a fork step does.
type ForkMove int

// The moves of a fork step.
const (
	// ForkWait does nothing until the next boundary.
	ForkWait ForkMove = iota
	// ForkAuthorise has the committee's hot keys authorised.
	ForkAuthorise
	// ForkPropose proposes the scheduled fork.
	ForkPropose
)

// ForkDecide is the step's move: the committee and the pools are ready
// when every seat's hot key is authorised and some pool holds stake. Below
// protocol version 11 a committee vote needs its hot key authorised
// beforehand, so that comes first even before the scheduled epoch, which
// spares the proposal an epoch's wait; from 11 the vote carries its own.
func ForkDecide(plan protocol.Schedule, epoch uint64, cur protocol.Version, committee, pools bool) ForkMove {
	switch {
	case !plan.On() || !cur.Less(plan.To):
		return ForkWait
	case cur.Major < protocol.VanRossem && !committee:
		return ForkAuthorise
	case !plan.Due(epoch, cur) || !pools:
		return ForkWait
	}
	return ForkPropose
}

// ForkEnv is what a fork step reads and acts through.
type ForkEnv interface {
	// Epoch is the chain view's epoch and Version the protocol version in
	// force in it.
	Epoch() uint64
	Version() protocol.Version
	// CommitteeReady reports that every seat's hot key is authorised;
	// PoolsReady that a registered pool holds stake.
	CommitteeReady() bool
	PoolsReady() bool
	// Arm schedules the next step at the first boundary after at.
	Arm(at sched.Time)
	// HotKeys and HardFork are the scenarios of the same names.
	HotKeys(ctx context.Context, at sched.Time) (string, error)
	HardFork(ctx context.Context, at sched.Time, arg string) (string, error)
}

// ForkStep runs the step at an epoch boundary and arms the next one. A
// scenario's refusal is not an error: the step tries again next epoch.
func ForkStep(ctx context.Context, at sched.Time, plan protocol.Schedule, env ForkEnv) error {
	env.Arm(at)

	var err error
	switch ForkDecide(plan, env.Epoch(), env.Version(), env.CommitteeReady(), env.PoolsReady()) {
	case ForkAuthorise:
		_, err = env.HotKeys(ctx, at)
	case ForkPropose:
		_, err = env.HardFork(ctx, at, plan.To.String())
	}
	return err
}
